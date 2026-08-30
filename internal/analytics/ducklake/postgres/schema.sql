-- DuckLake control capability schema (FAI-563/564).
--
-- This schema is applied to the LeapView control PostgreSQL database.  The
-- DuckLake metadata catalog itself remains in the separately provisioned
-- DuckLake PostgreSQL database and is accessed by local DuckDB connectors.
-- These rows carry only immutable identities and lifecycle evidence; they do
-- not duplicate DuckLake's table/file manifest.
CREATE SCHEMA IF NOT EXISTS ducklake;

CREATE TABLE IF NOT EXISTS ducklake.catalog_identity (
    physical_pool_id       text PRIMARY KEY,
    catalog_database       text NOT NULL
        CHECK (catalog_database = btrim(catalog_database) AND octet_length(catalog_database) BETWEEN 1 AND 255),
    catalog_id             text NOT NULL,
    catalog_uuid           text NOT NULL
        CHECK (catalog_uuid = btrim(catalog_uuid)
            AND catalog_uuid ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
    metadata_schema        text NOT NULL,
    compatibility_digest   text NOT NULL
        CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    catalog_schema_version text NOT NULL,
    created_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (physical_pool_id, catalog_id),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (metadata_schema = btrim(metadata_schema) AND metadata_schema ~ '^[A-Za-z_][A-Za-z0-9_]*$'),
    CHECK (catalog_schema_version = btrim(catalog_schema_version) AND octet_length(catalog_schema_version) BETWEEN 1 AND 128)
);

-- One row is the exact external attempt ledger.  A commit is accepted only
-- with the marker and snapshot selected by the local DuckLake connection;
-- lease expiry alone never permits a retry.
CREATE TABLE IF NOT EXISTS ducklake.attempt_evidence (
    attempt_id           uuid PRIMARY KEY,
    request_digest       text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    plan_digest          text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    physical_pool_id     text NOT NULL,
    catalog_id           text NOT NULL,
    owner_id             text NOT NULL,
    fencing_epoch        bigint NOT NULL CHECK (fencing_epoch > 0),
    lease_expires_at     timestamptz NOT NULL,
    session_identity     text NOT NULL,
    state                text NOT NULL CHECK (state IN ('running', 'committed', 'aborted', 'indeterminate', 'fenced')),
    snapshot_id          bigint CHECK (snapshot_id IS NULL OR snapshot_id > 0),
    commit_marker        jsonb,
    termination_evidence jsonb,
    created_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    terminal_at          timestamptz,
    FOREIGN KEY (physical_pool_id, catalog_id) REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK (lease_expires_at > created_at),
    CHECK (session_identity = btrim(session_identity) AND octet_length(session_identity) BETWEEN 1 AND 512),
    CHECK (commit_marker IS NULL OR (jsonb_typeof(commit_marker) = 'object' AND octet_length(commit_marker::text) <= 4096)),
    CHECK (termination_evidence IS NULL OR (jsonb_typeof(termination_evidence) = 'object' AND octet_length(termination_evidence::text) <= 32768)),
    CHECK ((state = 'running' AND terminal_at IS NULL) OR (state <> 'running' AND terminal_at IS NOT NULL)),
    CHECK ((state = 'committed' AND snapshot_id IS NOT NULL AND commit_marker IS NOT NULL)
           OR (state <> 'committed' AND snapshot_id IS NULL AND commit_marker IS NULL)),
    CHECK (state IN ('running', 'committed') OR termination_evidence IS NOT NULL)
);

-- A generation binding is immutable evidence.  Serving selects this exact
-- pool/catalog/snapshot tuple; it never selects a catalog by path or recency.
CREATE TABLE IF NOT EXISTS ducklake.generation_binding (
    delivery_id                text NOT NULL,
    generation_id              text NOT NULL,
    attempt_id                 uuid NOT NULL,
    physical_pool_id           text NOT NULL,
    catalog_id                 text NOT NULL,
    snapshot_id                bigint NOT NULL CHECK (snapshot_id > 0),
    relation_manifest_digest   text NOT NULL CHECK (relation_manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    compatibility_digest       text NOT NULL CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    serving_artifact_digest    text NOT NULL CHECK (serving_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    request_digest             text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    plan_digest                text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    fencing_epoch              bigint NOT NULL CHECK (fencing_epoch > 0),
    bound_at                   timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (delivery_id, generation_id),
    UNIQUE (attempt_id),
    UNIQUE (physical_pool_id, catalog_id, snapshot_id),
    FOREIGN KEY (physical_pool_id, catalog_id) REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    FOREIGN KEY (attempt_id) REFERENCES ducklake.attempt_evidence(attempt_id),
    CHECK (delivery_id = btrim(delivery_id) AND octet_length(delivery_id) BETWEEN 1 AND 255),
    CHECK (generation_id = btrim(generation_id) AND octet_length(generation_id) BETWEEN 1 AND 255)
);

CREATE INDEX IF NOT EXISTS ducklake_generation_binding_attempt_idx
    ON ducklake.generation_binding (attempt_id, fencing_epoch);

-- A retention row is the gate for every durable root and active query lease.
-- Retiring prevents new leases while existing leases drain.
CREATE TABLE IF NOT EXISTS ducklake.snapshot_retention (
    physical_pool_id text NOT NULL,
    catalog_id       text NOT NULL,
    snapshot_id      bigint NOT NULL CHECK (snapshot_id > 0),
    state            text NOT NULL CHECK (state IN ('live', 'retiring', 'expired', 'quarantined', 'cleanup-complete')),
    protected_until  timestamptz,
    retired_at       timestamptz,
    expired_at       timestamptz,
    cleanup_owner_id text,
    cleanup_fencing_epoch bigint NOT NULL DEFAULT 0 CHECK (cleanup_fencing_epoch >= 0),
    cleanup_lease_expires_at timestamptz,
    quarantined_at  timestamptz,
    cleanup_completed_at timestamptz,
    quarantine_evidence jsonb,
    cleanup_evidence jsonb,
    evidence         jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    created_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (physical_pool_id, catalog_id, snapshot_id),
    FOREIGN KEY (physical_pool_id, catalog_id) REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK ((state = 'live' AND retired_at IS NULL AND expired_at IS NULL) OR state <> 'live'),
    CHECK ((state = 'retiring' AND retired_at IS NOT NULL AND expired_at IS NULL) OR state <> 'retiring'),
    CHECK ((state IN ('expired', 'quarantined', 'cleanup-complete') AND expired_at IS NOT NULL) OR state NOT IN ('expired', 'quarantined', 'cleanup-complete')),
    CHECK (retired_at IS NULL OR retired_at >= created_at),
    CHECK (expired_at IS NULL OR expired_at >= COALESCE(retired_at, created_at)),
    CHECK ((cleanup_fencing_epoch = 0 AND cleanup_owner_id IS NULL AND cleanup_lease_expires_at IS NULL) OR (cleanup_fencing_epoch > 0 AND cleanup_owner_id IS NOT NULL AND cleanup_lease_expires_at IS NOT NULL)),
    CHECK (cleanup_owner_id IS NULL OR (cleanup_owner_id = btrim(cleanup_owner_id) AND octet_length(cleanup_owner_id) BETWEEN 1 AND 255)),
    CHECK (cleanup_lease_expires_at IS NULL OR cleanup_lease_expires_at > created_at),
    CHECK ((state IN ('quarantined', 'cleanup-complete') AND quarantined_at IS NOT NULL) OR state NOT IN ('quarantined', 'cleanup-complete')),
    CHECK ((state = 'cleanup-complete' AND cleanup_completed_at IS NOT NULL) OR state <> 'cleanup-complete'),
    CHECK (quarantine_evidence IS NULL OR (jsonb_typeof(quarantine_evidence) = 'object' AND octet_length(quarantine_evidence::text) <= 32768)),
    CHECK (cleanup_evidence IS NULL OR (jsonb_typeof(cleanup_evidence) = 'object' AND octet_length(cleanup_evidence::text) <= 32768)),
    CHECK (quarantined_at IS NULL OR quarantined_at >= COALESCE(expired_at, created_at)),
    CHECK (cleanup_completed_at IS NULL OR cleanup_completed_at >= COALESCE(quarantined_at, created_at))
);

-- Query leases carry the exact generation binding and owner fence.  They are
-- roots only while active; release and expiry are monotonic transitions.
-- Durable roots outlive a process and are the only non-query protection that
-- keeps a snapshot from retirement.  The generation binding itself creates a
-- generation root; rollback/recovery/candidate callers create additional
-- typed roots through the repository.
CREATE TABLE IF NOT EXISTS ducklake.snapshot_root (
    root_id          uuid PRIMARY KEY,
    physical_pool_id text NOT NULL,
    catalog_id       text NOT NULL,
    snapshot_id      bigint NOT NULL CHECK (snapshot_id > 0),
    root_kind        text NOT NULL CHECK (root_kind IN ('candidate', 'generation', 'rollback', 'recovery', 'active', 'cache', 'lineage', 'delivery')),
    state            text NOT NULL CHECK (state IN ('live', 'retiring', 'expired', 'quarantined', 'cleanup-complete')),
    created_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    retired_at       timestamptz,
    expired_at       timestamptz,
    quarantined_at   timestamptz,
    cleanup_completed_at timestamptz,
    quarantine_evidence jsonb,
    cleanup_evidence jsonb,
    evidence         jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    FOREIGN KEY (physical_pool_id, catalog_id, snapshot_id)
        REFERENCES ducklake.snapshot_retention(physical_pool_id, catalog_id, snapshot_id),
    CHECK ((state = 'live' AND retired_at IS NULL AND expired_at IS NULL) OR state <> 'live'),
    CHECK (state <> 'retiring' OR (retired_at IS NOT NULL AND expired_at IS NULL)),
    CHECK (state NOT IN ('expired', 'quarantined', 'cleanup-complete') OR expired_at IS NOT NULL),
    CHECK (retired_at IS NULL OR retired_at >= created_at),
    CHECK (expired_at IS NULL OR expired_at >= COALESCE(retired_at, created_at)),
    CHECK ((state IN ('quarantined', 'cleanup-complete') AND quarantined_at IS NOT NULL) OR state NOT IN ('quarantined', 'cleanup-complete')),
    CHECK ((state = 'cleanup-complete' AND cleanup_completed_at IS NOT NULL) OR state <> 'cleanup-complete'),
    CHECK (quarantine_evidence IS NULL OR (jsonb_typeof(quarantine_evidence) = 'object' AND octet_length(quarantine_evidence::text) <= 32768)),
    CHECK (cleanup_evidence IS NULL OR (jsonb_typeof(cleanup_evidence) = 'object' AND octet_length(cleanup_evidence::text) <= 32768)),
    CHECK (quarantined_at IS NULL OR quarantined_at >= COALESCE(expired_at, created_at)),
    CHECK (cleanup_completed_at IS NULL OR cleanup_completed_at >= COALESCE(quarantined_at, created_at))
);

CREATE INDEX IF NOT EXISTS ducklake_snapshot_root_active_idx
    ON ducklake.snapshot_root (physical_pool_id, catalog_id, snapshot_id, state);

CREATE TABLE IF NOT EXISTS ducklake.snapshot_lease (
    lease_id         uuid PRIMARY KEY,
    delivery_id      text NOT NULL,
    generation_id    text NOT NULL,
    physical_pool_id text NOT NULL,
    catalog_id       text NOT NULL,
    snapshot_id      bigint NOT NULL CHECK (snapshot_id > 0),
    owner_id         text NOT NULL,
    fencing_epoch    bigint NOT NULL CHECK (fencing_epoch > 0),
    state            text NOT NULL CHECK (state IN ('active', 'released', 'expired')),
    expires_at       timestamptz NOT NULL,
    acquired_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    released_at      timestamptz,
    evidence         jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    FOREIGN KEY (delivery_id, generation_id)
        REFERENCES ducklake.generation_binding(delivery_id, generation_id),
    FOREIGN KEY (physical_pool_id, catalog_id, snapshot_id)
        REFERENCES ducklake.snapshot_retention(physical_pool_id, catalog_id, snapshot_id),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK ((state = 'active' AND released_at IS NULL) OR (state <> 'active' AND released_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS ducklake_snapshot_lease_active_idx
    ON ducklake.snapshot_lease (physical_pool_id, catalog_id, snapshot_id, state, expires_at);

-- Orphan observations are immutable, bounded evidence for snapshots found in
-- DuckLake metadata before a control retention row was established.  They are
-- deliberately separate from retention: an orphan is not executable until a
-- qualified root is created, and cleanup workers must reconcile it first.
CREATE TABLE IF NOT EXISTS ducklake.snapshot_orphan (
    orphan_id        uuid PRIMARY KEY,
    physical_pool_id text NOT NULL,
    catalog_id       text NOT NULL,
    snapshot_id      bigint NOT NULL CHECK (snapshot_id > 0),
    state            text NOT NULL CHECK (state IN ('quarantined', 'cleanup-complete')),
    cleanup_owner_id text,
    cleanup_fencing_epoch bigint NOT NULL DEFAULT 0 CHECK (cleanup_fencing_epoch >= 0),
    cleanup_lease_expires_at timestamptz,
    evidence         jsonb NOT NULL CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    discovered_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    resolved_at      timestamptz,
    FOREIGN KEY (physical_pool_id, catalog_id) REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    UNIQUE (physical_pool_id, catalog_id, snapshot_id),
    CHECK ((state = 'quarantined' AND resolved_at IS NULL) OR (state = 'cleanup-complete' AND resolved_at IS NOT NULL)),
    CHECK ((cleanup_fencing_epoch = 0 AND cleanup_owner_id IS NULL AND cleanup_lease_expires_at IS NULL) OR (cleanup_fencing_epoch > 0 AND cleanup_owner_id IS NOT NULL AND cleanup_lease_expires_at IS NOT NULL)),
    CHECK (cleanup_owner_id IS NULL OR (cleanup_owner_id = btrim(cleanup_owner_id) AND octet_length(cleanup_owner_id) BETWEEN 1 AND 255)),
    CHECK (cleanup_lease_expires_at IS NULL OR cleanup_lease_expires_at > discovered_at)
);

CREATE INDEX IF NOT EXISTS ducklake_snapshot_orphan_backlog_idx
    ON ducklake.snapshot_orphan (physical_pool_id, catalog_id, state, discovered_at);

-- The clean-slate upgrade authority is deliberately separate from the
-- catalog identity and serving evidence above.  Runtime attachments only
-- read these tables; only the dedicated migrator role receives write access.
-- The tuple is stored in bounded typed columns rather than hidden in JSON so
-- an attach can compare every component exactly.
CREATE TABLE IF NOT EXISTS ducklake.catalog_runtime_compatibility (
    physical_pool_id       text PRIMARY KEY,
    catalog_id             text NOT NULL,
    duckdb_runtime         text NOT NULL,
    ducklake_extension     text NOT NULL,
    catalog_format         text NOT NULL,
    compatibility_digest   text NOT NULL CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    catalog_schema_version text NOT NULL,
    current_migration_id  uuid,
    updated_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (physical_pool_id, catalog_id)
        REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (duckdb_runtime = btrim(duckdb_runtime) AND octet_length(duckdb_runtime) BETWEEN 1 AND 255),
    CHECK (ducklake_extension = btrim(ducklake_extension) AND octet_length(ducklake_extension) BETWEEN 1 AND 255),
    CHECK (catalog_format = btrim(catalog_format) AND octet_length(catalog_format) BETWEEN 1 AND 255),
    CHECK (catalog_schema_version = btrim(catalog_schema_version) AND octet_length(catalog_schema_version) BETWEEN 1 AND 128)
);

-- A row exists for the global fence (physical_pool_id = '') and one row per
-- pool.  Epochs never move backwards; expiry only permits a successor claim
-- after the bounded lease has elapsed.  Global acquisition serializes with
-- pool acquisition in the repository by locking the global row first.
CREATE TABLE IF NOT EXISTS ducklake.migration_fence (
    scope                 text NOT NULL CHECK (scope IN ('global', 'pool')),
    physical_pool_id      text NOT NULL DEFAULT '',
    owner_id              text,
    fencing_epoch         bigint NOT NULL DEFAULT 0 CHECK (fencing_epoch >= 0),
    lease_expires_at      timestamptz,
    updated_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (scope, physical_pool_id),
    CHECK ((scope = 'global' AND physical_pool_id = '') OR
           (scope = 'pool' AND physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255)),
    CHECK ((owner_id IS NULL AND lease_expires_at IS NULL) OR
           (owner_id IS NOT NULL AND owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255 AND lease_expires_at IS NOT NULL))
);

INSERT INTO ducklake.migration_fence(scope, physical_pool_id)
VALUES ('global', '') ON CONFLICT (scope, physical_pool_id) DO NOTHING;

-- One immutable row records the exact before/after tuple and every lifecycle
-- outcome.  Completion/failure evidence is append-only through the guarded
-- repository transitions; a decision is mandatory for failed migrations so
-- operators can distinguish rollback from forward recovery.
CREATE TABLE IF NOT EXISTS ducklake.catalog_migration (
    migration_id          uuid PRIMARY KEY,
    physical_pool_id      text NOT NULL,
    catalog_id            text NOT NULL,
    owner_id              text NOT NULL,
    fencing_epoch         bigint NOT NULL CHECK (fencing_epoch > 0),
    global_fencing_epoch  bigint NOT NULL CHECK (global_fencing_epoch > 0),
    current_duckdb_runtime text NOT NULL,
    current_ducklake_extension text NOT NULL,
    current_catalog_format text NOT NULL,
    current_compatibility_digest text NOT NULL CHECK (current_compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    current_catalog_schema_version text NOT NULL,
    target_duckdb_runtime text NOT NULL,
    target_ducklake_extension text NOT NULL,
    target_catalog_format text NOT NULL,
    target_compatibility_digest text NOT NULL CHECK (target_compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    target_catalog_schema_version text NOT NULL,
    state                 text NOT NULL CHECK (state IN ('running', 'completed', 'failed')),
    started_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    terminal_at           timestamptz,
    begin_evidence        jsonb NOT NULL,
    completion_evidence   jsonb,
    failure_evidence      jsonb,
    recovery_decision     text CHECK (recovery_decision IS NULL OR recovery_decision IN ('rollback', 'forward_recovery')),
    decision_evidence     jsonb,
    FOREIGN KEY (physical_pool_id, catalog_id)
        REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK (current_catalog_schema_version = btrim(current_catalog_schema_version) AND octet_length(current_catalog_schema_version) BETWEEN 1 AND 128),
    CHECK (target_catalog_schema_version = btrim(target_catalog_schema_version) AND octet_length(target_catalog_schema_version) BETWEEN 1 AND 128),
    CHECK (octet_length(current_duckdb_runtime) BETWEEN 1 AND 255 AND current_duckdb_runtime = btrim(current_duckdb_runtime)),
    CHECK (octet_length(current_ducklake_extension) BETWEEN 1 AND 255 AND current_ducklake_extension = btrim(current_ducklake_extension)),
    CHECK (octet_length(current_catalog_format) BETWEEN 1 AND 255 AND current_catalog_format = btrim(current_catalog_format)),
    CHECK (octet_length(target_duckdb_runtime) BETWEEN 1 AND 255 AND target_duckdb_runtime = btrim(target_duckdb_runtime)),
    CHECK (octet_length(target_ducklake_extension) BETWEEN 1 AND 255 AND target_ducklake_extension = btrim(target_ducklake_extension)),
    CHECK (octet_length(target_catalog_format) BETWEEN 1 AND 255 AND target_catalog_format = btrim(target_catalog_format)),
    CHECK ((state = 'running' AND terminal_at IS NULL AND completion_evidence IS NULL AND failure_evidence IS NULL AND recovery_decision IS NULL AND decision_evidence IS NULL) OR
           (state <> 'running' AND terminal_at IS NOT NULL)),
    CHECK (jsonb_typeof(begin_evidence) = 'object' AND begin_evidence <> '{}'::jsonb AND octet_length(begin_evidence::text) BETWEEN 2 AND 32768),
    CHECK (completion_evidence IS NULL OR (jsonb_typeof(completion_evidence) = 'object' AND completion_evidence <> '{}'::jsonb AND octet_length(completion_evidence::text) BETWEEN 2 AND 32768)),
    CHECK (failure_evidence IS NULL OR (jsonb_typeof(failure_evidence) = 'object' AND failure_evidence <> '{}'::jsonb AND octet_length(failure_evidence::text) BETWEEN 2 AND 32768)),
    CHECK (decision_evidence IS NULL OR (jsonb_typeof(decision_evidence) = 'object' AND decision_evidence <> '{}'::jsonb AND octet_length(decision_evidence::text) BETWEEN 2 AND 32768)),
    CHECK ((state <> 'completed' OR (completion_evidence IS NOT NULL AND recovery_decision IS NULL AND decision_evidence IS NULL)) AND
           (state <> 'failed' OR (failure_evidence IS NOT NULL AND recovery_decision IS NOT NULL AND decision_evidence IS NOT NULL)))
);

CREATE UNIQUE INDEX IF NOT EXISTS ducklake_catalog_migration_identity_idx
    ON ducklake.catalog_migration (migration_id, physical_pool_id, catalog_id);

CREATE UNIQUE INDEX IF NOT EXISTS ducklake_catalog_migration_running_idx
    ON ducklake.catalog_migration (physical_pool_id, catalog_id) WHERE state = 'running';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'catalog_runtime_compatibility_migration_fk'
           AND conrelid = 'ducklake.catalog_runtime_compatibility'::regclass
    ) THEN
        ALTER TABLE ducklake.catalog_runtime_compatibility
            ADD CONSTRAINT catalog_runtime_compatibility_migration_fk
            FOREIGN KEY (current_migration_id, physical_pool_id, catalog_id)
            REFERENCES ducklake.catalog_migration (migration_id, physical_pool_id, catalog_id);
    END IF;
END
$$;

-- Each retained/active snapshot gets immutable evidence under the target
-- tuple.  Runtime attach checks every live/retiring retention row against the
-- current compatibility row; a missing or rejected qualification fails closed.
CREATE TABLE IF NOT EXISTS ducklake.snapshot_requalification (
    qualification_id       uuid PRIMARY KEY,
    physical_pool_id       text NOT NULL,
    catalog_id             text NOT NULL,
    snapshot_id            bigint NOT NULL CHECK (snapshot_id > 0),
    migration_id           uuid NOT NULL,
    duckdb_runtime         text NOT NULL,
    ducklake_extension     text NOT NULL,
    catalog_format         text NOT NULL,
    compatibility_digest   text NOT NULL CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    catalog_schema_version text NOT NULL,
    status                 text NOT NULL CHECK (status IN ('qualified', 'rejected')),
    evidence               jsonb NOT NULL CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb AND octet_length(evidence::text) BETWEEN 2 AND 32768),
    qualified_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (physical_pool_id, catalog_id, snapshot_id)
        REFERENCES ducklake.snapshot_retention(physical_pool_id, catalog_id, snapshot_id),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (octet_length(duckdb_runtime) BETWEEN 1 AND 255 AND duckdb_runtime = btrim(duckdb_runtime)),
    CHECK (octet_length(ducklake_extension) BETWEEN 1 AND 255 AND ducklake_extension = btrim(ducklake_extension)),
    CHECK (octet_length(catalog_format) BETWEEN 1 AND 255 AND catalog_format = btrim(catalog_format)),
    CHECK (catalog_schema_version = btrim(catalog_schema_version) AND octet_length(catalog_schema_version) BETWEEN 1 AND 128),
    UNIQUE (physical_pool_id, catalog_id, snapshot_id, migration_id)
);

-- Enforce that qualification evidence can only refer to the migration's exact
-- pool/catalog identity.  The migration epoch also lets runtime reject stale
-- evidence after a tuple cycle (A -> B -> A).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'snapshot_requalification_migration_identity_fk'
           AND conrelid = 'ducklake.snapshot_requalification'::regclass
    ) THEN
        ALTER TABLE ducklake.snapshot_requalification
            ADD CONSTRAINT snapshot_requalification_migration_identity_fk
            FOREIGN KEY (migration_id, physical_pool_id, catalog_id)
            REFERENCES ducklake.catalog_migration (migration_id, physical_pool_id, catalog_id);
    END IF;
END
$$;

CREATE INDEX IF NOT EXISTS ducklake_snapshot_requalification_lookup_idx
    ON ducklake.snapshot_requalification (physical_pool_id, catalog_id, snapshot_id, status, compatibility_digest, catalog_schema_version);

-- Reader-drain observability is a view over the authoritative lease rows.  It
-- intentionally exposes no payload or session secret, only bounded identity,
-- age and deadline information useful to maintenance workers and metrics.
CREATE OR REPLACE VIEW ducklake.snapshot_reader_drain AS
SELECT lease_id, delivery_id, generation_id, physical_pool_id, catalog_id,
       snapshot_id, owner_id, fencing_epoch, state, acquired_at, expires_at,
       (state = 'active' AND expires_at <= clock_timestamp()) AS overdue,
       (state = 'active' AND expires_at <= clock_timestamp()
        AND acquired_at < clock_timestamp() - interval '1 hour') AS non_draining
  FROM ducklake.snapshot_lease;

CREATE INDEX IF NOT EXISTS ducklake_attempt_evidence_identity_idx
    ON ducklake.attempt_evidence (physical_pool_id, catalog_id, request_digest, plan_digest);

CREATE OR REPLACE FUNCTION ducklake.reject_immutable_change()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'DuckLake identity evidence is immutable';
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.reject_attempt_identity_change()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'DuckLake attempt identity evidence is immutable';
    END IF;
    IF NEW.attempt_id <> OLD.attempt_id
       OR NEW.request_digest <> OLD.request_digest
       OR NEW.plan_digest <> OLD.plan_digest
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.owner_id <> OLD.owner_id
       OR NEW.fencing_epoch <> OLD.fencing_epoch
       OR NEW.session_identity <> OLD.session_identity THEN
        RAISE EXCEPTION 'DuckLake attempt identity is immutable';
    END IF;
    IF OLD.state <> 'running' AND NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at THEN
        RAISE EXCEPTION 'DuckLake terminal attempt lease expiry is immutable';
    ELSIF OLD.state = 'running' AND NEW.state = 'running'
          AND NEW.lease_expires_at < OLD.lease_expires_at THEN
        RAISE EXCEPTION 'DuckLake running attempt lease expiry cannot move backwards';
    ELSIF OLD.state = 'running' AND NEW.state <> 'running'
          AND NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at THEN
        RAISE EXCEPTION 'DuckLake terminal attempt lease expiry is immutable';
    END IF;
    IF OLD.state = 'indeterminate' AND NEW.state NOT IN ('indeterminate','committed','aborted') THEN
        RAISE EXCEPTION 'indeterminate DuckLake attempt may only be reconciled to committed or aborted';
    END IF;
    IF OLD.state NOT IN ('running','indeterminate') THEN
        IF NEW.state <> OLD.state
           OR NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id
           OR NEW.commit_marker IS DISTINCT FROM OLD.commit_marker
           OR NEW.termination_evidence IS DISTINCT FROM OLD.termination_evidence
           OR NEW.terminal_at IS DISTINCT FROM OLD.terminal_at
           OR NEW.updated_at IS DISTINCT FROM OLD.updated_at THEN
            RAISE EXCEPTION 'DuckLake terminal attempt evidence is immutable';
        END IF;
    ELSIF OLD.state = 'running' AND NEW.state = 'running'
          AND (NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id
               OR NEW.commit_marker IS DISTINCT FROM OLD.commit_marker
               OR NEW.termination_evidence IS DISTINCT FROM OLD.termination_evidence
               OR NEW.terminal_at IS DISTINCT FROM OLD.terminal_at) THEN
        RAISE EXCEPTION 'DuckLake running attempt evidence is immutable';
    ELSIF OLD.state = 'indeterminate' AND NEW.state = 'indeterminate'
          AND (NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id
               OR NEW.commit_marker IS DISTINCT FROM OLD.commit_marker
               OR NEW.termination_evidence IS DISTINCT FROM OLD.termination_evidence
               OR NEW.terminal_at IS DISTINCT FROM OLD.terminal_at
               OR NEW.updated_at IS DISTINCT FROM OLD.updated_at) THEN
        RAISE EXCEPTION 'DuckLake indeterminate attempt evidence is immutable';
    END IF;
    IF NEW.state = 'committed' THEN
        IF NEW.commit_marker->>'attempt_id' IS DISTINCT FROM NEW.attempt_id::text
           OR NEW.commit_marker->>'request_digest' IS DISTINCT FROM NEW.request_digest
           OR NEW.commit_marker->>'plan_digest' IS DISTINCT FROM NEW.plan_digest
           OR NEW.commit_marker->>'physical_pool_id' IS DISTINCT FROM NEW.physical_pool_id
           OR NEW.commit_marker->>'lease_epoch' IS DISTINCT FROM NEW.fencing_epoch::text THEN
            RAISE EXCEPTION 'DuckLake commit marker does not match attempt identity';
        END IF;
    ELSIF NEW.commit_marker IS NOT NULL OR NEW.snapshot_id IS NOT NULL THEN
        RAISE EXCEPTION 'DuckLake non-committed attempt cannot carry commit evidence';
    END IF;
    RETURN NEW;
END;
$$;

-- Snapshot roots and query leases expose the same immutable identity rule as
-- attempts. Their lifecycle columns are intentionally mutable only through
-- the monotonic release/expiry operations in the repository.
CREATE OR REPLACE FUNCTION ducklake.reject_snapshot_root_identity_change()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.root_id <> OLD.root_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.snapshot_id <> OLD.snapshot_id
       OR NEW.root_kind <> OLD.root_kind THEN
        RAISE EXCEPTION 'DuckLake snapshot root identity is immutable';
    END IF;
    IF NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.evidence IS DISTINCT FROM OLD.evidence THEN
        RAISE EXCEPTION 'DuckLake snapshot root evidence is immutable';
    END IF;
    IF OLD.state = 'cleanup-complete' THEN
        IF NEW.state <> OLD.state
           OR NEW.retired_at IS DISTINCT FROM OLD.retired_at
           OR NEW.expired_at IS DISTINCT FROM OLD.expired_at
           OR NEW.quarantined_at IS DISTINCT FROM OLD.quarantined_at
           OR NEW.cleanup_completed_at IS DISTINCT FROM OLD.cleanup_completed_at
           OR NEW.quarantine_evidence IS DISTINCT FROM OLD.quarantine_evidence
           OR NEW.cleanup_evidence IS DISTINCT FROM OLD.cleanup_evidence THEN
            RAISE EXCEPTION 'DuckLake cleanup-complete snapshot root is immutable';
        END IF;
    ELSIF OLD.state IN ('expired', 'quarantined') AND NEW.state = 'live' THEN
        RAISE EXCEPTION 'DuckLake snapshot root lifecycle is monotonic';
    ELSIF OLD.state = 'quarantined' AND NEW.state NOT IN ('quarantined', 'cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake snapshot root lifecycle is monotonic';
    ELSIF OLD.state = 'expired' AND NEW.state NOT IN ('expired', 'quarantined', 'cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake snapshot root lifecycle is monotonic';
    ELSIF OLD.state = 'live' AND NEW.state = 'retiring'
          AND (NEW.retired_at IS NULL OR NEW.expired_at IS NOT NULL) THEN
        RAISE EXCEPTION 'DuckLake retiring snapshot root requires retired_at';
    ELSIF OLD.state IN ('live', 'retiring') AND NEW.state = 'expired'
          AND NEW.expired_at IS NULL THEN
        RAISE EXCEPTION 'DuckLake expired snapshot root requires expired_at';
    ELSIF OLD.state = 'retiring' AND NEW.state NOT IN ('retiring', 'expired') THEN
        RAISE EXCEPTION 'DuckLake snapshot root lifecycle is monotonic';
    ELSIF OLD.state = 'live' AND NEW.state NOT IN ('live', 'retiring', 'expired') THEN
        RAISE EXCEPTION 'DuckLake snapshot root lifecycle is monotonic';
    ELSIF NEW.state = 'cleanup-complete' AND (OLD.state <> 'quarantined' OR NEW.cleanup_completed_at IS NULL OR NEW.cleanup_evidence IS NULL) THEN
        RAISE EXCEPTION 'DuckLake snapshot root must be quarantined before cleanup-complete';
    ELSIF NEW.state = 'quarantined' AND (NEW.expired_at IS NULL OR NEW.quarantined_at IS NULL OR NEW.quarantine_evidence IS NULL) THEN
        RAISE EXCEPTION 'DuckLake quarantined snapshot root requires expired_at';
    ELSIF NEW.state = OLD.state
          AND (NEW.retired_at IS DISTINCT FROM OLD.retired_at
               OR NEW.expired_at IS DISTINCT FROM OLD.expired_at
               OR NEW.quarantined_at IS DISTINCT FROM OLD.quarantined_at
               OR NEW.cleanup_completed_at IS DISTINCT FROM OLD.cleanup_completed_at
               OR NEW.quarantine_evidence IS DISTINCT FROM OLD.quarantine_evidence
               OR NEW.cleanup_evidence IS DISTINCT FROM OLD.cleanup_evidence
               ) THEN
        RAISE EXCEPTION 'DuckLake snapshot root evidence is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.reject_snapshot_lease_identity_change()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.lease_id <> OLD.lease_id
       OR NEW.delivery_id <> OLD.delivery_id
       OR NEW.generation_id <> OLD.generation_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.snapshot_id <> OLD.snapshot_id
       OR NEW.owner_id <> OLD.owner_id
       OR NEW.fencing_epoch <> OLD.fencing_epoch THEN
        RAISE EXCEPTION 'DuckLake snapshot lease identity is immutable';
    END IF;
    IF NEW.acquired_at IS DISTINCT FROM OLD.acquired_at
       OR NEW.evidence IS DISTINCT FROM OLD.evidence THEN
        RAISE EXCEPTION 'DuckLake snapshot lease evidence is immutable';
    END IF;
    IF OLD.state <> 'active' AND NEW.state <> OLD.state THEN
        RAISE EXCEPTION 'DuckLake snapshot lease lifecycle is monotonic';
    END IF;
    IF OLD.state <> 'active' THEN
        IF NEW.expires_at IS DISTINCT FROM OLD.expires_at
           OR NEW.acquired_at IS DISTINCT FROM OLD.acquired_at
           OR NEW.released_at IS DISTINCT FROM OLD.released_at
           OR NEW.evidence IS DISTINCT FROM OLD.evidence THEN
            RAISE EXCEPTION 'DuckLake terminal snapshot lease is immutable';
        END IF;
    ELSIF NEW.state = 'active'
          AND NEW.expires_at < OLD.expires_at THEN
        RAISE EXCEPTION 'DuckLake snapshot lease expiry cannot move backwards';
    ELSIF OLD.state = 'active' AND NEW.state <> 'active'
          AND NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
        RAISE EXCEPTION 'DuckLake terminal snapshot lease expiry is immutable';
    ELSIF NEW.state = 'released' AND NEW.released_at IS NULL THEN
        RAISE EXCEPTION 'Released DuckLake snapshot lease requires released_at';
    ELSIF NEW.state = 'expired' AND NEW.released_at IS NULL THEN
        RAISE EXCEPTION 'Expired DuckLake snapshot lease requires released_at';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.reject_snapshot_retention_identity_change()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.snapshot_id <> OLD.snapshot_id THEN
        RAISE EXCEPTION 'DuckLake snapshot retention identity is immutable';
    END IF;
    IF NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'DuckLake snapshot retention created_at is immutable';
    END IF;
    IF OLD.state = 'cleanup-complete' AND NEW.state <> OLD.state THEN
        RAISE EXCEPTION 'DuckLake cleanup-complete snapshot retention is immutable';
    END IF;
    IF OLD.state = 'quarantined' AND NEW.state NOT IN ('quarantined', 'cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle is monotonic';
    END IF;
    IF OLD.state = 'expired' AND NEW.state NOT IN ('expired', 'quarantined', 'cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle is monotonic';
    END IF;
    IF OLD.state IN ('expired', 'quarantined', 'cleanup-complete') AND NEW.state = OLD.state AND (
           NEW.protected_until IS DISTINCT FROM OLD.protected_until
        OR NEW.retired_at IS DISTINCT FROM OLD.retired_at
        OR NEW.expired_at IS DISTINCT FROM OLD.expired_at
        OR NEW.evidence IS DISTINCT FROM OLD.evidence
        OR NEW.quarantined_at IS DISTINCT FROM OLD.quarantined_at
        OR NEW.cleanup_completed_at IS DISTINCT FROM OLD.cleanup_completed_at
        OR NEW.quarantine_evidence IS DISTINCT FROM OLD.quarantine_evidence
        OR NEW.cleanup_evidence IS DISTINCT FROM OLD.cleanup_evidence
        OR (NEW.cleanup_owner_id IS DISTINCT FROM OLD.cleanup_owner_id
            OR NEW.cleanup_fencing_epoch IS DISTINCT FROM OLD.cleanup_fencing_epoch
            OR NEW.cleanup_lease_expires_at IS DISTINCT FROM OLD.cleanup_lease_expires_at)
           AND NOT (NEW.state IN ('expired','quarantined')
                    AND NEW.cleanup_fencing_epoch > OLD.cleanup_fencing_epoch)) THEN
        RAISE EXCEPTION 'DuckLake expired snapshot retention is immutable';
    END IF;
    IF NEW.cleanup_fencing_epoch < OLD.cleanup_fencing_epoch THEN
        RAISE EXCEPTION 'DuckLake cleanup fencing epoch cannot move backwards';
    END IF;
    IF NEW.cleanup_fencing_epoch > OLD.cleanup_fencing_epoch
       AND (NEW.state NOT IN ('expired','quarantined') OR NEW.cleanup_owner_id IS NULL OR NEW.cleanup_lease_expires_at IS NULL) THEN
        RAISE EXCEPTION 'DuckLake cleanup claim requires an expiring snapshot';
    END IF;
    IF NEW.protected_until IS NOT NULL AND OLD.protected_until IS NOT NULL
       AND NEW.protected_until < OLD.protected_until THEN
        RAISE EXCEPTION 'DuckLake snapshot protection cannot move backwards';
    END IF;
    IF OLD.state IN ('retiring', 'expired', 'quarantined', 'cleanup-complete') AND NEW.state = 'live' THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle is monotonic';
    END IF;
    IF OLD.state = 'retiring' AND NEW.state NOT IN ('retiring', 'expired') THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle is monotonic';
    END IF;
    IF OLD.state = 'live' AND NEW.state NOT IN ('live', 'retiring') THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle is monotonic';
    END IF;
    IF OLD.state = 'live' AND NEW.state = 'retiring'
       AND EXISTS (
           SELECT 1 FROM ducklake.snapshot_root
            WHERE physical_pool_id=OLD.physical_pool_id
              AND catalog_id=OLD.catalog_id
              AND snapshot_id=OLD.snapshot_id
              AND state IN ('live','retiring')) THEN
        RAISE EXCEPTION 'DuckLake snapshot durable roots must be released before retirement';
    END IF;
    IF NEW.evidence IS DISTINCT FROM OLD.evidence
       AND NOT (OLD.state = 'retiring' AND NEW.state = 'expired') THEN
        RAISE EXCEPTION 'DuckLake snapshot retention evidence is immutable';
    END IF;
    IF NEW.state = OLD.state AND (
           NEW.retired_at IS DISTINCT FROM OLD.retired_at
        OR NEW.expired_at IS DISTINCT FROM OLD.expired_at) THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle timestamps are immutable';
    END IF;
    IF OLD.state = 'live' AND NEW.state IN ('expired', 'quarantined', 'cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake snapshot must retire before expiration';
    END IF;
    IF OLD.state = 'retiring' AND NEW.state = 'expired'
       AND (EXISTS (
               SELECT 1 FROM ducklake.snapshot_lease
                WHERE physical_pool_id=OLD.physical_pool_id
                  AND catalog_id=OLD.catalog_id
                  AND snapshot_id=OLD.snapshot_id
                  AND state='active')
            OR EXISTS (
               SELECT 1 FROM ducklake.snapshot_root
                WHERE physical_pool_id=OLD.physical_pool_id
                  AND catalog_id=OLD.catalog_id
                  AND snapshot_id=OLD.snapshot_id
                  AND state IN ('live','retiring'))) THEN
        RAISE EXCEPTION 'DuckLake snapshot leases or roots remain';
    END IF;
    IF NEW.state = 'cleanup-complete' AND (OLD.state <> 'quarantined' OR NEW.cleanup_completed_at IS NULL OR NEW.cleanup_evidence IS NULL) THEN
        RAISE EXCEPTION 'DuckLake snapshot must be quarantined before cleanup-complete';
    END IF;
    IF NEW.state = 'quarantined' AND (NEW.expired_at IS NULL OR NEW.quarantined_at IS NULL OR NEW.quarantine_evidence IS NULL) THEN
        RAISE EXCEPTION 'DuckLake quarantined snapshot requires expired_at';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.reject_snapshot_orphan_identity_change()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.orphan_id <> OLD.orphan_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.snapshot_id <> OLD.snapshot_id
       OR NEW.discovered_at IS DISTINCT FROM OLD.discovered_at THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan identity is immutable';
    END IF;
    IF OLD.state = 'cleanup-complete' THEN
        IF NEW.state <> OLD.state OR NEW.evidence IS DISTINCT FROM OLD.evidence
           OR NEW.resolved_at IS DISTINCT FROM OLD.resolved_at
           OR NEW.cleanup_owner_id IS DISTINCT FROM OLD.cleanup_owner_id
           OR NEW.cleanup_fencing_epoch IS DISTINCT FROM OLD.cleanup_fencing_epoch
           OR NEW.cleanup_lease_expires_at IS DISTINCT FROM OLD.cleanup_lease_expires_at THEN
            RAISE EXCEPTION 'DuckLake cleanup-complete snapshot orphan is immutable';
        END IF;
    ELSIF OLD.state = 'quarantined' AND NEW.state NOT IN ('quarantined', 'cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan lifecycle is monotonic';
    ELSIF OLD.state = 'quarantined' AND NEW.state = 'quarantined'
          AND (NEW.evidence IS DISTINCT FROM OLD.evidence OR NEW.resolved_at IS DISTINCT FROM OLD.resolved_at
               OR ((NEW.cleanup_owner_id IS DISTINCT FROM OLD.cleanup_owner_id
                    OR NEW.cleanup_lease_expires_at IS DISTINCT FROM OLD.cleanup_lease_expires_at)
                   AND NOT (NEW.cleanup_fencing_epoch > OLD.cleanup_fencing_epoch))) THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan evidence is immutable';
    ELSIF NEW.state = 'cleanup-complete' AND NEW.resolved_at IS NULL THEN
        RAISE EXCEPTION 'DuckLake cleanup-complete snapshot orphan requires resolved_at';
    END IF;
    IF NEW.cleanup_fencing_epoch < OLD.cleanup_fencing_epoch THEN
        RAISE EXCEPTION 'DuckLake orphan cleanup fencing epoch cannot move backwards';
    END IF;
    IF NEW.cleanup_fencing_epoch > OLD.cleanup_fencing_epoch
       AND (NEW.state <> 'quarantined' OR NEW.cleanup_owner_id IS NULL OR NEW.cleanup_lease_expires_at IS NULL) THEN
        RAISE EXCEPTION 'DuckLake orphan cleanup claim requires a quarantined orphan';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS generation_binding_immutable ON ducklake.generation_binding;
CREATE TRIGGER generation_binding_immutable
    BEFORE UPDATE OR DELETE ON ducklake.generation_binding
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_immutable_change();

DROP TRIGGER IF EXISTS catalog_identity_immutable ON ducklake.catalog_identity;
CREATE TRIGGER catalog_identity_immutable
    BEFORE UPDATE OR DELETE ON ducklake.catalog_identity
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_immutable_change();

DROP TRIGGER IF EXISTS attempt_identity_immutable ON ducklake.attempt_evidence;
CREATE TRIGGER attempt_identity_immutable
    BEFORE UPDATE OR DELETE ON ducklake.attempt_evidence
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_attempt_identity_change();

DROP TRIGGER IF EXISTS snapshot_root_identity_immutable ON ducklake.snapshot_root;
CREATE TRIGGER snapshot_root_identity_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_root
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_root_identity_change();

DROP TRIGGER IF EXISTS snapshot_lease_identity_immutable ON ducklake.snapshot_lease;
CREATE TRIGGER snapshot_lease_identity_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_lease
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_lease_identity_change();

DROP TRIGGER IF EXISTS snapshot_retention_identity_immutable ON ducklake.snapshot_retention;
CREATE TRIGGER snapshot_retention_identity_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_retention
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_retention_identity_change();

DROP TRIGGER IF EXISTS snapshot_orphan_identity_immutable ON ducklake.snapshot_orphan;
CREATE TRIGGER snapshot_orphan_identity_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_orphan
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_orphan_identity_change();

CREATE OR REPLACE FUNCTION ducklake.reject_catalog_migration_change()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.migration_id <> OLD.migration_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.owner_id <> OLD.owner_id
       OR NEW.fencing_epoch <> OLD.fencing_epoch
       OR NEW.global_fencing_epoch <> OLD.global_fencing_epoch
       OR NEW.current_duckdb_runtime <> OLD.current_duckdb_runtime
       OR NEW.current_ducklake_extension <> OLD.current_ducklake_extension
       OR NEW.current_catalog_format <> OLD.current_catalog_format
       OR NEW.current_compatibility_digest <> OLD.current_compatibility_digest
       OR NEW.current_catalog_schema_version <> OLD.current_catalog_schema_version
       OR NEW.target_duckdb_runtime <> OLD.target_duckdb_runtime
       OR NEW.target_ducklake_extension <> OLD.target_ducklake_extension
       OR NEW.target_catalog_format <> OLD.target_catalog_format
       OR NEW.target_compatibility_digest <> OLD.target_compatibility_digest
       OR NEW.target_catalog_schema_version <> OLD.target_catalog_schema_version
       OR NEW.started_at <> OLD.started_at
       OR NEW.begin_evidence IS DISTINCT FROM OLD.begin_evidence THEN
        RAISE EXCEPTION 'DuckLake catalog migration identity is immutable';
    END IF;
    IF OLD.state <> 'running' THEN
        IF NEW.state <> OLD.state
           OR NEW.terminal_at IS DISTINCT FROM OLD.terminal_at
           OR NEW.completion_evidence IS DISTINCT FROM OLD.completion_evidence
           OR NEW.failure_evidence IS DISTINCT FROM OLD.failure_evidence
           OR NEW.recovery_decision IS DISTINCT FROM OLD.recovery_decision
           OR NEW.decision_evidence IS DISTINCT FROM OLD.decision_evidence THEN
            RAISE EXCEPTION 'DuckLake terminal catalog migration is immutable';
        END IF;
    ELSIF NEW.state = 'running' AND (NEW.terminal_at IS NOT NULL OR NEW.completion_evidence IS NOT NULL OR NEW.failure_evidence IS NOT NULL OR NEW.recovery_decision IS NOT NULL OR NEW.decision_evidence IS NOT NULL) THEN
        RAISE EXCEPTION 'running DuckLake catalog migration cannot carry terminal evidence';
    END IF;
    IF OLD.state = 'running' AND NEW.state NOT IN ('running', 'completed', 'failed') THEN
        RAISE EXCEPTION 'DuckLake catalog migration lifecycle is monotonic';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.reject_snapshot_requalification_change()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.qualification_id <> OLD.qualification_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.snapshot_id <> OLD.snapshot_id
       OR NEW.migration_id <> OLD.migration_id
       OR NEW.duckdb_runtime <> OLD.duckdb_runtime
       OR NEW.ducklake_extension <> OLD.ducklake_extension
       OR NEW.catalog_format <> OLD.catalog_format
       OR NEW.compatibility_digest <> OLD.compatibility_digest
       OR NEW.catalog_schema_version <> OLD.catalog_schema_version
       OR NEW.qualified_at <> OLD.qualified_at
       OR NEW.evidence IS DISTINCT FROM OLD.evidence
       OR NEW.status <> OLD.status THEN
        RAISE EXCEPTION 'DuckLake snapshot requalification evidence is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.reject_migration_fence_change()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.scope <> OLD.scope
       OR NEW.physical_pool_id <> OLD.physical_pool_id THEN
        RAISE EXCEPTION 'DuckLake migration fence identity is immutable';
    END IF;
    IF NEW.fencing_epoch < OLD.fencing_epoch THEN
        RAISE EXCEPTION 'DuckLake migration fencing epoch cannot move backwards';
    END IF;
    IF NEW.fencing_epoch = OLD.fencing_epoch
       AND NEW.owner_id IS DISTINCT FROM OLD.owner_id
       AND NOT (NEW.owner_id IS NULL AND NEW.lease_expires_at IS NULL) THEN
        RAISE EXCEPTION 'DuckLake migration fence owner change requires a new epoch';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS catalog_migration_immutable ON ducklake.catalog_migration;
CREATE TRIGGER catalog_migration_immutable
    BEFORE UPDATE OR DELETE ON ducklake.catalog_migration
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_catalog_migration_change();

DROP TRIGGER IF EXISTS snapshot_requalification_immutable ON ducklake.snapshot_requalification;
CREATE TRIGGER snapshot_requalification_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_requalification
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_requalification_change();

DROP TRIGGER IF EXISTS migration_fence_monotonic ON ducklake.migration_fence;
CREATE TRIGGER migration_fence_monotonic
    BEFORE UPDATE OR DELETE ON ducklake.migration_fence
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_migration_fence_change();

-- The control upgrade coordinator receives no direct DML on authority rows. These narrowly
-- scoped SECURITY DEFINER functions are the database capability boundary: all
-- fence claims, compatibility registration, lifecycle transitions, and
-- requalification writes re-check the active owner/epoch under PostgreSQL's
-- clock while holding the relevant rows.
CREATE OR REPLACE FUNCTION ducklake.acquire_migration_fence(
    p_scope text,
    p_physical_pool_id text,
    p_owner_id text,
    p_lease_expires_at timestamptz
) RETURNS TABLE(scope text, physical_pool_id text, owner_id text, fencing_epoch bigint, lease_expires_at timestamptz)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_lease timestamptz := COALESCE(p_lease_expires_at, v_now + interval '24 hours');
    v_owner text;
    v_epoch bigint;
    v_expiry timestamptz;
    v_global_owner text;
    v_global_epoch bigint;
    v_global_expiry timestamptz;
BEGIN
    IF p_scope NOT IN ('global','pool')
       OR (p_scope = 'global' AND p_physical_pool_id <> '')
       OR (p_scope = 'pool' AND (p_physical_pool_id = '' OR p_physical_pool_id <> btrim(p_physical_pool_id) OR octet_length(p_physical_pool_id) > 255))
       OR p_owner_id = '' OR p_owner_id <> btrim(p_owner_id) OR octet_length(p_owner_id) > 255
       OR v_lease <= v_now OR v_lease > v_now + interval '24 hours' THEN
        RAISE EXCEPTION 'invalid migration fence claim';
    END IF;
    INSERT INTO ducklake.migration_fence(scope,physical_pool_id)
    VALUES ('global','') ON CONFLICT DO NOTHING;
    SELECT f.owner_id,f.fencing_epoch,f.lease_expires_at
      INTO v_global_owner,v_global_epoch,v_global_expiry
      FROM ducklake.migration_fence f
     WHERE f.scope='global' AND f.physical_pool_id=''
     FOR UPDATE;
    IF v_global_owner IS NOT NULL AND v_global_expiry > v_now
       AND p_scope='pool' AND v_global_owner <> p_owner_id THEN
        RAISE EXCEPTION 'migration fence busy';
    END IF;
    IF p_scope='global' AND v_global_owner IS NOT NULL AND v_global_expiry > v_now
       AND v_global_owner = p_owner_id THEN
        RETURN QUERY SELECT 'global'::text,''::text,v_global_owner,v_global_epoch,v_global_expiry;
        RETURN;
    END IF;
    IF p_scope='global' AND EXISTS (
        SELECT 1 FROM ducklake.migration_fence f
         WHERE f.scope='pool' AND f.owner_id IS NOT NULL AND f.lease_expires_at > v_now
    ) THEN
        RAISE EXCEPTION 'migration fence busy';
    END IF;
    IF p_scope='pool' THEN
        INSERT INTO ducklake.migration_fence(scope,physical_pool_id)
        VALUES ('pool',p_physical_pool_id) ON CONFLICT DO NOTHING;
    END IF;
    SELECT f.owner_id,f.fencing_epoch,f.lease_expires_at
      INTO v_owner,v_epoch,v_expiry
      FROM ducklake.migration_fence f
     WHERE f.scope=p_scope AND f.physical_pool_id=p_physical_pool_id
     FOR UPDATE;
    IF v_owner IS NOT NULL AND v_expiry > v_now THEN
        IF v_owner = p_owner_id THEN
            RETURN QUERY SELECT p_scope,p_physical_pool_id,v_owner,v_epoch,v_expiry;
            RETURN;
        END IF;
        RAISE EXCEPTION 'migration fence busy';
    END IF;
    UPDATE ducklake.migration_fence f
       SET owner_id=p_owner_id, fencing_epoch=f.fencing_epoch+1,
           lease_expires_at=v_lease, updated_at=v_now
     WHERE f.scope=p_scope AND f.physical_pool_id=p_physical_pool_id
     RETURNING f.owner_id,f.fencing_epoch,f.lease_expires_at
      INTO v_owner,v_epoch,v_expiry;
    RETURN QUERY SELECT p_scope,p_physical_pool_id,v_owner,v_epoch,v_expiry;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.release_migration_fence(
    p_scope text, p_physical_pool_id text, p_owner_id text, p_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_owner text; v_epoch bigint;
BEGIN
    UPDATE ducklake.migration_fence
       SET owner_id=NULL, lease_expires_at=NULL, updated_at=clock_timestamp()
     WHERE scope=p_scope AND physical_pool_id=p_physical_pool_id
       AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch;
    IF FOUND THEN RETURN; END IF;
    SELECT owner_id,fencing_epoch INTO v_owner,v_epoch
      FROM ducklake.migration_fence
     WHERE scope=p_scope AND physical_pool_id=p_physical_pool_id;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence not found'; END IF;
    IF v_owner IS NULL AND v_epoch=p_fencing_epoch THEN RETURN; END IF;
    RAISE EXCEPTION 'migration fence stale';
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.renew_migration_fence(
    p_scope text, p_physical_pool_id text, p_owner_id text,
    p_fencing_epoch bigint, p_lease_expires_at timestamptz
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_lease timestamptz := COALESCE(p_lease_expires_at, v_now + interval '24 hours');
    v_owner text; v_epoch bigint; v_expiry timestamptz;
BEGIN
    IF v_lease <= v_now OR v_lease > v_now + interval '24 hours' THEN
        RAISE EXCEPTION 'invalid migration fence renewal';
    END IF;
    UPDATE ducklake.migration_fence
       SET lease_expires_at=v_lease, updated_at=v_now
     WHERE scope=p_scope AND physical_pool_id=p_physical_pool_id
       AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch
       AND lease_expires_at > v_now;
    IF FOUND THEN RETURN; END IF;
    SELECT owner_id,fencing_epoch,lease_expires_at INTO v_owner,v_epoch,v_expiry
      FROM ducklake.migration_fence
     WHERE scope=p_scope AND physical_pool_id=p_physical_pool_id;
    IF NOT FOUND OR v_owner IS DISTINCT FROM p_owner_id OR v_epoch <> p_fencing_epoch THEN
        RAISE EXCEPTION 'migration fence stale';
    END IF;
    IF v_expiry IS NULL OR v_expiry <= v_now THEN
        RAISE EXCEPTION 'migration fence expired';
    END IF;
    RAISE EXCEPTION 'migration fence stale';
END;
$$;

DROP FUNCTION IF EXISTS ducklake.register_catalog_runtime_compatibility(text,text,text,text,text,text,text);
CREATE OR REPLACE FUNCTION ducklake.register_catalog_runtime_compatibility(
    p_physical_pool_id text, p_catalog_id text, p_duckdb_runtime text,
    p_ducklake_extension text, p_catalog_format text,
    p_compatibility_digest text, p_catalog_schema_version text,
    p_owner_id text, p_pool_fencing_epoch bigint, p_global_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_exists boolean; v_now timestamptz := clock_timestamp(); v_existing record;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence
     WHERE scope='global' AND physical_pool_id='' AND owner_id=p_owner_id
       AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence
     WHERE scope='pool' AND physical_pool_id=p_physical_pool_id AND owner_id=p_owner_id
       AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    SELECT EXISTS (SELECT 1 FROM ducklake.catalog_runtime_compatibility WHERE physical_pool_id=p_physical_pool_id) INTO v_exists;
    IF v_exists THEN
        SELECT * INTO v_existing FROM ducklake.catalog_runtime_compatibility WHERE physical_pool_id=p_physical_pool_id;
        IF v_existing.catalog_id IS DISTINCT FROM p_catalog_id
           OR v_existing.duckdb_runtime IS DISTINCT FROM p_duckdb_runtime
           OR v_existing.ducklake_extension IS DISTINCT FROM p_ducklake_extension
           OR v_existing.catalog_format IS DISTINCT FROM p_catalog_format
           OR v_existing.compatibility_digest IS DISTINCT FROM p_compatibility_digest
           OR v_existing.catalog_schema_version IS DISTINCT FROM p_catalog_schema_version THEN
            RAISE EXCEPTION 'runtime compatibility mismatch';
        END IF;
        RETURN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM ducklake.catalog_identity
         WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id
           AND compatibility_digest=p_compatibility_digest
           AND catalog_schema_version=p_catalog_schema_version
    ) THEN
        RAISE EXCEPTION 'runtime compatibility mismatch';
    END IF;
    INSERT INTO ducklake.catalog_runtime_compatibility
      (physical_pool_id,catalog_id,duckdb_runtime,ducklake_extension,catalog_format,
       compatibility_digest,catalog_schema_version,updated_at)
    VALUES (p_physical_pool_id,p_catalog_id,p_duckdb_runtime,p_ducklake_extension,
            p_catalog_format,p_compatibility_digest,p_catalog_schema_version,clock_timestamp());
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.begin_catalog_migration(
    p_migration_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_pool_fencing_epoch bigint, p_global_fencing_epoch bigint,
    p_current_duckdb_runtime text, p_current_ducklake_extension text,
    p_current_catalog_format text, p_current_compatibility_digest text,
    p_current_catalog_schema_version text, p_target_duckdb_runtime text,
    p_target_ducklake_extension text, p_target_catalog_format text,
    p_target_compatibility_digest text, p_target_catalog_schema_version text,
    p_begin_evidence jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_catalog record; v_existing record;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence
     WHERE scope='global' AND physical_pool_id='' AND owner_id=p_owner_id
       AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence
     WHERE scope='pool' AND physical_pool_id=p_physical_pool_id AND owner_id=p_owner_id
       AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    IF jsonb_typeof(p_begin_evidence) <> 'object' OR p_begin_evidence = '{}'::jsonb
       OR NOT ((p_begin_evidence->>'drain_verified')='true' OR (p_begin_evidence->>'drained')='true' OR (p_begin_evidence->>'readers_drained')='true')
       OR NOT ((p_begin_evidence->>'backup_verified')='true' OR (p_begin_evidence->>'backup_verification')='true' OR (p_begin_evidence->>'backup')='true') THEN
        RAISE EXCEPTION 'migration evidence required';
    END IF;
    SELECT * INTO v_existing FROM ducklake.catalog_migration WHERE migration_id=p_migration_id FOR UPDATE;
    IF FOUND THEN
        IF v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
           OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id
           OR v_existing.owner_id IS DISTINCT FROM p_owner_id
           OR v_existing.fencing_epoch IS DISTINCT FROM p_pool_fencing_epoch
           OR v_existing.global_fencing_epoch IS DISTINCT FROM p_global_fencing_epoch
           OR v_existing.current_duckdb_runtime IS DISTINCT FROM p_current_duckdb_runtime
           OR v_existing.current_ducklake_extension IS DISTINCT FROM p_current_ducklake_extension
           OR v_existing.current_catalog_format IS DISTINCT FROM p_current_catalog_format
           OR v_existing.current_compatibility_digest IS DISTINCT FROM p_current_compatibility_digest
           OR v_existing.current_catalog_schema_version IS DISTINCT FROM p_current_catalog_schema_version
           OR v_existing.target_duckdb_runtime IS DISTINCT FROM p_target_duckdb_runtime
           OR v_existing.target_ducklake_extension IS DISTINCT FROM p_target_ducklake_extension
           OR v_existing.target_catalog_format IS DISTINCT FROM p_target_catalog_format
           OR v_existing.target_compatibility_digest IS DISTINCT FROM p_target_compatibility_digest
           OR v_existing.target_catalog_schema_version IS DISTINCT FROM p_target_catalog_schema_version
           OR v_existing.state IS DISTINCT FROM 'running'
           OR v_existing.begin_evidence IS DISTINCT FROM p_begin_evidence THEN
            RAISE EXCEPTION 'migration conflict';
        END IF;
        RETURN;
    END IF;
    SELECT * INTO v_catalog FROM ducklake.catalog_runtime_compatibility
     WHERE physical_pool_id=p_physical_pool_id FOR SHARE;
    IF NOT FOUND OR v_catalog.catalog_id <> p_catalog_id
       OR v_catalog.duckdb_runtime <> p_current_duckdb_runtime
       OR v_catalog.ducklake_extension <> p_current_ducklake_extension
       OR v_catalog.catalog_format <> p_current_catalog_format
       OR v_catalog.compatibility_digest <> p_current_compatibility_digest
       OR v_catalog.catalog_schema_version <> p_current_catalog_schema_version THEN
        RAISE EXCEPTION 'runtime compatibility mismatch';
    END IF;
    INSERT INTO ducklake.catalog_migration
      (migration_id,physical_pool_id,catalog_id,owner_id,fencing_epoch,global_fencing_epoch,
       current_duckdb_runtime,current_ducklake_extension,current_catalog_format,
       current_compatibility_digest,current_catalog_schema_version,target_duckdb_runtime,
       target_ducklake_extension,target_catalog_format,target_compatibility_digest,
       target_catalog_schema_version,state,started_at,begin_evidence)
    VALUES (p_migration_id,p_physical_pool_id,p_catalog_id,p_owner_id,p_pool_fencing_epoch,
            p_global_fencing_epoch,p_current_duckdb_runtime,p_current_ducklake_extension,
            p_current_catalog_format,p_current_compatibility_digest,p_current_catalog_schema_version,
            p_target_duckdb_runtime,p_target_ducklake_extension,p_target_catalog_format,
            p_target_compatibility_digest,p_target_catalog_schema_version,'running',v_now,p_begin_evidence)
    ON CONFLICT (migration_id) DO NOTHING;
    SELECT * INTO v_existing FROM ducklake.catalog_migration WHERE migration_id=p_migration_id;
    IF NOT FOUND OR v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id OR v_existing.owner_id IS DISTINCT FROM p_owner_id
       OR v_existing.fencing_epoch IS DISTINCT FROM p_pool_fencing_epoch OR v_existing.global_fencing_epoch IS DISTINCT FROM p_global_fencing_epoch
       OR v_existing.current_duckdb_runtime IS DISTINCT FROM p_current_duckdb_runtime OR v_existing.current_ducklake_extension IS DISTINCT FROM p_current_ducklake_extension
       OR v_existing.current_catalog_format IS DISTINCT FROM p_current_catalog_format OR v_existing.current_compatibility_digest IS DISTINCT FROM p_current_compatibility_digest
       OR v_existing.current_catalog_schema_version IS DISTINCT FROM p_current_catalog_schema_version OR v_existing.target_duckdb_runtime IS DISTINCT FROM p_target_duckdb_runtime
       OR v_existing.target_ducklake_extension IS DISTINCT FROM p_target_ducklake_extension OR v_existing.target_catalog_format IS DISTINCT FROM p_target_catalog_format
       OR v_existing.target_compatibility_digest IS DISTINCT FROM p_target_compatibility_digest OR v_existing.target_catalog_schema_version IS DISTINCT FROM p_target_catalog_schema_version
       OR v_existing.state IS DISTINCT FROM 'running' OR v_existing.begin_evidence IS DISTINCT FROM p_begin_evidence THEN
        RAISE EXCEPTION 'migration conflict';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.complete_catalog_migration(
    p_migration_id uuid, p_owner_id text, p_pool_fencing_epoch bigint,
    p_global_fencing_epoch bigint, p_completion_evidence jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE m record; v_now timestamptz := clock_timestamp(); v_missing bigint; v_rows bigint;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='global' AND physical_pool_id=''
      AND owner_id=p_owner_id AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='pool' AND physical_pool_id=(SELECT physical_pool_id FROM ducklake.catalog_migration WHERE migration_id=p_migration_id)
      AND owner_id=p_owner_id AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    SELECT * INTO m FROM ducklake.catalog_migration WHERE migration_id=p_migration_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'catalog migration not found'; END IF;
    IF m.state='completed' THEN
        IF m.completion_evidence = p_completion_evidence THEN RETURN; END IF;
        RAISE EXCEPTION 'migration conflict';
    END IF;
    IF m.state <> 'running' THEN RAISE EXCEPTION 'catalog migration terminal'; END IF;
    IF m.owner_id <> p_owner_id OR m.fencing_epoch <> p_pool_fencing_epoch OR m.global_fencing_epoch <> p_global_fencing_epoch THEN
        RAISE EXCEPTION 'migration fence stale';
    END IF;
    SELECT count(*) INTO v_missing FROM ducklake.snapshot_retention r
     WHERE r.physical_pool_id=m.physical_pool_id AND r.catalog_id=m.catalog_id AND r.state IN ('live','retiring')
       AND NOT EXISTS (SELECT 1 FROM ducklake.snapshot_requalification q WHERE q.physical_pool_id=r.physical_pool_id AND q.catalog_id=r.catalog_id AND q.snapshot_id=r.snapshot_id AND q.migration_id=p_migration_id AND q.status='qualified' AND q.duckdb_runtime=m.target_duckdb_runtime AND q.ducklake_extension=m.target_ducklake_extension AND q.catalog_format=m.target_catalog_format AND q.compatibility_digest=m.target_compatibility_digest AND q.catalog_schema_version=m.target_catalog_schema_version);
    IF v_missing <> 0 THEN RAISE EXCEPTION 'snapshot qualification missing'; END IF;
    UPDATE ducklake.catalog_runtime_compatibility
       SET duckdb_runtime=m.target_duckdb_runtime,ducklake_extension=m.target_ducklake_extension,
           catalog_format=m.target_catalog_format,compatibility_digest=m.target_compatibility_digest,
           catalog_schema_version=m.target_catalog_schema_version,current_migration_id=p_migration_id,
           updated_at=v_now
     WHERE physical_pool_id=m.physical_pool_id AND catalog_id=m.catalog_id
       AND duckdb_runtime=m.current_duckdb_runtime AND ducklake_extension=m.current_ducklake_extension
       AND catalog_format=m.current_catalog_format AND compatibility_digest=m.current_compatibility_digest
       AND catalog_schema_version=m.current_catalog_schema_version;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'runtime compatibility mismatch'; END IF;
    UPDATE ducklake.catalog_migration
       SET state='completed',terminal_at=v_now,completion_evidence=p_completion_evidence
     WHERE migration_id=p_migration_id AND state='running' AND owner_id=p_owner_id
       AND fencing_epoch=p_pool_fencing_epoch AND global_fencing_epoch=p_global_fencing_epoch;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'migration fence stale'; END IF;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.fail_catalog_migration(
    p_migration_id uuid, p_owner_id text, p_pool_fencing_epoch bigint,
    p_global_fencing_epoch bigint, p_failure_evidence jsonb,
    p_recovery_decision text, p_decision_evidence jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE m record; v_now timestamptz := clock_timestamp(); v_rows bigint;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='global' AND physical_pool_id=''
      AND owner_id=p_owner_id AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='pool' AND physical_pool_id=(SELECT physical_pool_id FROM ducklake.catalog_migration WHERE migration_id=p_migration_id)
      AND owner_id=p_owner_id AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    SELECT * INTO m FROM ducklake.catalog_migration WHERE migration_id=p_migration_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'catalog migration not found'; END IF;
    IF m.state='failed' THEN
        IF m.failure_evidence = p_failure_evidence AND m.recovery_decision = p_recovery_decision AND m.decision_evidence = p_decision_evidence THEN RETURN; END IF;
        RAISE EXCEPTION 'migration conflict';
    END IF;
    IF m.state <> 'running' THEN RAISE EXCEPTION 'catalog migration terminal'; END IF;
    UPDATE ducklake.catalog_migration
       SET state='failed',terminal_at=v_now,failure_evidence=p_failure_evidence,
           recovery_decision=p_recovery_decision,decision_evidence=p_decision_evidence
     WHERE migration_id=p_migration_id AND state='running';
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'migration fence stale'; END IF;
END;
$$;

-- Requalification must retain its composite foreign-key protection without
-- granting the migrator role REFERENCES/UPDATE on retention rows. This
-- narrowly scoped definer function performs validation and insertion as the
-- schema owner; runtime roles do not receive EXECUTE.
DROP FUNCTION IF EXISTS ducklake.record_snapshot_requalification(uuid,text,text,bigint,uuid,text,text,text,text,text,text,jsonb,timestamptz);
DROP FUNCTION IF EXISTS ducklake.lock_snapshot_retention(text,text,bigint);
CREATE OR REPLACE FUNCTION ducklake.record_snapshot_requalification(
    p_qualification_id uuid,
    p_physical_pool_id text,
    p_catalog_id text,
    p_snapshot_id bigint,
    p_migration_id uuid,
    p_duckdb_runtime text,
    p_ducklake_extension text,
    p_catalog_format text,
    p_compatibility_digest text,
    p_catalog_schema_version text,
    p_status text,
    p_evidence jsonb,
    p_qualified_at timestamptz,
    p_owner_id text,
    p_pool_fencing_epoch bigint,
    p_global_fencing_epoch bigint
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_migration record;
    v_state text;
    v_existing record;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='global' AND physical_pool_id=''
      AND owner_id=p_owner_id AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='pool' AND physical_pool_id=p_physical_pool_id
      AND owner_id=p_owner_id AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    SELECT q.* INTO v_existing FROM ducklake.snapshot_requalification q
     WHERE q.qualification_id=p_qualification_id FOR UPDATE;
    IF FOUND THEN
        IF v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
           OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id
           OR v_existing.snapshot_id IS DISTINCT FROM p_snapshot_id
           OR v_existing.migration_id IS DISTINCT FROM p_migration_id
           OR v_existing.duckdb_runtime IS DISTINCT FROM p_duckdb_runtime
           OR v_existing.ducklake_extension IS DISTINCT FROM p_ducklake_extension
           OR v_existing.catalog_format IS DISTINCT FROM p_catalog_format
           OR v_existing.compatibility_digest IS DISTINCT FROM p_compatibility_digest
           OR v_existing.catalog_schema_version IS DISTINCT FROM p_catalog_schema_version
           OR v_existing.status IS DISTINCT FROM p_status
           OR v_existing.evidence IS DISTINCT FROM p_evidence THEN
            RAISE EXCEPTION 'qualification conflict';
        END IF;
        RETURN;
    END IF;
    SELECT * INTO v_migration FROM ducklake.catalog_migration WHERE migration_id=p_migration_id FOR SHARE;
    IF NOT FOUND OR v_migration.physical_pool_id <> p_physical_pool_id OR v_migration.catalog_id <> p_catalog_id
       OR v_migration.owner_id <> p_owner_id OR v_migration.fencing_epoch <> p_pool_fencing_epoch
       OR v_migration.global_fencing_epoch <> p_global_fencing_epoch OR v_migration.state <> 'running'
       OR v_migration.target_duckdb_runtime <> p_duckdb_runtime OR v_migration.target_ducklake_extension <> p_ducklake_extension
       OR v_migration.target_catalog_format <> p_catalog_format OR v_migration.target_compatibility_digest <> p_compatibility_digest
       OR v_migration.target_catalog_schema_version <> p_catalog_schema_version THEN
        RAISE EXCEPTION 'qualification conflict';
    END IF;
    SELECT state INTO v_state FROM ducklake.snapshot_retention
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id
       AND state IN ('live','retiring') FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'snapshot not found'; END IF;
    INSERT INTO ducklake.snapshot_requalification
      (qualification_id,physical_pool_id,catalog_id,snapshot_id,migration_id,
       duckdb_runtime,ducklake_extension,catalog_format,compatibility_digest,
       catalog_schema_version,status,evidence,qualified_at)
    VALUES
      (p_qualification_id,p_physical_pool_id,p_catalog_id,p_snapshot_id,p_migration_id,
       p_duckdb_runtime,p_ducklake_extension,p_catalog_format,p_compatibility_digest,
       p_catalog_schema_version,p_status,p_evidence,v_now);
END;
$$;

-- DuckLake control state is capability-gated.  PUBLIC receives no schema,
-- relation, sequence, or trigger-function privileges; application roles are
-- granted only the exact lifecycle operations exposed by the repository.
REVOKE ALL ON SCHEMA ducklake FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA ducklake FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA ducklake FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA ducklake FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA ducklake TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT, UPDATE ON TABLE '
            || 'ducklake.catalog_identity, ducklake.attempt_evidence, '
            || 'ducklake.generation_binding, ducklake.snapshot_retention, '
            || 'ducklake.snapshot_root, ducklake.snapshot_lease, '
            || 'ducklake.snapshot_orphan TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.snapshot_reader_drain TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.catalog_runtime_compatibility, ducklake.migration_fence, ducklake.catalog_migration, ducklake.snapshot_requalification TO leapview_control_runtime';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA ducklake TO leapview_control_readonly';
        EXECUTE 'GRANT SELECT ON TABLE '
            || 'ducklake.catalog_identity, ducklake.attempt_evidence, '
            || 'ducklake.generation_binding, ducklake.snapshot_retention, '
            || 'ducklake.snapshot_root, ducklake.snapshot_lease, '
            || 'ducklake.snapshot_orphan, ducklake.snapshot_reader_drain, '
            || 'ducklake.catalog_runtime_compatibility, ducklake.migration_fence, '
            || 'ducklake.catalog_migration, ducklake.snapshot_requalification '
            || 'TO leapview_control_readonly';
    END IF;
    -- The upgrade coordinator is a control-database capability.  It is
    -- deliberately distinct from the DuckLake catalog migrator role (which
    -- is owner-capable in the separate leapview_ducklake database and has no
    -- control-database CONNECT privilege).
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_upgrade_coordinator') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA ducklake TO leapview_control_upgrade_coordinator';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES ON TABLE ducklake.catalog_runtime_compatibility, ducklake.catalog_migration, ducklake.snapshot_requalification, ducklake.migration_fence FROM leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.catalog_identity, ducklake.snapshot_retention, ducklake.catalog_runtime_compatibility, ducklake.migration_fence, ducklake.catalog_migration, ducklake.snapshot_requalification TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.acquire_migration_fence(text,text,text,timestamptz) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.release_migration_fence(text,text,text,bigint) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.renew_migration_fence(text,text,text,bigint,timestamptz) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.register_catalog_runtime_compatibility(text,text,text,text,text,text,text,text,bigint,bigint) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.begin_catalog_migration(uuid,text,text,text,bigint,bigint,text,text,text,text,text,text,text,text,text,text,jsonb) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.complete_catalog_migration(uuid,text,bigint,bigint,jsonb) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.fail_catalog_migration(uuid,text,bigint,bigint,jsonb,text,jsonb) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.record_snapshot_requalification(uuid,text,text,bigint,uuid,text,text,text,text,text,text,jsonb,timestamptz,text,bigint,bigint) TO leapview_control_upgrade_coordinator';
    END IF;
END
$$;
