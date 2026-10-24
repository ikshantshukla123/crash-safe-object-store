package coordinator_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/coordinator"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/nodeserver"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/testutil"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/wal"
)

type harness struct {
	db      *testutil.DB
	q       *dbgen.Queries
	coord   *coordinator.Coordinator
	walLog  *wal.Log
	nodes   []placement.Node
	servers []*httptest.Server
	roots   []string
	ownerID string
	bucket  dbgen.Bucket
	user    dbgen.User
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := testutil.NewDB(t)
	q := dbgen.New(db.Pool)

	h := &harness{db: db, q: q}
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("node%d", i)
		root := t.TempDir()
		store, err := objects.NewLocalChunkStore(root)
		if err != nil {
			t.Fatalf("new local store: %v", err)
		}
		srv := httptest.NewServer(nodeserver.Handler(store, root, slog.New(slog.DiscardHandler)))
		h.servers = append(h.servers, srv)
		h.roots = append(h.roots, root)
		id := placement.NodeID(name)
		if _, err := q.UpsertStorageNode(context.Background(), dbgen.UpsertStorageNodeParams{
			ID:      mustUUID(t, id),
			Name:    name,
			BaseUrl: srv.URL,
		}); err != nil {
			t.Fatalf("register node: %v", err)
		}
		h.nodes = append(h.nodes, placement.Node{
			ID:     id,
			Name:   name,
			Status: "up",
			Store:  objects.NewNodeClient(id, name, srv.URL),
		})
	}

	walLog, err := wal.Open(t.TempDir(), 1<<20, 1)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}
	h.walLog = walLog
	coord, err := coordinator.New(db.Pool, q, slog.New(slog.DiscardHandler), walLog, h.nodes, coordinator.Config{
		Replicas:        3,
		WriteQuorum:     2,
		MaxPartSize:     16 << 20,
		MultipartExpiry: 24 * 60 * 60 * 1e9,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	h.coord = coord

	user, err := q.CreateUser(context.Background(), dbgen.CreateUserParams{
		Email:        "user@example.com",
		PasswordHash: "not-used",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	h.user = user
	h.ownerID = user.ID.String()
	bucket, err := coord.CreateBucket(context.Background(), h.ownerID, "test-bucket")
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	h.bucket = bucket
	t.Cleanup(func() {
		for _, s := range h.servers {
			s.Close()
		}
		_ = h.walLog.Close(context.Background())
	})
	return h
}

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("parse uuid %s: %v", s, err)
	}
	return u
}

func (h *harness) get(t *testing.T, key string) []byte {
	t.Helper()
	reader, err := h.coord.GetObject(context.Background(), h.ownerID, h.bucket.Name, key)
	if err != nil {
		t.Fatalf("GetObject %s: %v", key, err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read object %s: %v", key, err)
	}
	return data
}

func (h *harness) count(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := h.db.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count query: %v", err)
	}
	return n
}

func TestPutGetSmallObjectIsIdempotent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	body := []byte("hello crash-safe world")

	v1, err := h.coord.PutObject(ctx, h.ownerID, h.bucket.Name, "dir/hello.txt", "text/plain", "req-1", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	v2, err := h.coord.PutObject(ctx, h.ownerID, h.bucket.Name, "dir/hello.txt", "text/plain", "req-1", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("PutObject retry: %v", err)
	}
	if v1.ID != v2.ID {
		t.Errorf("retry created a second version: %s then %s", v1.ID.String(), v2.ID.String())
	}
	if got := h.count(t, "SELECT count(*) FROM object_versions"); got != 1 {
		t.Errorf("object_versions = %d, want 1", got)
	}
	if got := h.count(t, "SELECT count(*) FROM chunks"); got != 1 {
		t.Errorf("chunks = %d, want 1", got)
	}
	if got := h.count(t, "SELECT count(*) FROM chunk_replicas WHERE status = 'healthy'"); got != 3 {
		t.Errorf("healthy replicas = %d, want 3", got)
	}
	if got := h.count(t, "SELECT count(*) FROM chunk_replicas WHERE status = 'pending'"); got != 0 {
		t.Errorf("pending replicas = %d, want 0", got)
	}

	got := h.get(t, "dir/hello.txt")
	if !bytes.Equal(got, body) {
		t.Errorf("downloaded %q, want %q", got, body)
	}
	sum := sha256.Sum256(body)
	if v1.Checksum != hex.EncodeToString(sum[:]) {
		t.Errorf("checksum = %s, want %s", v1.Checksum, hex.EncodeToString(sum[:]))
	}
}

func TestMultipartResumeAndConcurrentComplete(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	upload, err := h.coord.StartUpload(ctx, h.ownerID, h.bucket.Name, "big.bin", "application/octet-stream", "")
	if err != nil {
		t.Fatalf("StartUpload: %v", err)
	}
	part1 := bytes.Repeat([]byte("a"), 1024)
	part2 := bytes.Repeat([]byte("b"), 2048)
	for i, part := range [][]byte{part1, part2} {
		if _, err := h.coord.UploadPart(ctx, h.ownerID, h.bucket.Name, upload.ID.String(), int32(i+1), bytes.NewReader(part), ""); err != nil {
			t.Fatalf("UploadPart %d: %v", i+1, err)
		}
		parts, err := h.coord.ListUploadParts(ctx, h.ownerID, h.bucket.Name, upload.ID.String())
		if err != nil {
			t.Fatalf("ListUploadParts: %v", err)
		}
		if len(parts) != i+1 {
			t.Fatalf("resume sees %d parts, want %d", len(parts), i+1)
		}
	}

	const concurrent = 20
	type result struct {
		version dbgen.ObjectVersion
		err     error
	}
	results := make(chan result, concurrent)
	var wg sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := h.coord.CompleteUpload(ctx, h.ownerID, h.bucket.Name, upload.ID.String(), []int32{1, 2})
			results <- result{version: v, err: err}
		}()
	}
	wg.Wait()
	close(results)

	var versionID string
	for r := range results {
		if r.err != nil {
			t.Fatalf("concurrent complete: %v", r.err)
		}
		if versionID == "" {
			versionID = r.version.ID.String()
		}
		if r.version.ID.String() != versionID {
			t.Errorf("concurrent completes returned different versions: %s and %s", versionID, r.version.ID.String())
		}
	}
	if got := h.count(t, "SELECT count(*) FROM object_versions"); got != 1 {
		t.Errorf("object_versions = %d, want 1", got)
	}
	if got := h.count(t, "SELECT count(*) FROM upload_parts"); got != 2 {
		t.Errorf("upload_parts = %d, want 2", got)
	}

	want := append(append([]byte{}, part1...), part2...)
	if got := h.get(t, "big.bin"); !bytes.Equal(got, want) {
		t.Errorf("multipart download has %d bytes, want %d", len(got), len(want))
	}

	h1 := sha256.Sum256(part1)
	h2 := sha256.Sum256(part2)
	composite := sha256.New()
	composite.Write(h1[:])
	composite.Write(h2[:])
	wantChecksum := hex.EncodeToString(composite.Sum(nil)) + "-2"
	var checksum string
	if err := h.db.Pool.QueryRow(ctx, "SELECT checksum FROM object_versions WHERE id = $1", mustUUID(t, versionID)).Scan(&checksum); err != nil {
		t.Fatalf("read checksum: %v", err)
	}
	if checksum != wantChecksum {
		t.Errorf("composite checksum = %s, want %s", checksum, wantChecksum)
	}
}

