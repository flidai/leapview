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
    created_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (physical_pool_id, catalog_id),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (metadata_schema = btrim(metadata_schema) AND metadata_schema ~ '^[A-Za-z_][A-Za-z0-9_]*$')
);

-- Marker anomalies are durable pool-wide quarantine evidence.  They are
-- deliberately separate from positive attempt termination evidence: an
-- ambiguous or mismatched external marker cannot be represented as an abort
-- and must gate every successor attempt/recovery for this physical pool.
DO $$
BEGIN
    CREATE TYPE ducklake.marker_quarantine_reason AS ENUM
        ('duplicate', 'digest_mismatch', 'identity_mismatch');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS ducklake.marker_quarantine (
    physical_pool_id       text NOT NULL,
    catalog_id             text NOT NULL,
    attempt_id             uuid NOT NULL,
    request_digest         text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    plan_digest            text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    reason                 ducklake.marker_quarantine_reason NOT NULL,
    evidence               jsonb NOT NULL,
    observed_marker_digest text NOT NULL CHECK (observed_marker_digest ~ '^sha256:[0-9a-f]{64}$'),
    observed_snapshot_ids  bigint[] NOT NULL DEFAULT '{}'::bigint[],
    created_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (physical_pool_id, catalog_id, attempt_id),
    FOREIGN KEY (attempt_id, physical_pool_id, catalog_id)
        REFERENCES delivery.delivery_build_attempt(attempt_id, physical_pool_id, catalog_id),
    FOREIGN KEY (physical_pool_id, catalog_id)
        REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    CHECK (cardinality(observed_snapshot_ids) <= 128)
);

CREATE INDEX IF NOT EXISTS ducklake_marker_quarantine_pool_idx
    ON ducklake.marker_quarantine (physical_pool_id, created_at);


-- Source observations are captured by the exact native DuckLake writer while
-- its prepared source session is still live.  The attempt key makes the
-- capture replay-safe; marker and envelope bytes are canonical identities,
-- not mutable diagnostic payloads.
CREATE TABLE IF NOT EXISTS ducklake.source_observation_capture (
    attempt_id            uuid PRIMARY KEY,
    commit_marker         jsonb NOT NULL,
    observation_envelope  jsonb NOT NULL,
    content_digest        text NOT NULL,
    captured_at           timestamptz NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (attempt_id) REFERENCES delivery.delivery_build_attempt(attempt_id) ON DELETE RESTRICT,
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

-- Runtime admission is a narrow capability: callers may name only the
-- canonical delivery seal, while this owner-executed function derives the
-- exact physical pool/catalog/snapshot identity and idempotently creates the
-- live retention gate. Keep the search path fixed and qualify every object
-- so an untrusted caller cannot redirect SECURITY DEFINER resolution.
CREATE OR REPLACE FUNCTION ducklake.admit_snapshot_retention_from_seal(p_seal_id uuid)
RETURNS TABLE (physical_pool_id text, catalog_id text, snapshot_id bigint, retention_state text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_physical_pool_id text;
    v_catalog_id text;
    v_snapshot_id bigint;
BEGIN
    SELECT s.physical_pool_id, s.catalog_id, s.ducklake_snapshot_id
      INTO v_physical_pool_id, v_catalog_id, v_snapshot_id
      FROM delivery.delivery_snapshot_seal AS s
     WHERE s.seal_id = p_seal_id;
    IF NOT FOUND THEN
        RETURN;
    END IF;

    -- Enforce the same migration/maintenance serialization at the database
    -- capability boundary. A caller with EXECUTE privilege must not be able
    -- to bypass the repository's admission check by invoking this function
    -- directly.
    PERFORM ducklake.assert_attempt_admission_fence(v_physical_pool_id, v_catalog_id);

    INSERT INTO ducklake.snapshot_retention AS r
        (physical_pool_id, catalog_id, snapshot_id, state)
    VALUES (v_physical_pool_id, v_catalog_id, v_snapshot_id, 'live')
    ON CONFLICT ON CONSTRAINT snapshot_retention_pkey DO NOTHING;

    SELECT r.state
      INTO retention_state
      FROM ducklake.snapshot_retention AS r
     WHERE r.physical_pool_id = v_physical_pool_id
       AND r.catalog_id = v_catalog_id
       AND r.snapshot_id = v_snapshot_id
     FOR UPDATE;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    physical_pool_id := v_physical_pool_id;
    catalog_id := v_catalog_id;
    snapshot_id := v_snapshot_id;
    RETURN NEXT;
END;
$$;
REVOKE ALL ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) FROM PUBLIC;

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
    cleanup_not_before timestamptz NOT NULL DEFAULT clock_timestamp(),
    resolved_at      timestamptz,
    FOREIGN KEY (physical_pool_id, catalog_id) REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    UNIQUE (physical_pool_id, catalog_id, snapshot_id),
    CHECK ((state = 'quarantined' AND resolved_at IS NULL) OR (state = 'cleanup-complete' AND resolved_at IS NOT NULL)),
    CHECK ((cleanup_fencing_epoch = 0 AND cleanup_owner_id IS NULL AND cleanup_lease_expires_at IS NULL) OR (cleanup_fencing_epoch > 0 AND cleanup_owner_id IS NOT NULL AND cleanup_lease_expires_at IS NOT NULL)),
    CHECK (cleanup_owner_id IS NULL OR (cleanup_owner_id = btrim(cleanup_owner_id) AND octet_length(cleanup_owner_id) BETWEEN 1 AND 255)),
    CHECK (cleanup_lease_expires_at IS NULL OR cleanup_lease_expires_at > discovered_at),
    CHECK (cleanup_not_before >= discovered_at)
);

-- Older installations predate orphan cleanup grace.  Add the column in place,
-- backfill existing observations from their discovery timestamp, and restore
-- the same default, nullability, and ordering invariant as fresh databases.
ALTER TABLE ducklake.snapshot_orphan
    ADD COLUMN IF NOT EXISTS cleanup_not_before timestamptz;
-- The current immutable-row trigger also protects cleanup_not_before.  Drop it
-- for the transactional backfill; the schema's trigger definition is recreated
-- below after this migration block has completed.
DROP TRIGGER IF EXISTS snapshot_orphan_identity_immutable ON ducklake.snapshot_orphan;
UPDATE ducklake.snapshot_orphan
   SET cleanup_not_before = CASE
       WHEN cleanup_not_before IS NULL OR cleanup_not_before < discovered_at THEN discovered_at
       ELSE cleanup_not_before
   END
 WHERE cleanup_not_before IS NULL OR cleanup_not_before < discovered_at;
ALTER TABLE ducklake.snapshot_orphan
    ALTER COLUMN cleanup_not_before SET DEFAULT clock_timestamp(),
    ALTER COLUMN cleanup_not_before SET NOT NULL;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint
         WHERE conrelid = 'ducklake.snapshot_orphan'::regclass
           AND conname = 'snapshot_orphan_cleanup_not_before_check'
    ) THEN
        ALTER TABLE ducklake.snapshot_orphan
            ADD CONSTRAINT snapshot_orphan_cleanup_not_before_check
            CHECK (cleanup_not_before >= discovered_at);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS ducklake_snapshot_orphan_backlog_idx
    ON ducklake.snapshot_orphan (physical_pool_id, catalog_id, state, discovered_at);

