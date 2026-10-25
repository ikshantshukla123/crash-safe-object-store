-- name: ListObjectVersions :many
SELECT v.*
FROM object_versions v
JOIN objects o ON o.id = v.object_id
WHERE o.bucket_id = $1 AND o.key = $2
ORDER BY v.version_no DESC;

-- name: GetVersionForObject :one
SELECT v.*
FROM object_versions v
JOIN objects o ON o.id = v.object_id
WHERE v.id = $1 AND o.bucket_id = $2 AND o.key = $3;
