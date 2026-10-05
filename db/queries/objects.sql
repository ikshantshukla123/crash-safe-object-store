-- name: UpsertObject :one
-- DO UPDATE rather than DO NOTHING because it must return the row either way,
-- and it takes a row lock held until the transaction ends. That lock is what
-- serialises two concurrent writes to the same key.
INSERT INTO objects (bucket_id, key)
VALUES ($1, $2)
ON CONFLICT (bucket_id, key) DO UPDATE SET updated_at = now()
RETURNING *;

-- name: BumpObjectVersionNo :one
-- A relative update, which is normally avoided because retries double-count.
-- It is safe here only because the caller holds the upload row lock and has
-- already checked that the upload is not yet completed, so this runs at most
-- once per upload.
UPDATE objects
SET last_version_no = last_version_no + 1, updated_at = now()
WHERE id = $1
RETURNING last_version_no;

-- name: CreateObjectVersion :one
INSERT INTO object_versions (
    object_id, version_no, size_bytes, checksum, content_type, is_delete_marker
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: SetCurrentVersion :exec
UPDATE objects SET current_version_id = $2, updated_at = now() WHERE id = $1;

-- name: GetObjectByKey :one
SELECT * FROM objects WHERE bucket_id = $1 AND key = $2;

-- name: GetCurrentVersion :one
-- The live version of a key. A delete marker is returned rather than hidden,
-- so the handler can tell "never existed" (no row) from "deleted" (tombstone)
-- and respond 404 for both without guessing.
SELECT v.*
FROM objects o
JOIN object_versions v ON v.id = o.current_version_id
WHERE o.bucket_id = $1 AND o.key = $2;

-- name: ListObjects :many
SELECT o.key, v.size_bytes, v.checksum, v.content_type, v.created_at
FROM objects o
JOIN object_versions v ON v.id = o.current_version_id
WHERE o.bucket_id = $1 AND v.is_delete_marker = false
ORDER BY o.key
LIMIT $2 OFFSET $3;
