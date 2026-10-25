-- Version metadata, search indexes and embeddings.

ALTER TABLE object_versions
    ADD COLUMN metadata jsonb NOT NULL DEFAULT '{}'::jsonb;

-- Trigram index for key search. pg_trgm is installed in the public schema by
-- migration 000001; the GIN operator class lets ILIKE '%term%' use this index.
CREATE INDEX objects_key_trgm_idx ON objects USING gin (key gin_trgm_ops);
CREATE INDEX object_versions_metadata_gin_idx
    ON object_versions USING gin (metadata jsonb_path_ops);

CREATE INDEX chunks_state_created_idx ON chunks (state, created_at);

CREATE TABLE object_embeddings (
    version_id uuid PRIMARY KEY REFERENCES object_versions (id) ON DELETE CASCADE,
    content    text NOT NULL,
    model      text NOT NULL,
    embedding  vector(1536) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX object_embeddings_embedding_idx
    ON object_embeddings USING hnsw (embedding vector_cosine_ops);
