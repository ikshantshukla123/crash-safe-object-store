package coordinator

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
)

func (c *Coordinator) CreateBucket(ctx context.Context, ownerID, name string) (dbgen.Bucket, error) {
	if !validBucketName(name) {
		return dbgen.Bucket{}, ErrInvalidBucket
	}
	owner, err := parseUUID(ownerID)
	if err != nil {
		return dbgen.Bucket{}, ErrForbidden
	}
	bucket, err := c.q.CreateBucket(ctx, dbgen.CreateBucketParams{Name: name, OwnerID: owner})
	if err == nil {
		return bucket, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Bucket{}, fmt.Errorf("create bucket %s: %w", name, err)
	}
	existing, getErr := c.q.GetBucketByName(ctx, name)
	if getErr != nil {
		return dbgen.Bucket{}, fmt.Errorf("look up existing bucket %s: %w", name, getErr)
	}
	return existing, ErrBucketExists
}

// CheckBucketOwner verifies that ownerID owns bucketName. Handlers call it
// before minting credentials that bypass JWT, such as presigned URLs.
func (c *Coordinator) CheckBucketOwner(ctx context.Context, ownerID, name string) error {
	_, err := c.ownedBucket(ctx, ownerID, name)
	return err
}

func (c *Coordinator) ListBuckets(ctx context.Context, ownerID string) ([]dbgen.Bucket, error) {
	owner, err := parseUUID(ownerID)
	if err != nil {
		return nil, ErrForbidden
	}
	buckets, err := c.q.ListBucketsByOwner(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("list buckets: %w", err)
	}
	return buckets, nil
}

func (c *Coordinator) DeleteBucket(ctx context.Context, ownerID, name string) error {
	owner, err := parseUUID(ownerID)
	if err != nil {
		return ErrForbidden
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete bucket %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	qtx := c.q.WithTx(tx)

	// Lock the bucket row. Inserting an object takes a key-share lock on this
	// row, so this wait prevents an object appearing between the emptiness
	// check and the delete.
	bucket, err := qtx.GetBucketByNameForUpdate(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock bucket %s: %w", name, err)
	}
	if bucket.OwnerID != owner {
		return ErrForbidden
	}

	live, err := qtx.CountLiveObjectsInBucket(ctx, bucket.ID)
	if err != nil {
		return fmt.Errorf("count live objects in bucket %s: %w", name, err)
	}
	if live > 0 {
		return ErrBucketNotEmpty
	}
	if err := qtx.DeleteUploadsForBucket(ctx, bucket.ID); err != nil {
		return fmt.Errorf("delete uploads in bucket %s: %w", name, err)
	}
	if err := qtx.DeleteObjectRowsForBucket(ctx, bucket.ID); err != nil {
		return fmt.Errorf("delete object rows in bucket %s: %w", name, err)
	}
	n, err := qtx.DeleteBucket(ctx, dbgen.DeleteBucketParams{ID: bucket.ID, OwnerID: bucket.OwnerID})
	if err != nil {
		return fmt.Errorf("delete bucket %s: %w", name, err)
	}
	if n == 0 {
		return ErrBucketNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete bucket %s: %w", name, err)
	}
	return nil
}

func (c *Coordinator) ownedBucket(ctx context.Context, ownerID, name string) (dbgen.Bucket, error) {
	bucket, err := c.q.GetBucketByName(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Bucket{}, ErrBucketNotFound
	}
	if err != nil {
		return dbgen.Bucket{}, fmt.Errorf("get bucket %s: %w", name, err)
	}
	owner, err := parseUUID(ownerID)
	if err != nil {
		return dbgen.Bucket{}, ErrForbidden
	}
	if bucket.OwnerID != owner {
		return dbgen.Bucket{}, ErrForbidden
	}
	return bucket, nil
}
