package coordinator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/crash"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/objects"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/wal"
)

// PutObject performs the single-object write path: create or reuse an upload,
// write one part through the WAL and quorum, then complete it.
func (c *Coordinator) PutObject(ctx context.Context, ownerID, bucketName, key, contentType, idempotencyKey string, body io.Reader) (dbgen.ObjectVersion, error) {
	if !validKey(key) {
		return dbgen.ObjectVersion{}, fmt.Errorf("%w: key must be 1-1024 characters", ErrInvalidKey)
	}
	bucket, err := c.ownedBucket(ctx, ownerID, bucketName)
	if err != nil {
		return dbgen.ObjectVersion{}, err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	data, err := readBounded(body, c.cfg.MaxPartSize)
	if err != nil {
		return dbgen.ObjectVersion{}, err
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	identity := idempotencyKey
	if identity != "" {
		identity = ownerID + ":" + uuidString(bucket.ID) + ":" + idempotencyKey
	}
	if identity == "" {
		var versionNo int64
		obj, getErr := c.q.GetObjectByKey(ctx, dbgen.GetObjectByKeyParams{BucketID: bucket.ID, Key: key})
		if getErr == nil {
			versionNo = obj.LastVersionNo
		} else if !errors.Is(getErr, pgx.ErrNoRows) {
			return dbgen.ObjectVersion{}, fmt.Errorf("look up object %s: %w", key, getErr)
		}
		identity = uuidString(bucket.ID) + "/" + key + "/" + sha + "/" + strconv.FormatInt(versionNo, 10)
	}

	upload, err := c.ensureUpload(ctx, uuidString(bucket.ID), key, contentType, identity)
	if err != nil {
		return dbgen.ObjectVersion{}, err
	}
	if upload.Status == "completed" {
		return c.versionByID(ctx, upload.CompletedVersionID)
	}

	if _, err := c.putPart(ctx, upload, 1, data, ""); err != nil {
		return dbgen.ObjectVersion{}, err
	}
	return c.CompleteUpload(ctx, ownerID, bucketName, uuidString(upload.ID), nil)
}

func (c *Coordinator) ensureUpload(ctx context.Context, bucketID, key, contentType, identity string) (dbgen.MultipartUpload, error) {
	uploadID := uploadIDForKey(identity)
	parsed, err := parseUUID(uploadID)
	if err != nil {
		return dbgen.MultipartUpload{}, ErrUploadNotFound
	}
	existing, err := c.q.GetUpload(ctx, parsed)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return dbgen.MultipartUpload{}, fmt.Errorf("get idempotent upload: %w", err)
	}
	bucketUUID, err := parseUUID(bucketID)
	if err != nil {
		return dbgen.MultipartUpload{}, ErrBucketNotFound
	}
	created, err := c.q.CreateUploadWithID(ctx, dbgen.CreateUploadWithIDParams{
		ID:          parsed,
		BucketID:    bucketUUID,
		Key:         key,
		ContentType: contentType,
		ExpiresAt:   expiry(c.cfg.Now(), c.cfg.MultipartExpiry),
	})
	if err == nil {
		return created, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return dbgen.MultipartUpload{}, fmt.Errorf("create idempotent upload: %w", err)
	}
	existing, err = c.q.GetUpload(ctx, parsed)
	if err != nil {
		return dbgen.MultipartUpload{}, fmt.Errorf("get raced idempotent upload: %w", err)
	}
	return existing, nil
}

// InitUpload starts a multipart upload. A non-empty idempotency key makes the
// upload id deterministic, so a retried init returns the same upload.
func (c *Coordinator) InitUpload(ctx context.Context, bucketID, key, contentType string) (dbgen.MultipartUpload, error) {
	return c.initUpload(ctx, bucketID, key, contentType, "")
}

// InitUploadIdempotent is InitUpload with a client-supplied idempotency key.
func (c *Coordinator) InitUploadIdempotent(ctx context.Context, bucketID, key, contentType, idempotencyKey string) (dbgen.MultipartUpload, error) {
	return c.initUpload(ctx, bucketID, key, contentType, idempotencyKey)
}

func (c *Coordinator) initUpload(ctx context.Context, bucketID, key, contentType, idempotencyKey string) (dbgen.MultipartUpload, error) {
	if !validKey(key) {
		return dbgen.MultipartUpload{}, ErrInvalidKey
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	bucket, err := parseUUID(bucketID)
	if err != nil {
		return dbgen.MultipartUpload{}, ErrBucketNotFound
	}
	if idempotencyKey != "" {
		return c.ensureUpload(ctx, bucketID, key, contentType, idempotencyKey)
	}
	upload, err := c.q.CreateUpload(ctx, dbgen.CreateUploadParams{
		BucketID:    bucket,
		Key:         key,
		ContentType: contentType,
		ExpiresAt:   expiry(c.cfg.Now(), c.cfg.MultipartExpiry),
	})
	if err != nil {
		return dbgen.MultipartUpload{}, fmt.Errorf("create upload: %w", err)
	}
	return upload, nil
}

// UploadPart stores one multipart part. The body must be 1 to MaxPartSize
// bytes. clientSHA, when set, must match the body.
func (c *Coordinator) UploadPart(ctx context.Context, ownerID, bucketName, uploadID string, partNo int32, body io.Reader, clientSHA string) (string, error) {
	if partNo < 1 {
		return "", ErrInvalidPart
	}
	bucket, err := c.ownedBucket(ctx, ownerID, bucketName)
	if err != nil {
		return "", err
	}
	id, err := parseUUID(uploadID)
	if err != nil {
		return "", ErrUploadNotFound
	}
	upload, err := c.q.GetUpload(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUploadNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get upload: %w", err)
	}
	if upload.BucketID != bucket.ID {
		return "", ErrForbidden
	}
	if upload.Status == "completed" {
		return "", ErrUploadCompleted
	}
	if upload.Status != "open" {
		return "", ErrUploadNotOpen
	}
	if c.cfg.Now().After(upload.ExpiresAt.Time) {
		return "", ErrUploadExpired
	}

	data, err := readBounded(body, c.cfg.MaxPartSize)
	if err != nil {
		return "", err
	}
	return c.putPart(ctx, upload, partNo, data, clientSHA)
}

func (c *Coordinator) putPart(ctx context.Context, upload dbgen.MultipartUpload, partNo int32, data []byte, clientSHA string) (string, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if clientSHA != "" && clientSHA != sha {
		return "", fmt.Errorf("%w: client checksum does not match body", ErrClientChecksum)
	}

	chunk := chunkID(uuidString(upload.ID), partNo, sha)
	if existing, err := c.q.GetUploadPart(ctx, dbgen.GetUploadPartParams{UploadID: upload.ID, PartNo: partNo}); err == nil {
		if uuidString(existing.ChunkID) == chunk {
			return sha, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("get existing upload part: %w", err)
	}

	ordered, err := c.orderedNodes(ctx)
	if err != nil {
		return "", err
	}
	chosen := ordered
	if len(chosen) > c.cfg.Replicas {
		chosen = chosen[:c.cfg.Replicas]
	}

	intent := wal.PutIntent{
		ChunkID:    chunk,
		UploadID:   uuidString(upload.ID),
		PartNo:     partNo,
		ChunkIndex: partNo - 1,
		SizeBytes:  int64(len(data)),
		SHA256:     sha,
		NodeIDs:    nodeIDs(chosen),
	}
	if err := c.appendWal(ctx, wal.RecordBeginPut, intent, true); err != nil {
		return "", err
	}
	crash.Point(crash.AfterWALSync)

	acked, err := c.writeChunks(ctx, chosen, chunk, sha, data)
	if err != nil {
		return "", err
	}
	if len(acked) < c.cfg.WriteQuorum {
		_ = c.appendWal(ctx, wal.RecordAbortPut, wal.ChunkRef{ChunkID: chunk}, false)
		c.deleteFromNodes(ctx, chosen, chunk)
		return "", fmt.Errorf("%w: %d of %d acks, need %d", ErrQuorum, len(acked), len(chosen), c.cfg.WriteQuorum)
	}
	crash.Point(crash.AfterNodeAck)

	if err := c.recordPart(ctx, upload, partNo, chunk, data, sha, chosen, acked); err != nil {
		return "", err
	}
	crash.Point(crash.AfterDBCommit)

	_ = c.appendWal(ctx, wal.RecordCommitPut, wal.ChunkRef{ChunkID: chunk}, false)
	return sha, nil
}

func (c *Coordinator) appendWal(ctx context.Context, typ wal.RecordType, payload any, sync bool) error {
	if c.wal == nil {
		return nil
	}
	var err error
	if sync {
		_, err = c.wal.AppendSync(ctx, typ, payload)
	} else {
		_, err = c.wal.Append(ctx, typ, payload)
	}
	if err != nil {
		return fmt.Errorf("append wal %s: %w", typ, err)
	}
	return nil
}

func (c *Coordinator) writeChunks(ctx context.Context, chosen []placement.Node, chunk, sha string, data []byte) ([]string, error) {
	type result struct {
		id  string
		err error
	}
	results := make(chan result, len(chosen))
	var wg sync.WaitGroup
	for _, n := range chosen {
		wg.Add(1)
		go func(n placement.Node) {
			defer wg.Done()
			err := n.Store.Put(ctx, chunk, sha, bytes.NewReader(data))
			results <- result{id: n.ID, err: err}
		}(n)
	}
	wg.Wait()
	close(results)

	var acked []string
	var errs []error
	for r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		acked = append(acked, r.id)
	}
	if len(acked) == 0 {
		return nil, fmt.Errorf("%w: %w", ErrQuorum, errors.Join(errs...))
	}
	return acked, nil
}

func (c *Coordinator) deleteFromNodes(ctx context.Context, nodes []placement.Node, chunk string) {
	for _, n := range nodes {
		if err := n.Store.Delete(ctx, chunk); err != nil && !objects.IsNotFound(err) {
			c.log.WarnContext(ctx, "cleaning up rejected chunk", "chunk_id", chunk, "node_id", n.ID, "err", err)
		}
	}
}

func (c *Coordinator) recordPart(ctx context.Context, upload dbgen.MultipartUpload, partNo int32, chunk string, data []byte, sha string, ordered []placement.Node, acked []string) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin part transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := c.q.WithTx(tx)

	chunkUUID, err := parseUUID(chunk)
	if err != nil {
		return ErrChunkIntegrity
	}
	row, err := qtx.UpsertStagedChunk(ctx, dbgen.UpsertStagedChunkParams{
		ID:         chunkUUID,
		ChunkIndex: partNo - 1,
		SizeBytes:  int64(len(data)),
		Sha256:     sha,
	})
	if err != nil {
		return fmt.Errorf("upsert staged chunk: %w", err)
	}
	if row.State == "deleted" {
		return fmt.Errorf("%w: chunk %s is deleted", ErrChunkIntegrity, chunk)
	}

	if err := qtx.UpsertUploadPart(ctx, dbgen.UpsertUploadPartParams{
		UploadID:  upload.ID,
		PartNo:    partNo,
		ChunkID:   chunkUUID,
		SizeBytes: int64(len(data)),
		Sha256:    sha,
	}); err != nil {
		return fmt.Errorf("upsert upload part: %w", err)
	}

	ackedSet := make(map[string]bool, len(acked))
	for _, id := range acked {
		ackedSet[id] = true
	}
	for _, n := range ordered {
		nodeUUID, err := parseUUID(n.ID)
		if err != nil {
			return fmt.Errorf("parse node id: %w", err)
		}
		status := "pending"
		if ackedSet[n.ID] {
			status = "healthy"
		}
		if err := qtx.UpsertChunkReplica(ctx, dbgen.UpsertChunkReplicaParams{
			ChunkID: chunkUUID,
			NodeID:  nodeUUID,
			Status:  status,
		}); err != nil {
			return fmt.Errorf("upsert chunk replica: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit part transaction: %w", err)
	}
	return nil
}

func (c *Coordinator) CompleteUpload(ctx context.Context, ownerID, bucketName, uploadID string, requestedParts []int32) (dbgen.ObjectVersion, error) {
	bucket, err := c.ownedBucket(ctx, ownerID, bucketName)
	if err != nil {
		return dbgen.ObjectVersion{}, err
	}
	id, err := parseUUID(uploadID)
	if err != nil {
		return dbgen.ObjectVersion{}, ErrUploadNotFound
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("begin complete transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := c.q.WithTx(tx)

	upload, err := qtx.GetUploadForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.ObjectVersion{}, ErrUploadNotFound
	}
	if err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("lock upload: %w", err)
	}
	if upload.BucketID != bucket.ID {
		return dbgen.ObjectVersion{}, ErrForbidden
	}
	if upload.Status == "completed" {
		return c.versionByIDTx(ctx, qtx, upload.CompletedVersionID)
	}
	if upload.Status != "open" {
		return dbgen.ObjectVersion{}, ErrUploadNotOpen
	}
	if c.cfg.Now().After(upload.ExpiresAt.Time) {
		if _, err := qtx.AbortUpload(ctx, id); err != nil {
			return dbgen.ObjectVersion{}, fmt.Errorf("abort expired upload: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return dbgen.ObjectVersion{}, fmt.Errorf("commit expired upload abort: %w", err)
		}
		return dbgen.ObjectVersion{}, ErrUploadExpired
	}

	parts, err := qtx.ListUploadParts(ctx, id)
	if err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("list upload parts: %w", err)
	}
	if err := validateParts(parts, requestedParts); err != nil {
		return dbgen.ObjectVersion{}, err
	}
	totalSize := int64(0)
	chunkIDs := make([]pgtype.UUID, len(parts))
	for i, p := range parts {
		totalSize += p.SizeBytes
		chunkIDs[i] = p.ChunkID
	}
	checksum, err := compositeChecksum(parts)
	if err != nil {
		return dbgen.ObjectVersion{}, err
	}

	obj, err := qtx.UpsertObject(ctx, dbgen.UpsertObjectParams{BucketID: bucket.ID, Key: upload.Key})
	if err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("lock object: %w", err)
	}
	versionNo, err := qtx.BumpObjectVersionNo(ctx, obj.ID)
	if err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("bump object version: %w", err)
	}
	version, err := qtx.CreateObjectVersion(ctx, dbgen.CreateObjectVersionParams{
		ObjectID:       obj.ID,
		VersionNo:      versionNo,
		SizeBytes:      totalSize,
		Checksum:       checksum,
		ContentType:    upload.ContentType,
		IsDeleteMarker: false,
	})
	if err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("create object version: %w", err)
	}
	if err := qtx.CommitChunks(ctx, dbgen.CommitChunksParams{VersionID: version.ID, ChunkIds: chunkIDs}); err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("commit chunks: %w", err)
	}
	committed, err := qtx.ListChunksForVersion(ctx, version.ID)
	if err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("verify committed chunks: %w", err)
	}
	if len(committed) != len(parts) {
		return dbgen.ObjectVersion{}, fmt.Errorf("%w: version has %d chunks, expected %d",
			ErrChunkIntegrity, len(committed), len(parts))
	}
	if err := qtx.SetCurrentVersion(ctx, dbgen.SetCurrentVersionParams{
		ID:               obj.ID,
		CurrentVersionID: version.ID,
	}); err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("set current version: %w", err)
	}
	if err := qtx.MarkUploadCompleted(ctx, dbgen.MarkUploadCompletedParams{
		ID:                 id,
		CompletedVersionID: version.ID,
	}); err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("mark upload completed: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("commit complete transaction: %w", err)
	}
	c.invalidateMeta(ctx, bucket.Name, upload.Key)
	if c.cfg.Metrics != nil {
		c.cfg.Metrics.ObjectWrites.Add(1)
	}
	if c.cfg.Embeddings != nil {
		if err := c.cfg.Embeddings.Enqueue(ctx, uuidString(version.ID)); err != nil {
			c.log.WarnContext(ctx, "enqueue embedding", "version_id", uuidString(version.ID), "err", err)
		}
	}
	return version, nil
}

