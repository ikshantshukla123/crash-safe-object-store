package coordinator

import (
	"context"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
)

// StartUpload validates ownership before creating a multipart upload.
func (c *Coordinator) StartUpload(ctx context.Context, ownerID, bucketName, key, contentType, idempotencyKey string) (dbgen.MultipartUpload, error) {
	bucket, err := c.ownedBucket(ctx, ownerID, bucketName)
	if err != nil {
		return dbgen.MultipartUpload{}, err
	}
	if idempotencyKey != "" {
		idempotencyKey = ownerID + ":" + uuidString(bucket.ID) + ":" + idempotencyKey
	}
	return c.initUpload(ctx, uuidString(bucket.ID), key, contentType, idempotencyKey)
}