-- A snapshot-orphan scan is a durable, resumable walk over one exact
-- physical pool/catalog.  The catalog metadata lives in the separately
-- provisioned DuckLake database; this control-side ledger stores only the
-- bounded page identities and evidence supplied by that catalog adapter.
CREATE TABLE IF NOT EXISTS ducklake.snapshot_orphan_scan (
    scan_id             uuid PRIMARY KEY,
    physical_pool_id    text NOT NULL,
    catalog_id          text NOT NULL,
    owner_id            text NOT NULL,
    fencing_epoch       bigint NOT NULL CHECK (fencing_epoch > 0),
    page_size           integer NOT NULL CHECK (page_size BETWEEN 1 AND 256),
    grace_micros        bigint NOT NULL CHECK (grace_micros BETWEEN 1 AND 2592000000000),
    cursor_snapshot_id  bigint NOT NULL DEFAULT 0 CHECK (cursor_snapshot_id >= 0),
    pages_scanned       integer NOT NULL DEFAULT 0 CHECK (pages_scanned >= 0),
    snapshots_scanned   bigint NOT NULL DEFAULT 0 CHECK (snapshots_scanned >= 0),
    orphans_recorded    bigint NOT NULL DEFAULT 0 CHECK (orphans_recorded >= 0),
    state               text NOT NULL CHECK (state IN ('running','completed')),
    request_evidence    jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(request_evidence) = 'object' AND octet_length(request_evidence::text) <= 32768),
    completion_evidence jsonb,
    cleanup_not_before  timestamptz NOT NULL,
    pruned_at           timestamptz,
    pruned_page_count   integer NOT NULL DEFAULT 0 CHECK (pruned_page_count >= 0),
    pruned_page_digest  text NOT NULL DEFAULT '' CHECK (pruned_page_digest = '' OR pruned_page_digest ~ '^sha256:[0-9a-f]{64}$'),
    started_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at        timestamptz,
    FOREIGN KEY (physical_pool_id, catalog_id)
        REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    UNIQUE (scan_id, physical_pool_id, catalog_id),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK (completed_at IS NULL OR (state = 'completed' AND completed_at >= started_at)),
    CHECK (cleanup_not_before >= started_at),
    CHECK (completion_evidence IS NULL OR (jsonb_typeof(completion_evidence) = 'object' AND octet_length(completion_evidence::text) <= 32768)),
    CHECK ((pruned_at IS NULL AND pruned_page_count = 0 AND pruned_page_digest = '') OR (state = 'completed' AND pruned_at IS NOT NULL AND pruned_page_count >= 0 AND pruned_page_digest ~ '^sha256:[0-9a-f]{64}$'))
);

CREATE TABLE IF NOT EXISTS ducklake.snapshot_orphan_scan_page (
    scan_id             uuid NOT NULL REFERENCES ducklake.snapshot_orphan_scan(scan_id) ON DELETE RESTRICT,
    physical_pool_id    text NOT NULL,
    catalog_id          text NOT NULL,
    page_number         integer NOT NULL CHECK (page_number > 0),
    cursor_before       bigint NOT NULL CHECK (cursor_before >= 0),
    cursor_after        bigint NOT NULL CHECK (cursor_after >= cursor_before),
    snapshot_ids        bigint[] NOT NULL,
    orphan_count        integer NOT NULL DEFAULT 0 CHECK (orphan_count >= 0 AND orphan_count <= 256),
    terminal            boolean NOT NULL DEFAULT false,
    page_digest         text NOT NULL CHECK (page_digest ~ '^sha256:[0-9a-f]{64}$'),
    evidence            jsonb NOT NULL
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (scan_id, page_number),
    FOREIGN KEY (scan_id, physical_pool_id, catalog_id)
        REFERENCES ducklake.snapshot_orphan_scan(scan_id, physical_pool_id, catalog_id),
    CHECK (cardinality(snapshot_ids) <= 256),
    CHECK (snapshot_ids = '{}'::bigint[] OR snapshot_ids[1] > cursor_before),
    CHECK (snapshot_ids = '{}'::bigint[] OR snapshot_ids[array_length(snapshot_ids,1)] <= cursor_after)
);

CREATE UNIQUE INDEX IF NOT EXISTS ducklake_snapshot_orphan_scan_page_cursor_idx
    ON ducklake.snapshot_orphan_scan_page (scan_id, cursor_before, cursor_after);

CREATE INDEX IF NOT EXISTS ducklake_snapshot_orphan_scan_backlog_idx
    ON ducklake.snapshot_orphan_scan (physical_pool_id, catalog_id, state, updated_at);

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

CREATE OR REPLACE FUNCTION ducklake.reject_immutable_change()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'DuckLake identity evidence is immutable';
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
           -- A delivery root is physically attributable only through its
           -- immutable snapshot seal. Roots with a NULL seal are not mapped
           -- to a pool/catalog/snapshot by inference.
           SELECT 1
             FROM delivery.delivery_retention_root root
             JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = root.snapshot_seal_id
            WHERE seal.physical_pool_id=OLD.physical_pool_id
              AND seal.catalog_id=OLD.catalog_id
              AND seal.ducklake_snapshot_id=OLD.snapshot_id
              AND root.state IN ('live','retiring')) THEN
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
               SELECT 1
                 FROM serving_state.reader_lease l
                 JOIN delivery.delivery_generation g ON g.generation_id = l.generation_id
                 JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
                WHERE seal.physical_pool_id=OLD.physical_pool_id
                  AND seal.catalog_id=OLD.catalog_id
                  AND seal.ducklake_snapshot_id=OLD.snapshot_id
                  AND l.released_at IS NULL)
            OR EXISTS (
               SELECT 1
                 FROM delivery.delivery_retention_root root
                 JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = root.snapshot_seal_id
                WHERE seal.physical_pool_id=OLD.physical_pool_id
                  AND seal.catalog_id=OLD.catalog_id
                  AND seal.ducklake_snapshot_id=OLD.snapshot_id
                  AND root.state IN ('live','retiring'))
            ) THEN
        RAISE EXCEPTION 'canonical snapshot protections remain';
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
       OR NEW.discovered_at IS DISTINCT FROM OLD.discovered_at
       OR NEW.cleanup_not_before IS DISTINCT FROM OLD.cleanup_not_before THEN
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

CREATE OR REPLACE FUNCTION ducklake.reject_snapshot_orphan_scan_change()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.scan_id <> OLD.scan_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.page_size <> OLD.page_size
       OR NEW.grace_micros <> OLD.grace_micros
       OR NEW.cleanup_not_before <> OLD.cleanup_not_before
       OR NEW.started_at <> OLD.started_at
       OR NEW.request_evidence IS DISTINCT FROM OLD.request_evidence THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan scan identity is immutable';
    END IF;
    IF OLD.state = 'completed' AND
       (NEW.state <> OLD.state OR NEW.cursor_snapshot_id <> OLD.cursor_snapshot_id
        OR NEW.pages_scanned <> OLD.pages_scanned OR NEW.snapshots_scanned <> OLD.snapshots_scanned
        OR NEW.orphans_recorded <> OLD.orphans_recorded OR NEW.updated_at IS DISTINCT FROM OLD.updated_at
        OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
        OR NEW.completion_evidence IS DISTINCT FROM OLD.completion_evidence
        OR (NEW.pruned_at IS DISTINCT FROM OLD.pruned_at
            OR NEW.pruned_page_count <> OLD.pruned_page_count
            OR NEW.pruned_page_digest <> OLD.pruned_page_digest)
           AND NOT (OLD.pruned_at IS NULL AND NEW.pruned_at IS NOT NULL
                    AND NEW.pruned_page_count >= 0
                    AND NEW.pruned_page_digest ~ '^sha256:[0-9a-f]{64}$')) THEN
        RAISE EXCEPTION 'DuckLake terminal snapshot orphan scan is immutable';
    END IF;
    IF NEW.fencing_epoch < OLD.fencing_epoch
       OR (NEW.fencing_epoch = OLD.fencing_epoch AND NEW.owner_id IS DISTINCT FROM OLD.owner_id) THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan scan fence epoch cannot move backwards';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan scan updated_at cannot move backwards';
    END IF;
    IF NEW.cursor_snapshot_id < OLD.cursor_snapshot_id
       OR NEW.pages_scanned < OLD.pages_scanned
       OR NEW.snapshots_scanned < OLD.snapshots_scanned
       OR NEW.orphans_recorded < OLD.orphans_recorded THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan scan progress cannot move backwards';
    END IF;
    IF (NEW.state = 'running' AND (NEW.completed_at IS NOT NULL OR NEW.completion_evidence IS NOT NULL))
       OR (NEW.state = 'completed' AND (NEW.completed_at IS NULL OR NEW.completion_evidence IS NULL)) THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan scan terminal evidence is inconsistent';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.reject_snapshot_orphan_scan_page_change()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF current_setting('ducklake.scan_prune', true) = 'on' THEN RETURN OLD; END IF;
        RAISE EXCEPTION 'DuckLake snapshot orphan scan page evidence is immutable';
    END IF;
    IF TG_OP <> 'UPDATE'
       OR NEW.scan_id <> OLD.scan_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.page_number <> OLD.page_number
       OR NEW.cursor_before <> OLD.cursor_before
       OR NEW.cursor_after <> OLD.cursor_after
       OR NEW.snapshot_ids IS DISTINCT FROM OLD.snapshot_ids
       OR NEW.orphan_count <> OLD.orphan_count
       OR NEW.terminal <> OLD.terminal
       OR NEW.page_digest <> OLD.page_digest
       OR NEW.evidence IS DISTINCT FROM OLD.evidence
       OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan scan page evidence is immutable';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS catalog_identity_immutable ON ducklake.catalog_identity;