func TestRangeReadsSliceChunks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	upload, err := h.coord.StartUpload(ctx, h.ownerID, h.bucket.Name, "range.bin", "application/octet-stream", "")
	if err != nil {
		t.Fatalf("StartUpload: %v", err)
	}
	parts := [][]byte{[]byte("0123456789"), []byte("abcdefghij"), []byte("KLMNOPQRST")}
	for i, part := range parts {
		if _, err := h.coord.UploadPart(ctx, h.ownerID, h.bucket.Name, upload.ID.String(), int32(i+1), bytes.NewReader(part), ""); err != nil {
			t.Fatalf("UploadPart %d: %v", i+1, err)
		}
	}
	if _, err := h.coord.CompleteUpload(ctx, h.ownerID, h.bucket.Name, upload.ID.String(), []int32{1, 2, 3}); err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	all := bytes.Join(parts, nil)

	cases := []struct {
		header  string
		want    []byte
		partial bool
		start   int64
		end     int64
	}{
		{"", all, false, 0, 29},
		{"bytes=5-15", all[5:16], true, 5, 15},
		{"bytes=15-", all[15:], true, 15, 29},
		{"bytes=-5", all[25:], true, 25, 29},
		{"bytes=0-0", all[:1], true, 0, 0},
		{"bytes=25-100", all[25:], true, 25, 29},
	}
	for _, tc := range cases {
		reader, err := h.coord.GetObjectRange(ctx, h.ownerID, h.bucket.Name, "range.bin", tc.header)
		if err != nil {
			t.Fatalf("range %q: %v", tc.header, err)
		}
		got, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatalf("range %q read: %v", tc.header, err)
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("range %q = %q, want %q", tc.header, got, tc.want)
		}
		if reader.Partial != tc.partial || reader.Start != tc.start || reader.End != tc.end || reader.FullSize != 30 {
			t.Errorf("range %q metadata = partial %v, %d-%d of %d", tc.header, reader.Partial, reader.Start, reader.End, reader.FullSize)
		}
	}

	for _, header := range []string{"bytes=30-", "bytes=5-2", "bytes=abc", "bytes=0-1,3-4", "items=0-1"} {
		if _, err := h.coord.GetObjectRange(ctx, h.ownerID, h.bucket.Name, "range.bin", header); !errors.Is(err, coordinator.ErrInvalidRange) {
			t.Errorf("range %q error = %v, want ErrInvalidRange", header, err)
		}
	}
}

