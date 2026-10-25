package coordinator

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
)

// ListVersions returns every version for a key, newest first, including delete
// markers. Only the bucket owner can list them.
func (c *Coordinator) ListVersions(ctx context.Context, ownerID, bucketName, key string) ([]dbgen.ObjectVersion, error) {
	bucket, err := c.ownedBucket(ctx, ownerID, bucketName)
	if err != nil {
		return nil, err
	}
	if !validKey(key) {
		return nil, ErrInvalidKey
	}
	versions, err := c.q.ListObjectVersions(ctx, dbgen.ListObjectVersionsParams{
		BucketID: bucket.ID,
		Key:      key,
	})
	if err != nil {
		return nil, fmt.Errorf("list versions for %s: %w", key, err)
	}
	return versions, nil
}

// Rollback points objects.current_version_id at an existing version. It copies
// no bytes: chunks already belong to that version.
func (c *Coordinator) Rollback(ctx context.Context, ownerID, bucketName, key, versionID string) (dbgen.ObjectVersion, error) {
	bucket, err := c.ownedBucket(ctx, ownerID, bucketName)
	if err != nil {
		return dbgen.ObjectVersion{}, err
	}
	if !validKey(key) {
		return dbgen.ObjectVersion{}, ErrInvalidKey
	}
	versionUUID, err := parseUUID(versionID)
	if err != nil {
		return dbgen.ObjectVersion{}, ErrNotFound
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("begin rollback: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	qtx := c.q.WithTx(tx)

	obj, err := qtx.GetObjectForUpdate(ctx, dbgen.GetObjectForUpdateParams{BucketID: bucket.ID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.ObjectVersion{}, ErrNotFound
	}
	if err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("lock object: %w", err)
	}
	version, err := qtx.GetVersionForObject(ctx, dbgen.GetVersionForObjectParams{
		ID:       versionUUID,
		BucketID: bucket.ID,
		Key:      key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.ObjectVersion{}, ErrNotFound
	}
	if err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("get rollback version: %w", err)
	}
	if err := qtx.SetCurrentVersion(ctx, dbgen.SetCurrentVersionParams{
		ID:               obj.ID,
		CurrentVersionID: version.ID,
	}); err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("set rolled back version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("commit rollback: %w", err)
	}
	c.invalidateMeta(ctx, bucket.Name, key)
	return version, nil
}
