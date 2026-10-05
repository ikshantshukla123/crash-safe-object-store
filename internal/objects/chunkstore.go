// Package objects stores and retrieves chunk bytes and the object write path.
package objects

import (
	"context"
	"errors"
	"io"
)

var (
	// ErrChunkNotFound means no bytes are stored under that chunk id.
	ErrChunkNotFound = errors.New("chunk not found")
	// ErrChecksumMismatch means the bytes written did not hash to the
	// checksum the caller promised. The write is discarded.
	ErrChecksumMismatch = errors.New("chunk checksum mismatch")
)

// ChunkStore holds immutable chunk bytes addressed by chunk id.
//
// It is an interface so the coordinator can be tested without touching a disk,
// and so Phase 3 can swap the local backend for real storage nodes without the
// write path noticing.
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
