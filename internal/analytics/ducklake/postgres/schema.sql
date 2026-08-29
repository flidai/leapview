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
    catalog_id             text NOT NULL,
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
    CHECK (state <> 'committed' OR (snapshot_id IS NOT NULL AND commit_marker IS NOT NULL)),
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
       OR NEW.lease_expires_at <> OLD.lease_expires_at
       OR NEW.session_identity <> OLD.session_identity THEN
        RAISE EXCEPTION 'DuckLake attempt identity is immutable';
    END IF;
    IF OLD.state <> 'running' THEN
        IF NEW.state <> OLD.state
           OR NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id
           OR NEW.commit_marker IS DISTINCT FROM OLD.commit_marker
           OR NEW.termination_evidence IS DISTINCT FROM OLD.termination_evidence
           OR NEW.terminal_at IS DISTINCT FROM OLD.terminal_at
           OR NEW.updated_at IS DISTINCT FROM OLD.updated_at THEN
            RAISE EXCEPTION 'DuckLake terminal attempt evidence is immutable';
        END IF;
    ELSIF NEW.state = 'running'
          AND (NEW.snapshot_id IS NOT NULL
               OR NEW.commit_marker IS NOT NULL
               OR NEW.termination_evidence IS NOT NULL
               OR NEW.terminal_at IS NOT NULL) THEN
            RAISE EXCEPTION 'DuckLake running attempt cannot carry terminal evidence';
    END IF;
    IF NEW.state = 'committed' THEN
        IF NEW.commit_marker->>'attempt_id' IS DISTINCT FROM NEW.attempt_id::text
           OR NEW.commit_marker->>'request_digest' IS DISTINCT FROM NEW.request_digest
           OR NEW.commit_marker->>'plan_digest' IS DISTINCT FROM NEW.plan_digest
           OR NEW.commit_marker->>'physical_pool_id' IS DISTINCT FROM NEW.physical_pool_id
           OR NEW.commit_marker->>'lease_epoch' IS DISTINCT FROM NEW.fencing_epoch::text THEN
            RAISE EXCEPTION 'DuckLake commit marker does not match attempt identity';
        END IF;
    ELSIF NEW.commit_marker IS NOT NULL THEN
        RAISE EXCEPTION 'DuckLake non-committed attempt cannot carry a commit marker';
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
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA ducklake TO leapview_control_readonly';
        EXECUTE 'GRANT SELECT ON TABLE '
            || 'ducklake.catalog_identity, ducklake.attempt_evidence, '
            || 'ducklake.generation_binding, ducklake.snapshot_retention, '
            || 'ducklake.snapshot_root, ducklake.snapshot_lease, '
            || 'ducklake.snapshot_orphan, ducklake.snapshot_reader_drain '
            || 'TO leapview_control_readonly';
    END IF;
END
$$;
