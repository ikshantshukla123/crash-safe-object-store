package objects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LocalChunkStore keeps chunks on a local filesystem at
// <root>/<first-2-hex-of-id>/<id>.
//
// The two-character fan-out keeps any single directory from holding hundreds
// of thousands of entries, which slows down lookups on most filesystems.
type LocalChunkStore struct {
	root string
}

var _ ChunkStore = (*LocalChunkStore)(nil)

// NewLocalChunkStore prepares root and removes any temp files left behind by
// a crash. Those are partial writes that were never renamed into place, so
// they are garbage by definition.
func NewLocalChunkStore(root string) (*LocalChunkStore, error) {
	if root == "" {
		return nil, errors.New("chunk store root must not be empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create chunk store root %s: %w", root, err)
	}
	s := &LocalChunkStore{root: root}
	if err := s.cleanTemp(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *LocalChunkStore) path(id string) string {
	return filepath.Join(s.root, id[:2], id)
}

// Put streams r to a temporary file while hashing, verifies the digest, then
// atomically renames it into place.
//
// The ordering matters and is the whole point of this function:
//
//  1. write to a temp name, so a crash never leaves a half-written file under
//     a real chunk id
//  2. verify the hash before publishing, so corrupt bytes are never visible
//  3. fsync the file, so the data is on disk and not just in the page cache
//  4. rename, which is atomic: readers see either no file or the whole file
//  5. fsync the directory, so the rename itself survives a power cut
//
// Skipping step 5 is a real bug, not a nicety. Without it the file contents
// are durable but the directory entry pointing at them may not be.
func (s *LocalChunkStore) Put(ctx context.Context, id, wantSHA256 string, r io.Reader) error {
	if err := validateID(id); err != nil {
		return err
	}
	if len(wantSHA256) != 64 {
		return fmt.Errorf("%w: expected checksum must be 64 hex characters", ErrChecksumMismatch)
	}

	final := s.path(id)
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create chunk dir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, id+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp chunk file: %w", err)
	}
	tmpName := tmp.Name()
	// Runs on every failure path. Once the rename succeeds the temp name no
	// longer exists, so the remove is a harmless no-op.
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), r); err != nil {
		return fmt.Errorf("write chunk %s: %w", id, err)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if got != wantSHA256 {
		return fmt.Errorf("%w: chunk %s hashed to %s, expected %s",
			ErrChecksumMismatch, id, got, wantSHA256)
	}

	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("fsync chunk %s: %w", id, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close chunk %s: %w", id, err)
	}

	// An existing file with the same id already holds these exact bytes,
	// because the id is derived from the content hash and we just verified
	// it. Rename over it anyway: it is atomic and costs nothing.
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("publish chunk %s: %w", id, err)
	}

	return fsyncDir(dir)
}

func (s *LocalChunkStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	f, err := os.Open(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrChunkNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("open chunk %s: %w", id, err)
	}
	return f, nil
}

// Delete removes a chunk. Deleting one that is already gone is success, so a
// retried delete does not fail.
func (s *LocalChunkStore) Delete(ctx context.Context, id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete chunk %s: %w", id, err)
	}
	return nil
}

// cleanTemp deletes *.tmp-* files left by writes interrupted mid-flight.
func (s *LocalChunkStore) cleanTemp() error {
	matches, err := filepath.Glob(filepath.Join(s.root, "*", "*.tmp-*"))
	if err != nil {
		return fmt.Errorf("scan for temp chunks: %w", err)
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale temp chunk %s: %w", m, err)
		}
	}
	return nil
}

// fsyncDir flushes a directory entry so a rename survives a crash.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir %s for fsync: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync dir %s: %w", dir, err)
	}
	return nil
}

// validateID rejects anything that could escape the root directory or produce
// a path shorter than the two-character fan-out needs.
func validateID(id string) error {
	if len(id) < 3 || strings.ContainsAny(id, `/\.`) {
		return fmt.Errorf("invalid chunk id %q", id)
	}
	return nil
}