CREATE TRIGGER catalog_identity_immutable
    BEFORE UPDATE OR DELETE ON ducklake.catalog_identity
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_immutable_change();

DROP TRIGGER IF EXISTS marker_quarantine_immutable ON ducklake.marker_quarantine;
CREATE TRIGGER marker_quarantine_immutable
    BEFORE UPDATE OR DELETE ON ducklake.marker_quarantine
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_immutable_change();

DROP TRIGGER IF EXISTS snapshot_retention_identity_immutable ON ducklake.snapshot_retention;
CREATE TRIGGER snapshot_retention_identity_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_retention
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_retention_identity_change();

DROP TRIGGER IF EXISTS snapshot_orphan_identity_immutable ON ducklake.snapshot_orphan;
CREATE TRIGGER snapshot_orphan_identity_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_orphan
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_orphan_identity_change();

DROP TRIGGER IF EXISTS snapshot_orphan_scan_immutable ON ducklake.snapshot_orphan_scan;
CREATE TRIGGER snapshot_orphan_scan_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_orphan_scan
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_orphan_scan_change();

DROP TRIGGER IF EXISTS snapshot_orphan_scan_page_immutable ON ducklake.snapshot_orphan_scan_page;
CREATE TRIGGER snapshot_orphan_scan_page_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_orphan_scan_page
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_orphan_scan_page_change();

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
    -- Admission serializes on the same global→pool→maintenance lock order;
    -- once this maintenance row is locked, no admitted running writer can
    -- appear after this check. A running attempt must drain before a
    -- maintenance fence can be acquired.
    IF EXISTS (SELECT 1 FROM delivery.delivery_build_attempt a
               WHERE a.physical_pool_id=p_physical_pool_id
                 AND a.catalog_id=p_catalog_id
                 AND a.state='running') THEN
        RAISE EXCEPTION 'pool maintenance fence busy: running attempt';
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

-- Build admission is the one runtime operation that must observe the
-- migration/retention authorities.  Keep the row locks inside a narrowly
-- scoped SECURITY DEFINER function: the runtime role may not UPDATE fence
-- tables directly, yet the locks remain held by the caller's transaction
-- through the subsequent attempt insert.  The order is global migration ->
-- pool migration -> pool maintenance, matching every fence claim path.
CREATE OR REPLACE FUNCTION ducklake.assert_attempt_admission_fence(
    p_physical_pool_id text,
    p_catalog_id text
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_owner text;
    v_expiry timestamptz;
BEGIN
    IF p_physical_pool_id = '' OR p_physical_pool_id <> btrim(p_physical_pool_id) OR octet_length(p_physical_pool_id) > 255
       OR p_catalog_id = '' OR p_catalog_id <> btrim(p_catalog_id) OR octet_length(p_catalog_id) > 255 THEN
        RAISE EXCEPTION 'invalid attempt admission fence scope';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM ducklake.catalog_identity ci
                   WHERE ci.physical_pool_id=p_physical_pool_id AND ci.catalog_id=p_catalog_id) THEN
        RAISE EXCEPTION 'catalog identity not found';
    END IF;

    INSERT INTO ducklake.migration_fence(scope,physical_pool_id)
    VALUES ('global','') ON CONFLICT DO NOTHING;
    SELECT owner_id,lease_expires_at INTO v_owner,v_expiry
      FROM ducklake.migration_fence
     WHERE scope='global' AND physical_pool_id=''
     FOR UPDATE;
    IF v_owner IS NOT NULL AND v_expiry > v_now THEN
        RAISE EXCEPTION 'migration fence busy';
    END IF;

    INSERT INTO ducklake.migration_fence(scope,physical_pool_id)
    VALUES ('pool',p_physical_pool_id) ON CONFLICT DO NOTHING;
    SELECT owner_id,lease_expires_at INTO v_owner,v_expiry
      FROM ducklake.migration_fence
     WHERE scope='pool' AND physical_pool_id=p_physical_pool_id
     FOR UPDATE;
    IF v_owner IS NOT NULL AND v_expiry > v_now THEN
        RAISE EXCEPTION 'migration fence busy';
    END IF;

    -- Materialize and lock the exact pool/catalog maintenance row before
    -- checking its owner. Without this first-use insert, admission could
    -- observe no row while a concurrent retention claim creates and activates
    -- the fence, then proceed to insert a running attempt after the check.
    INSERT INTO ducklake.pool_maintenance_fence(physical_pool_id,catalog_id)
    VALUES (p_physical_pool_id,p_catalog_id) ON CONFLICT DO NOTHING;
    SELECT owner_id,lease_expires_at INTO v_owner,v_expiry
      FROM ducklake.pool_maintenance_fence
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id
     FOR UPDATE;
    IF v_owner IS NOT NULL AND v_expiry > v_now THEN
        RAISE EXCEPTION 'pool maintenance fence busy';
    END IF;
END;
$$;

-- PostgreSQL grants EXECUTE on new functions to PUBLIC by default.  This
-- runtime-only admission capability must be explicitly private before role
-- grants are applied below.
REVOKE EXECUTE ON FUNCTION ducklake.assert_attempt_admission_fence(text,text) FROM PUBLIC;

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

