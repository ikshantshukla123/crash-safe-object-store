package objects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func newStore(t *testing.T) (*LocalChunkStore, string) {
	t.Helper()
	root := t.TempDir()
	s, err := NewLocalChunkStore(root)
	if err != nil {
		t.Fatalf("NewLocalChunkStore: %v", err)
	}
	return s, root
}

const testChunkID = "abc123def456"

func TestPutGetRoundTrip(t *testing.T) {
	s, _ := newStore(t)
	data := []byte("some chunk bytes")

	if err := s.Put(context.Background(), testChunkID, sha256hex(data), strings.NewReader(string(data))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, err := s.Get(context.Background(), testChunkID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading chunk: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

// Writing the same chunk twice must succeed and leave identical state. Every
// retry in this system depends on that.
func TestPutIsIdempotent(t *testing.T) {
	s, root := newStore(t)
	data := []byte("repeat me")
	sum := sha256hex(data)

	for i := 0; i < 3; i++ {
		if err := s.Put(context.Background(), testChunkID, sum, strings.NewReader(string(data))); err != nil {
			t.Fatalf("Put attempt %d: %v", i+1, err)
		}
	}

	files := walkFiles(t, root)
	if len(files) != 1 {
		t.Errorf("after three identical writes there are %d files, want 1: %v", len(files), files)
	}
}

// Bytes that do not match the promised checksum must never become visible,
// and must not leave a partial file behind.
func TestPutRejectsChecksumMismatch(t *testing.T) {
	s, root := newStore(t)

	err := s.Put(context.Background(), testChunkID, sha256hex([]byte("expected")), strings.NewReader("actual"))
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Put error = %v, want ErrChecksumMismatch", err)
	}

	if files := walkFiles(t, root); len(files) != 0 {
		t.Errorf("a rejected write left files behind: %v", files)
	}
	if _, err := s.Get(context.Background(), testChunkID); !errors.Is(err, ErrChunkNotFound) {
		t.Errorf("rejected chunk is readable; Get error = %v, want ErrChunkNotFound", err)
	}
}

func TestGetMissingChunk(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Get(context.Background(), testChunkID); !errors.Is(err, ErrChunkNotFound) {
		t.Errorf("Get error = %v, want ErrChunkNotFound", err)
	}
}

// Deleting something already gone is success, so a retried delete does not
// turn into a spurious failure.
func TestDeleteIsIdempotent(t *testing.T) {
	s, _ := newStore(t)
	data := []byte("delete me")

	if err := s.Put(context.Background(), testChunkID, sha256hex(data), strings.NewReader(string(data))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := s.Delete(context.Background(), testChunkID); err != nil {
			t.Fatalf("Delete attempt %d: %v", i+1, err)
		}
	}
}

func TestSuccessfulPutLeavesNoTempFiles(t *testing.T) {
	s, root := newStore(t)
	data := []byte("clean up after yourself")

	if err := s.Put(context.Background(), testChunkID, sha256hex(data), strings.NewReader(string(data))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for _, f := range walkFiles(t, root) {
		if strings.Contains(f, ".tmp-") {
			t.Errorf("temp file survived a successful put: %s", f)
		}
	}
}

// A crash can leave a partial write behind. Those are garbage: they were
// never renamed into place, so nothing references them.
func TestStartupRemovesStaleTempFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ab")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := filepath.Join(dir, "abc123.tmp-999")
	if err := os.WriteFile(stale, []byte("half a chunk"), 0o644); err != nil {
		t.Fatalf("writing stale temp: %v", err)
	}

	if _, err := NewLocalChunkStore(root); err != nil {
		t.Fatalf("NewLocalChunkStore: %v", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Error("stale temp file survived startup")
	}
}

// A chunk id is used to build a filesystem path, so it must never be able to
// point outside the store root.
func TestRejectsPathTraversal(t *testing.T) {
	s, _ := newStore(t)
	for _, bad := range []string{"../etc/passwd", "a/b", "..", "x", `a\b`} {
		if err := s.Put(context.Background(), bad, sha256hex(nil), strings.NewReader("")); err == nil {
			t.Errorf("Put accepted dangerous id %q", bad)
		}
		if _, err := s.Get(context.Background(), bad); err == nil {
			t.Errorf("Get accepted dangerous id %q", bad)
		}
	}
}

func walkFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return out
}
