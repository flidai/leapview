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

-- Source observations are captured by the exact native DuckLake writer while
-- its prepared source session is still live.  The attempt key makes the
-- capture replay-safe; marker and envelope bytes are canonical identities,
-- not mutable diagnostic payloads.
CREATE TABLE IF NOT EXISTS ducklake.source_observation_capture (
    attempt_id            uuid PRIMARY KEY REFERENCES ducklake.attempt_evidence(attempt_id) ON DELETE RESTRICT,
    commit_marker         jsonb NOT NULL,
    observation_envelope  jsonb NOT NULL,
    content_digest        text NOT NULL,
    captured_at           timestamptz NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (jsonb_typeof(commit_marker) = 'object' AND octet_length(commit_marker::text) <= 4096),
    CHECK (jsonb_typeof(observation_envelope) = 'object' AND octet_length(observation_envelope::text) <= 8388608),
    CHECK (content_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE OR REPLACE FUNCTION ducklake.guard_source_observation_capture_immutable() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'source observation captures are immutable';
    END IF;
    IF NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
       OR NEW.commit_marker IS DISTINCT FROM OLD.commit_marker
       OR NEW.observation_envelope IS DISTINCT FROM OLD.observation_envelope
       OR NEW.content_digest IS DISTINCT FROM OLD.content_digest
       OR NEW.captured_at IS DISTINCT FROM OLD.captured_at
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'source observation capture identity is immutable';
    END IF;
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS source_observation_capture_immutable ON ducklake.source_observation_capture;
CREATE TRIGGER source_observation_capture_immutable
BEFORE UPDATE OR DELETE ON ducklake.source_observation_capture
FOR EACH ROW EXECUTE FUNCTION ducklake.guard_source_observation_capture_immutable();

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
-- Retiring/expiring prevents new leases while existing leases drain; expiring
-- additionally records the exact maintenance claim being reconciled.
CREATE TABLE IF NOT EXISTS ducklake.snapshot_retention (
    physical_pool_id text NOT NULL,
    catalog_id       text NOT NULL,
    snapshot_id      bigint NOT NULL CHECK (snapshot_id > 0),
    state            text NOT NULL CHECK (state IN ('live', 'retiring', 'expiring', 'expired', 'quarantined', 'cleanup-complete')),
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
    retention_claim_id uuid,
    retention_claim_owner_id text,
    retention_claim_fencing_epoch bigint NOT NULL DEFAULT 0 CHECK (retention_claim_fencing_epoch >= 0),
    retention_claimed_at timestamptz,
    created_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (physical_pool_id, catalog_id, snapshot_id),
    FOREIGN KEY (physical_pool_id, catalog_id) REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK ((state = 'live' AND retired_at IS NULL AND expired_at IS NULL) OR state <> 'live'),
    CHECK ((state = 'retiring' AND retired_at IS NOT NULL AND expired_at IS NULL) OR state <> 'retiring'),
    CHECK ((state = 'expiring' AND retired_at IS NOT NULL AND expired_at IS NULL) OR state <> 'expiring'),
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
    CHECK (cleanup_completed_at IS NULL OR cleanup_completed_at >= COALESCE(quarantined_at, created_at)),
    CHECK ((retention_claim_fencing_epoch = 0 AND retention_claim_id IS NULL AND retention_claim_owner_id IS NULL AND retention_claimed_at IS NULL)
        OR (retention_claim_fencing_epoch > 0 AND retention_claim_id IS NOT NULL AND retention_claim_owner_id IS NOT NULL AND retention_claim_owner_id = btrim(retention_claim_owner_id) AND octet_length(retention_claim_owner_id) BETWEEN 1 AND 255 AND retention_claimed_at IS NOT NULL))
);

-- Add claim columns to installations created before the resumable retention
-- coordinator existed. The checks above cover fresh databases; these guarded
-- constraints preserve the same invariant during in-place upgrades.
ALTER TABLE ducklake.snapshot_retention
    ADD COLUMN IF NOT EXISTS retention_claim_id uuid,
    ADD COLUMN IF NOT EXISTS retention_claim_owner_id text,
    ADD COLUMN IF NOT EXISTS retention_claim_fencing_epoch bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS retention_claimed_at timestamptz;
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='ducklake.snapshot_retention'::regclass AND conname='snapshot_retention_claim_epoch_nonnegative') THEN
        ALTER TABLE ducklake.snapshot_retention ADD CONSTRAINT snapshot_retention_claim_epoch_nonnegative CHECK (retention_claim_fencing_epoch >= 0);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='ducklake.snapshot_retention'::regclass AND conname='snapshot_retention_claim_shape') THEN
        ALTER TABLE ducklake.snapshot_retention ADD CONSTRAINT snapshot_retention_claim_shape CHECK ((retention_claim_fencing_epoch = 0 AND retention_claim_id IS NULL AND retention_claim_owner_id IS NULL AND retention_claimed_at IS NULL)
            OR (retention_claim_fencing_epoch > 0 AND retention_claim_id IS NOT NULL AND retention_claim_owner_id IS NOT NULL AND retention_claim_owner_id = btrim(retention_claim_owner_id) AND octet_length(retention_claim_owner_id) BETWEEN 1 AND 255 AND retention_claimed_at IS NOT NULL));
    END IF;
END $$;

-- Older installations predate the resumable expiry claim state and retain the
-- original inline state check (which does not admit `expiring`).  Replace that
-- generated constraint in place so claim_retention_snapshots can advance rows
-- without requiring a destructive table rebuild.  Fresh databases already
-- carry the current definition and therefore take the no-op path.
DO $$
DECLARE
    v_definition text;
BEGIN
    SELECT pg_get_constraintdef(oid)
      INTO v_definition
      FROM pg_constraint
     WHERE conrelid='ducklake.snapshot_retention'::regclass
       AND conname='snapshot_retention_state_check';
    IF v_definition IS NOT NULL AND position('expiring' IN v_definition) = 0 THEN
        ALTER TABLE ducklake.snapshot_retention DROP CONSTRAINT snapshot_retention_state_check;
        v_definition := NULL;
    END IF;
    IF v_definition IS NULL THEN
        ALTER TABLE ducklake.snapshot_retention
            ADD CONSTRAINT snapshot_retention_state_check
            CHECK (state IN ('live', 'retiring', 'expiring', 'expired', 'quarantined', 'cleanup-complete'));
    END IF;
END $$;

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

