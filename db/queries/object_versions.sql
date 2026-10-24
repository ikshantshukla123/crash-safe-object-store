-- name: GetObjectVersionByID :one
SELECT * FROM object_versions WHERE id = $1;
