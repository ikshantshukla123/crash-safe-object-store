# Crash-Safe Object Store in Go

S3-core demo with durability proof. Not AWS parity, demo-scale.

## What it is
Go API + Postgres metadata + Redis cache + 3 Docker vols + React demo console.
Focus: multipart resumable, WAL replay after crash, replication + repair demo.

## Features
- Buckets/objects, JWT owner isolation, presigned URLs, checksums
- Multipart init/part/complete with resume
- Versioning + metadata search
- WAL append-only log with fsync + replay on restart
- Replicator + repair worker across store-1,2,3
- Select-lite filter + semantic search via pg_trgm + pgvector
- Demo console: upload, kill/heal, metrics, reset/seed

## Architecture
![arch](./docs/arch.png)