-- Catalog-wide retention has its own authority.  It deliberately does not
-- reuse migration_fence: snapshot expiry and physical-file cleanup are a
-- resumable maintenance operation, not a catalog schema migration.  The
-- composite key prevents two workers from expiring the same pool/catalog
-- concurrently while the monotonically increasing epoch fences stale
-- workers after lease takeover.
CREATE TABLE IF NOT EXISTS ducklake.pool_maintenance_fence (
    physical_pool_id       text NOT NULL,
    catalog_id             text NOT NULL,
    owner_id               text,
    fencing_epoch          bigint NOT NULL DEFAULT 0 CHECK (fencing_epoch >= 0),
    lease_expires_at       timestamptz,
    updated_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (physical_pool_id, catalog_id),
    FOREIGN KEY (physical_pool_id, catalog_id)
        REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK ((owner_id IS NULL AND lease_expires_at IS NULL) OR
           (owner_id IS NOT NULL AND owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255 AND lease_expires_at IS NOT NULL))
);

-- A maintenance run and its exact snapshot set survive worker crashes.  The
-- operation row records catalog-wide phase progress; child rows carry the
-- per-snapshot evidence needed for exact replay and audit.
CREATE TABLE IF NOT EXISTS ducklake.retention_maintenance (
    maintenance_id    uuid PRIMARY KEY,
    physical_pool_id  text NOT NULL,
    catalog_id        text NOT NULL,
    owner_id           text NOT NULL,
    fencing_epoch      bigint NOT NULL CHECK (fencing_epoch > 0),
    state              text NOT NULL CHECK (state IN ('running','completed','failed')),
    phase              text NOT NULL CHECK (phase IN ('expiry','old-files','orphans','completed')),
    dry_run            boolean NOT NULL,
    file_grace_micros  bigint NOT NULL CHECK (file_grace_micros > 0),
    snapshot_set_digest text NOT NULL DEFAULT '' CHECK (snapshot_set_digest = '' OR snapshot_set_digest ~ '^sha256:[0-9a-f]{64}$'),
    phase_evidence     jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(phase_evidence) = 'object' AND octet_length(phase_evidence::text) <= 32768),
    started_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at       timestamptz,
    FOREIGN KEY (physical_pool_id, catalog_id)
        REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    UNIQUE (maintenance_id, physical_pool_id, catalog_id),
    CHECK (completed_at IS NULL OR state IN ('completed','failed'))
);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='ducklake.snapshot_retention'::regclass AND conname='snapshot_retention_maintenance_claim_fk') THEN
        ALTER TABLE ducklake.snapshot_retention
            ADD CONSTRAINT snapshot_retention_maintenance_claim_fk
            FOREIGN KEY (retention_claim_id, physical_pool_id, catalog_id)
            REFERENCES ducklake.retention_maintenance(maintenance_id, physical_pool_id, catalog_id);
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS ducklake.retention_maintenance_snapshot (
    maintenance_id      uuid NOT NULL REFERENCES ducklake.retention_maintenance(maintenance_id) ON DELETE RESTRICT,
    physical_pool_id    text NOT NULL,
    catalog_id          text NOT NULL,
    snapshot_id         bigint NOT NULL CHECK (snapshot_id > 0),
    phase                text NOT NULL CHECK (phase IN ('eligible','expired','quarantined','cleanup-complete')),
    expiry_evidence     jsonb,
    quarantine_evidence jsonb,
    cleanup_evidence    jsonb,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (maintenance_id, physical_pool_id, catalog_id, snapshot_id),
    FOREIGN KEY (physical_pool_id, catalog_id, snapshot_id)
        REFERENCES ducklake.snapshot_retention(physical_pool_id, catalog_id, snapshot_id),
    FOREIGN KEY (maintenance_id, physical_pool_id, catalog_id)
        REFERENCES ducklake.retention_maintenance(maintenance_id, physical_pool_id, catalog_id),
    CHECK (expiry_evidence IS NULL OR (jsonb_typeof(expiry_evidence) = 'object' AND octet_length(expiry_evidence::text) <= 32768)),
    CHECK (quarantine_evidence IS NULL OR (jsonb_typeof(quarantine_evidence) = 'object' AND octet_length(quarantine_evidence::text) <= 32768)),
    CHECK (cleanup_evidence IS NULL OR (jsonb_typeof(cleanup_evidence) = 'object' AND octet_length(cleanup_evidence::text) <= 32768)),
    CHECK ((phase = 'eligible' AND expiry_evidence IS NULL AND quarantine_evidence IS NULL AND cleanup_evidence IS NULL)
        OR phase <> 'eligible'),
    CHECK ((phase IN ('expired','quarantined','cleanup-complete') AND expiry_evidence IS NOT NULL) OR phase IN ('eligible')),
    CHECK ((phase IN ('quarantined','cleanup-complete') AND quarantine_evidence IS NOT NULL) OR phase IN ('eligible','expired')),
    CHECK ((phase = 'cleanup-complete' AND cleanup_evidence IS NOT NULL) OR phase <> 'cleanup-complete')
);

