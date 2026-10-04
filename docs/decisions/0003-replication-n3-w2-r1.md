# 0003 — Replication: N=3, W=2, R=1 verified, single coordinator

Status: accepted · Date: 2026-10-01

## Context

Storing one copy of a chunk means one disk failure loses data. Storing three
copies means a write has to wait for three machines, and the slowest one sets
your latency — and if any node is down, writes stop entirely.

The quorum question is: how many copies must acknowledge before we tell the
client "saved"?

## Decision

- **N = 3** replicas of every chunk.
- **W = 2** acknowledgements required before the write succeeds. The third copy
  is made asynchronously by the replicator worker.
- **R = 1** copy read, but the read is **verified**: we re-hash the bytes and
  compare to the stored SHA-256 before sending them to the client. A mismatch
  falls back to another replica and marks the bad one `corrupt`.
- **One coordinator** (a single API process). No consensus protocol, no leader
  election.

## Why W=2 and R=1 is safe here

The usual quorum rule is `W + R > N`, which would demand `R = 2`. We use R=1
and get safety a different way: **every chunk is content-addressed by its
SHA-256**, and we verify that hash on read. So a reader cannot be fooled by a
stale or corrupt copy the way it could with mutable rows — there is no "older
version of this chunk" to read, because chunks are immutable (see 0005). The
hash check turns "pick any replica" into "pick any replica, and detect if it
lied."

W=2 means we survive losing any one node immediately after an ack, without
waiting on all three during the write.

## Consequences

- Writes tolerate one node being down. Two nodes down means writes fail with
  503 — correct behaviour, not a bug.
- A window exists where only 2 copies are durable. The replicator closes it.
- The single coordinator removes an enormous amount of complexity (no Raft, no
  split-brain, no term numbers). It is also a single point of failure — which
  is the point of the crash demo: it dies, replays its WAL, and comes back
  consistent.
- With exactly 3 nodes there is no spare to rebuild onto. "Repair" therefore
  means catching a *returned* node up, not rebuilding onto a fresh one.

## Alternatives rejected

- **W=3.** Any node down halts all writes. Unacceptable for a failure demo
  whose whole premise is killing a node.
- **W=1.** A single disk failure between the ack and replication loses data.
- **Erasure coding.** Better storage efficiency, far more complex repair, and
  it obscures the replication story this project exists to demonstrate.
- **Raft / leader election.** Correct, and completely out of scope. The thing
  being demonstrated is crash-safe local durability, not consensus.
