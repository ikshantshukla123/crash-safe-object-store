# 0006 — Cache is derived, GC runs in the API, versions are retained

Status: accepted · Date: 2026-10-25

## Context

Three behaviours needed an explicit owner once versioning, caching and garbage
collection landed:

1. Which process writes deletion intent to the WAL?
2. What happens to chunk rows for old versions?
3. How is Redis kept from becoming a second source of truth?

## Decision

- **GC runs in the API process.** Only the API owns the single-writer WAL. GC
  appends `BEGIN_DELETE` before removing node files and `DONE_DELETE` after,
  then deletes the chunk row. A separate worker process must not open the WAL.
- **Old versions are retained.** Deleting an object inserts a tombstone and
  moves `objects.current_version_id`. It does not delete earlier versions or
  their chunks, so rollback always has bytes to point at. GC therefore removes
  staged/abandoned chunks and untracked orphan files, not committed chunks
  referenced by retained versions.
- **Redis is never authoritative.** `meta:{bucket}:{key}` has a 60 s TTL and is
  deleted after a successful Postgres commit, never updated in place.
  `obj:{versionId}` is immutable and can be cached without invalidation. Small
  objects (≤ 1 MiB) are cached; cache misses fall back to Postgres and verified
  chunk reads. Flushing Redis loses no durable state.

## Consequences

- Disk usage grows with version history until a future retention/pruning
  feature exists. That is intentional: rollback correctness is more important
  than reclaiming old versions in this demo.
- If the API dies between a node delete and `DONE_DELETE`, replay sees the
  in-doubt delete and finishes it idempotently.
- The orphan sweep uses a 24 h grace period so an in-flight upload cannot have
  its files deleted before the metadata transaction commits.
