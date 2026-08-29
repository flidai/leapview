
-- Durable jobs capability schema. The control-plane baseline creates the
-- schema; CREATE SCHEMA keeps this capability migration independently
-- applicable to a clean PostgreSQL database used by conformance tests.
CREATE SCHEMA IF NOT EXISTS jobs;

CREATE TABLE IF NOT EXISTS jobs.job (
    id                      text PRIMARY KEY,
    kind                    text NOT NULL,
    workload_class          text NOT NULL CHECK (workload_class IN ('background', 'control')),
    principal_id            text NOT NULL CHECK (principal_id = btrim(principal_id) AND length(principal_id) BETWEEN 1 AND 256),
    group_ids               text[] NOT NULL DEFAULT '{}'::text[],
    resource_kind           text NOT NULL,
    resource_id             text NOT NULL,
    estimated_memory_bytes  bigint NOT NULL CHECK (estimated_memory_bytes > 0),
    payload                 jsonb NOT NULL CHECK (octet_length(payload::text) <= 1048576),
    request_digest          text NOT NULL CHECK (request_digest ~* '^(sha256:)?[0-9a-f]{64}$'),
    status                  text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    attempt_count           bigint NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts            bigint NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 100),
    lease_owner             text NOT NULL DEFAULT '',
    lease_expires_at        timestamptz,
    lease_generation        bigint NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    available_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    error                   jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (octet_length(error::text) <= 65536),
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at              timestamptz,
    finished_at             timestamptz,
    CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 256),
    CHECK (kind = btrim(kind) AND length(kind) BETWEEN 1 AND 128),
    CHECK (resource_kind = btrim(resource_kind) AND length(resource_kind) BETWEEN 1 AND 128),
    CHECK (resource_id = btrim(resource_id) AND length(resource_id) BETWEEN 1 AND 256),
    CHECK (array_position(group_ids, NULL) IS NULL),
    CHECK (cardinality(group_ids) <= 256),
    CHECK ((status = 'running' AND lease_owner <> '' AND lease_expires_at IS NOT NULL)
        OR (status <> 'running' AND lease_owner = '' AND lease_expires_at IS NULL))
);

CREATE INDEX IF NOT EXISTS job_claim_idx
    ON jobs.job (workload_class, status, available_at, created_at, id);
CREATE INDEX IF NOT EXISTS job_principal_order_idx
    ON jobs.job (workload_class, principal_id, created_at, id);

-- One durable row per claim. The row captures both the attempt number and
-- fencing generation; heartbeat and terminal transitions close its outcome.
CREATE TABLE IF NOT EXISTS jobs.attempt (
    job_id             text NOT NULL REFERENCES jobs.job(id) ON DELETE CASCADE,
    attempt_number     bigint NOT NULL CHECK (attempt_number > 0),
    fencing_generation bigint NOT NULL CHECK (fencing_generation > 0),
    owner              text NOT NULL CHECK (owner = btrim(owner) AND length(owner) BETWEEN 1 AND 256),
    leased_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_expires_at   timestamptz NOT NULL,
    started_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at        timestamptz,
    outcome            text NOT NULL DEFAULT 'running' CHECK (outcome IN ('running', 'succeeded', 'failed', 'cancelled', 'expired', 'retrying')),
    retry_at           timestamptz,
    error              jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (octet_length(error::text) <= 65536),
    PRIMARY KEY (job_id, attempt_number),
    UNIQUE (job_id, fencing_generation)
);

CREATE INDEX IF NOT EXISTS attempt_retry_idx ON jobs.attempt (retry_at) WHERE outcome = 'retrying';

CREATE TABLE IF NOT EXISTS jobs.event_sequence (
    resource_kind text NOT NULL,
    resource_id   text NOT NULL,
    next_event_id bigint NOT NULL CHECK (next_event_id > 0),
    PRIMARY KEY (resource_kind, resource_id)
);

CREATE TABLE IF NOT EXISTS jobs.event (
    resource_kind text NOT NULL,
    resource_id   text NOT NULL,
    event_id      bigint NOT NULL CHECK (event_id > 0),
    event_type    text NOT NULL,
    event_key     text NOT NULL DEFAULT '',
    data          jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (octet_length(data::text) <= 1048576),
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (resource_kind, resource_id, event_id),
    CHECK (event_key = '' OR (event_key = btrim(event_key) AND length(event_key) <= 256))
);

-- Empty event keys are intentionally reusable; a partial index gives workflow
-- keys idempotency without imposing uniqueness on ordinary append-only events.
CREATE UNIQUE INDEX IF NOT EXISTS event_resource_key_idx
    ON jobs.event (resource_kind, resource_id, event_key)
    WHERE event_key <> '';
