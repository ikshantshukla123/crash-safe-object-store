# 0005 — Immutable chunks with deterministic IDs

Status: accepted · Date: 2026-10-01 · Implements: CLAUDE.md §3.3, §7

## Context

Every mutating step in this system can run twice. A client retries a timed-out
part upload. A worker crashes after copying bytes but before updating the row.
Replay re-runs an operation it already finished. If "run it again" can produce
a *different* result than "run it once", the system is not crash-safe no matter
how good the WAL is.

The usual culprit is generating a fresh random ID inside a retryable step: the
retry makes a second ID, a second file, a second row, and now there are two
truths.

## Decision

**Chunks are immutable and write-once.** A chunk ID never refers to different
bytes. Overwriting an object creates a new version with new chunks.

**Chunk IDs are deterministic**, derived from request identity:

```
chunk_id = uuidv5(NS, uploadID + ":" + partNo + ":" + sha256hex)
```

Same upload, same part number, same bytes → same ID, forever. A retry lands on
the same ID, hits the same row via `INSERT ... ON CONFLICT`, and the same file
path on the node.

One multipart part = one chunk (1–16 MiB, default 8 MiB). Single PUT is capped
at 16 MiB; a small PUT is internally init + one part + complete, so there is
**one write path**, not two.

## Why this makes the rest easy

Immutability plus content addressing removes whole categories of problem:

- **Caching needs no invalidation.** `obj:{versionId}` can never go stale,
  because that version's bytes cannot change (§11).
- **R=1 reads are safe.** There is no "older copy" of a chunk to accidentally
  read — only a correct copy or a corrupt one, and the hash tells us which
  (see 0003).
- **Repair is a byte-for-byte copy**, with no merge or conflict resolution.
- **A node can safely answer a duplicate PUT**: same checksum → 200, different
  checksum for an existing ID → 409. That 409 is a real bug signal, not a race.

## Supporting rules (§7)

- Absolute updates (`set status = healthy`), never relative ones (`increment`).
  Relative updates are not idempotent.
- Deleting something already gone is **success**.
- State machines move forward to terminal states; re-running a step on a
  terminal state returns the recorded result. This is why `complete` locks the
  upload row `FOR UPDATE` and returns the stored `completed_version_id` if it
  already ran.
- Workers claim work with `SELECT ... FOR UPDATE SKIP LOCKED`, and the work
  itself is independently idempotent — the lock is for efficiency, not
  correctness.
- Every new mutating endpoint ships with a test that calls it twice and asserts
  identical final state.

## Consequences

- Storage cost: overwriting a 1 GiB object stores a second 1 GiB. Acceptable;
  versioning is a feature here.
- A chunk belongs to exactly one version. **No reference counting** — rollback
  moves `objects.current_version_id` and copies no bytes.
- The version checksum must be composite (SHA-256 of the concatenated raw part
  hashes, with a `-N` part-count suffix), because the object's bytes are never
  hashed as a whole.

## Alternatives rejected

- **Random chunk IDs + a dedupe table.** The dedupe lookup is itself a
  check-then-insert race, which is the problem we were avoiding.
- **Mutable chunks (overwrite in place).** Breaks immutable caching, makes R=1
  unsafe, and turns repair into conflict resolution.
- **Global content-addressing (`chunk_id = sha256`) shared across uploads.**
  Real dedupe, but it forces reference counting and makes delete dangerous —
  explicitly out of scope per §3.4.
