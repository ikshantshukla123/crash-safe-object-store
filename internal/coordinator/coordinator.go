// Package coordinator implements the single write coordinator for the object
// store: quorum chunk writes, multipart completion and verified reads.
package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrForbidden       = errors.New("forbidden")
	ErrBucketExists    = errors.New("bucket already exists")
	ErrBucketNotFound  = errors.New("bucket not found")
	ErrBucketNotEmpty  = errors.New("bucket is not empty")
	ErrUploadNotFound  = errors.New("upload not found")
	ErrUploadNotOpen   = errors.New("upload is not open")
	ErrUploadExpired   = errors.New("upload expired")
	ErrUploadCompleted = errors.New("upload already completed")
	ErrInvalidPart     = errors.New("invalid part list")
	ErrTooLarge        = errors.New("body too large")
	ErrQuorum          = errors.New("write quorum not reached")
	ErrChunkIntegrity  = errors.New("chunk failed verification")
	ErrInvalidKey      = errors.New("invalid object key")
	ErrInvalidRange    = errors.New("invalid range")
	ErrInvalidBucket   = errors.New("invalid bucket name")
	ErrClientChecksum  = errors.New("client checksum mismatch")
)

var (
	uploadNamespace = uuid.MustParse("2f1c5f4e-6f4f-4f4f-9f4f-000000000001")
	chunkNamespace  = uuid.MustParse("2f1c5f4e-6f4f-4f4f-9f4f-000000000002")
)

func chunkID(uploadID string, partNo int32, sha string) string {
	return uuid.NewSHA1(chunkNamespace, []byte(uploadID+":"+strconv.Itoa(int(partNo))+":"+sha)).String()
}

func uploadIDForKey(key string) string {
	return uuid.NewSHA1(uploadNamespace, []byte(key)).String()
}

func parseUUID(s string) (pgtype.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("parse uuid %q: %w", s, err)
	}
	return pgtype.UUID{Bytes: u, Valid: true}, nil
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

func compositeChecksum(parts []dbgen.UploadPart) (string, error) {
	if len(parts) == 1 {
		return parts[0].Sha256, nil
	}
	h := sha256.New()
	for _, p := range parts {
		raw, err := hex.DecodeString(p.Sha256)
		if err != nil {
			return "", fmt.Errorf("decode part checksum: %w", err)
		}
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(parts)), nil
}

func emptyChecksum() string {
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}

func validKey(key string) bool {
	return key != "" && len(key) <= 1024
}

func validBucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '.' || r == '-') && i > 0 && i < len(name)-1:
		default:
			return false
		}
	}
	return true
}

func expiry(now time.Time, d time.Duration) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: now.Add(d), Valid: true}
}
