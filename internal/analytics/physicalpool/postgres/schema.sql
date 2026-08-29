-- Clean-slate PostgreSQL physical-pool authority (ADR-0016).
--
-- DuckLake remains authoritative for table and object membership.  This
-- capability stores only the stable, non-secret namespace identity and
-- append-only conformance evidence used to admit a runtime tuple.
CREATE SCHEMA IF NOT EXISTS physical_pool;

CREATE TABLE IF NOT EXISTS physical_pool.physical_pools (
    id                    text PRIMARY KEY,
    identity_digest       text NOT NULL UNIQUE,
    storage_location      text NOT NULL,
    storage_namespace     text NOT NULL,
    storage_implementation text NOT NULL,
    object_naming_contract text NOT NULL,
    region                text NOT NULL DEFAULT '',
    tenant                text NOT NULL DEFAULT '',
    isolation_boundary    text NOT NULL,
    encryption_key_ref    text NOT NULL DEFAULT '',
    credential_reference  text NOT NULL DEFAULT '',
    retention_authority   text NOT NULL,
    orphan_grace_period_seconds bigint NOT NULL CHECK (orphan_grace_period_seconds >= 0),
    reader_grace_period_seconds bigint NOT NULL CHECK (reader_grace_period_seconds >= 0),
    build_grace_period_seconds bigint NOT NULL CHECK (build_grace_period_seconds >= 0),
    retention_policy      jsonb NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (id = identity_digest AND id ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (storage_location = btrim(storage_location) AND octet_length(storage_location) BETWEEN 1 AND 2048),
    CHECK (storage_namespace = btrim(storage_namespace) AND octet_length(storage_namespace) BETWEEN 1 AND 512),
    CHECK (storage_implementation = btrim(storage_implementation) AND octet_length(storage_implementation) BETWEEN 1 AND 128),
    CHECK (object_naming_contract = btrim(object_naming_contract) AND octet_length(object_naming_contract) BETWEEN 1 AND 128),
    CHECK (region = btrim(region) AND octet_length(region) <= 255),
    CHECK (tenant = btrim(tenant) AND octet_length(tenant) <= 255),
    CHECK (isolation_boundary = btrim(isolation_boundary) AND octet_length(isolation_boundary) BETWEEN 1 AND 255),
    CHECK (encryption_key_ref = btrim(encryption_key_ref) AND octet_length(encryption_key_ref) <= 512),
    CHECK (credential_reference = btrim(credential_reference) AND octet_length(credential_reference) <= 512),
    CHECK (retention_authority = btrim(retention_authority) AND octet_length(retention_authority) BETWEEN 1 AND 255),
    CHECK (jsonb_typeof(retention_policy) = 'object' AND octet_length(retention_policy::text) BETWEEN 2 AND 4096),
    CHECK (retention_policy = jsonb_build_object('orphan_grace_period_seconds', orphan_grace_period_seconds, 'reader_grace_period_seconds', reader_grace_period_seconds, 'build_grace_period_seconds', build_grace_period_seconds)),
    CHECK (retention_policy - 'orphan_grace_period_seconds' - 'reader_grace_period_seconds' - 'build_grace_period_seconds' = '{}'::jsonb)
);

-- A namespace is deletable only once.  Runtime, extension, or catalog-format
-- upgrades retain this key and append a new admission row instead.
CREATE UNIQUE INDEX IF NOT EXISTS physical_pool_namespace_idx
    ON physical_pool.physical_pools (storage_implementation, storage_location, storage_namespace);

CREATE TABLE IF NOT EXISTS physical_pool.physical_pool_admissions (
    pool_id              text NOT NULL REFERENCES physical_pool.physical_pools(id) ON DELETE CASCADE,
    compatibility_json   jsonb NOT NULL,
    duckdb_runtime       text NOT NULL,
    ducklake_extension   text NOT NULL,
    catalog_format       text NOT NULL,
    storage_implementation text NOT NULL,
    object_naming_contract text NOT NULL,
    evidence_json        jsonb NOT NULL,
    evidence_digest      text NOT NULL,
    compatibility_digest text NOT NULL,
    conformance_version  text NOT NULL,
    admitted_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (pool_id, evidence_digest),
    CHECK (evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(compatibility_json) = 'object' AND octet_length(compatibility_json::text) BETWEEN 2 AND 4096),
    CHECK (jsonb_typeof(evidence_json) = 'object' AND octet_length(evidence_json::text) BETWEEN 2 AND 32768),
    CHECK (duckdb_runtime = btrim(duckdb_runtime) AND octet_length(duckdb_runtime) BETWEEN 1 AND 255),
    CHECK (ducklake_extension = btrim(ducklake_extension) AND octet_length(ducklake_extension) BETWEEN 1 AND 255),
    CHECK (catalog_format = btrim(catalog_format) AND octet_length(catalog_format) BETWEEN 1 AND 255),
    CHECK (storage_implementation = btrim(storage_implementation) AND octet_length(storage_implementation) BETWEEN 1 AND 128),
    CHECK (object_naming_contract = btrim(object_naming_contract) AND octet_length(object_naming_contract) BETWEEN 1 AND 128),
    CHECK (conformance_version = btrim(conformance_version) AND octet_length(conformance_version) BETWEEN 1 AND 255),
    CHECK (compatibility_json->>'duckdb_runtime' = duckdb_runtime
        AND compatibility_json->>'ducklake_extension' = ducklake_extension
        AND compatibility_json->>'catalog_format' = catalog_format
        AND compatibility_json->>'storage_implementation' = storage_implementation
        AND compatibility_json->>'object_naming_contract' = object_naming_contract),
    CHECK (evidence_json->'compatibility' = compatibility_json
        AND evidence_json->>'digest' = evidence_digest
        AND evidence_json->>'conformance_version' = conformance_version),
    UNIQUE (pool_id, compatibility_digest, evidence_digest)
);

CREATE INDEX IF NOT EXISTS physical_pool_admissions_compatibility_idx
    ON physical_pool.physical_pool_admissions (pool_id, compatibility_digest, admitted_at DESC);

-- This ledger records what the external namespace marker admitted. It is an
-- audit/restart aid only: a database row cannot prove that an object-store
-- marker was actually created, so callers must still invoke the marker's
-- conditional Acquire/Verify operation before deletion authority is granted.
CREATE TABLE IF NOT EXISTS physical_pool.namespace_ownership_claims (
    pool_id              text NOT NULL REFERENCES physical_pool.physical_pools(id) ON DELETE CASCADE,
    compatibility_digest text NOT NULL CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    evidence_digest      text NOT NULL CHECK (evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
    owner_id             text NOT NULL CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    claimed_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (pool_id, compatibility_digest, evidence_digest)
        REFERENCES physical_pool.physical_pool_admissions(pool_id, compatibility_digest, evidence_digest)
        ON DELETE RESTRICT,
    PRIMARY KEY (pool_id, evidence_digest)
);

CREATE INDEX IF NOT EXISTS physical_pool_ownership_owner_idx
    ON physical_pool.namespace_ownership_claims (pool_id, owner_id, claimed_at DESC);

CREATE TABLE IF NOT EXISTS physical_pool.namespace_deletion_leases (
    singleton       boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    owner_id        text NOT NULL,
    token           uuid NOT NULL,
    expires_at      timestamptz NOT NULL,
    acquired_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK (expires_at > acquired_at)
);

CREATE OR REPLACE FUNCTION physical_pool.reject_immutable_change()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, physical_pool
AS $$
BEGIN
    RAISE EXCEPTION 'physical-pool identity and admissions are immutable';
END;
$$;

DROP TRIGGER IF EXISTS physical_pools_immutable ON physical_pool.physical_pools;
CREATE TRIGGER physical_pools_immutable
    BEFORE UPDATE OR DELETE ON physical_pool.physical_pools
    FOR EACH ROW EXECUTE FUNCTION physical_pool.reject_immutable_change();

DROP TRIGGER IF EXISTS physical_pool_admissions_immutable ON physical_pool.physical_pool_admissions;
CREATE TRIGGER physical_pool_admissions_immutable
    BEFORE UPDATE OR DELETE ON physical_pool.physical_pool_admissions
    FOR EACH ROW EXECUTE FUNCTION physical_pool.reject_immutable_change();

DROP TRIGGER IF EXISTS physical_pool_ownership_immutable ON physical_pool.namespace_ownership_claims;
CREATE TRIGGER physical_pool_ownership_immutable
    BEFORE UPDATE OR DELETE ON physical_pool.namespace_ownership_claims
    FOR EACH ROW EXECUTE FUNCTION physical_pool.reject_immutable_change();

-- Capability-owned ACLs.  Runtime can reconstruct and verify an admission but
-- cannot forge, replace, or delete one.  Provisioned owner/migrator roles own
-- the objects and retain migration authority; all other roles are explicit.
REVOKE ALL ON SCHEMA physical_pool FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA physical_pool FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA physical_pool FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_migrator;
        GRANT SELECT, INSERT ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims TO leapview_control_migrator;
        GRANT SELECT, INSERT, UPDATE, DELETE ON physical_pool.namespace_deletion_leases TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_runtime;
        GRANT SELECT ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims, physical_pool.namespace_deletion_leases TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims FROM leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON physical_pool.namespace_deletion_leases FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_maintenance;
        GRANT SELECT ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims TO leapview_control_maintenance;
        GRANT SELECT, INSERT, UPDATE, DELETE ON physical_pool.namespace_deletion_leases TO leapview_control_maintenance;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_readonly;
        GRANT SELECT ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims, physical_pool.namespace_deletion_leases TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_backup;
        GRANT SELECT ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims, physical_pool.namespace_deletion_leases TO leapview_control_backup;
    END IF;
END
$$;
