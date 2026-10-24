package crash_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/coordinator"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/crash"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/nodeserver"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/testutil"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/wal"
)

const (
	helperEnv   = "CRASH_HELPER"
	bodyEnv     = "CRASH_BODY"
	keyEnv      = "CRASH_KEY"
	ownerEnv    = "CRASH_OWNER"
	bucketEnv   = "CRASH_BUCKET"
	idemEnv     = "CRASH_IDEMPOTENCY"
	nodesEnv    = "CRASH_NODES"
	walDirEnv   = "CRASH_WAL_DIR"
	schemaEnv   = "CRASH_SCHEMA"
	crashTestID = "TestCrashHelper"
)

func TestCrashRecoveryHarness(t *testing.T) {
	if testutil.URL() == "" {
		t.Skip("TEST_DATABASE_URL or DATABASE_URL not set")
	}
	for _, point := range []string{crash.AfterWALSync, crash.AfterNodeAck, crash.AfterDBCommit} {
		t.Run(point, func(t *testing.T) {
			db := testutil.NewDB(t)
			q := dbgen.New(db.Pool)
			ctx := context.Background()

			owner, err := q.CreateUser(ctx, dbgen.CreateUserParams{
				Email:        "crash@example.com",
				PasswordHash: "x",
			})
			if err != nil {
				t.Fatalf("create user: %v", err)
			}
			bucket, err := q.CreateBucket(ctx, dbgen.CreateBucketParams{Name: "crash-bucket", OwnerID: owner.ID})
			if err != nil {
				t.Fatalf("create bucket: %v", err)
			}

			var nodeSpecs []string
			var nodes []placement.Node
			for i := 1; i <= 3; i++ {
				name := fmt.Sprintf("node%d", i)
				root := t.TempDir()
				store, err := objects.NewLocalChunkStore(root)
				if err != nil {
					t.Fatalf("local store: %v", err)
				}
				srv := httptest.NewServer(nodeserver.Handler(store, root, slog.New(slog.DiscardHandler)))
				t.Cleanup(srv.Close)
				id := placement.NodeID(name)
				if _, err := q.UpsertStorageNode(ctx, dbgen.UpsertStorageNodeParams{
					ID:      pgUUID(t, id),
					Name:    name,
					BaseUrl: srv.URL,
				}); err != nil {
					t.Fatalf("register node: %v", err)
				}
				nodeSpecs = append(nodeSpecs, name+"="+srv.URL)
				nodes = append(nodes, placement.Node{
					ID: id, Name: name, Status: "up",
					Store: objects.NewNodeClient(id, name, srv.URL),
				})
			}

			walDir := t.TempDir()
			key := "crash/object.txt"
			body := "the crash harness body"
			idempotency := "crash-" + point
			baseEnv := append(cleanEnv(os.Environ(), "CRASH_AT"),
				helperEnv+"=1",
				bodyEnv+"="+body,
				keyEnv+"="+key,
				ownerEnv+"="+owner.ID.String(),
				bucketEnv+"="+bucket.Name,
				idemEnv+"="+idempotency,
				nodesEnv+"="+strings.Join(nodeSpecs, ","),
				walDirEnv+"="+walDir,
				schemaEnv+"="+db.Schema,
			)

			first := runHelper(t, append(append([]string{}, baseEnv...), "CRASH_AT="+point))
			if first.err == nil {
				t.Fatalf("first run did not crash at %s\n%s", point, first.output)
			}
			if !strings.Contains(first.output, "crash injection") {
				t.Fatalf("first run failed for the wrong reason:\n%s", first.output)
			}

			second := runHelper(t, baseEnv)
			if second.err != nil {
				t.Fatalf("retry after crash failed: %v\n%s", second.err, second.output)
			}

			if got := count(t, db, "SELECT count(*) FROM object_versions"); got != 1 {
				t.Errorf("object_versions = %d, want 1", got)
			}
			if got := count(t, db, "SELECT count(*) FROM chunks"); got != 1 {
				t.Errorf("chunks = %d, want 1", got)
			}
			if got := count(t, db, "SELECT count(*) FROM upload_parts"); got != 1 {
				t.Errorf("upload_parts = %d, want 1", got)
			}
			if got := count(t, db, "SELECT count(*) FROM upload_parts WHERE chunk_id IS NULL"); got != 0 {
				t.Errorf("upload_parts with no chunk = %d", got)
			}
			if got := count(t, db, "SELECT count(*) FROM multipart_uploads WHERE status = 'completed' AND completed_version_id IS NOT NULL"); got != 1 {
				t.Errorf("completed uploads = %d, want 1", got)
			}
			if got := count(t, db, "SELECT count(*) FROM chunk_replicas WHERE status = 'healthy'"); got < 2 {
				t.Errorf("healthy replicas = %d, want at least 2", got)
			}
			if got := count(t, db, "SELECT count(*) FROM objects WHERE current_version_id IS NOT NULL"); got != 1 {
				t.Errorf("objects with a current version = %d, want 1", got)
			}

			coord, err := coordinator.New(db.Pool, q, slog.New(slog.DiscardHandler), nil, nodes, coordinator.Config{
				Replicas: 3, WriteQuorum: 2, MaxPartSize: 16 << 20, MultipartExpiry: time.Hour,
			})
			if err != nil {
				t.Fatalf("parent coordinator: %v", err)
			}
			reader, err := coord.GetObject(ctx, owner.ID.String(), bucket.Name, key)
			if err != nil {
				t.Fatalf("read recovered object: %v", err)
			}
			got, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatalf("read recovered bytes: %v", err)
			}
			if string(got) != body {
				t.Errorf("recovered %q, want %q", got, body)
			}
		})
	}
}