func TestAbortIsIdempotent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	upload, err := h.coord.StartUpload(ctx, h.ownerID, h.bucket.Name, "abort.bin", "", "")
	if err != nil {
		t.Fatalf("StartUpload: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := h.coord.AbortUpload(ctx, h.ownerID, h.bucket.Name, upload.ID.String()); err != nil {
			t.Fatalf("AbortUpload attempt %d: %v", i+1, err)
		}
	}
	if _, err := h.coord.CompleteUpload(ctx, h.ownerID, h.bucket.Name, upload.ID.String(), nil); !errors.Is(err, coordinator.ErrUploadNotOpen) {
		t.Errorf("Complete after abort = %v, want ErrUploadNotOpen", err)
	}
}

func TestQuorumFailureLeavesNoMetadata(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for i, name := range []string{"node2", "node3"} {
		if err := h.q.SetStorageNodeStatus(ctx, dbgen.SetStorageNodeStatusParams{Name: name, Status: "down"}); err != nil {
			t.Fatalf("mark %s down: %v", name, err)
		}
		h.servers[i+1].Close()
	}
	_, err := h.coord.PutObject(ctx, h.ownerID, h.bucket.Name, "fail.bin", "", "", bytes.NewReader([]byte("needs two acks")))
	if !errors.Is(err, coordinator.ErrQuorum) {
		t.Fatalf("PutObject error = %v, want ErrQuorum", err)
	}
	if got := h.count(t, "SELECT count(*) FROM chunks"); got != 0 {
		t.Errorf("chunks = %d after failed quorum, want 0", got)
	}
	if got := h.count(t, "SELECT count(*) FROM upload_parts"); got != 0 {
		t.Errorf("upload_parts = %d after failed quorum, want 0", got)
	}
	if got := h.count(t, "SELECT count(*) FROM object_versions"); got != 0 {
		t.Errorf("object_versions = %d after failed quorum, want 0", got)
	}
	for _, root := range h.roots {
		if files := walkFiles(t, root); len(files) != 0 {
			t.Errorf("failed write left files on a node: %v", files)
		}
	}
}

