-- name: GetUpload :one
SELECT * FROM multipart_uploads WHERE id = $1;

-- name: AbortUpload :execrows
UPDATE multipart_uploads SET status = 'aborted'
WHERE id = $1 AND status = 'open';

-- name: GetUploadPart :one
SELECT * FROM upload_parts WHERE upload_id = $1 AND part_no = $2;

-- name: DeleteUploadPartByChunk :exec
DELETE FROM upload_parts WHERE chunk_id = $1;

-- name: CreateUploadWithID :one
INSERT INTO multipart_uploads (id, bucket_id, key, content_type, expires_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (id) DO NOTHING
RETURNING *;
