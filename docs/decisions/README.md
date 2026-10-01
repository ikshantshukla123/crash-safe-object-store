# Architecture decision records

Numbered, append-only. To change a decision, add a new record that supersedes
the old one — do not edit history §3.

| # | Decision | Covers |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | process |
| [0002](0002-postgres-is-the-single-source-of-truth.md) | Postgres is the single source of truth | §3.1 ownership of state |
| [0003](0003-replication-n3-w2-r1.md) | N=3, W=2, R=1 verified, single coordinator | §3.2 replication model |
| [0004](0004-wal-covers-node-file-operations-only.md) | The WAL covers node file operations only | §3.1, §6, §8 |
| [0005](0005-immutable-chunks-deterministic-ids.md) | Immutable chunks, deterministic IDs | §3.3, §7 idempotency |

Suggested reading order for someone new to the system: 0002 → 0005 → 0003 → 0004.