CREATE OR REPLACE FUNCTION ducklake.insert_retention_maintenance_snapshots(
    p_maintenance_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_snapshot_ids bigint[], p_owner_id text, p_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_matches integer; v_count integer;
BEGIN
    v_count := COALESCE(cardinality(p_snapshot_ids), 0);
    IF v_count < 1 OR v_count > 256
       OR (SELECT count(DISTINCT ids.snapshot_id) FROM unnest(p_snapshot_ids) AS ids(snapshot_id)) <> v_count
       OR EXISTS (SELECT 1 FROM unnest(p_snapshot_ids) AS ids(snapshot_id) WHERE ids.snapshot_id <= 0) THEN
        RAISE EXCEPTION 'invalid retention snapshot batch';
    END IF;
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
    SELECT count(*) INTO v_matches
      FROM unnest(p_snapshot_ids) AS ids(snapshot_id)
      JOIN ducklake.snapshot_retention r
        ON r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id
       AND r.snapshot_id=ids.snapshot_id
     WHERE (v_operation.dry_run AND r.state IN ('retiring','expired') AND r.retention_claim_id IS NULL)
        OR (NOT v_operation.dry_run AND r.retention_claim_id=p_maintenance_id AND r.state IN ('expiring','expired'));
    IF v_matches <> v_count THEN RAISE EXCEPTION 'retention snapshot claim mismatch'; END IF;
    INSERT INTO ducklake.retention_maintenance_snapshot
      (maintenance_id,physical_pool_id,catalog_id,snapshot_id,phase)
    SELECT p_maintenance_id,p_physical_pool_id,p_catalog_id,ids.snapshot_id,'eligible'
      FROM unnest(p_snapshot_ids) AS ids(snapshot_id)
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
    p_owner_id text, p_fencing_epoch bigint, p_limit integer
) RETURNS bigint[]
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_ids bigint[];
BEGIN
    IF p_limit < 1 OR p_limit > 256 THEN RAISE EXCEPTION 'invalid retention claim limit'; END IF;
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
    WITH candidates AS (
        SELECT r.snapshot_id
          FROM ducklake.snapshot_retention AS r
         WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id
           AND r.state IN ('retiring','expired') AND r.retention_claim_id IS NULL
           AND NOT EXISTS (
               SELECT 1
                 FROM delivery.delivery_retention_root root
                 JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = root.snapshot_seal_id
                WHERE seal.physical_pool_id=r.physical_pool_id
                  AND seal.catalog_id=r.catalog_id
                  AND seal.ducklake_snapshot_id=r.snapshot_id
                  AND root.state IN ('live','retiring'))
           AND NOT EXISTS (
               SELECT 1
                 FROM serving_state.reader_lease lease
                 JOIN delivery.delivery_generation g ON g.generation_id = lease.generation_id
                 JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
                WHERE seal.physical_pool_id=r.physical_pool_id
                  AND seal.catalog_id=r.catalog_id
                  AND seal.ducklake_snapshot_id=r.snapshot_id
                  AND lease.released_at IS NULL)
         ORDER BY r.snapshot_id
         LIMIT p_limit
         FOR UPDATE OF r
    ), changed AS (
        UPDATE ducklake.snapshot_retention AS r
       SET state=CASE WHEN r.state='retiring' THEN 'expiring' ELSE r.state END,
           retention_claim_id=p_maintenance_id,retention_claim_owner_id=p_owner_id,
           retention_claim_fencing_epoch=p_fencing_epoch,retention_claimed_at=v_now
      FROM candidates
     WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id
       AND r.snapshot_id=candidates.snapshot_id
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
        IF EXISTS (
               SELECT 1
                 FROM serving_state.reader_lease l
                 JOIN delivery.delivery_generation g ON g.generation_id = l.generation_id
                 JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
                WHERE seal.physical_pool_id=p_physical_pool_id
                  AND seal.catalog_id=p_catalog_id
                  AND seal.ducklake_snapshot_id=p_snapshot_id
                  AND l.released_at IS NULL)
           OR EXISTS (
               SELECT 1
                 FROM delivery.delivery_retention_root root
                 JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = root.snapshot_seal_id
                WHERE seal.physical_pool_id=p_physical_pool_id
                  AND seal.catalog_id=p_catalog_id
                  AND seal.ducklake_snapshot_id=p_snapshot_id
                  AND root.state IN ('live','retiring'))
           THEN
            RAISE EXCEPTION 'canonical snapshot protections remain';
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

-- RetentionCoordinator submits one bounded batch per control phase.  These
-- wrappers deliberately invoke the existing fenced lifecycle capabilities in
-- one SQL statement: every child still receives the same row locks, fence,
-- operation identity, monotonicity, and evidence checks, while the client no
-- longer performs a network round trip for each child.  A failure rolls back
-- the whole statement, so a successor can replay the exact frozen set.
CREATE OR REPLACE FUNCTION ducklake.expire_snapshots_under_maintenance_fence(
    p_snapshot_ids bigint[], p_expired_at timestamptz, p_items jsonb,
    p_physical_pool_id text, p_catalog_id text, p_maintenance_id uuid,
    p_maintenance_owner_id text, p_maintenance_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_item jsonb; v_id bigint; v_evidence jsonb; v_count integer;
BEGIN
    v_count := COALESCE(cardinality(p_snapshot_ids), 0);
    IF v_count < 1 OR v_count > 256 OR (SELECT count(DISTINCT id) FROM unnest(p_snapshot_ids) AS u(id)) <> v_count
       OR jsonb_typeof(COALESCE(p_items, 'null'::jsonb)) <> 'array'
       OR jsonb_array_length(p_items) <> v_count THEN
        RAISE EXCEPTION 'invalid retention expiry batch';
    END IF;
    FOR v_item IN SELECT value FROM jsonb_array_elements(p_items) LOOP
        IF jsonb_typeof(v_item) <> 'object' OR (v_item->>'snapshot_id') IS NULL
           OR (v_item->'evidence') IS NULL OR jsonb_typeof(v_item->'evidence') <> 'object' THEN
            RAISE EXCEPTION 'invalid retention expiry evidence';
        END IF;
        v_id := (v_item->>'snapshot_id')::bigint;
        IF NOT (v_id = ANY(p_snapshot_ids)) THEN
            RAISE EXCEPTION 'retention expiry snapshot is outside the frozen set';
        END IF;
        v_evidence := v_item->'evidence';
        PERFORM ducklake.expire_snapshot_under_maintenance_fence(
            p_expired_at, v_evidence, p_physical_pool_id, p_catalog_id, v_id,
            p_maintenance_id, p_maintenance_owner_id, p_maintenance_fencing_epoch);
    END LOOP;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.reconcile_retention_maintenance_snapshots(
    p_items jsonb, p_maintenance_id uuid, p_physical_pool_id text,
    p_catalog_id text, p_owner_id text, p_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_item jsonb; v_id bigint; v_phase text;
        v_old_phase text; v_expiry jsonb; v_quarantine jsonb; v_cleanup jsonb;
BEGIN
    IF jsonb_typeof(COALESCE(p_items, 'null'::jsonb)) <> 'array' OR jsonb_array_length(p_items) > 256 THEN
        RAISE EXCEPTION 'invalid retention reconciliation batch';
    END IF;
    FOR v_item IN SELECT value FROM jsonb_array_elements(p_items) LOOP
        IF jsonb_typeof(v_item) <> 'object' OR (v_item->>'snapshot_id') IS NULL
           OR (v_item->>'phase') IS NULL THEN
            RAISE EXCEPTION 'invalid retention reconciliation item';
        END IF;
        v_id := (v_item->>'snapshot_id')::bigint;
        v_phase := v_item->>'phase';
        v_expiry := CASE WHEN v_item ? 'expiry_evidence' AND jsonb_typeof(v_item->'expiry_evidence') <> 'null' THEN v_item->'expiry_evidence' ELSE NULL END;
        v_quarantine := CASE WHEN v_item ? 'quarantine_evidence' AND jsonb_typeof(v_item->'quarantine_evidence') <> 'null' THEN v_item->'quarantine_evidence' ELSE NULL END;
        v_cleanup := CASE WHEN v_item ? 'cleanup_evidence' AND jsonb_typeof(v_item->'cleanup_evidence') <> 'null' THEN v_item->'cleanup_evidence' ELSE NULL END;
        SELECT s.phase INTO v_old_phase
          FROM ducklake.retention_maintenance_snapshot s
         WHERE s.maintenance_id=p_maintenance_id AND s.physical_pool_id=p_physical_pool_id
           AND s.catalog_id=p_catalog_id AND s.snapshot_id=v_id FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION 'retention maintenance snapshot not found'; END IF;
        -- The immutable trigger intentionally permits only one lifecycle edge
        -- from eligible (eligible→expired→quarantined). Replay may still
        -- collapse those edges into this single network round trip.
        IF v_old_phase = 'eligible' AND v_phase IN ('quarantined','cleanup-complete') THEN
            PERFORM ducklake.update_retention_maintenance_snapshot(
                p_maintenance_id, p_physical_pool_id, p_catalog_id, v_id,
                p_owner_id, p_fencing_epoch, 'expired', v_expiry, NULL, NULL);
            v_old_phase := 'expired';
        END IF;
        IF v_old_phase IN ('eligible','expired') AND v_phase = 'cleanup-complete' THEN
            IF v_old_phase = 'eligible' THEN
                PERFORM ducklake.update_retention_maintenance_snapshot(
                    p_maintenance_id, p_physical_pool_id, p_catalog_id, v_id,
                    p_owner_id, p_fencing_epoch, 'expired', v_expiry, NULL, NULL);
            END IF;
            PERFORM ducklake.update_retention_maintenance_snapshot(
                p_maintenance_id, p_physical_pool_id, p_catalog_id, v_id,
                p_owner_id, p_fencing_epoch, 'quarantined', v_expiry, v_quarantine, NULL);
            v_old_phase := 'quarantined';
        ELSIF v_old_phase = 'eligible' AND v_phase = 'quarantined' THEN
            PERFORM ducklake.update_retention_maintenance_snapshot(
                p_maintenance_id, p_physical_pool_id, p_catalog_id, v_id,
                p_owner_id, p_fencing_epoch, 'expired', v_expiry, NULL, NULL);
            v_old_phase := 'expired';
            PERFORM ducklake.update_retention_maintenance_snapshot(
                p_maintenance_id, p_physical_pool_id, p_catalog_id, v_id,
                p_owner_id, p_fencing_epoch, 'quarantined', v_expiry, v_quarantine, NULL);
            v_old_phase := 'quarantined';
        END IF;
        IF v_old_phase <> v_phase THEN
            PERFORM ducklake.update_retention_maintenance_snapshot(
                p_maintenance_id, p_physical_pool_id, p_catalog_id, v_id,
                p_owner_id, p_fencing_epoch, v_phase, v_expiry, v_quarantine, v_cleanup);
        END IF;
    END LOOP;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.quarantine_snapshots_under_maintenance_fence(
    p_snapshot_ids bigint[], p_items jsonb, p_cleanup_lease_expires_at timestamptz,
    p_quarantined_at timestamptz,
    p_physical_pool_id text, p_catalog_id text, p_maintenance_id uuid,
    p_maintenance_owner_id text, p_maintenance_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_item jsonb; v_id bigint; v_evidence jsonb; v_cleanup_epoch bigint; v_count integer;
BEGIN
    v_count := COALESCE(cardinality(p_snapshot_ids), 0);
    IF v_count < 1 OR v_count > 256 OR (SELECT count(DISTINCT id) FROM unnest(p_snapshot_ids) AS u(id)) <> v_count
       OR jsonb_typeof(COALESCE(p_items, 'null'::jsonb)) <> 'array'
       OR jsonb_array_length(p_items) <> v_count THEN
        RAISE EXCEPTION 'invalid retention quarantine batch';
    END IF;
    FOR v_item IN SELECT value FROM jsonb_array_elements(p_items) LOOP
        IF jsonb_typeof(v_item) <> 'object' OR (v_item->>'snapshot_id') IS NULL
           OR (v_item->'evidence') IS NULL OR jsonb_typeof(v_item->'evidence') <> 'object' THEN
            RAISE EXCEPTION 'invalid retention quarantine evidence';
        END IF;
        v_id := (v_item->>'snapshot_id')::bigint;
        IF NOT (v_id = ANY(p_snapshot_ids)) THEN
            RAISE EXCEPTION 'retention quarantine snapshot is outside the frozen set';
        END IF;
        v_evidence := v_item->'evidence';
        v_cleanup_epoch := ducklake.claim_snapshot_cleanup_under_maintenance_fence(
            p_physical_pool_id, p_catalog_id, v_id, p_maintenance_owner_id,
            p_cleanup_lease_expires_at, p_maintenance_id, p_maintenance_owner_id,
            p_maintenance_fencing_epoch);
        PERFORM ducklake.quarantine_snapshot_under_maintenance_fence(
            v_evidence, p_quarantined_at, p_physical_pool_id, p_catalog_id, v_id,
            p_maintenance_owner_id, v_cleanup_epoch, p_maintenance_id,
            p_maintenance_owner_id, p_maintenance_fencing_epoch);
    END LOOP;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.complete_snapshots_cleanup_under_maintenance_fence(
    p_snapshot_ids bigint[], p_items jsonb, p_cleanup_lease_expires_at timestamptz,
    p_cleanup_completed_at timestamptz,
    p_physical_pool_id text, p_catalog_id text, p_maintenance_id uuid,
    p_maintenance_owner_id text, p_maintenance_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_item jsonb; v_id bigint; v_evidence jsonb; v_cleanup_epoch bigint;
        v_state text; v_owner text; v_count integer;
BEGIN
    v_count := COALESCE(cardinality(p_snapshot_ids), 0);
    IF v_count < 1 OR v_count > 256 OR (SELECT count(DISTINCT id) FROM unnest(p_snapshot_ids) AS u(id)) <> v_count
       OR jsonb_typeof(COALESCE(p_items, 'null'::jsonb)) <> 'array'
       OR jsonb_array_length(p_items) <> v_count THEN
        RAISE EXCEPTION 'invalid retention cleanup batch';
    END IF;
    FOR v_item IN SELECT value FROM jsonb_array_elements(p_items) LOOP
        IF jsonb_typeof(v_item) <> 'object' OR (v_item->>'snapshot_id') IS NULL
           OR (v_item->'evidence') IS NULL OR jsonb_typeof(v_item->'evidence') <> 'object' THEN
            RAISE EXCEPTION 'invalid retention cleanup evidence';
        END IF;
        v_id := (v_item->>'snapshot_id')::bigint;
        IF NOT (v_id = ANY(p_snapshot_ids)) THEN
            RAISE EXCEPTION 'retention cleanup snapshot is outside the frozen set';
        END IF;
        v_evidence := v_item->'evidence';
        SELECT r.state, r.cleanup_owner_id, r.cleanup_fencing_epoch
          INTO v_state, v_owner, v_cleanup_epoch
          FROM ducklake.snapshot_retention r
         WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id
           AND r.snapshot_id=v_id FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION 'snapshot retention not found'; END IF;
        IF v_state <> 'cleanup-complete' THEN
            v_cleanup_epoch := ducklake.claim_snapshot_cleanup_under_maintenance_fence(
                p_physical_pool_id, p_catalog_id, v_id, p_maintenance_owner_id,
                p_cleanup_lease_expires_at, p_maintenance_id, p_maintenance_owner_id,
                p_maintenance_fencing_epoch);
        ELSIF v_owner IS DISTINCT FROM p_maintenance_owner_id OR v_cleanup_epoch <= 0 THEN
            RAISE EXCEPTION 'maintenance fence stale';
        END IF;
        PERFORM ducklake.complete_snapshot_cleanup_under_maintenance_fence(
            v_evidence, p_cleanup_completed_at, p_physical_pool_id, p_catalog_id, v_id,
            p_maintenance_owner_id, v_cleanup_epoch, p_maintenance_id,
            p_maintenance_owner_id, p_maintenance_fencing_epoch);
    END LOOP;
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

-- The orphan UUID is derived from the exact physical identity tuple.  The
-- unique relational key remains authoritative; this deterministic value makes
-- replay independent of caller-generated UUIDs.
CREATE OR REPLACE FUNCTION ducklake.snapshot_orphan_uuid(
    p_physical_pool_id text, p_catalog_id text, p_snapshot_id bigint
) RETURNS uuid
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
SET search_path = pg_catalog, ducklake
AS $$
    SELECT md5(length(p_physical_pool_id)::text || ':' || p_physical_pool_id
             || length(p_catalog_id)::text || ':' || p_catalog_id
             || p_snapshot_id::text)::uuid
$$;

-- Begin a bounded scanner under the exact catalog-wide maintenance fence.
-- The control role receives EXECUTE only; all scan state and cursor evidence
-- remain in the immutable ledger tables below.
CREATE OR REPLACE FUNCTION ducklake.begin_snapshot_orphan_scan(
    p_scan_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_fencing_epoch bigint, p_page_size integer,
    p_grace_micros bigint, p_request_evidence jsonb
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_existing record;
BEGIN
    IF p_physical_pool_id = '' OR p_physical_pool_id <> btrim(p_physical_pool_id) OR octet_length(p_physical_pool_id) > 255
       OR p_catalog_id = '' OR p_catalog_id <> btrim(p_catalog_id) OR octet_length(p_catalog_id) > 255
       OR p_owner_id = '' OR p_owner_id <> btrim(p_owner_id) OR octet_length(p_owner_id) > 255
       OR p_fencing_epoch <= 0 OR p_page_size < 1 OR p_page_size > 256
       OR p_grace_micros < 1 OR p_grace_micros > 2592000000000
       OR jsonb_typeof(COALESCE(p_request_evidence, '{}'::jsonb)) <> 'object'
       OR octet_length(COALESCE(p_request_evidence, '{}'::jsonb)::text) > 32768 THEN
        RAISE EXCEPTION 'invalid snapshot orphan scan request';
    END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_existing FROM ducklake.snapshot_orphan_scan
     WHERE scan_id=p_scan_id FOR UPDATE;
    IF FOUND THEN
        IF v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
           OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id
           OR v_existing.page_size IS DISTINCT FROM p_page_size
           OR v_existing.grace_micros IS DISTINCT FROM p_grace_micros
           OR v_existing.request_evidence IS DISTINCT FROM COALESCE(p_request_evidence, '{}'::jsonb) THEN
            RAISE EXCEPTION 'snapshot orphan scan conflict';
        END IF;
        IF v_existing.state <> 'running' THEN RETURN; END IF;
        IF v_existing.owner_id IS DISTINCT FROM p_owner_id
           OR v_existing.fencing_epoch IS DISTINCT FROM p_fencing_epoch THEN
            IF p_fencing_epoch <= v_existing.fencing_epoch THEN
                RAISE EXCEPTION 'snapshot orphan scan owned by another fence';
            END IF;
            -- The active exact pool fence has already advanced, so a
            -- successor may take over the durable cursor without resetting
            -- page evidence or counters.
            UPDATE ducklake.snapshot_orphan_scan
               SET owner_id=p_owner_id,fencing_epoch=p_fencing_epoch,updated_at=v_now
             WHERE scan_id=p_scan_id;
        END IF;
        RETURN;
    END IF;
    INSERT INTO ducklake.snapshot_orphan_scan
      (scan_id,physical_pool_id,catalog_id,owner_id,fencing_epoch,page_size,grace_micros,cleanup_not_before,state,request_evidence,started_at,updated_at)
    VALUES
      (p_scan_id,p_physical_pool_id,p_catalog_id,p_owner_id,p_fencing_epoch,p_page_size,p_grace_micros,v_now + p_grace_micros * interval '1 microsecond','running',COALESCE(p_request_evidence, '{}'::jsonb),v_now,v_now);
END;
$$;

-- Record exactly one catalog page. The adapter supplies sorted snapshot IDs
-- and an object keyed by snapshot ID containing the catalog's bounded evidence.
-- Every candidate is rechecked against all control-plane authorities before a
-- deterministic orphan row is inserted. Replaying an existing page requires
-- byte-equivalent identities/evidence and never advances the cursor twice.
CREATE OR REPLACE FUNCTION ducklake.record_snapshot_orphan_scan_page(
    p_scan_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_fencing_epoch bigint, p_page_number integer,
    p_cursor_before bigint, p_cursor_after bigint, p_snapshot_ids bigint[],
    p_page_digest text, p_evidence jsonb, p_terminal boolean
) RETURNS TABLE(next_cursor bigint, orphan_count integer)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_scan record;
    v_page record;
    v_len integer := COALESCE(cardinality(p_snapshot_ids), 0);
    v_prev bigint := p_cursor_before;
    v_id bigint;
    v_i integer;
    v_orphans integer := 0;
    v_protected boolean;
    v_item jsonb;
    v_expected_digest text;
BEGIN
    p_snapshot_ids := COALESCE(p_snapshot_ids, '{}'::bigint[]);
    p_evidence := COALESCE(p_evidence, '{}'::jsonb);
    IF p_fencing_epoch <= 0 OR p_page_number <= 0 OR p_cursor_before < 0
       OR p_cursor_after < p_cursor_before OR v_len > 256
       OR p_page_digest IS NULL OR p_page_digest !~ '^sha256:[0-9a-f]{64}$'
       OR jsonb_typeof(p_evidence) <> 'object'
       OR octet_length(p_evidence::text) > 32768 THEN
        RAISE EXCEPTION 'invalid snapshot orphan scan page';
    END IF;
    IF v_len = 0 AND p_cursor_after <> p_cursor_before THEN
        RAISE EXCEPTION 'empty terminal snapshot orphan page must preserve cursor';
    END IF;
    v_expected_digest := 'sha256:' || pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(p_evidence::text, 'UTF8')), 'hex');
    IF p_page_digest <> v_expected_digest THEN
        RAISE EXCEPTION 'snapshot orphan scan page digest mismatch';
    END IF;
    IF v_len = 0 AND NOT COALESCE(p_terminal, false) THEN
        RAISE EXCEPTION 'empty non-terminal snapshot orphan scan page';
    END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_scan FROM ducklake.snapshot_orphan_scan s
     WHERE s.scan_id=p_scan_id FOR UPDATE;
    IF NOT FOUND OR v_scan.physical_pool_id <> p_physical_pool_id OR v_scan.catalog_id <> p_catalog_id THEN
        RAISE EXCEPTION 'snapshot orphan scan not found';
    END IF;
    IF v_scan.owner_id <> p_owner_id OR v_scan.fencing_epoch <> p_fencing_epoch THEN
        RAISE EXCEPTION 'snapshot orphan scan fence stale';
    END IF;
    SELECT * INTO v_page FROM ducklake.snapshot_orphan_scan_page
     WHERE scan_id=p_scan_id AND page_number=p_page_number FOR UPDATE;
    IF FOUND THEN
        IF v_page.physical_pool_id <> p_physical_pool_id OR v_page.catalog_id <> p_catalog_id
           OR v_page.cursor_before <> p_cursor_before OR v_page.cursor_after <> p_cursor_after
           OR v_page.snapshot_ids IS DISTINCT FROM p_snapshot_ids
           OR v_page.page_digest <> p_page_digest OR v_page.evidence IS DISTINCT FROM COALESCE(p_evidence, '{}'::jsonb) THEN
            RAISE EXCEPTION 'snapshot orphan scan page conflict';
        END IF;
        IF v_page.terminal IS DISTINCT FROM COALESCE(p_terminal, false) THEN
            RAISE EXCEPTION 'snapshot orphan scan page conflict';
        END IF;
        RETURN QUERY SELECT v_page.cursor_after, v_page.orphan_count;
        RETURN;
    END IF;
    IF v_scan.state <> 'running' THEN RAISE EXCEPTION 'snapshot orphan scan terminal'; END IF;
    IF v_scan.pages_scanned + 1 <> p_page_number OR v_scan.cursor_snapshot_id <> p_cursor_before THEN
        RAISE EXCEPTION 'snapshot orphan scan cursor mismatch';
    END IF;
    IF v_len > v_scan.page_size THEN RAISE EXCEPTION 'snapshot orphan scan page exceeds bound'; END IF;
    IF v_len > 0 AND p_cursor_after <> p_snapshot_ids[v_len] THEN
        RAISE EXCEPTION 'snapshot orphan scan cursor must equal final snapshot';
    END IF;
    FOR v_i IN 1..v_len LOOP
        v_id := p_snapshot_ids[v_i];
        IF v_id IS NULL OR v_id <= v_prev OR NOT (p_evidence ? v_id::text) THEN
            RAISE EXCEPTION 'snapshot orphan scan page is not strictly ordered or lacks evidence';
        END IF;
        v_prev := v_id;
    END LOOP;
    FOR v_i IN 1..v_len LOOP
        v_id := p_snapshot_ids[v_i];
        -- A snapshot is protected if any authoritative control row knows it.
        -- Retention rows (including terminal rows), delivery attempts,
        -- generation/seal evidence, active reader leases, maintenance
        -- children, or live durable roots all suppress orphan classification.
        -- A delivery root with no snapshot_seal_id remains un-attributable;
        -- never guess its physical identity from target/generation metadata.
        SELECT EXISTS (
            SELECT 1 FROM ducklake.snapshot_retention r
             WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id AND r.snapshot_id=v_id
            UNION ALL
            SELECT 1
              FROM delivery.delivery_retention_root root
              JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = root.snapshot_seal_id
             WHERE seal.physical_pool_id=p_physical_pool_id
               AND seal.catalog_id=p_catalog_id
               AND seal.ducklake_snapshot_id=v_id
               AND root.state IN ('live','retiring')
            UNION ALL
            SELECT 1
              FROM serving_state.reader_lease l
              JOIN delivery.delivery_generation g ON g.generation_id = l.generation_id
              JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
             WHERE seal.physical_pool_id=p_physical_pool_id
               AND seal.catalog_id=p_catalog_id
               AND seal.ducklake_snapshot_id=v_id
               AND l.released_at IS NULL
            UNION ALL
            SELECT 1
              FROM delivery.delivery_generation g
              JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
             WHERE seal.physical_pool_id=p_physical_pool_id
               AND seal.catalog_id=p_catalog_id
               AND seal.ducklake_snapshot_id=v_id
            UNION ALL
            SELECT 1 FROM delivery.delivery_build_attempt a
             WHERE a.physical_pool_id=p_physical_pool_id AND a.catalog_id=p_catalog_id AND a.snapshot_id=v_id
            UNION ALL
            SELECT 1 FROM ducklake.retention_maintenance_snapshot m
             WHERE m.physical_pool_id=p_physical_pool_id AND m.catalog_id=p_catalog_id AND m.snapshot_id=v_id
        ) INTO v_protected;
        IF NOT v_protected THEN
            v_item := p_evidence -> (v_id::text);
            IF jsonb_typeof(v_item) <> 'object' THEN RAISE EXCEPTION 'snapshot orphan evidence must be an object'; END IF;
            INSERT INTO ducklake.snapshot_orphan
              (orphan_id,physical_pool_id,catalog_id,snapshot_id,state,evidence,discovered_at,cleanup_not_before)
            VALUES
              (ducklake.snapshot_orphan_uuid(p_physical_pool_id,p_catalog_id,v_id),p_physical_pool_id,p_catalog_id,v_id,'quarantined',
               jsonb_build_object('catalog',v_item,'snapshot_id',v_id),v_now,v_scan.cleanup_not_before)
            ON CONFLICT (physical_pool_id,catalog_id,snapshot_id) DO NOTHING;
            IF FOUND THEN
                v_orphans := v_orphans + 1;
            ELSE
                SELECT evidence INTO v_item FROM ducklake.snapshot_orphan
                 WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=v_id FOR UPDATE;
                IF (v_item -> 'catalog') IS DISTINCT FROM (COALESCE(p_evidence, '{}'::jsonb) -> (v_id::text)) THEN
                    RAISE EXCEPTION 'snapshot orphan evidence conflict';
                END IF;
            END IF;
        END IF;
    END LOOP;
    INSERT INTO ducklake.snapshot_orphan_scan_page
      (scan_id,physical_pool_id,catalog_id,page_number,cursor_before,cursor_after,snapshot_ids,orphan_count,terminal,page_digest,evidence,created_at)
    VALUES
      (p_scan_id,p_physical_pool_id,p_catalog_id,p_page_number,p_cursor_before,p_cursor_after,p_snapshot_ids,v_orphans,COALESCE(p_terminal,false),p_page_digest,p_evidence,v_now);
    UPDATE ducklake.snapshot_orphan_scan
       SET cursor_snapshot_id=p_cursor_after,pages_scanned=pages_scanned+1,
           snapshots_scanned=snapshots_scanned+v_len,orphans_recorded=orphans_recorded+v_orphans,
           updated_at=v_now
     WHERE scan_id=p_scan_id;
    RETURN QUERY SELECT p_cursor_after, v_orphans;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.complete_snapshot_orphan_scan(
    p_scan_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_fencing_epoch bigint, p_completion_evidence jsonb
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_scan record;
    v_terminal boolean;
BEGIN
    IF p_fencing_epoch <= 0 OR jsonb_typeof(COALESCE(p_completion_evidence, '{}'::jsonb)) <> 'object' THEN
        RAISE EXCEPTION 'invalid snapshot orphan scan completion';
    END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_scan FROM ducklake.snapshot_orphan_scan WHERE scan_id=p_scan_id FOR UPDATE;
    IF NOT FOUND OR v_scan.physical_pool_id <> p_physical_pool_id OR v_scan.catalog_id <> p_catalog_id THEN RAISE EXCEPTION 'snapshot orphan scan not found'; END IF;
    IF v_scan.owner_id <> p_owner_id OR v_scan.fencing_epoch <> p_fencing_epoch THEN RAISE EXCEPTION 'snapshot orphan scan fence stale'; END IF;
    IF v_scan.state = 'completed' THEN
        IF v_scan.completion_evidence IS DISTINCT FROM COALESCE(p_completion_evidence, '{}'::jsonb) THEN RAISE EXCEPTION 'snapshot orphan scan completion conflict'; END IF;
        RETURN;
    END IF;
    IF v_scan.state <> 'running' THEN RAISE EXCEPTION 'snapshot orphan scan terminal'; END IF;
    SELECT p.terminal INTO v_terminal FROM ducklake.snapshot_orphan_scan_page p
     WHERE p.scan_id=p_scan_id ORDER BY p.page_number DESC LIMIT 1;
    IF NOT FOUND OR NOT v_terminal THEN RAISE EXCEPTION 'snapshot orphan scan requires a terminal page'; END IF;
    UPDATE ducklake.snapshot_orphan_scan
       SET state='completed',updated_at=v_now,completed_at=v_now,
           completion_evidence=COALESCE(p_completion_evidence, '{}'::jsonb)
     WHERE scan_id=p_scan_id;
END;
$$;

-- Prune page payloads only after a completed scan has aged past the bounded
-- policy window. The scan summary, counters, completion evidence, and a
-- server-computed digest of the removed page sequence remain as audit proof.
CREATE OR REPLACE FUNCTION ducklake.prune_snapshot_orphan_scan_pages(
    p_physical_pool_id text, p_catalog_id text, p_owner_id text,
    p_fencing_epoch bigint, p_min_age_micros bigint, p_max_scans integer
) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_cutoff timestamptz;
    v_scan record;
    v_count integer := 0;
    v_pages integer;
    v_digest text;
BEGIN
    IF p_physical_pool_id = '' OR p_catalog_id = '' OR p_owner_id = ''
       OR p_fencing_epoch <= 0 OR p_min_age_micros < 86400000000
       OR p_min_age_micros > 2592000000000 OR p_max_scans < 1 OR p_max_scans > 64 THEN
        RAISE EXCEPTION 'invalid snapshot orphan scan prune request';
    END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    v_cutoff := v_now - p_min_age_micros * interval '1 microsecond';
    FOR v_scan IN
        SELECT s.scan_id FROM ducklake.snapshot_orphan_scan s
         WHERE s.physical_pool_id=p_physical_pool_id AND s.catalog_id=p_catalog_id
           AND s.state='completed' AND s.completed_at IS NOT NULL
           AND s.completed_at <= v_cutoff AND s.pruned_at IS NULL
         ORDER BY s.completed_at,s.scan_id
         LIMIT p_max_scans
         FOR UPDATE
    LOOP
        SELECT count(*)::integer,
               COALESCE('sha256:' || pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(string_agg(p.page_digest, ',' ORDER BY p.page_number), 'UTF8')), 'hex'), 'sha256:' || pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to('', 'UTF8')), 'hex'))
          INTO v_pages,v_digest
          FROM ducklake.snapshot_orphan_scan_page p WHERE p.scan_id=v_scan.scan_id;
        PERFORM set_config('ducklake.scan_prune', 'on', true);
        DELETE FROM ducklake.snapshot_orphan_scan_page WHERE scan_id=v_scan.scan_id;
        PERFORM set_config('ducklake.scan_prune', 'off', true);
        UPDATE ducklake.snapshot_orphan_scan
           SET pruned_at=v_now,pruned_page_count=v_pages,pruned_page_digest=v_digest
         WHERE scan_id=v_scan.scan_id;
        v_count := v_count + 1;
    END LOOP;
    RETURN v_count;
END;
$$;

-- Fenced orphan cleanup capabilities. Existing direct DML remains denied to
-- the maintenance role; these functions validate the exact pool fence and
-- enforce monotonic lifecycle transitions under row locks.
CREATE OR REPLACE FUNCTION ducklake.claim_snapshot_orphan_cleanup_under_pool_fence(
    p_physical_pool_id text, p_catalog_id text, p_snapshot_id bigint,
    p_owner_id text, p_cleanup_lease_expires_at timestamptz,
    p_fence_owner_id text, p_fencing_epoch bigint
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_epoch bigint; v_owner text; v_expiry timestamptz; v_state text;
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_fence_owner_id AND f.fencing_epoch=p_fencing_epoch AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT state,cleanup_owner_id,cleanup_fencing_epoch,cleanup_lease_expires_at INTO v_state,v_owner,v_epoch,v_expiry
      FROM ducklake.snapshot_orphan WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'snapshot orphan not found'; END IF;
    IF v_state <> 'quarantined' THEN RAISE EXCEPTION 'snapshot orphan is terminal'; END IF;
    IF v_owner IS NOT NULL AND v_expiry > v_now THEN
        IF v_owner = p_owner_id THEN RETURN v_epoch; END IF;
        RAISE EXCEPTION 'snapshot orphan cleanup busy';
    END IF;
    IF p_cleanup_lease_expires_at IS NULL OR p_cleanup_lease_expires_at <= v_now OR p_cleanup_lease_expires_at > v_now + interval '24 hours' THEN RAISE EXCEPTION 'invalid orphan cleanup lease'; END IF;
    IF (SELECT cleanup_not_before FROM ducklake.snapshot_orphan
         WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id) > v_now THEN
        RAISE EXCEPTION 'snapshot orphan cleanup grace is active';
    END IF;
    -- Recheck every protected authority while the exact fence and orphan row
    -- remain locked. The admission path takes the same pool-fence row first,
    -- so no new running writer can slip in after this check. Roots lacking a
    -- snapshot seal are deliberately not assigned a physical identity.
    IF EXISTS (SELECT 1 FROM ducklake.snapshot_retention r
               WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id AND r.snapshot_id=p_snapshot_id)
       OR EXISTS (
              SELECT 1
                FROM delivery.delivery_retention_root root
                JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = root.snapshot_seal_id
               WHERE seal.physical_pool_id=p_physical_pool_id
                 AND seal.catalog_id=p_catalog_id
                 AND seal.ducklake_snapshot_id=p_snapshot_id
                 AND root.state IN ('live','retiring'))
       OR EXISTS (
              SELECT 1
                FROM serving_state.reader_lease l
                JOIN delivery.delivery_generation g ON g.generation_id = l.generation_id
                JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
               WHERE seal.physical_pool_id=p_physical_pool_id
                 AND seal.catalog_id=p_catalog_id
                 AND seal.ducklake_snapshot_id=p_snapshot_id
                 AND l.released_at IS NULL)
       OR EXISTS (
              SELECT 1
                FROM delivery.delivery_generation g
                JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
               WHERE seal.physical_pool_id=p_physical_pool_id
                 AND seal.catalog_id=p_catalog_id
                 AND seal.ducklake_snapshot_id=p_snapshot_id)
       OR EXISTS (SELECT 1 FROM delivery.delivery_build_attempt a
                  WHERE a.physical_pool_id=p_physical_pool_id AND a.catalog_id=p_catalog_id AND a.snapshot_id=p_snapshot_id)
       OR EXISTS (SELECT 1 FROM ducklake.retention_maintenance_snapshot m
                  WHERE m.physical_pool_id=p_physical_pool_id AND m.catalog_id=p_catalog_id AND m.snapshot_id=p_snapshot_id) THEN
        RAISE EXCEPTION 'snapshot orphan became protected';
    END IF;
    UPDATE ducklake.snapshot_orphan
       SET cleanup_owner_id=p_owner_id,cleanup_fencing_epoch=v_epoch+1,cleanup_lease_expires_at=p_cleanup_lease_expires_at
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    RETURN v_epoch+1;
END;
$$;

CREATE OR REPLACE FUNCTION ducklake.complete_snapshot_orphan_cleanup_under_pool_fence(
    p_physical_pool_id text, p_catalog_id text, p_snapshot_id bigint,
    p_owner_id text, p_fencing_epoch bigint, p_evidence jsonb,
    p_fence_owner_id text, p_pool_fencing_epoch bigint
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_state text; v_owner text; v_epoch bigint; v_expiry timestamptz; v_existing jsonb;
BEGIN
    IF jsonb_typeof(COALESCE(p_evidence, '{}'::jsonb)) <> 'object' OR p_evidence = '{}'::jsonb THEN RAISE EXCEPTION 'cleanup evidence is required'; END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_fence_owner_id AND f.fencing_epoch=p_pool_fencing_epoch AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT state,cleanup_owner_id,cleanup_fencing_epoch,cleanup_lease_expires_at,evidence INTO v_state,v_owner,v_epoch,v_expiry,v_existing
      FROM ducklake.snapshot_orphan WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'snapshot orphan not found'; END IF;
    IF v_state='cleanup-complete' THEN
        IF (v_existing -> 'cleanup') IS DISTINCT FROM p_evidence THEN
            RAISE EXCEPTION 'snapshot orphan cleanup evidence conflict';
        END IF;
        RETURN;
    END IF;
    IF v_state <> 'quarantined' OR v_owner IS DISTINCT FROM p_owner_id OR v_epoch <> p_fencing_epoch OR v_expiry IS NULL OR v_expiry <= v_now THEN RAISE EXCEPTION 'snapshot orphan cleanup fence stale'; END IF;
    -- Preserve the immutable discovery/catalog proof and append cleanup
    -- evidence under a bounded namespaced object. Replays compare only this
    -- subobject, never replacing the original observation.
    UPDATE ducklake.snapshot_orphan
       SET state='cleanup-complete',
           evidence=jsonb_set(v_existing, '{cleanup}', p_evidence, true),
           resolved_at=v_now
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
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
        EXECUTE 'GRANT SELECT, INSERT ON TABLE ducklake.catalog_identity TO leapview_control_runtime';
        EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE ducklake.catalog_identity FROM leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.snapshot_retention TO leapview_control_runtime';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE ducklake.snapshot_retention FROM leapview_control_runtime';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) TO leapview_control_runtime';
        -- Runtime admission does not discover or reconcile physical orphans.
        -- Keep the row visible for bounded diagnostics, but remove every
        -- direct lifecycle mutation capability (including grants left by an
        -- earlier schema revision).
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES ON TABLE ducklake.snapshot_orphan FROM leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.snapshot_orphan TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.snapshot_orphan_scan, ducklake.snapshot_orphan_scan_page TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT ON TABLE ducklake.marker_quarantine TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT ON TABLE ducklake.source_observation_capture TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.catalog_runtime_compatibility, ducklake.migration_fence, ducklake.pool_maintenance_fence, ducklake.retention_maintenance, ducklake.retention_maintenance_snapshot, ducklake.catalog_migration, ducklake.snapshot_requalification TO leapview_control_runtime';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.assert_attempt_admission_fence(text,text) TO leapview_control_runtime';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA ducklake TO leapview_control_readonly';
        EXECUTE 'REVOKE EXECUTE ON FUNCTION ducklake.assert_attempt_admission_fence(text,text) FROM leapview_control_readonly';
        EXECUTE 'REVOKE ALL ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) FROM leapview_control_readonly';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES ON TABLE ducklake.snapshot_orphan FROM leapview_control_readonly';
        EXECUTE 'GRANT SELECT ON TABLE '
            || 'ducklake.catalog_identity, ducklake.snapshot_retention, '
            || 'ducklake.snapshot_orphan, ducklake.marker_quarantine, '
            || 'ducklake.snapshot_orphan_scan, ducklake.snapshot_orphan_scan_page, '
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
        EXECUTE 'REVOKE EXECUTE ON FUNCTION ducklake.assert_attempt_admission_fence(text,text) FROM leapview_control_maintenance';
        EXECUTE 'REVOKE ALL ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) FROM leapview_control_maintenance';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES ON TABLE ducklake.snapshot_orphan FROM leapview_control_maintenance';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES ON TABLE ducklake.snapshot_orphan_scan, ducklake.snapshot_orphan_scan_page FROM leapview_control_maintenance';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES ON TABLE ducklake.retention_maintenance, ducklake.retention_maintenance_snapshot, ducklake.snapshot_retention FROM leapview_control_maintenance';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.pool_maintenance_fence TO leapview_control_maintenance';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.retention_maintenance, ducklake.retention_maintenance_snapshot, ducklake.snapshot_retention TO leapview_control_maintenance';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.catalog_identity, ducklake.snapshot_orphan, ducklake.snapshot_orphan_scan, ducklake.snapshot_orphan_scan_page TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.acquire_pool_maintenance_fence(text,text,text,timestamptz) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.release_pool_maintenance_fence(text,text,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.renew_pool_maintenance_fence(text,text,text,bigint,timestamptz) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.begin_retention_maintenance(uuid,text,text,text,bigint,boolean,bigint,text,jsonb) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.update_retention_maintenance(uuid,text,text,text,bigint,text,text,boolean,bigint,text,jsonb,timestamptz) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.insert_retention_maintenance_snapshot(uuid,text,text,bigint,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.insert_retention_maintenance_snapshots(uuid,text,text,bigint[],text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.claim_retention_snapshots(uuid,text,text,text,bigint,integer) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.expire_snapshot_under_maintenance_fence(timestamptz,jsonb,text,text,bigint,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.claim_snapshot_cleanup_under_maintenance_fence(text,text,bigint,text,timestamptz,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.quarantine_snapshot_under_maintenance_fence(jsonb,timestamptz,text,text,bigint,text,bigint,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.complete_snapshot_cleanup_under_maintenance_fence(jsonb,timestamptz,text,text,bigint,text,bigint,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.expire_snapshots_under_maintenance_fence(bigint[],timestamptz,jsonb,text,text,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.reconcile_retention_maintenance_snapshots(jsonb,uuid,text,text,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.quarantine_snapshots_under_maintenance_fence(bigint[],jsonb,timestamptz,timestamptz,text,text,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.complete_snapshots_cleanup_under_maintenance_fence(bigint[],jsonb,timestamptz,timestamptz,text,text,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.begin_snapshot_orphan_scan(uuid,text,text,text,bigint,integer,bigint,jsonb) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.record_snapshot_orphan_scan_page(uuid,text,text,text,bigint,integer,bigint,bigint,bigint[],text,jsonb,boolean) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.complete_snapshot_orphan_scan(uuid,text,text,text,bigint,jsonb) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.prune_snapshot_orphan_scan_pages(text,text,text,bigint,bigint,integer) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.claim_snapshot_orphan_cleanup_under_pool_fence(text,text,bigint,text,timestamptz,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.complete_snapshot_orphan_cleanup_under_pool_fence(text,text,bigint,text,bigint,jsonb,text,bigint) TO leapview_control_maintenance';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        EXECUTE 'REVOKE ALL ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) FROM leapview_control_backup';
    END IF;
END
$$;
