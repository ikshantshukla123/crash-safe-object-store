# 0002 — Postgres is the single source of truth

Status: accepted · Date: 2026-10-01

## Context

A distributed store has many places that *look* authoritative: the database,
the cache, the storage node disks, a worker's in-memory view of which nodes are
up. The classic failure in systems like this is not losing bytes. It is two
components disagreeing about what is true, and each one being partly right.

Concretely: if both Redis and Postgres can say "object X points at version V",
then a crash between the two writes leaves a split brain, and every reader has
to decide which to believe. There is no correct answer at read time.

## Decision

Exactly one owner per fact:

| Fact | Owner |
|---|---|
| Buckets, objects, versions, chunk lists, tags | PostgreSQL |
| Which node holds which chunk, and is it healthy | PostgreSQL (`chunk_replicas`) |
| Node up/down | PostgreSQL (`storage_nodes`), written by the health checker only |
| Chunk bytes | Storage node disks |
| Unfinished file operations on nodes | WAL |
| Presigned URL validity | The signature itself (stateless) |

Everything else — Redis, in-memory maps, dashboard state — is **derived**. It
may be stale, it may be wiped, and the system must still be correct.

## Consequences

- Recovery is simple: rebuild derived state from Postgres, never the reverse.
- A full Redis flush is a performance event, not a correctness event. The
  dashboard's Reset button relies on this.
- Node up/down has exactly one writer, so there is no race between a worker
  marking a node down and the health checker marking it up.
- Cost: Postgres is on the hot path for every read. Mitigated by caching, but
  deliberately *not* by giving the cache authority (we cache immutable
  data by immutable ID, and we **delete** the mutable pointer after commit
  rather than updating it in place).

## Alternatives rejected

- **Metadata in Redis with Postgres as backup.** Faster reads, but Redis
  persistence is weaker and a flush would be data loss, not cache loss.
- **A second WAL in front of Postgres.** Postgres transactions already give us
  atomicity and durability for metadata. Writing our own log in front of it
  would duplicate that guarantee and add a second thing to recover.
