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
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ikshantshukla123/crash-safe-object-store/internal/cache"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/db/dbgen"
	"github.com/ikshantshukla123/crash-safe-object-store/internal/placement"
)

// ObjectReader streams a verified object. Close must always be called.
type ObjectReader struct {
	Version     dbgen.ObjectVersion
	ContentType string
	Size        int64
	FullSize    int64
	Start       int64
	End         int64
	Partial     bool
	body        io.ReadCloser
}

func (o *ObjectReader) Read(p []byte) (int, error) {
	return o.body.Read(p)
}

func (o *ObjectReader) Close() error {
	return o.body.Close()
}

func (c *Coordinator) GetObject(ctx context.Context, ownerID, bucketName, key string) (*ObjectReader, error) {
	return c.GetObjectRange(ctx, ownerID, bucketName, key, "")
}

// GetObjectRange opens an object and optionally applies an HTTP Range header.
// The returned reader verifies every chunk before slicing it.
func (c *Coordinator) GetObjectRange(ctx context.Context, ownerID, bucketName, key, rangeHeader string) (*ObjectReader, error) {
	bucket, err := c.ownedBucket(ctx, ownerID, bucketName)
	if err != nil {
		return nil, err
	}
	return c.openObjectByBucket(ctx, bucket.ID, bucket.Name, key, rangeHeader)
}

// GetObjectPublic opens an object without an owner check. Callers must already
// have verified a presigned URL.
func (c *Coordinator) GetObjectPublic(ctx context.Context, bucketName, key string) (*ObjectReader, error) {
	return c.GetObjectPublicRange(ctx, bucketName, key, "")
}

// GetObjectPublicRange is GetObjectPublic with an HTTP Range header.
func (c *Coordinator) GetObjectPublicRange(ctx context.Context, bucketName, key, rangeHeader string) (*ObjectReader, error) {
	bucket, err := c.q.GetBucketByName(ctx, bucketName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBucketNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get bucket %s: %w", bucketName, err)
	}
	return c.openObjectByBucket(ctx, bucket.ID, bucket.Name, key, rangeHeader)
}

func (c *Coordinator) openObjectByBucket(ctx context.Context, bucketID pgtype.UUID, bucketName, key, rangeHeader string) (*ObjectReader, error) {
	if c.cfg.Metrics != nil {
		c.cfg.Metrics.ObjectReads.Add(1)
	}

	version, metaHit, cacheable, err := c.versionForRead(ctx, bucketID, bucketName, key, rangeHeader == "")
	if err != nil {
		return nil, err
	}
	if version.IsDeleteMarker {
		return nil, ErrNotFound
	}

	chunks, err := c.q.ListChunksForVersion(ctx, version.ID)
	if err != nil {
		return nil, fmt.Errorf("list chunks for version: %w", err)
	}
	var total int64
	for i, chunk := range chunks {
		if chunk.ChunkIndex != int32(i) {
			return nil, fmt.Errorf("%w: version %s has a gap at chunk %d",
				ErrChunkIntegrity, uuidString(version.ID), i)
		}
		total += chunk.SizeBytes
	}
	if total != version.SizeBytes {
		return nil, fmt.Errorf("%w: version %s has %d chunk bytes, expected %d",
			ErrChunkIntegrity, uuidString(version.ID), total, version.SizeBytes)
	}

	start, end, partial, err := parseRange(rangeHeader, version.SizeBytes)
	if err != nil {
		return nil, err
	}

	reader := &ObjectReader{
		Version:     version,
		ContentType: version.ContentType,
		Size:        end - start + 1,
		FullSize:    version.SizeBytes,
		Start:       start,
		End:         end,
		Partial:     partial,
	}

	// Full objects under the cache limit are fetched and verified before the
	// response starts. That lets us cache immutable version bytes and still
	// guarantee a cache hit never serves bytes that skipped verification.
	if cacheable && !partial && c.cfg.Cache != nil && c.cfg.SmallObjectLimit > 0 && version.SizeBytes <= c.cfg.SmallObjectLimit {
		if cached, ok := c.cachedObject(ctx, version.ID); ok {
			c.recordCache(true)
			reader.body = io.NopCloser(bytes.NewReader(cached))
			return reader, nil
		}
		c.recordCache(false)
		data, err := c.readAllChunks(ctx, chunks)
		if err != nil {
			return nil, err
		}
		reader.body = io.NopCloser(bytes.NewReader(data))
		c.cacheObject(ctx, version.ID, bucketName, key, data)
		return reader, nil
	}
	if c.cfg.Cache != nil && cacheable {
		c.recordCache(metaHit)
	}

	pr, pw := io.Pipe()
	reader.body = pr
	go c.streamChunks(ctx, chunks, start, end, pw)
	return reader, nil
}

