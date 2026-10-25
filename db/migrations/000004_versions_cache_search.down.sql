DROP TABLE IF EXISTS object_embeddings;
DROP INDEX IF EXISTS chunks_state_created_idx;
DROP INDEX IF EXISTS object_versions_metadata_gin_idx;
DROP INDEX IF EXISTS objects_key_trgm_idx;
ALTER TABLE object_versions DROP COLUMN IF EXISTS metadata;
