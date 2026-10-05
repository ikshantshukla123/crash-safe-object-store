-- name: CreateUpload :one
INSERT INTO multipart_uploads (bucket_id, key, content_type, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUploadForUpdate :one
-- FOR UPDATE serialises concurrent completes of the same upload. The second
-- caller blocks here, then sees status = 'completed' and returns the recorded
-- version instead of creating a second one.
SELECT * FROM multipart_uploads WHERE id = $1 FOR UPDATE;

-- name: MarkUploadCompleted :exec
UPDATE multipart_uploads
SET status = 'completed', completed_version_id = $2
WHERE id = $1;

-- name: UpsertUploadPart :exec
-- Re-uploading a part with identical bytes lands on the same chunk id and
-- rewrites the same values, so a retry is a no-op.
INSERT INTO upload_parts (upload_id, part_no, chunk_id, size_bytes, sha256)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (upload_id, part_no) DO UPDATE
SET chunk_id = EXCLUDED.chunk_id,
    size_bytes = EXCLUDED.size_bytes,
    sha256 = EXCLUDED.sha256;

-- name: ListUploadParts :many
SELECT * FROM upload_parts WHERE upload_id = $1 ORDER BY part_no;