CREATE INDEX IF NOT EXISTS ducklake_retention_maintenance_snapshot_idx
    ON ducklake.retention_maintenance_snapshot (physical_pool_id, catalog_id, phase, snapshot_id);

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
    IF OLD.state = 'expiring' AND NEW.state NOT IN ('expiring', 'expired') THEN
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
    IF NEW.state = 'expiring'
       AND (NEW.retention_claim_id IS NULL OR NEW.retention_claim_owner_id IS NULL
            OR NEW.retention_claim_fencing_epoch <= 0 OR NEW.retention_claimed_at IS NULL) THEN
        RAISE EXCEPTION 'DuckLake expiring snapshot requires a maintenance claim';
    END IF;
    IF NEW.protected_until IS NOT NULL AND OLD.protected_until IS NOT NULL
       AND NEW.protected_until < OLD.protected_until THEN
        RAISE EXCEPTION 'DuckLake snapshot protection cannot move backwards';
    END IF;
    IF OLD.state IN ('retiring', 'expiring', 'expired', 'quarantined', 'cleanup-complete') AND NEW.state = 'live' THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle is monotonic';
    END IF;
    IF OLD.state = 'retiring' AND NEW.state NOT IN ('retiring', 'expiring') THEN
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
       AND NOT (OLD.state IN ('retiring', 'expiring') AND NEW.state = 'expired') THEN
        RAISE EXCEPTION 'DuckLake snapshot retention evidence is immutable';
    END IF;
    IF NEW.state = OLD.state AND (
           NEW.retired_at IS DISTINCT FROM OLD.retired_at
        OR NEW.expired_at IS DISTINCT FROM OLD.expired_at) THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle timestamps are immutable';
    END IF;
    IF OLD.state = 'live' AND NEW.state IN ('expiring', 'expired', 'quarantined', 'cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake snapshot must retire before expiration';
    END IF;
    IF OLD.state IN ('retiring', 'expiring') AND NEW.state = 'expired'
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

CREATE OR REPLACE FUNCTION ducklake.reject_pool_maintenance_fence_change()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id THEN
        RAISE EXCEPTION 'DuckLake pool maintenance fence identity is immutable';
    END IF;
    IF NEW.fencing_epoch < OLD.fencing_epoch THEN
        RAISE EXCEPTION 'DuckLake pool maintenance fencing epoch cannot move backwards';
    END IF;
    IF NEW.fencing_epoch = OLD.fencing_epoch
       AND NEW.owner_id IS DISTINCT FROM OLD.owner_id
       AND NOT (NEW.owner_id IS NULL AND NEW.lease_expires_at IS NULL) THEN
        RAISE EXCEPTION 'DuckLake pool maintenance fence owner change requires a new epoch';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.reject_retention_maintenance_change()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.maintenance_id <> OLD.maintenance_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.started_at <> OLD.started_at THEN
        RAISE EXCEPTION 'DuckLake retention maintenance identity is immutable';
    END IF;
    IF OLD.state IN ('completed','failed') AND
       (NEW.state <> OLD.state OR NEW.phase <> OLD.phase
        OR NEW.owner_id <> OLD.owner_id OR NEW.fencing_epoch <> OLD.fencing_epoch
        OR NEW.dry_run <> OLD.dry_run OR NEW.file_grace_micros <> OLD.file_grace_micros
        OR NEW.snapshot_set_digest <> OLD.snapshot_set_digest
        OR NEW.phase_evidence IS DISTINCT FROM OLD.phase_evidence
        OR NEW.completed_at IS DISTINCT FROM OLD.completed_at) THEN
        RAISE EXCEPTION 'DuckLake terminal retention maintenance is immutable';
    END IF;
    IF OLD.state = 'running'
       AND (NEW.dry_run <> OLD.dry_run OR NEW.file_grace_micros <> OLD.file_grace_micros
            OR (OLD.snapshot_set_digest <> '' AND NEW.snapshot_set_digest <> OLD.snapshot_set_digest)) THEN
        RAISE EXCEPTION 'DuckLake retention maintenance request identity is immutable';
    END IF;
    IF NEW.fencing_epoch < OLD.fencing_epoch THEN
        RAISE EXCEPTION 'DuckLake retention maintenance fencing epoch cannot move backwards';
    END IF;
    IF NEW.state = 'completed' AND (NEW.phase <> 'completed' OR NEW.completed_at IS NULL) THEN
        RAISE EXCEPTION 'DuckLake completed retention maintenance requires terminal phase';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.reject_retention_maintenance_snapshot_change()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.maintenance_id <> OLD.maintenance_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.snapshot_id <> OLD.snapshot_id
       OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'DuckLake retention maintenance snapshot identity is immutable';
    END IF;
    IF OLD.phase = 'cleanup-complete' AND (NEW.phase <> OLD.phase OR NEW.expiry_evidence IS DISTINCT FROM OLD.expiry_evidence OR NEW.quarantine_evidence IS DISTINCT FROM OLD.quarantine_evidence OR NEW.cleanup_evidence IS DISTINCT FROM OLD.cleanup_evidence) THEN
        RAISE EXCEPTION 'DuckLake completed retention maintenance snapshot is immutable';
    END IF;
    IF OLD.phase = 'quarantined' AND NEW.phase NOT IN ('quarantined','cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake retention maintenance snapshot lifecycle is monotonic';
    END IF;
    IF OLD.phase = 'expired' AND NEW.phase NOT IN ('expired','quarantined','cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake retention maintenance snapshot lifecycle is monotonic';
    END IF;
    IF OLD.phase = 'eligible' AND NEW.phase NOT IN ('eligible','expired','quarantined') THEN
        RAISE EXCEPTION 'DuckLake retention maintenance snapshot lifecycle is monotonic';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS pool_maintenance_fence_monotonic ON ducklake.pool_maintenance_fence;
CREATE TRIGGER pool_maintenance_fence_monotonic
    BEFORE UPDATE OR DELETE ON ducklake.pool_maintenance_fence
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_pool_maintenance_fence_change();
DROP TRIGGER IF EXISTS retention_maintenance_immutable ON ducklake.retention_maintenance;
CREATE TRIGGER retention_maintenance_immutable
    BEFORE UPDATE OR DELETE ON ducklake.retention_maintenance
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_retention_maintenance_change();
DROP TRIGGER IF EXISTS retention_maintenance_snapshot_immutable ON ducklake.retention_maintenance_snapshot;
CREATE TRIGGER retention_maintenance_snapshot_immutable
    BEFORE UPDATE OR DELETE ON ducklake.retention_maintenance_snapshot
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_retention_maintenance_snapshot_change();

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
    -- Retention acquisition locks global migration → pool migration →
    -- maintenance.  Use that same order to close the cross-authority race.
    IF p_scope = 'global' THEN
        PERFORM 1 FROM ducklake.pool_maintenance_fence f
         WHERE f.owner_id IS NOT NULL AND f.lease_expires_at > v_now
         FOR UPDATE;
    ELSE
        PERFORM 1 FROM ducklake.pool_maintenance_fence f
         WHERE f.physical_pool_id=p_physical_pool_id
           AND f.owner_id IS NOT NULL AND f.lease_expires_at > v_now
         FOR UPDATE;
    END IF;
    IF FOUND THEN
        RAISE EXCEPTION 'migration fence busy';
    END IF;
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

-- Retention authority is catalog-wide for one physical pool.  Claims are
-- serialized by the row lock and fenced by a monotonically increasing epoch.
-- The function intentionally has no age-based defaults: callers enumerate
-- exact control-plane eligible snapshots before invoking DuckLake expiry.
CREATE OR REPLACE FUNCTION ducklake.acquire_pool_maintenance_fence(
    p_physical_pool_id text,
    p_catalog_id text,
    p_owner_id text,
    p_lease_expires_at timestamptz
) RETURNS TABLE(physical_pool_id text, catalog_id text, owner_id text, fencing_epoch bigint, lease_expires_at timestamptz)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_lease timestamptz := COALESCE(p_lease_expires_at, v_now + interval '24 hours');
    v_owner text;
    v_epoch bigint;
    v_expiry timestamptz;
BEGIN
    IF p_physical_pool_id = '' OR p_physical_pool_id <> btrim(p_physical_pool_id) OR octet_length(p_physical_pool_id) > 255
       OR p_catalog_id = '' OR p_catalog_id <> btrim(p_catalog_id) OR octet_length(p_catalog_id) > 255
       OR p_owner_id = '' OR p_owner_id <> btrim(p_owner_id) OR octet_length(p_owner_id) > 255
       OR v_lease <= v_now OR v_lease > v_now + interval '24 hours' THEN
        RAISE EXCEPTION 'invalid pool maintenance fence claim';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM ducklake.catalog_identity ci WHERE ci.physical_pool_id=p_physical_pool_id AND ci.catalog_id=p_catalog_id) THEN
        RAISE EXCEPTION 'catalog identity not found';
    END IF;
    -- Lock the migration rows first.  Migration acquisition uses the same
    -- global→pool→maintenance order, preventing a check-then-act race.
    INSERT INTO ducklake.migration_fence(scope,physical_pool_id)
    VALUES ('global','') ON CONFLICT DO NOTHING;
    PERFORM 1 FROM ducklake.migration_fence mf
     WHERE mf.scope='global' AND mf.physical_pool_id='' FOR UPDATE;
    INSERT INTO ducklake.migration_fence(scope,physical_pool_id)
    VALUES ('pool',p_physical_pool_id) ON CONFLICT DO NOTHING;
    PERFORM 1 FROM ducklake.migration_fence mf
     WHERE mf.scope='pool' AND mf.physical_pool_id=p_physical_pool_id FOR UPDATE;
    INSERT INTO ducklake.pool_maintenance_fence(physical_pool_id,catalog_id)
    VALUES (p_physical_pool_id,p_catalog_id) ON CONFLICT DO NOTHING;
    SELECT f.owner_id,f.fencing_epoch,f.lease_expires_at
      INTO v_owner,v_epoch,v_expiry
      FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
     FOR UPDATE;
    IF v_owner IS NOT NULL AND v_expiry > v_now THEN
        IF v_owner = p_owner_id THEN
            RETURN QUERY SELECT p_physical_pool_id,p_catalog_id,v_owner,v_epoch,v_expiry;
            RETURN;
        END IF;
        RAISE EXCEPTION 'pool maintenance fence busy';
    END IF;
    -- Maintenance and catalog migration are separate authorities, but may not
    -- mutate the same pool concurrently.
    IF EXISTS (SELECT 1 FROM ducklake.migration_fence f
              WHERE ((f.scope='global' AND f.physical_pool_id='') OR
                     (f.scope='pool' AND f.physical_pool_id=p_physical_pool_id))
                AND f.owner_id IS NOT NULL AND f.lease_expires_at > v_now) THEN
        RAISE EXCEPTION 'pool maintenance fence busy';
    END IF;
    UPDATE ducklake.pool_maintenance_fence f
       SET owner_id=p_owner_id, fencing_epoch=f.fencing_epoch+1,
           lease_expires_at=v_lease, updated_at=v_now
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
     RETURNING f.owner_id,f.fencing_epoch,f.lease_expires_at
      INTO v_owner,v_epoch,v_expiry;
    RETURN QUERY SELECT p_physical_pool_id,p_catalog_id,v_owner,v_epoch,v_expiry;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.release_pool_maintenance_fence(
    p_physical_pool_id text, p_catalog_id text, p_owner_id text, p_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_owner text; v_epoch bigint;
BEGIN
    UPDATE ducklake.pool_maintenance_fence
       SET owner_id=NULL, lease_expires_at=NULL, updated_at=clock_timestamp()
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id
       AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch;
    IF FOUND THEN RETURN; END IF;
    SELECT owner_id,fencing_epoch INTO v_owner,v_epoch
      FROM ducklake.pool_maintenance_fence
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id;
    IF NOT FOUND THEN RAISE EXCEPTION 'pool maintenance fence not found'; END IF;
    IF v_owner IS NULL AND v_epoch=p_fencing_epoch THEN RETURN; END IF;
    RAISE EXCEPTION 'pool maintenance fence stale';
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.renew_pool_maintenance_fence(
    p_physical_pool_id text, p_catalog_id text, p_owner_id text,
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
        RAISE EXCEPTION 'invalid pool maintenance fence renewal';
    END IF;
    UPDATE ducklake.pool_maintenance_fence
       SET lease_expires_at=v_lease, updated_at=v_now
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id
       AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch
       AND lease_expires_at > v_now;
    IF FOUND THEN RETURN; END IF;
    SELECT owner_id,fencing_epoch,lease_expires_at INTO v_owner,v_epoch,v_expiry
      FROM ducklake.pool_maintenance_fence
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id;
    IF NOT FOUND OR v_owner IS DISTINCT FROM p_owner_id OR v_epoch <> p_fencing_epoch THEN
        RAISE EXCEPTION 'pool maintenance fence stale';
    END IF;
    IF v_expiry IS NULL OR v_expiry <= v_now THEN
        RAISE EXCEPTION 'pool maintenance fence expired';
    END IF;
    RAISE EXCEPTION 'pool maintenance fence stale';
END;
$$;

-- Retention lifecycle writes are capability-gated.  The maintenance role has
-- no INSERT/UPDATE privilege on these tables; each function validates the
-- exact pool fence and operation identity before changing state.  Snapshot
-- retention and its durable per-operation evidence are advanced together in
-- the same function transaction, so a crash cannot expose one without the
-- other.
CREATE OR REPLACE FUNCTION ducklake.begin_retention_maintenance(
    p_maintenance_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_fencing_epoch bigint, p_dry_run boolean,
    p_file_grace_micros bigint, p_snapshot_set_digest text,
    p_phase_evidence jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_existing record;
BEGIN
    IF p_physical_pool_id = '' OR p_physical_pool_id <> btrim(p_physical_pool_id) OR octet_length(p_physical_pool_id) > 255
       OR p_catalog_id = '' OR p_catalog_id <> btrim(p_catalog_id) OR octet_length(p_catalog_id) > 255
       OR p_owner_id = '' OR p_owner_id <> btrim(p_owner_id) OR octet_length(p_owner_id) > 255
       OR p_fencing_epoch <= 0 OR p_file_grace_micros <= 0
       OR p_snapshot_set_digest <> '' AND p_snapshot_set_digest !~ '^sha256:[0-9a-f]{64}$'
       OR jsonb_typeof(COALESCE(p_phase_evidence, '{}'::jsonb)) <> 'object' THEN
        RAISE EXCEPTION 'invalid retention maintenance request';
    END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_existing FROM ducklake.retention_maintenance
     WHERE maintenance_id=p_maintenance_id FOR UPDATE;
    IF FOUND THEN
        IF v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
           OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id
           OR v_existing.dry_run IS DISTINCT FROM p_dry_run
           OR v_existing.file_grace_micros IS DISTINCT FROM p_file_grace_micros
           OR (v_existing.snapshot_set_digest <> '' AND p_snapshot_set_digest <> '' AND v_existing.snapshot_set_digest IS DISTINCT FROM p_snapshot_set_digest)
           OR v_existing.state NOT IN ('running','completed') THEN
            RAISE EXCEPTION 'retention maintenance conflict';
        END IF;
        IF v_existing.state='completed' THEN RETURN; END IF;
        UPDATE ducklake.retention_maintenance
           SET owner_id=p_owner_id, fencing_epoch=p_fencing_epoch, updated_at=v_now
         WHERE maintenance_id=p_maintenance_id;
        RETURN;
    END IF;
    INSERT INTO ducklake.retention_maintenance
      (maintenance_id,physical_pool_id,catalog_id,owner_id,fencing_epoch,state,phase,
       dry_run,file_grace_micros,snapshot_set_digest,phase_evidence,started_at,updated_at)
    VALUES
      (p_maintenance_id,p_physical_pool_id,p_catalog_id,p_owner_id,p_fencing_epoch,
       'running','expiry',p_dry_run,p_file_grace_micros,COALESCE(p_snapshot_set_digest,''),
       COALESCE(p_phase_evidence,'{}'::jsonb),v_now,v_now);
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.update_retention_maintenance(
    p_maintenance_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_fencing_epoch bigint, p_state text, p_phase text,
    p_dry_run boolean, p_file_grace_micros bigint, p_snapshot_set_digest text,
    p_phase_evidence jsonb, p_completed_at timestamptz
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_existing record; v_missing bigint;
BEGIN
    IF p_state NOT IN ('running','completed','failed') OR p_phase NOT IN ('expiry','old-files','orphans','completed')
       OR p_owner_id = '' OR p_owner_id <> btrim(p_owner_id) OR octet_length(p_owner_id) > 255
       OR p_fencing_epoch <= 0 OR p_file_grace_micros <= 0
       OR p_snapshot_set_digest <> '' AND p_snapshot_set_digest !~ '^sha256:[0-9a-f]{64}$'
       OR jsonb_typeof(COALESCE(p_phase_evidence, '{}'::jsonb)) <> 'object' THEN
        RAISE EXCEPTION 'invalid retention maintenance update';
    END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_existing FROM ducklake.retention_maintenance
     WHERE maintenance_id=p_maintenance_id FOR UPDATE;
    IF NOT FOUND OR v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_existing.owner_id IS DISTINCT FROM p_owner_id
       OR v_existing.fencing_epoch IS DISTINCT FROM p_fencing_epoch THEN
        RAISE EXCEPTION 'maintenance fence stale';
    END IF;
    IF v_existing.state <> 'running' THEN
        IF v_existing.state=p_state AND v_existing.phase=p_phase
           AND v_existing.phase_evidence IS NOT DISTINCT FROM COALESCE(p_phase_evidence,'{}'::jsonb) THEN RETURN; END IF;
        RAISE EXCEPTION 'terminal retention maintenance is immutable';
    END IF;
    IF p_state='completed' AND p_snapshot_set_digest = '' THEN
        RAISE EXCEPTION 'retention maintenance snapshot set is not frozen';
    END IF;
    IF p_state='completed' AND NOT p_dry_run THEN
        SELECT count(*) INTO v_missing
          FROM ducklake.retention_maintenance_snapshot s
          JOIN ducklake.snapshot_retention r
            ON r.physical_pool_id=s.physical_pool_id AND r.catalog_id=s.catalog_id AND r.snapshot_id=s.snapshot_id
         WHERE s.maintenance_id=p_maintenance_id
           AND (s.phase <> 'cleanup-complete' OR r.state <> 'cleanup-complete' OR r.retention_claim_id IS DISTINCT FROM p_maintenance_id);
        IF v_missing <> 0 THEN RAISE EXCEPTION 'retention cleanup evidence incomplete'; END IF;
    END IF;
    UPDATE ducklake.retention_maintenance
       SET state=p_state,phase=p_phase,dry_run=p_dry_run,
           file_grace_micros=p_file_grace_micros,
           snapshot_set_digest=p_snapshot_set_digest,
           phase_evidence=COALESCE(p_phase_evidence,'{}'::jsonb),updated_at=v_now,
           completed_at=CASE WHEN p_state='completed' THEN v_now ELSE NULL END
     WHERE maintenance_id=p_maintenance_id AND state='running';
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.insert_retention_maintenance_snapshot(
    p_maintenance_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_snapshot_id bigint, p_owner_id text, p_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_retention record;
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_operation FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id FOR SHARE;
    IF NOT FOUND OR v_operation.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_operation.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_operation.owner_id IS DISTINCT FROM p_owner_id
       OR v_operation.fencing_epoch IS DISTINCT FROM p_fencing_epoch
       OR v_operation.state <> 'running' OR v_operation.snapshot_set_digest <> '' THEN
        RAISE EXCEPTION 'maintenance fence stale';
    END IF;
    SELECT retention_claim_id,state INTO v_retention FROM ducklake.snapshot_retention
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    IF NOT FOUND OR (v_operation.dry_run AND (v_retention.state NOT IN ('retiring','expired') OR v_retention.retention_claim_id IS NOT NULL))
       OR (NOT v_operation.dry_run AND (v_retention.retention_claim_id IS DISTINCT FROM p_maintenance_id OR v_retention.state NOT IN ('expiring','expired'))) THEN
        RAISE EXCEPTION 'retention snapshot claim mismatch';
    END IF;
    INSERT INTO ducklake.retention_maintenance_snapshot
      (maintenance_id,physical_pool_id,catalog_id,snapshot_id,phase)
    VALUES (p_maintenance_id,p_physical_pool_id,p_catalog_id,p_snapshot_id,'eligible')
    ON CONFLICT (maintenance_id,physical_pool_id,catalog_id,snapshot_id) DO NOTHING;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.update_retention_maintenance_snapshot(
    p_maintenance_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_snapshot_id bigint, p_owner_id text, p_fencing_epoch bigint,
    p_phase text, p_expiry_evidence jsonb, p_quarantine_evidence jsonb,
    p_cleanup_evidence jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_old record; v_retention record;
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    PERFORM 1 FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id AND m.physical_pool_id=p_physical_pool_id
       AND m.catalog_id=p_catalog_id AND m.owner_id=p_owner_id AND m.fencing_epoch=p_fencing_epoch;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    SELECT * INTO v_old FROM ducklake.retention_maintenance_snapshot s
     WHERE s.maintenance_id=p_maintenance_id AND s.physical_pool_id=p_physical_pool_id
       AND s.catalog_id=p_catalog_id AND s.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'retention maintenance snapshot not found'; END IF;
    SELECT state,evidence,quarantine_evidence,cleanup_evidence INTO v_retention
      FROM ducklake.snapshot_retention
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    IF NOT FOUND THEN RAISE EXCEPTION 'snapshot retention not found'; END IF;
    IF p_phase NOT IN ('eligible','expired','quarantined','cleanup-complete') THEN
        RAISE EXCEPTION 'invalid retention maintenance snapshot phase';
    END IF;
    IF v_old.phase='cleanup-complete' THEN
        IF v_old.phase IS DISTINCT FROM p_phase OR v_old.expiry_evidence IS DISTINCT FROM p_expiry_evidence
           OR v_old.quarantine_evidence IS DISTINCT FROM p_quarantine_evidence OR v_old.cleanup_evidence IS DISTINCT FROM p_cleanup_evidence THEN
            RAISE EXCEPTION 'completed retention maintenance snapshot is immutable';
        END IF;
        RETURN;
    END IF;
    IF (v_old.phase='quarantined' AND p_phase NOT IN ('quarantined','cleanup-complete'))
       OR (v_old.phase='expired' AND p_phase NOT IN ('expired','quarantined','cleanup-complete'))
       OR (v_old.phase='eligible' AND p_phase NOT IN ('eligible','expired','quarantined','cleanup-complete')) THEN
        RAISE EXCEPTION 'retention maintenance snapshot lifecycle is monotonic';
    END IF;
    IF p_phase='expired' AND (v_retention.state NOT IN ('expired','quarantined','cleanup-complete') OR v_retention.evidence IS DISTINCT FROM p_expiry_evidence) THEN
        RAISE EXCEPTION 'snapshot expiry evidence is not durable';
    ELSIF p_phase='quarantined' AND (v_retention.state NOT IN ('quarantined','cleanup-complete') OR v_retention.quarantine_evidence IS DISTINCT FROM p_quarantine_evidence) THEN
        RAISE EXCEPTION 'snapshot quarantine evidence is not durable';
    ELSIF p_phase='cleanup-complete' AND (v_retention.state <> 'cleanup-complete' OR v_retention.cleanup_evidence IS DISTINCT FROM p_cleanup_evidence) THEN
        RAISE EXCEPTION 'snapshot cleanup evidence is not durable';
    END IF;
    UPDATE ducklake.retention_maintenance_snapshot
       SET phase=p_phase,expiry_evidence=p_expiry_evidence,
           quarantine_evidence=p_quarantine_evidence,cleanup_evidence=p_cleanup_evidence,
           updated_at=v_now
     WHERE maintenance_id=p_maintenance_id AND physical_pool_id=p_physical_pool_id
       AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.claim_retention_snapshots(
    p_maintenance_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_fencing_epoch bigint
) RETURNS bigint[]
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_ids bigint[];
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_operation FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id FOR SHARE;
    IF NOT FOUND OR v_operation.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_operation.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_operation.owner_id IS DISTINCT FROM p_owner_id
       OR v_operation.fencing_epoch IS DISTINCT FROM p_fencing_epoch
       OR v_operation.state <> 'running' OR v_operation.snapshot_set_digest <> '' THEN
        RAISE EXCEPTION 'maintenance fence stale';
    END IF;
    WITH changed AS (
        UPDATE ducklake.snapshot_retention AS r
       SET state=CASE WHEN r.state='retiring' THEN 'expiring' ELSE r.state END,
           retention_claim_id=p_maintenance_id,retention_claim_owner_id=p_owner_id,
           retention_claim_fencing_epoch=p_fencing_epoch,retention_claimed_at=v_now
     WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id
       AND r.state IN ('retiring','expired') AND r.retention_claim_id IS NULL
       AND NOT EXISTS (SELECT 1 FROM ducklake.snapshot_root root WHERE root.physical_pool_id=r.physical_pool_id AND root.catalog_id=r.catalog_id AND root.snapshot_id=r.snapshot_id AND root.state IN ('live','retiring'))
       AND NOT EXISTS (SELECT 1 FROM ducklake.snapshot_lease lease WHERE lease.physical_pool_id=r.physical_pool_id AND lease.catalog_id=r.catalog_id AND lease.snapshot_id=r.snapshot_id AND lease.state='active')
        RETURNING r.snapshot_id
    )
    SELECT COALESCE(array_agg(snapshot_id ORDER BY snapshot_id),'{}'::bigint[]) INTO v_ids FROM changed;
    RETURN v_ids;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.expire_snapshot_under_maintenance_fence(
    p_expired_at timestamptz, p_evidence jsonb, p_physical_pool_id text,
    p_catalog_id text, p_snapshot_id bigint, p_maintenance_id uuid,
    p_maintenance_owner_id text, p_maintenance_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_retention record; v_child record; v_expired timestamptz := COALESCE(p_expired_at, v_now);
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_maintenance_owner_id AND f.fencing_epoch=p_maintenance_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_operation FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id FOR SHARE;
    IF NOT FOUND OR v_operation.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_operation.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_operation.owner_id IS DISTINCT FROM p_maintenance_owner_id
       OR v_operation.fencing_epoch IS DISTINCT FROM p_maintenance_fencing_epoch
       OR v_operation.dry_run THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    SELECT * INTO v_retention FROM ducklake.snapshot_retention r
     WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id AND r.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND OR v_retention.retention_claim_id IS DISTINCT FROM p_maintenance_id THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    IF v_retention.state='expiring' THEN
        IF EXISTS (SELECT 1 FROM ducklake.snapshot_lease l WHERE l.physical_pool_id=p_physical_pool_id AND l.catalog_id=p_catalog_id AND l.snapshot_id=p_snapshot_id AND l.state='active')
           OR EXISTS (SELECT 1 FROM ducklake.snapshot_root root WHERE root.physical_pool_id=p_physical_pool_id AND root.catalog_id=p_catalog_id AND root.snapshot_id=p_snapshot_id AND root.state IN ('live','retiring')) THEN
            RAISE EXCEPTION 'DuckLake snapshot leases or roots remain';
        END IF;
        UPDATE ducklake.snapshot_retention SET state='expired',expired_at=v_expired,evidence=p_evidence
         WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id AND state='expiring';
    ELSIF v_retention.state='expired' THEN
        IF v_retention.evidence IS DISTINCT FROM p_evidence THEN RAISE EXCEPTION 'expiration evidence differs'; END IF;
    ELSE
        RAISE EXCEPTION 'snapshot must be expiring before expiry';
    END IF;
    SELECT * INTO v_child FROM ducklake.retention_maintenance_snapshot s
     WHERE s.maintenance_id=p_maintenance_id AND s.physical_pool_id=p_physical_pool_id AND s.catalog_id=p_catalog_id AND s.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'retention maintenance snapshot not found'; END IF;
    IF v_child.phase='eligible' THEN
        UPDATE ducklake.retention_maintenance_snapshot
           SET phase='expired',expiry_evidence=p_evidence,updated_at=v_now
         WHERE maintenance_id=p_maintenance_id AND physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    ELSIF v_child.phase='expired' AND v_child.expiry_evidence IS DISTINCT FROM p_evidence THEN
        RAISE EXCEPTION 'expiration evidence differs';
    ELSIF v_child.phase NOT IN ('expired','quarantined','cleanup-complete') THEN
        RAISE EXCEPTION 'retention maintenance snapshot lifecycle is invalid';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.claim_snapshot_cleanup_under_maintenance_fence(
    p_physical_pool_id text, p_catalog_id text, p_snapshot_id bigint,
    p_cleanup_owner_id text, p_cleanup_lease_expires_at timestamptz,
    p_maintenance_id uuid, p_maintenance_owner_id text,
    p_maintenance_fencing_epoch bigint
) RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_retention record; v_lease timestamptz := p_cleanup_lease_expires_at;
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_maintenance_owner_id AND f.fencing_epoch=p_maintenance_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_operation FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id FOR SHARE;
    IF NOT FOUND OR v_operation.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_operation.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_operation.owner_id IS DISTINCT FROM p_maintenance_owner_id
       OR v_operation.fencing_epoch IS DISTINCT FROM p_maintenance_fencing_epoch
       OR v_operation.dry_run THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    SELECT * INTO v_retention FROM ducklake.snapshot_retention r
     WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id AND r.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND OR v_retention.retention_claim_id IS DISTINCT FROM p_maintenance_id THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    IF v_retention.state NOT IN ('expired','quarantined') THEN RAISE EXCEPTION 'snapshot cleanup pending'; END IF;
    IF v_retention.cleanup_owner_id IS NOT NULL AND v_retention.cleanup_lease_expires_at > v_now THEN
        IF v_retention.cleanup_owner_id=p_cleanup_owner_id THEN RETURN v_retention.cleanup_fencing_epoch; END IF;
        RAISE EXCEPTION 'snapshot cleanup busy';
    END IF;
    IF v_lease IS NULL THEN v_lease := v_now + interval '24 hours'; END IF;
    IF v_lease <= v_now OR v_lease > v_now + interval '24 hours' OR p_cleanup_owner_id = '' OR p_cleanup_owner_id <> btrim(p_cleanup_owner_id) OR octet_length(p_cleanup_owner_id) > 255 THEN
        RAISE EXCEPTION 'invalid snapshot cleanup claim';
    END IF;
    UPDATE ducklake.snapshot_retention
       SET cleanup_owner_id=p_cleanup_owner_id,cleanup_fencing_epoch=cleanup_fencing_epoch+1,cleanup_lease_expires_at=v_lease
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    SELECT cleanup_fencing_epoch INTO v_retention FROM ducklake.snapshot_retention
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    RETURN v_retention.cleanup_fencing_epoch;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.quarantine_snapshot_under_maintenance_fence(
    p_quarantine_evidence jsonb, p_quarantined_at timestamptz,
    p_physical_pool_id text, p_catalog_id text, p_snapshot_id bigint,
    p_cleanup_owner_id text, p_cleanup_fencing_epoch bigint,
    p_maintenance_id uuid, p_maintenance_owner_id text,
    p_maintenance_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_retention record; v_child record; v_at timestamptz := v_now;
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_maintenance_owner_id AND f.fencing_epoch=p_maintenance_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_operation FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id FOR SHARE;
    IF NOT FOUND OR v_operation.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_operation.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_operation.owner_id IS DISTINCT FROM p_maintenance_owner_id
       OR v_operation.fencing_epoch IS DISTINCT FROM p_maintenance_fencing_epoch
       OR v_operation.dry_run THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    SELECT * INTO v_retention FROM ducklake.snapshot_retention r
     WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id AND r.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND OR v_retention.retention_claim_id IS DISTINCT FROM p_maintenance_id
       OR v_retention.cleanup_owner_id IS DISTINCT FROM p_cleanup_owner_id
       OR v_retention.cleanup_fencing_epoch IS DISTINCT FROM p_cleanup_fencing_epoch
       OR v_retention.cleanup_lease_expires_at <= v_now THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    IF v_retention.state='expired' THEN
        UPDATE ducklake.snapshot_retention SET state='quarantined',quarantine_evidence=p_quarantine_evidence,quarantined_at=v_at
         WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id AND state='expired';
    ELSIF v_retention.state='quarantined' THEN
        IF v_retention.quarantine_evidence IS DISTINCT FROM p_quarantine_evidence THEN RAISE EXCEPTION 'quarantine evidence differs'; END IF;
    ELSE
        RAISE EXCEPTION 'snapshot must be expired before quarantine';
    END IF;
    SELECT * INTO v_child FROM ducklake.retention_maintenance_snapshot s
     WHERE s.maintenance_id=p_maintenance_id AND s.physical_pool_id=p_physical_pool_id AND s.catalog_id=p_catalog_id AND s.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'retention maintenance snapshot not found'; END IF;
    IF v_child.phase IN ('expired','quarantined') THEN
        IF v_child.phase='quarantined' AND v_child.quarantine_evidence IS DISTINCT FROM p_quarantine_evidence THEN RAISE EXCEPTION 'quarantine evidence differs'; END IF;
        IF v_child.phase='expired' THEN
            UPDATE ducklake.retention_maintenance_snapshot SET phase='quarantined',quarantine_evidence=p_quarantine_evidence,updated_at=v_now
             WHERE maintenance_id=p_maintenance_id AND physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
        END IF;
    ELSIF v_child.phase='cleanup-complete' THEN RETURN;
    ELSE RAISE EXCEPTION 'retention maintenance snapshot expiry evidence missing';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.complete_snapshot_cleanup_under_maintenance_fence(
    p_cleanup_evidence jsonb, p_cleanup_completed_at timestamptz,
    p_physical_pool_id text, p_catalog_id text, p_snapshot_id bigint,
    p_cleanup_owner_id text, p_cleanup_fencing_epoch bigint,
    p_maintenance_id uuid, p_maintenance_owner_id text,
    p_maintenance_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_retention record; v_child record; v_at timestamptz := v_now;
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_maintenance_owner_id AND f.fencing_epoch=p_maintenance_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_operation FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id FOR SHARE;
    IF NOT FOUND OR v_operation.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_operation.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_operation.owner_id IS DISTINCT FROM p_maintenance_owner_id
       OR v_operation.fencing_epoch IS DISTINCT FROM p_maintenance_fencing_epoch
       OR v_operation.dry_run THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    SELECT * INTO v_retention FROM ducklake.snapshot_retention r
     WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id AND r.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND OR v_retention.retention_claim_id IS DISTINCT FROM p_maintenance_id
       OR v_retention.cleanup_owner_id IS DISTINCT FROM p_cleanup_owner_id
       OR v_retention.cleanup_fencing_epoch IS DISTINCT FROM p_cleanup_fencing_epoch THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    IF v_retention.state='quarantined' THEN
        IF v_retention.cleanup_lease_expires_at <= v_now THEN RAISE EXCEPTION 'snapshot cleanup lease expired'; END IF;
        UPDATE ducklake.snapshot_retention SET state='cleanup-complete',cleanup_evidence=p_cleanup_evidence,cleanup_completed_at=v_at
         WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id AND state='quarantined';
    ELSIF v_retention.state='cleanup-complete' THEN
        IF v_retention.cleanup_evidence IS DISTINCT FROM p_cleanup_evidence THEN RAISE EXCEPTION 'cleanup evidence differs'; END IF;
    ELSE
        RAISE EXCEPTION 'snapshot must be quarantined before cleanup-complete';
    END IF;
    SELECT * INTO v_child FROM ducklake.retention_maintenance_snapshot s
     WHERE s.maintenance_id=p_maintenance_id AND s.physical_pool_id=p_physical_pool_id AND s.catalog_id=p_catalog_id AND s.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'retention maintenance snapshot not found'; END IF;
    IF v_child.phase='cleanup-complete' THEN
        IF v_child.cleanup_evidence IS DISTINCT FROM p_cleanup_evidence THEN RAISE EXCEPTION 'cleanup evidence differs'; END IF;
    ELSIF v_child.phase='quarantined' THEN
        UPDATE ducklake.retention_maintenance_snapshot SET phase='cleanup-complete',cleanup_evidence=p_cleanup_evidence,updated_at=v_now
         WHERE maintenance_id=p_maintenance_id AND physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    ELSE
        RAISE EXCEPTION 'retention maintenance snapshot quarantine evidence missing';
    END IF;
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
DECLARE m record; v_now timestamptz := clock_timestamp(); v_terminal timestamptz; v_missing bigint; v_rows bigint;
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
    -- Serialize the retained-snapshot boundary with commit attempts. Writers
    -- that started before completion become part of this migration epoch;
    -- writers admitted after the transaction commits receive a later
    -- created_at and rely on their native qualification evidence instead.
    LOCK TABLE ducklake.snapshot_retention IN SHARE MODE;
    v_terminal := clock_timestamp();
    SELECT count(*) INTO v_missing FROM ducklake.snapshot_retention r
     WHERE r.physical_pool_id=m.physical_pool_id AND r.catalog_id=m.catalog_id AND r.state IN ('live','retiring')
       AND NOT EXISTS (SELECT 1 FROM ducklake.snapshot_requalification q WHERE q.physical_pool_id=r.physical_pool_id AND q.catalog_id=r.catalog_id AND q.snapshot_id=r.snapshot_id AND q.migration_id=p_migration_id AND q.status='qualified' AND q.duckdb_runtime=m.target_duckdb_runtime AND q.ducklake_extension=m.target_ducklake_extension AND q.catalog_format=m.target_catalog_format AND q.compatibility_digest=m.target_compatibility_digest AND q.catalog_schema_version=m.target_catalog_schema_version);
    IF v_missing <> 0 THEN RAISE EXCEPTION 'snapshot qualification missing'; END IF;
    UPDATE ducklake.catalog_runtime_compatibility
       SET duckdb_runtime=m.target_duckdb_runtime,ducklake_extension=m.target_ducklake_extension,
           catalog_format=m.target_catalog_format,compatibility_digest=m.target_compatibility_digest,
           catalog_schema_version=m.target_catalog_schema_version,current_migration_id=p_migration_id,
           updated_at=v_terminal
     WHERE physical_pool_id=m.physical_pool_id AND catalog_id=m.catalog_id
       AND duckdb_runtime=m.current_duckdb_runtime AND ducklake_extension=m.current_ducklake_extension
       AND catalog_format=m.current_catalog_format AND compatibility_digest=m.current_compatibility_digest
       AND catalog_schema_version=m.current_catalog_schema_version;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'runtime compatibility mismatch'; END IF;
    UPDATE ducklake.catalog_migration
       SET state='completed',terminal_at=v_terminal,completion_evidence=p_completion_evidence
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
        EXECUTE 'GRANT SELECT, INSERT ON TABLE ducklake.source_observation_capture TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.snapshot_reader_drain TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.catalog_runtime_compatibility, ducklake.migration_fence, ducklake.pool_maintenance_fence, ducklake.retention_maintenance, ducklake.retention_maintenance_snapshot, ducklake.catalog_migration, ducklake.snapshot_requalification TO leapview_control_runtime';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA ducklake TO leapview_control_readonly';
        EXECUTE 'GRANT SELECT ON TABLE '
            || 'ducklake.catalog_identity, ducklake.attempt_evidence, '
            || 'ducklake.generation_binding, ducklake.snapshot_retention, '
            || 'ducklake.snapshot_root, ducklake.snapshot_lease, '
            || 'ducklake.snapshot_orphan, ducklake.snapshot_reader_drain, '
            || 'ducklake.catalog_runtime_compatibility, ducklake.migration_fence, ducklake.pool_maintenance_fence, '
            || 'ducklake.retention_maintenance, ducklake.retention_maintenance_snapshot, '
            || 'ducklake.catalog_migration, ducklake.snapshot_requalification, '
            || 'ducklake.source_observation_capture '
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
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA ducklake TO leapview_control_maintenance';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES ON TABLE ducklake.retention_maintenance, ducklake.retention_maintenance_snapshot, ducklake.snapshot_retention FROM leapview_control_maintenance';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.pool_maintenance_fence TO leapview_control_maintenance';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.retention_maintenance, ducklake.retention_maintenance_snapshot, ducklake.snapshot_retention TO leapview_control_maintenance';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.catalog_identity, ducklake.snapshot_root, ducklake.snapshot_lease TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.acquire_pool_maintenance_fence(text,text,text,timestamptz) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.release_pool_maintenance_fence(text,text,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.renew_pool_maintenance_fence(text,text,text,bigint,timestamptz) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.begin_retention_maintenance(uuid,text,text,text,bigint,boolean,bigint,text,jsonb) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.update_retention_maintenance(uuid,text,text,text,bigint,text,text,boolean,bigint,text,jsonb,timestamptz) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.insert_retention_maintenance_snapshot(uuid,text,text,bigint,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.claim_retention_snapshots(uuid,text,text,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.expire_snapshot_under_maintenance_fence(timestamptz,jsonb,text,text,bigint,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.claim_snapshot_cleanup_under_maintenance_fence(text,text,bigint,text,timestamptz,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.quarantine_snapshot_under_maintenance_fence(jsonb,timestamptz,text,text,bigint,text,bigint,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.complete_snapshot_cleanup_under_maintenance_fence(jsonb,timestamptz,text,text,bigint,text,bigint,uuid,text,bigint) TO leapview_control_maintenance';
    END IF;
END
$$;
