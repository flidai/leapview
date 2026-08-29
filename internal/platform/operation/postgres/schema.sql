-- Idempotent operation capability schema.
-- This schema is deliberately self-contained: callers apply it from their
-- own migration transaction and no transaction control is embedded here.
CREATE SCHEMA IF NOT EXISTS platform;

-- The baseline declares platform.operation. This capability-owned definition
-- is the final shape consumed by this repository and can be applied in an
-- isolated capability database before the platform migration is assembled.
CREATE TABLE IF NOT EXISTS platform.operation (
    operation_id          uuid PRIMARY KEY,
    scope_id              text NOT NULL CHECK (scope_id = btrim(scope_id) AND octet_length(scope_id) BETWEEN 1 AND 255),
    operation_type        text NOT NULL DEFAULT 'idempotency' CHECK (operation_type = btrim(operation_type) AND octet_length(operation_type) BETWEEN 1 AND 255),
    idempotency_key       text NOT NULL CHECK (idempotency_key = btrim(idempotency_key) AND octet_length(idempotency_key) BETWEEN 1 AND 512),
    request_digest        text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    state                 text NOT NULL CHECK (state IN ('pending', 'completed', 'failed', 'indeterminate')),
    owner_id              text NOT NULL CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    lease_expires_at      timestamptz NOT NULL,
    fencing_generation    bigint NOT NULL CHECK (fencing_generation > 0),
    outcome              jsonb NOT NULL DEFAULT '{}'::jsonb,
    attempt_id            uuid,
    attempt_identity      text CHECK (attempt_identity IS NULL OR (attempt_identity = btrim(attempt_identity) AND octet_length(attempt_identity) BETWEEN 1 AND 512)),
    attempt_evidence      jsonb,
    resolution_evidence   jsonb,
    created_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    terminal_at           timestamptz,
    retention_interval    interval NOT NULL CHECK (retention_interval > interval '0' AND retention_interval <= interval '365 days'),
    expires_at            timestamptz NOT NULL,
    UNIQUE (scope_id, idempotency_key),
    CONSTRAINT idempotency_operation_outcome_json CHECK (jsonb_typeof(outcome) = 'object' AND octet_length(outcome::text) <= 32768),
    CONSTRAINT idempotency_operation_attempt_evidence_json CHECK (attempt_evidence IS NULL OR (jsonb_typeof(attempt_evidence) = 'object' AND octet_length(attempt_evidence::text) <= 32768)),
    CONSTRAINT idempotency_operation_resolution_evidence_json CHECK (resolution_evidence IS NULL OR (jsonb_typeof(resolution_evidence) = 'object' AND octet_length(resolution_evidence::text) <= 32768)),
    CONSTRAINT idempotency_operation_attempt_identity_pair CHECK ((attempt_id IS NULL) = (attempt_identity IS NULL)),
    CONSTRAINT idempotency_operation_attempt_evidence_pair CHECK (attempt_evidence IS NULL OR attempt_id IS NOT NULL),
    CONSTRAINT idempotency_operation_indeterminate_attempt CHECK (state <> 'indeterminate' OR attempt_id IS NOT NULL),
    CONSTRAINT idempotency_operation_resolution_pair CHECK (resolution_evidence IS NULL OR (attempt_id IS NOT NULL AND state IN ('completed', 'failed'))),
    CONSTRAINT idempotency_operation_terminal_at CHECK (
        (state = 'pending' AND terminal_at IS NULL) OR (state <> 'pending' AND terminal_at IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idempotency_operation_expiry_idx
    ON platform.operation (expires_at)
    WHERE state <> 'pending';

CREATE INDEX IF NOT EXISTS idempotency_operation_attempt_idx
    ON platform.operation (attempt_id)
    WHERE attempt_id IS NOT NULL;

COMMENT ON TABLE platform.operation IS
    'Durable scoped idempotency records with owner leases and fencing generations';
COMMENT ON COLUMN platform.operation.outcome IS
    'Canonical JSON outcome. jsonb gives semantic exactness on replay and bounds persisted payload size';
