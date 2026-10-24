// Package testutil provides real-Postgres test fixtures.
package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var schemaCounter atomic.Uint64

// URL returns the Postgres URL used by tests, or "" when none is configured.
func URL() string {
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	return os.Getenv("DATABASE_URL")
}

// DB is an isolated schema with every migration applied.
type DB struct {
	Pool   *pgxpool.Pool
	Schema string
	URL    string
}

// NewDB creates a fresh schema, applies migrations and drops it on cleanup.
// It skips the test when no Postgres is reachable.
func NewDB(t *testing.T) *DB {
	t.Helper()
	url := URL()
	if url == "" {
		t.Skip("TEST_DATABASE_URL or DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("postgres is not reachable: %v", err)
	}
	// CREATE EXTENSION is database-wide, so concurrent test schemas race on
	// the extension catalog. Serialise schema setup and migrations.
	if _, err := admin.Exec(ctx, "SELECT pg_advisory_lock(741852963)"); err != nil {
		admin.Close(ctx)
		t.Fatalf("acquire migration lock: %v", err)
	}
	unlock := func() {
		_, _ = admin.Exec(context.Background(), "SELECT pg_advisory_unlock(741852963)")
	}
	schema := fmt.Sprintf("test_%d_%d", os.Getpid(), schemaCounter.Add(1))
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		unlock()
		admin.Close(ctx)
		t.Fatalf("create test schema: %v", err)
	}

	pool, err := NewPool(ctx, url, schema)
	if err != nil {
		unlock()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close(ctx)
		t.Fatalf("create test pool: %v", err)
	}
	if err := ApplyMigrations(ctx, pool); err != nil {
		pool.Close()
		unlock()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close(ctx)
		t.Fatalf("apply migrations: %v", err)
	}
	unlock()

	db := &DB{Pool: pool, Schema: schema, URL: url}
	t.Cleanup(func() {
		db.Pool.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close(cleanupCtx)
	})
	return db
}

// NewPool opens a pool whose connections use the given schema.
func NewPool(ctx context.Context, url, schema string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping pool: %w", err)
	}
	return pool, nil
}

// ApplyMigrations runs every *.up.sql migration in filename order.
func ApplyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	dir, err := migrationsDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".sql" && strings.Contains(e.Name(), ".up.sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	// Extensions are database-wide, so their objects must live in a schema
	// every test schema can see. The migration itself does CREATE EXTENSION
	// IF NOT EXISTS; doing it first in public makes that a no-op instead of
	// installing the functions into whichever schema ran first.
	if _, err := pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public"); err != nil {
		return fmt.Errorf("ensure pg_trgm in public: %w", err)
	}
	if _, err := pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector SCHEMA public"); err != nil {
		return fmt.Errorf("ensure vector in public: %w", err)
	}
	for _, name := range files {
		sql, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

func migrationsDir() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("locate testutil source")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations"), nil
}
