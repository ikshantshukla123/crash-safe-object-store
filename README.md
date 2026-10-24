# Crash-Safe Object Store in Go

S3-core demo with a durability proof. Not AWS parity, demo-scale.

## What works today (phases 0-5)

- JWT auth with per-owner buckets and objects
- Single PUT and multipart upload (init, upload part, list, complete, abort)
- SHA-256 chunk checksums, deterministic chunk IDs, idempotent retries
- Three real storage-node processes with quorum writes (`W = 2` of `N = 3`)
- Verified reads: every chunk is hashed before it is sent; a bad replica is
  marked and another copy is tried
- HTTP Range reads mapped onto chunks
- Presigned GET URLs with expiry, scoped to the bucket owner
- Write-ahead log with fsync, group commits, segment rotation, torn-tail
  truncation and idempotent replay
- Crash injection at `after_wal_sync`, `after_node_ack` and `after_db_commit`
- Postgres metadata, Redis connection, Docker Compose stack with nodes and WAL
  on separate volumes

Not built yet: health checker/replicator/repair/GC (phase 6), version rollback
and caches (7), metrics/chaos/dashboard (8-9), embeddings (10), EC2 deploy (11).

## Run it

```bash
cp .env.example .env      # fill POSTGRES_PASSWORD, JWT_SECRET, PRESIGN_SECRET
make up                   # Postgres, Redis, three nodes, migrations, API
curl localhost:8080/health
make down
```

The API listens on `http://localhost:8080`. Flash storage nodes are on the
Compose network only.

## Layout

- `cmd/api` - HTTP API and single write coordinator
- `cmd/node` - one storage node serving `/chunks/{id}`, `/health`, `/stats`
- `internal/coordinator` - quorum writes, multipart completion, verified reads,
  WAL recovery
- `internal/wal` - record codec, replay, single-writer log
- `internal/nodeserver` - storage-node HTTP layer
- `internal/objects` - chunk store interface, local disk store, node client
- `internal/placement` - deterministic node ordering
- `internal/presign` - HMAC-SHA256 presigned URLs
- `db/migrations`, `db/queries` - schema and sqlc queries
- `test/crash` - subprocess crash-injection harness

## Testing

```bash
make test         # go vet + all tests with -race
make lint         # golangci-lint
make crash-test   # WAL truncation tests + subprocess crash/recovery harness
```

Database tests use `TEST_DATABASE_URL` or `DATABASE_URL` and skip only when
neither is set. `make up` publishes Postgres on port 5433, so the local URL from
`.env.example` works. CI runs them against the `pgvector/pgvector:pg16` service.

The crash harness starts a child process, kills it at one of the three crash
points, restarts against the same WAL directory and database schema, retries the
write, and asserts exactly one version, chunk, part and completed upload, with
at least two healthy replicas and readable bytes.

## Design notes

- Postgres is the source of truth for metadata and replica placement. The WAL
  records chunk-file intent only, never bytes.
- Chunks are immutable and write-once. A retry recomputes the same UUIDv5 chunk
  id from upload, part number and SHA-256.
- A write returns success only after two nodes have fsynced the bytes and the
  metadata transaction has committed.
- Reads verify SHA-256 and fall back to another replica. Transport failures are
  recorded as `missing`; checksum failures as `corrupt`.
- Deletes write a tombstone version. Deleting an already-deleted key is a no-op.
- Deleting a bucket first locks the bucket row, refuses if a live object exists,
  then removes the bucket's object rows, their versions, and its uploads.