func validateParts(parts []dbgen.UploadPart, requested []int32) error {
	if len(parts) == 0 {
		return fmt.Errorf("%w: no parts uploaded", ErrInvalidPart)
	}
	for i, p := range parts {
		if p.PartNo != int32(i+1) {
			return fmt.Errorf("%w: parts are not contiguous from 1", ErrInvalidPart)
		}
	}
	if len(requested) > 0 {
		if len(requested) != len(parts) {
			return fmt.Errorf("%w: requested %d parts, have %d", ErrInvalidPart, len(requested), len(parts))
		}
		for i, no := range requested {
			if no != parts[i].PartNo {
				return fmt.Errorf("%w: part list does not match uploaded parts", ErrInvalidPart)
			}
		}
	}
	return nil
}

func (c *Coordinator) AbortUpload(ctx context.Context, ownerID, bucketName, uploadID string) error {
	bucket, err := c.ownedBucket(ctx, ownerID, bucketName)
	if err != nil {
		return err
	}
	id, err := parseUUID(uploadID)
	if err != nil {
		return ErrUploadNotFound
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin abort transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := c.q.WithTx(tx)
	upload, err := qtx.GetUploadForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUploadNotFound
	}
	if err != nil {
		return fmt.Errorf("lock upload: %w", err)
	}
	if upload.BucketID != bucket.ID {
		return ErrForbidden
	}
	if upload.Status == "completed" {
		return ErrUploadCompleted
	}
	if upload.Status == "aborted" {
		return nil
	}
	if _, err := qtx.AbortUpload(ctx, id); err != nil {
		return fmt.Errorf("abort upload: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit abort transaction: %w", err)
	}
	return nil
}