func TestReadFallsBackAndMarksCorruptReplica(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	body := []byte("survive a corrupt replica")
	if _, err := h.coord.PutObject(ctx, h.ownerID, h.bucket.Name, "corrupt.bin", "", "req-corrupt", bytes.NewReader(body)); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	var chunkID string
	if err := h.db.Pool.QueryRow(ctx, "SELECT id::text FROM chunks LIMIT 1").Scan(&chunkID); err != nil {
		t.Fatalf("read chunk id: %v", err)
	}
	path := filepath.Join(h.roots[0], chunkID[:2], chunkID)
	if err := os.WriteFile(path, []byte("wrong bytes"), 0o644); err != nil {
		t.Fatalf("corrupt chunk: %v", err)
	}

	if got := h.get(t, "corrupt.bin"); !bytes.Equal(got, body) {
		t.Errorf("read did not fall back: got %q", got)
	}
	var status string
	err := h.db.Pool.QueryRow(ctx, `
		SELECT r.status FROM chunk_replicas r
		JOIN storage_nodes n ON n.id = r.node_id
		WHERE n.name = 'node1'`).Scan(&status)
	if err != nil {
		t.Fatalf("read replica status: %v", err)
	}
	if status != "corrupt" {
		t.Errorf("node1 replica status = %s, want corrupt", status)
	}
	if got := h.get(t, "corrupt.bin"); !bytes.Equal(got, body) {
		t.Errorf("second read failed: got %q", got)
	}
}

func TestDeleteObjectWritesMarker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.coord.PutObject(ctx, h.ownerID, h.bucket.Name, "gone.txt", "", "req-del", bytes.NewReader([]byte("bye"))); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := h.coord.DeleteObject(ctx, h.ownerID, h.bucket.Name, "gone.txt"); err != nil {
			t.Fatalf("DeleteObject attempt %d: %v", i+1, err)
		}
	}
	if _, err := h.coord.GetObject(ctx, h.ownerID, h.bucket.Name, "gone.txt"); !errors.Is(err, coordinator.ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
	if got := h.count(t, "SELECT count(*) FROM object_versions WHERE is_delete_marker"); got != 1 {
		t.Errorf("delete markers = %d, want 1", got)
	}
}

func TestConcurrentDeleteWritesOneMarker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.coord.PutObject(ctx, h.ownerID, h.bucket.Name, "race.txt", "text/plain", "put-race", bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	const concurrent = 20
	var wg sync.WaitGroup
	errs := make(chan error, concurrent)
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- h.coord.DeleteObject(ctx, h.ownerID, h.bucket.Name, "race.txt")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent delete: %v", err)
		}
	}
	if got := h.count(t, "SELECT count(*) FROM object_versions WHERE is_delete_marker"); got != 1 {
		t.Errorf("delete markers = %d, want 1", got)
	}
	if got := h.count(t, "SELECT count(*) FROM object_versions"); got != 2 {
		t.Errorf("object_versions = %d, want 2 (the object and one marker)", got)
	}
}

func TestBucketOwnership(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	other, err := h.q.CreateUser(ctx, dbgen.CreateUserParams{Email: "other@example.com", PasswordHash: "x"})
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	if _, err := h.coord.ListObjects(ctx, other.ID.String(), h.bucket.Name, 10, 0); !errors.Is(err, coordinator.ErrForbidden) {
		t.Errorf("foreign list = %v, want ErrForbidden", err)
	}
	if _, err := h.coord.CreateBucket(ctx, other.ID.String(), h.bucket.Name); !errors.Is(err, coordinator.ErrBucketExists) {
		t.Errorf("duplicate bucket = %v, want ErrBucketExists", err)
	}
}

func walkFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

var _ = pgx.ErrNoRows
