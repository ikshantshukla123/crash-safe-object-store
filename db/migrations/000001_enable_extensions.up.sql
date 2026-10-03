-- pg_trgm powers the trigram metadata search in Phase 7.
-- vector (pgvector) powers the embedding search in Phase 10.
--
-- Extensions live in their own migration because creating them requires
-- elevated privileges that later table migrations do not need, and because
-- they must exist before any column references the `vector` type.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS vector;
