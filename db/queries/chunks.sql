-- name: UpsertStagedChunk :one
-- The id is supplied by the application, not generated here: it is a
-- deterministic UUIDv5 of the upload, part number and content hash, so a
-- retried upload of the same bytes resolves to this same row.
INSERT INTO chunks (id, chunk_index, size_bytes, sha256)
VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO UPDATE
SET chunk_index = EXCLUDED.chunk_index
RETURNING *;

-- name: CommitChunks :exec
-- Absolute assignment, not a state transition, so running it twice is
-- harmless. Deleted chunks are excluded so a commit can never resurrect one.
UPDATE chunks
SET version_id = $1, state = 'committed'
WHERE id = ANY (@chunk_ids::uuid[]) AND state <> 'deleted';

-- name: ListChunksForVersion :many
SELECT * FROM chunks
WHERE version_id = $1 AND state = 'committed'
ORDER BY chunk_index;
