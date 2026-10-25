// Package objects stores and retrieves chunk bytes and the object write path.
package objects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"
)

var (
	// ErrChunkNotFound means no bytes are stored under that chunk id.
	ErrChunkNotFound = errors.New("chunk not found")
	// ErrChecksumMismatch means the bytes written did not hash to the
	// checksum the caller promised. The write is discarded.
	ErrChecksumMismatch = errors.New("chunk checksum mismatch")
	// ErrOutOfSpace means the storage node has no room for the chunk.
	ErrOutOfSpace = errors.New("storage node out of space")
)

// ChunkStore holds immutable chunk bytes addressed by chunk id.
//
// It is an interface so the coordinator can be tested without touching a disk,
// and so Phase 3 can swap the local backend for real storage nodes without the
// write path noticing.
// ChunkInfo is one stored chunk file. GC uses it to find files whose database
// row disappeared (for example after a bucket delete cascaded).
type ChunkInfo struct {
	ID       string    `json:"id"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// Lister is implemented by stores that can enumerate their chunks.
type Lister interface {
	List(ctx context.Context) ([]ChunkInfo, error)
}

// Purger is implemented by stores that can remove every chunk (demo reset).
type Purger interface {
	PurgeAll(ctx context.Context) error
}

type ChunkStore interface {
	// Put stores r under id, verifying that the bytes hash to wantSHA256.
	//
	// Put is idempotent: writing the same id with the same bytes again
	// succeeds and changes nothing. Writing the same id with *different*
	// bytes returns ErrChecksumMismatch, because chunks are immutable and a
	// chunk id must never refer to two different contents.
	Put(ctx context.Context, id, wantSHA256 string, r io.Reader) error

	// Get returns the bytes stored under id. The caller must close it.
	Get(ctx context.Context, id string) (io.ReadCloser, error)

	// Delete removes id. Deleting something already gone is success.
	Delete(ctx context.Context, id string) error
}

// CopyVerified copies one chunk between two stores, verifying the source bytes
// against the checksum before the target ever sees them.
func CopyVerified(ctx context.Context, source, target ChunkStore, id string, sizeBytes int64, sha string) error {
	rc, err := source.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("read source chunk %s: %w", id, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, sizeBytes+1))
	if err != nil {
		return fmt.Errorf("read source chunk %s: %w", id, err)
	}
	if int64(len(data)) != sizeBytes {
		return fmt.Errorf("%w: source chunk %s has %d bytes, expected %d",
			ErrChecksumMismatch, id, len(data), sizeBytes)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != sha {
		return fmt.Errorf("%w: source chunk %s failed sha256", ErrChecksumMismatch, id)
	}
	if err := target.Put(ctx, id, sha, bytesReader(data)); err != nil {
		if !errors.Is(err, ErrChecksumMismatch) {
			return fmt.Errorf("write target chunk %s: %w", id, err)
		}
		// A corrupt copy already occupies the immutable path. Remove it and
		// retry once; the source was verified above, so the bytes are good.
		if delErr := target.Delete(ctx, id); delErr != nil && !IsNotFound(delErr) {
			return fmt.Errorf("remove corrupt target chunk %s: %w", id, delErr)
		}
		if retryErr := target.Put(ctx, id, sha, bytesReader(data)); retryErr != nil {
			return fmt.Errorf("rewrite target chunk %s: %w", id, retryErr)
		}
	}
	return nil
}

type byteReader struct {
	data []byte
	off  int
}

func bytesReader(b []byte) *byteReader { return &byteReader{data: b} }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
