-- name: ResetMetadata :exec
-- Demo reset only. storage_nodes survives so the cluster mapping does not
-- disappear while the API is still running.
TRUNCATE TABLE
    users,
    buckets,
    objects,
    object_versions,
    chunks,
    multipart_uploads,
    upload_parts,
    chunk_replicas,
    object_embeddings
CASCADE;
