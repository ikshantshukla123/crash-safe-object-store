-- name: GetChunk :one
SELECT * FROM chunks WHERE id = $1;

-- name: DeleteStagedChunk :execrows
DELETE FROM chunks WHERE id = $1 AND state = 'staged' AND version_id IS NULL;

-- name: MarkChunkDeleted :exec
UPDATE chunks SET state = 'deleted' WHERE id = $1;