func (c *Coordinator) ListUploadParts(ctx context.Context, ownerID, bucketName, uploadID string) ([]dbgen.UploadPart, error) {
	bucket, err := c.ownedBucket(ctx, ownerID, bucketName)
	if err != nil {
		return nil, err
	}
	id, err := parseUUID(uploadID)
	if err != nil {
		return nil, ErrUploadNotFound
	}
	upload, err := c.q.GetUpload(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUploadNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get upload: %w", err)
	}
	if upload.BucketID != bucket.ID {
		return nil, ErrForbidden
	}
	parts, err := c.q.ListUploadParts(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list upload parts: %w", err)
	}
	return parts, nil
}

func (c *Coordinator) versionByID(ctx context.Context, id pgtype.UUID) (dbgen.ObjectVersion, error) {
	return c.versionByIDQ(ctx, c.q, id)
}

func (c *Coordinator) versionByIDTx(ctx context.Context, qtx *dbgen.Queries, id pgtype.UUID) (dbgen.ObjectVersion, error) {
	return c.versionByIDQ(ctx, qtx, id)
}

func (c *Coordinator) versionByIDQ(ctx context.Context, q *dbgen.Queries, id pgtype.UUID) (dbgen.ObjectVersion, error) {
	version, err := q.GetObjectVersionByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.ObjectVersion{}, ErrNotFound
	}
	if err != nil {
		return dbgen.ObjectVersion{}, fmt.Errorf("get object version: %w", err)
	}
	return version, nil
}