// TestCrashHelper runs in a child process and performs one write. Without
// CRASH_HELPER it does nothing, so the normal test suite skips it.
func TestCrashHelper(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("child process helper")
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	url := testutil.URL()
	schema := os.Getenv(schemaEnv)
	pool, err := testutil.NewPool(ctx, url, schema)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	q := dbgen.New(pool)

	nodes := parseNodes(t, os.Getenv(nodesEnv))
	recovery := coordinator.NewRecovery(pool, q, log, nodes, 2)
	walLog, _, err := wal.Recover(ctx, os.Getenv(walDirEnv), 1<<20, recovery)
	if err != nil {
		t.Fatalf("recover wal: %v", err)
	}
	defer walLog.Close(context.Background())

	coord, err := coordinator.New(pool, q, log, walLog, nodes, coordinator.Config{
		Replicas: 3, WriteQuorum: 2, MaxPartSize: 16 << 20, MultipartExpiry: time.Hour,
	})
	if err != nil {
		t.Fatalf("coordinator: %v", err)
	}
	_, err = coord.PutObject(
		ctx,
		os.Getenv(ownerEnv),
		os.Getenv(bucketEnv),
		os.Getenv(keyEnv),
		"text/plain",
		os.Getenv(idemEnv),
		strings.NewReader(os.Getenv(bodyEnv)),
	)
	if err != nil {
		t.Fatalf("PutObject in helper: %v", err)
	}
}

type helperResult struct {
	output string
	err    error
}

func runHelper(t *testing.T, env []string) helperResult {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+crashTestID+"$", "-test.v")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return helperResult{output: string(out), err: err}
}

func parseNodes(t *testing.T, raw string) []placement.Node {
	t.Helper()
	var nodes []placement.Node
	for _, spec := range strings.Split(raw, ",") {
		name, url, ok := strings.Cut(spec, "=")
		if !ok {
			t.Fatalf("bad node spec %q", spec)
		}
		id := placement.NodeID(name)
		nodes = append(nodes, placement.Node{
			ID: id, Name: name, Status: "up",
			Store: objects.NewNodeClient(id, name, url),
		})
	}
	return nodes
}

func pgUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	u, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("parse uuid: %v", err)
	}
	return pgtype.UUID{Bytes: u, Valid: true}
}

func count(t *testing.T, db *testutil.DB, query string) int64 {
	t.Helper()
	var n int64
	if err := db.Pool.QueryRow(context.Background(), query).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func cleanEnv(env []string, remove ...string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		drop := false
		for _, r := range remove {
			if strings.HasPrefix(e, r+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, e)
		}
	}
	return out
}
