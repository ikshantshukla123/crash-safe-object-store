-- Core metadata schema: users, buckets, objects, versions, chunks, uploads.
--
-- Postgres is the single source of truth for all of this. Chunk bytes live on
-- disk; every fact about them lives here.

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text        NOT NULL,
    password_hash text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- Case-insensitive uniqueness without needing the citext extension.
-- Alice@x.com and alice@x.com must not be two accounts.
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));

CREATE TABLE buckets (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    owner_id   uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT buckets_name_key UNIQUE (name),
    -- S3-style naming: 3-63 chars, lowercase alphanumeric, dots and hyphens,
    -- must start and end alphanumeric. Enforced here so a bad name cannot
    -- enter the database through any code path.
    CONSTRAINT buckets_name_valid CHECK (name ~ '^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$')
);

CREATE INDEX buckets_owner_id_idx ON buckets (owner_id);

CREATE TABLE objects (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    bucket_id       uuid        NOT NULL REFERENCES buckets (id) ON DELETE RESTRICT,
    key             text        NOT NULL,
    -- Points at the live version. The foreign key is added after
    -- object_versions exists, because the two tables reference each other.
    current_version_id uuid,
    -- Monotonic counter per object. Never decreases, even after deletes, so a
    -- version number is never reused.
    last_version_no bigint      NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT objects_bucket_key_key UNIQUE (bucket_id, key)
);

CREATE TABLE object_versions (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    object_id        uuid        NOT NULL REFERENCES objects (id) ON DELETE CASCADE,
    version_no       bigint      NOT NULL,
    size_bytes       bigint      NOT NULL CHECK (size_bytes >= 0),
    -- For a single-chunk object this is that chunk's SHA-256. For multi-part
    -- objects it is the composite checksum with a part-count suffix.
    checksum         text        NOT NULL,
    content_type     text        NOT NULL DEFAULT 'application/octet-stream',
    -- A delete is a new version carrying a tombstone, not a row removal, so
    -- history survives and the delete itself can be rolled back.
    is_delete_marker boolean     NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT object_versions_object_no_key UNIQUE (object_id, version_no)
);

ALTER TABLE objects
    ADD CONSTRAINT objects_current_version_fk
    FOREIGN KEY (current_version_id) REFERENCES object_versions (id) ON DELETE SET NULL;

CREATE TABLE chunks (
    -- Deterministic UUIDv5 derived from upload, part number and content hash.
    -- Deliberately has no DEFAULT: the application always supplies it, so a
    -- retry computes the same id and lands on the same row.
    id          uuid PRIMARY KEY,
    -- NULL while staged: the bytes exist but no version claims them yet.
    version_id  uuid        REFERENCES object_versions (id) ON DELETE CASCADE,
    chunk_index int         NOT NULL CHECK (chunk_index >= 0),
    size_bytes  bigint      NOT NULL CHECK (size_bytes >= 0),
    sha256      text        NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    state       text        NOT NULL DEFAULT 'staged'
                            CHECK (state IN ('staged', 'committed', 'deleted')),
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Two chunks of the same version can never share a position. Partial, because
-- staged chunks have no version yet and many may share a chunk_index.
CREATE UNIQUE INDEX chunks_version_index_key
    ON chunks (version_id, chunk_index) WHERE version_id IS NOT NULL;

CREATE TABLE multipart_uploads (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    bucket_id            uuid        NOT NULL REFERENCES buckets (id) ON DELETE RESTRICT,
    key                  text        NOT NULL,
    content_type         text        NOT NULL DEFAULT 'application/octet-stream',
    status               text        NOT NULL DEFAULT 'open'
                                     CHECK (status IN ('open', 'completed', 'aborted')),
    -- Recorded on completion so a repeated complete returns the same version
    -- instead of creating a second one.
    completed_version_id uuid        REFERENCES object_versions (id),
    created_at           timestamptz NOT NULL DEFAULT now(),
    expires_at           timestamptz NOT NULL,

    -- A completed upload must know its version; an open one must not have one.
    CONSTRAINT multipart_uploads_completed_has_version CHECK (
        (status = 'completed' AND completed_version_id IS NOT NULL) OR
        (status <> 'completed' AND completed_version_id IS NULL)
    )
);

CREATE INDEX multipart_uploads_open_idx
    ON multipart_uploads (expires_at) WHERE status = 'open';

CREATE TABLE upload_parts (
    upload_id  uuid   NOT NULL REFERENCES multipart_uploads (id) ON DELETE CASCADE,
    part_no    int    NOT NULL CHECK (part_no >= 1),
    chunk_id   uuid   NOT NULL REFERENCES chunks (id),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    sha256     text   NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),

    PRIMARY KEY (upload_id, part_no)
);