// DeleteObject writes a delete marker as a new version.
func (c *Coordinator) DeleteObject(ctx context.Context, ownerID, bucketName, key string) error {
	bucket, err := c.ownedBucket(ctx, ownerID, bucketName)
	if err != nil {
		return err
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := c.q.WithTx(tx)

	obj, err := qtx.GetObjectForUpdate(ctx, dbgen.GetObjectForUpdateParams{BucketID: bucket.ID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get object: %w", err)
	}
	current, err := qtx.GetCurrentVersion(ctx, dbgen.GetCurrentVersionParams{BucketID: bucket.ID, Key: key})
	if err == nil && current.IsDeleteMarker {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("get current version: %w", err)
	}
	versionNo, err := qtx.BumpObjectVersionNo(ctx, obj.ID)
	if err != nil {
		return fmt.Errorf("bump object version: %w", err)
	}
	marker, err := qtx.CreateObjectVersion(ctx, dbgen.CreateObjectVersionParams{
		ObjectID:       obj.ID,
		VersionNo:      versionNo,
		SizeBytes:      0,
		Checksum:       emptyChecksum(),
		ContentType:    "application/octet-stream",
		IsDeleteMarker: true,
	})
	if err != nil {
		return fmt.Errorf("create delete marker: %w", err)
	}
	if err := qtx.SetCurrentVersion(ctx, dbgen.SetCurrentVersionParams{
		ID:               obj.ID,
		CurrentVersionID: marker.ID,
	}); err != nil {
		return fmt.Errorf("set current version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete transaction: %w", err)
	}
	c.invalidateMeta(ctx, bucket.Name, key)
	return nil
}

func (c *Coordinator) ListObjects(ctx context.Context, ownerID, bucketName string, limit, offset int32) ([]dbgen.ListObjectsRow, error) {
	bucket, err := c.ownedBucket(ctx, ownerID, bucketName)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := c.q.ListObjects(ctx, dbgen.ListObjectsParams{
		BucketID: bucket.ID,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}
	return rows, nil
}

func readBounded(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(data)) > max {
		return nil, ErrTooLarge
	}
	return data, nil
}

func nodeIDs(nodes []placement.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}
