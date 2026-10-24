-- name: UpsertStorageNode :one
INSERT INTO storage_nodes (id, name, base_url, status)
VALUES ($1, $2, $3, 'up')
ON CONFLICT (name) DO UPDATE
SET base_url = EXCLUDED.base_url
RETURNING *;

-- name: ListStorageNodes :many
SELECT * FROM storage_nodes ORDER BY name;

-- name: GetStorageNodeByName :one
SELECT * FROM storage_nodes WHERE name = $1;

-- name: SetStorageNodeStatus :exec
UPDATE storage_nodes SET status = $2, last_seen_at = now() WHERE name = $1;