func (c *Coordinator) versionForRead(ctx context.Context, bucketID pgtype.UUID, bucketName, key string, allowCache bool) (dbgen.ObjectVersion, bool, bool, error) {
	if allowCache && c.cfg.Cache != nil {
		if raw, err := c.cfg.Cache.Get(ctx, cache.MetaKey(bucketName, key)); err == nil {
			if id, parseErr := parseUUID(string(raw)); parseErr == nil {
				if version, err := c.q.GetObjectVersionByID(ctx, id); err == nil {
					return version, true, true, nil
				}
			}
			_ = c.cfg.Cache.Delete(ctx, cache.MetaKey(bucketName, key))
		}
	}

	version, err := c.q.GetCurrentVersion(ctx, dbgen.GetCurrentVersionParams{BucketID: bucketID, Key: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.ObjectVersion{}, false, false, ErrNotFound
	}
	if err != nil {
		return dbgen.ObjectVersion{}, false, false, fmt.Errorf("get current version %s: %w", key, err)
	}
	return version, false, true, nil
}

func (c *Coordinator) cachedObject(ctx context.Context, versionID pgtype.UUID) ([]byte, bool) {
	raw, err := c.cfg.Cache.Get(ctx, cache.ObjectKey(uuidString(versionID)))
	if err != nil {
		return nil, false
	}
	return raw, true
}

func (c *Coordinator) cacheObject(ctx context.Context, versionID pgtype.UUID, bucketName, key string, data []byte) {
	if c.cfg.Cache == nil {
		return
	}
	if err := c.cfg.Cache.Set(ctx, cache.ObjectKey(uuidString(versionID)), data, 0); err != nil {
		c.log.WarnContext(ctx, "caching object", "version_id", uuidString(versionID), "err", err)
	}
	if err := c.cfg.Cache.Set(ctx, cache.MetaKey(bucketName, key), []byte(uuidString(versionID)), c.cfg.CacheTTL); err != nil {
		c.log.WarnContext(ctx, "caching metadata pointer", "bucket", bucketName, "key", key, "err", err)
	}
}

func (c *Coordinator) recordCache(hit bool) {
	if c.cfg.Metrics == nil {
		return
	}
	if hit {
		c.cfg.Metrics.CacheHits.Add(1)
		return
	}
	c.cfg.Metrics.CacheMisses.Add(1)
}

func (c *Coordinator) invalidateMeta(ctx context.Context, bucketName, key string) {
	if c.cfg.Cache == nil {
		return
	}
	if err := c.cfg.Cache.Delete(ctx, cache.MetaKey(bucketName, key)); err != nil {
		c.log.WarnContext(ctx, "invalidating metadata cache", "bucket", bucketName, "key", key, "err", err)
	}
}

func (c *Coordinator) readAllChunks(ctx context.Context, chunks []dbgen.Chunk) ([]byte, error) {
	var out bytes.Buffer
	for _, chunk := range chunks {
		data, err := c.fetchChunk(ctx, chunk)
		if err != nil {
			return nil, err
		}
		out.Write(data)
	}
	return out.Bytes(), nil
}

func (c *Coordinator) streamChunks(ctx context.Context, chunks []dbgen.Chunk, start, end int64, pw *io.PipeWriter) {
	var offset int64
	for _, chunk := range chunks {
		chunkStart := offset
		chunkEnd := offset + chunk.SizeBytes - 1
		offset += chunk.SizeBytes
		if chunkEnd < start {
			continue
		}
		if chunkStart > end {
			break
		}
		data, err := c.fetchChunk(ctx, chunk)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		from := max64(start-chunkStart, 0)
		to := min64(end-chunkStart, int64(len(data))-1)
		if from > to {
			continue
		}
		if _, err := pw.Write(data[from : to+1]); err != nil {
			return
		}
	}
	pw.Close()
}

func parseRange(header string, size int64) (start, end int64, partial bool, err error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, size - 1, false, nil
	}
	if !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return 0, 0, false, ErrInvalidRange
	}
	spec := strings.TrimSpace(strings.TrimPrefix(header, "bytes="))
	left, right, ok := strings.Cut(spec, "-")
	if !ok || size <= 0 {
		return 0, 0, false, ErrInvalidRange
	}
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	switch {
	case left == "":
		n, parseErr := strconv.ParseInt(right, 10, 64)
		if parseErr != nil || n <= 0 {
			return 0, 0, false, ErrInvalidRange
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true, nil
	case right == "":
		n, parseErr := strconv.ParseInt(left, 10, 64)
		if parseErr != nil || n < 0 || n >= size {
			return 0, 0, false, ErrInvalidRange
		}
		return n, size - 1, true, nil
	default:
		n, parseErr := strconv.ParseInt(left, 10, 64)
		if parseErr != nil || n < 0 || n >= size {
			return 0, 0, false, ErrInvalidRange
		}
		m, parseErr := strconv.ParseInt(right, 10, 64)
		if parseErr != nil || m < n {
			return 0, 0, false, ErrInvalidRange
		}
		if m >= size {
			m = size - 1
		}
		return n, m, true, nil
	}
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (c *Coordinator) fetchChunk(ctx context.Context, chunk dbgen.Chunk) ([]byte, error) {
	replicas, err := c.q.ListReplicasForChunk(ctx, chunk.ID)
	if err != nil {
		return nil, fmt.Errorf("list replicas for chunk %s: %w", uuidString(chunk.ID), err)
	}

	candidates := make([]placement.Node, 0, len(replicas))
	for _, r := range replicas {
		node, ok := c.nodeByID(ctx, uuidString(r.NodeID))
		if !ok {
			continue
		}
		candidates = append(candidates, node)
	}
	if len(candidates) == 0 {
		candidates, err = c.orderedNodes(ctx)
		if err != nil {
			return nil, err
		}
	}

	var lastErr error
	for _, node := range candidates {
		data, err := c.readVerified(ctx, node, chunk)
		if err == nil {
			if err := c.markReplicaHealthy(ctx, chunk.ID, node.ID); err != nil {
				c.log.WarnContext(ctx, "marking replica healthy", "chunk_id", uuidString(chunk.ID), "node_id", node.ID, "err", err)
			}
			return data, nil
		}
		lastErr = err
		status := "missing"
		if errors.Is(err, ErrChunkIntegrity) {
			status = "corrupt"
		}
		if markErr := c.setReplicaStatus(ctx, chunk.ID, node.ID, status); markErr != nil {
			c.log.WarnContext(ctx, "recording bad replica", "chunk_id", uuidString(chunk.ID), "node_id", node.ID, "err", markErr)
		}
	}
	if lastErr == nil {
		lastErr = ErrNotFound
	}
	return nil, fmt.Errorf("%w: chunk %s: %w", ErrChunkIntegrity, uuidString(chunk.ID), lastErr)
}

func (c *Coordinator) readVerified(ctx context.Context, node placement.Node, chunk dbgen.Chunk) ([]byte, error) {
	rc, err := node.Store.Get(ctx, uuidString(chunk.ID))
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, chunk.SizeBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read chunk from %s: %w", node.Name, err)
	}
	if int64(len(data)) != chunk.SizeBytes {
		return nil, fmt.Errorf("%w: chunk %s has %d bytes, expected %d",
			ErrChunkIntegrity, uuidString(chunk.ID), len(data), chunk.SizeBytes)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != chunk.Sha256 {
		return nil, fmt.Errorf("%w: chunk %s failed sha256", ErrChunkIntegrity, uuidString(chunk.ID))
	}
	return data, nil
}

func (c *Coordinator) markReplicaHealthy(ctx context.Context, chunkID pgtype.UUID, nodeID string) error {
	return c.setReplicaStatus(ctx, chunkID, nodeID, "healthy")
}

func (c *Coordinator) setReplicaStatus(ctx context.Context, chunkID pgtype.UUID, nodeID, status string) error {
	node, err := parseUUID(nodeID)
	if err != nil {
		return err
	}
	return c.q.SetReplicaStatus(ctx, dbgen.SetReplicaStatusParams{
		ChunkID: chunkID,
		NodeID:  node,
		Status:  status,
	})
}
