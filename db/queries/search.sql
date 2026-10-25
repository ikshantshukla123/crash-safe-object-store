-- name: UpsertEmbedding :exec
INSERT INTO object_embeddings (version_id, content, model, embedding)
VALUES ($1, $2, $3, sqlc.arg(embedding)::vector)
ON CONFLICT (version_id) DO UPDATE
SET content = EXCLUDED.content,
    model = EXCLUDED.model,
    embedding = EXCLUDED.embedding,
    created_at = now();

-- name: GetEmbedding :one
SELECT * FROM object_embeddings WHERE version_id = $1;

-- name: SearchObjects :many
SELECT o.key,
       b.name AS bucket_name,
       v.id AS version_id,
       v.size_bytes,
       v.checksum,
       v.content_type,
       (1 - (e.embedding <=> sqlc.arg(query_embedding)::vector))::float8 AS score
FROM object_embeddings e
JOIN object_versions v ON v.id = e.version_id
JOIN objects o ON o.id = v.object_id
JOIN buckets b ON b.id = o.bucket_id
WHERE b.owner_id = sqlc.arg(owner_id)
  AND v.is_delete_marker = false
ORDER BY e.embedding <=> sqlc.arg(query_embedding)::vector
LIMIT sqlc.arg(limit_count);

-- name: GetVersionForEmbedding :one
SELECT v.id, v.size_bytes, v.checksum, v.content_type, v.is_delete_marker,
       o.key, b.name AS bucket_name
FROM object_versions v
JOIN objects o ON o.id = v.object_id
JOIN buckets b ON b.id = o.bucket_id
WHERE v.id = $1;
