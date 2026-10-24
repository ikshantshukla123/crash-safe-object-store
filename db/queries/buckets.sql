-- name: CreateBucket :one
-- DO NOTHING rather than DO UPDATE: a bucket name is global, and silently
-- returning someone else's bucket would hand them another user's data. A nil
-- result means "taken", and the caller then checks who owns it.
INSERT INTO buckets (name, owner_id)
VALUES ($1, $2)
ON CONFLICT (name) DO NOTHING
RETURNING *;

-- name: GetBucketByName :one
SELECT * FROM buckets WHERE name = $1;

-- name: ListBucketsByOwner :many
SELECT * FROM buckets WHERE owner_id = $1 ORDER BY name;

-- name: DeleteBucket :execrows
DELETE FROM buckets WHERE id = $1 AND owner_id = $2;

-- name: GetBucketByNameForUpdate :one
SELECT * FROM buckets WHERE name = $1 FOR UPDATE;

-- name: CountLiveObjectsInBucket :one
SELECT count(*) FROM objects o
JOIN object_versions v ON v.id = o.current_version_id
WHERE o.bucket_id = $1 AND NOT v.is_delete_marker;

-- name: DeleteUploadsForBucket :exec
DELETE FROM multipart_uploads WHERE bucket_id = $1;

-- name: DeleteObjectRowsForBucket :exec
DELETE FROM objects WHERE bucket_id = $1;
