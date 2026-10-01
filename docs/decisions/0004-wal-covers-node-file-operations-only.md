# 0004 — The WAL covers storage-node file operations only

Status: accepted · Date: 2026-10-01 · Implements: CLAUDE.md §3.1, §6, §8

## Context

The write path touches two systems that cannot be committed atomically
together: files on three storage nodes, and rows in Postgres. A crash between
them leaves one of two bad states:

1. Bytes written to nodes, no Postgres row → **orphaned files**, leaked disk.
2. Postgres row written, bytes missing → **dangling pointer**, a 404 or worse
   a corrupt read on an object we promised was saved.

State 2 is a durability lie and is unacceptable. State 1 is merely wasteful.

## Decision

A write-ahead log of **intent**, owned by the coordinator, covering only file
operations on storage nodes:

- Records: `BEGIN_PUT`, `COMMIT_PUT`, `ABORT_PUT`, `BEGIN_DELETE`,
  `DONE_DELETE`, `CHECKPOINT`.
- `BEGIN_*` is appended **and fsynced before any node is touched** (§6 step 5).
  `COMMIT`/`ABORT`/`DONE` need not be fsynced — their absence is recoverable.
- The WAL stores intent, **never chunk bytes**.
- There is no WAL in front of Postgres. Postgres transactions cover metadata.

The ordering is what buys safety: because intent is durable before the first
byte reaches a node, replay can always find in-doubt operations. It can never
encounter a file it has no record of.

## Replay (§8)

Read segments in order; stop and truncate at the first short read or bad CRC
**at the tail** (that is a torn write from the crash — expected, fine). A bad
record followed by *valid* records is real corruption, not a torn tail: refuse
to start rather than silently skip data.

For each in-doubt operation: if ≥ W good copies exist, finish it; otherwise
delete the partials and abort. Then append `CHECKPOINT` and drop fully
checkpointed segments. Replay must be idempotent — running it twice produces
identical state.

## Consequences

- We can always resolve a crash to a definite outcome: the write either
  completed or left nothing behind.
- Orphaned files are still possible in narrow windows; the GC orphan sweep
  cleans them after a 24 h grace period (the grace protects in-flight uploads).
- An **fsync error is fatal**: log and exit, do not retry. A failed fsync means
  we do not know what is on disk, and continuing would make the log a liar.
  Recovery happens through replay on restart.
- The WAL directory must live on a Docker volume, or a container recreate
  destroys exactly the thing that makes crashes survivable.

## Alternatives rejected

- **Two-phase commit across nodes and Postgres.** Needs a coordinator log
  anyway (this WAL *is* that log, minus the blocking protocol) and blocks on
  coordinator failure.
- **Write to Postgres first, then nodes.** Produces state 2, the dangling
  pointer. Never acceptable.
- **No WAL, reconcile with a background scan.** The window of inconsistency is
  unbounded and the demo has nothing to show at restart.
