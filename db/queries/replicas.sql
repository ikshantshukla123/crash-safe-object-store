-- name: UpsertChunkReplica :exec
INSERT INTO chunk_replicas (chunk_id, node_id, status, verified_at)
VALUES ($1, $2, $3, CASE WHEN $3 = 'healthy' THEN now() ELSE NULL END)
ON CONFLICT (chunk_id, node_id) DO UPDATE
SET status = EXCLUDED.status,
    verified_at = CASE
        WHEN EXCLUDED.status = 'healthy' THEN now()
        ELSE chunk_replicas.verified_at
    END;

-- name: ListReplicasForChunk :many
SELECT r.chunk_id, r.node_id, r.status, r.verified_at, r.created_at,
       n.name AS node_name, n.base_url AS node_base_url, n.status AS node_status
FROM chunk_replicas r
JOIN storage_nodes n ON n.id = r.node_id
WHERE r.chunk_id = $1
ORDER BY (r.status = 'healthy') DESC, n.name;

-- name: SetReplicaStatus :exec
UPDATE chunk_replicas
SET status = $3,
    verified_at = CASE WHEN $3 = 'healthy' THEN now() ELSE verified_at END
WHERE chunk_id = $1 AND node_id = $2;
