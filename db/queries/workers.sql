-- name: ListChunksForNode :many
SELECT c.*, r.status AS replica_status
FROM chunk_replicas r
JOIN chunks c ON c.id = r.chunk_id
WHERE r.node_id = $1 AND c.state IN ('staged', 'committed')
ORDER BY c.created_at;

-- name: ListPendingReplicas :many
SELECT r.chunk_id, r.node_id, c.size_bytes, c.sha256, c.state, c.created_at
FROM chunk_replicas r
JOIN chunks c ON c.id = r.chunk_id
JOIN storage_nodes n ON n.id = r.node_id
WHERE r.status = 'pending' AND c.state IN ('staged', 'committed') AND n.status = 'up'
ORDER BY r.created_at
LIMIT $1;

-- name: ListUnderReplicatedChunks :many
SELECT c.id, c.size_bytes, c.sha256, c.created_at,
       count(r.node_id) FILTER (WHERE r.status = 'healthy' AND n.status = 'up')::int AS healthy_replicas
FROM chunks c
LEFT JOIN chunk_replicas r ON r.chunk_id = c.id
LEFT JOIN storage_nodes n ON n.id = r.node_id
WHERE c.state IN ('staged', 'committed')
GROUP BY c.id
HAVING count(r.node_id) FILTER (WHERE r.status = 'healthy' AND n.status = 'up')::int < sqlc.arg(target_replicas)::int
ORDER BY healthy_replicas ASC, c.created_at ASC
LIMIT sqlc.arg(limit_count);

-- name: ListScrubCandidates :many
SELECT c.id, c.size_bytes, c.sha256, r.node_id
FROM chunk_replicas r
JOIN chunks c ON c.id = r.chunk_id
JOIN storage_nodes n ON n.id = r.node_id
WHERE r.status = 'healthy' AND n.status = 'up' AND c.state IN ('staged', 'committed')
ORDER BY r.verified_at NULLS FIRST, c.created_at
LIMIT $1;

-- name: ListGarbageChunks :many
SELECT c.*
FROM chunks c
WHERE c.state = 'deleted'
   OR (
       c.state = 'staged'
       AND c.version_id IS NULL
       AND c.created_at < $1
       AND NOT EXISTS (
           SELECT 1
           FROM upload_parts up
           JOIN multipart_uploads u ON u.id = up.upload_id
           WHERE up.chunk_id = c.id
             AND u.status = 'open'
             AND u.expires_at > now()
       )
   )
ORDER BY c.created_at
LIMIT $2;

-- name: ListReferencedChunkIDs :many
SELECT id FROM chunks;

-- name: DeleteChunkRow :exec
DELETE FROM chunks WHERE id = $1 AND state = 'deleted';

-- name: MarkUploadsExpired :exec
UPDATE multipart_uploads
SET status = 'aborted'
WHERE status = 'open' AND expires_at < now();

-- name: ListVersionsMissingEmbedding :many
SELECT v.id
FROM object_versions v
JOIN objects o ON o.id = v.object_id
WHERE v.is_delete_marker = false
  AND NOT EXISTS (SELECT 1 FROM object_embeddings e WHERE e.version_id = v.id)
ORDER BY v.created_at
LIMIT $1;
