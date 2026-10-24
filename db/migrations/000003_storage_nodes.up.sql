-- Storage nodes and the replicas they hold. Postgres owns the truth about
-- which node has which chunk; node disks own only the bytes.

CREATE TABLE storage_nodes (
    id           uuid PRIMARY KEY,
    name         text NOT NULL,
    base_url     text NOT NULL,
    status       text NOT NULL DEFAULT 'up'
                 CHECK (status IN ('up', 'down')),
    last_seen_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT storage_nodes_name_key UNIQUE (name)
);

CREATE TABLE chunk_replicas (
    chunk_id    uuid NOT NULL REFERENCES chunks (id) ON DELETE CASCADE,
    node_id     uuid NOT NULL REFERENCES storage_nodes (id) ON DELETE CASCADE,
    status      text NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'healthy', 'corrupt', 'missing')),
    verified_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (chunk_id, node_id)
);

CREATE INDEX chunk_replicas_pending_idx
    ON chunk_replicas (node_id, created_at) WHERE status = 'pending';

CREATE INDEX chunk_replicas_status_idx
    ON chunk_replicas (chunk_id, status);
