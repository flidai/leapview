-- RecoverySet v3 persistence (FAI-520).
--
-- This is an additive, owner-owned storage contract.  It stores the exact
-- canonical evidence bytes that were associated by the writer, but has no
-- lifecycle, publication, restore or admission operation.  The migration
-- with the same revision contains this SQL because Goose migrations are
-- immutable and cannot include a filesystem asset at runtime.

CREATE SCHEMA IF NOT EXISTS recovery;

CREATE OR REPLACE FUNCTION recovery.successor_raw_sha256(payload bytea)
RETURNS text LANGUAGE sql IMMUTABLE STRICT
SET search_path = pg_catalog, recovery, managed_data
AS $$
    SELECT encode(managed_data.digest(payload, 'sha256'), 'hex')
$$;

CREATE OR REPLACE FUNCTION recovery.successor_domain_sha256(domain text, payload bytea)
RETURNS text LANGUAGE sql IMMUTABLE STRICT
SET search_path = pg_catalog, recovery, managed_data
AS $$
    SELECT 'sha256:' || recovery.successor_raw_sha256(convert_to(domain, 'UTF8') || payload)
$$;

CREATE TABLE IF NOT EXISTS recovery.set_identity_registry (
    set_id       uuid PRIMARY KEY,
    wire_version smallint NOT NULL CHECK (wire_version IN (1, 3)),
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp()
);

DO $$
BEGIN
    IF to_regclass('recovery.recovery_set') IS NOT NULL THEN
        INSERT INTO recovery.set_identity_registry(set_id, wire_version)
        SELECT set_id, 1 FROM recovery.recovery_set
        ON CONFLICT (set_id) DO NOTHING;
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS recovery.successor_evidence_v2 (
    payload_family text NOT NULL CHECK (payload_family IN ('manifest', 'anchor', 'profile', 'core', 'receipt', 'authority')),
    payload_version smallint NOT NULL CHECK (payload_version = 2),
    payload_digest text NOT NULL CHECK (payload_digest ~ '^sha256:[0-9a-f]{64}$'),
    canonical_bytes bytea NOT NULL CHECK (octet_length(canonical_bytes) BETWEEN 2 AND 8388608),
    raw_sha256 text GENERATED ALWAYS AS (recovery.successor_raw_sha256(canonical_bytes)) STORED,
    byte_length bigint GENERATED ALWAYS AS (octet_length(canonical_bytes)) STORED,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (payload_family, payload_version, payload_digest),
    CHECK (raw_sha256 ~ '^[0-9a-f]{64}$'),
    CHECK (
        payload_digest = recovery.successor_domain_sha256(
            CASE payload_family
                WHEN 'manifest' THEN 'leapview/managed-observations/v2'
                WHEN 'anchor' THEN 'leapview/recovery-source-anchor/v2'
                WHEN 'profile' THEN 'leapview/managed-provider-profiles/v2'
                WHEN 'core' THEN 'leapview/managed-capture-core/v2'
                WHEN 'receipt' THEN 'leapview/managed-capture-receipt/v2'
                WHEN 'authority' THEN 'leapview/authority-registry/v2'
            END || chr(10), canonical_bytes
        )
    )
);

CREATE TABLE IF NOT EXISTS recovery.successor_evidence_locator_v2 (
    payload_family text NOT NULL,
    payload_version smallint NOT NULL CHECK (payload_version = 2),
    payload_digest text NOT NULL,
    backend text NOT NULL DEFAULT 's3' CHECK (backend = 's3'),
    storage_profile_id text NOT NULL,
    storage_profile_revision bigint NOT NULL CHECK (storage_profile_revision > 0),
    account_identity text NOT NULL,
    endpoint text NOT NULL,
    region text NOT NULL,
    bucket text NOT NULL,
    namespace text NOT NULL,
    object_key text NOT NULL,
    version_id text NOT NULL,
    byte_length bigint NOT NULL CHECK (byte_length >= 2 AND byte_length <= 8388608),
    raw_sha256 text NOT NULL CHECK (raw_sha256 ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (payload_family, payload_version, payload_digest),
    FOREIGN KEY (payload_family, payload_version, payload_digest)
        REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest)
        ON DELETE RESTRICT,
    CHECK (payload_family IN ('manifest', 'anchor', 'profile', 'core', 'receipt', 'authority')),
    CHECK (storage_profile_id = btrim(storage_profile_id) AND storage_profile_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
    CHECK (account_identity = btrim(account_identity) AND octet_length(account_identity) BETWEEN 1 AND 4096),
    CHECK (endpoint = btrim(endpoint) AND octet_length(endpoint) BETWEEN 1 AND 2048),
    CHECK (region = btrim(region) AND octet_length(region) BETWEEN 1 AND 4096),
    CHECK (bucket = btrim(bucket) AND octet_length(bucket) BETWEEN 3 AND 63),
    CHECK (namespace = btrim(namespace) AND octet_length(namespace) BETWEEN 1 AND 4096),
    CHECK (object_key = btrim(object_key) AND octet_length(object_key) BETWEEN 1 AND 1024),
    CHECK (version_id = btrim(version_id) AND octet_length(version_id) BETWEEN 1 AND 4096)
);

CREATE TABLE IF NOT EXISTS recovery.successor_manifest_binding (
    manifest_digest text PRIMARY KEY CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    manifest_family text GENERATED ALWAYS AS ('manifest') STORED,
    manifest_version smallint GENERATED ALWAYS AS (2) STORED,
    set_id uuid NOT NULL REFERENCES recovery.set_identity_registry(set_id) ON DELETE RESTRICT,
    anchor_digest text NOT NULL CHECK (anchor_digest ~ '^sha256:[0-9a-f]{64}$'),
    anchor_family text GENERATED ALWAYS AS ('anchor') STORED,
    anchor_version smallint GENERATED ALWAYS AS (2) STORED,
    profile_digest text NOT NULL CHECK (profile_digest ~ '^sha256:[0-9a-f]{64}$'),
    profile_family text GENERATED ALWAYS AS ('profile') STORED,
    profile_version smallint GENERATED ALWAYS AS (2) STORED,
	capture_core_digest text,
	capture_core_required boolean NOT NULL DEFAULT false,
	core_family text GENERATED ALWAYS AS ('core') STORED,
	core_version smallint GENERATED ALWAYS AS (2) STORED,
    receipt_digest text NOT NULL CHECK (receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    receipt_family text GENERATED ALWAYS AS ('receipt') STORED,
    receipt_version smallint GENERATED ALWAYS AS (2) STORED,
    authority_digest text NOT NULL CHECK (authority_digest ~ '^sha256:[0-9a-f]{64}$'),
    authority_family text GENERATED ALWAYS AS ('authority') STORED,
    authority_version smallint GENERATED ALWAYS AS (2) STORED,
    canonical_set bytea NOT NULL CHECK (octet_length(canonical_set) BETWEEN 2 AND 1048576),
    canonical_set_sha256 text GENERATED ALWAYS AS (recovery.successor_raw_sha256(canonical_set)) STORED,
    set_locator bytea NOT NULL CHECK (octet_length(set_locator) BETWEEN 2 AND 16384),
    verification_metadata bytea NOT NULL CHECK (octet_length(verification_metadata) BETWEEN 2 AND 65536),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (manifest_digest, set_id),
    UNIQUE (set_id),
    FOREIGN KEY (manifest_family, manifest_version, manifest_digest)
        REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest) ON DELETE RESTRICT,
    FOREIGN KEY (anchor_family, anchor_version, anchor_digest)
        REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest) ON DELETE RESTRICT,
    FOREIGN KEY (profile_family, profile_version, profile_digest)
        REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest) ON DELETE RESTRICT,
	CHECK ((capture_core_required AND capture_core_digest IS NOT NULL AND capture_core_digest ~ '^sha256:[0-9a-f]{64}$') OR
	       (NOT capture_core_required AND capture_core_digest IS NULL)),
	FOREIGN KEY (core_family, core_version, capture_core_digest)
		REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest) ON DELETE RESTRICT,
    FOREIGN KEY (receipt_family, receipt_version, receipt_digest)
        REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest) ON DELETE RESTRICT,
    FOREIGN KEY (authority_family, authority_version, authority_digest)
        REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS recovery.successor_trust_generation (
    singleton boolean PRIMARY KEY CHECK (singleton),
    incarnation_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    policy_digest text NOT NULL CHECK (policy_digest ~ '^sha256:[0-9a-f]{64}$'),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);


CREATE TABLE IF NOT EXISTS recovery.recovery_set_v3 (
    set_id uuid PRIMARY KEY,
    schema_version smallint NOT NULL DEFAULT 3 CHECK (schema_version = 3),
    manifest_family text NOT NULL DEFAULT 'manifest' CHECK (manifest_family = 'manifest'),
    manifest_version smallint NOT NULL DEFAULT 2 CHECK (manifest_version = 2),
    manifest_digest text NOT NULL,
    anchor_family text NOT NULL DEFAULT 'anchor' CHECK (anchor_family = 'anchor'),
    anchor_version smallint NOT NULL DEFAULT 2 CHECK (anchor_version = 2),
    anchor_digest text NOT NULL,
    profile_family text NOT NULL DEFAULT 'profile' CHECK (profile_family = 'profile'),
    profile_version smallint NOT NULL DEFAULT 2 CHECK (profile_version = 2),
    profile_digest text NOT NULL,
    receipt_family text NOT NULL DEFAULT 'receipt' CHECK (receipt_family = 'receipt'),
    receipt_version smallint NOT NULL DEFAULT 2 CHECK (receipt_version = 2),
	receipt_digest text NOT NULL,
	receipt_core_digest text NOT NULL CHECK (receipt_core_digest ~ '^sha256:[0-9a-f]{64}$'),
	capture_core_digest text,
	capture_core_required boolean NOT NULL DEFAULT false,
	core_family text GENERATED ALWAYS AS ('core') STORED,
	core_version smallint GENERATED ALWAYS AS (2) STORED,
    authority_family text NOT NULL DEFAULT 'authority' CHECK (authority_family = 'authority'),
    authority_version smallint NOT NULL DEFAULT 2 CHECK (authority_version = 2),
    authority_digest text NOT NULL,
    frontier_projection bytea NOT NULL CHECK (octet_length(frontier_projection) BETWEEN 2 AND 1048576),
    frontier_digest text NOT NULL CHECK (frontier_digest = recovery.successor_domain_sha256('leapview/recovery-frontier/v3' || chr(10), frontier_projection)),
    canonical_bytes bytea NOT NULL CHECK (octet_length(canonical_bytes) BETWEEN 2 AND 1048576),
    canonical_sha256 text GENERATED ALWAYS AS (recovery.successor_raw_sha256(canonical_bytes)) STORED,
    status text NOT NULL DEFAULT 'prepared' CHECK (status = 'prepared'),
    created_by text NOT NULL CHECK (created_by = btrim(created_by) AND octet_length(created_by) BETWEEN 1 AND 4096),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (set_id) REFERENCES recovery.set_identity_registry(set_id) ON DELETE RESTRICT,
    FOREIGN KEY (manifest_family, manifest_version, manifest_digest)
        REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest)
        ON DELETE RESTRICT,
    FOREIGN KEY (manifest_digest, set_id)
        REFERENCES recovery.successor_manifest_binding(manifest_digest, set_id) ON DELETE RESTRICT,
    FOREIGN KEY (anchor_family, anchor_version, anchor_digest)
        REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest)
        ON DELETE RESTRICT,
    FOREIGN KEY (profile_family, profile_version, profile_digest)
        REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest)
        ON DELETE RESTRICT,
    FOREIGN KEY (receipt_family, receipt_version, receipt_digest)
        REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest)
        ON DELETE RESTRICT,
	CHECK ((capture_core_required AND capture_core_digest IS NOT NULL AND capture_core_digest = receipt_core_digest) OR
	       (NOT capture_core_required AND capture_core_digest IS NULL)),
	FOREIGN KEY (core_family, core_version, capture_core_digest)
		REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest)
        ON DELETE RESTRICT,
    FOREIGN KEY (authority_family, authority_version, authority_digest)
        REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest)
        ON DELETE RESTRICT,
    CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (anchor_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (profile_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (authority_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (frontier_digest = recovery.successor_domain_sha256('leapview/recovery-frontier/v3' || chr(10), frontier_projection)),
    CHECK (canonical_sha256 ~ '^[0-9a-f]{64}$')
);

CREATE TABLE IF NOT EXISTS recovery.recovery_set_v3_root (
    set_id uuid NOT NULL REFERENCES recovery.recovery_set_v3(set_id) ON DELETE RESTRICT,
    root_kind text NOT NULL CHECK (root_kind IN ('ducklake', 'serving-artifact')),
    root_uri text NOT NULL CHECK (root_uri = btrim(root_uri) AND octet_length(root_uri) BETWEEN 1 AND 8192),
    version_id text NOT NULL CHECK (version_id = btrim(version_id) AND octet_length(version_id) BETWEEN 1 AND 4096),
    root_digest text NOT NULL CHECK (root_digest ~ '^sha256:[0-9a-f]{64}$'),
    provider_recovery_frontier text NOT NULL CHECK (provider_recovery_frontier = btrim(provider_recovery_frontier) AND octet_length(provider_recovery_frontier) <= 4096 AND (root_uri !~* '^(s3|gs|az)://' OR provider_recovery_frontier <> '')),
    canonical_bytes bytea NOT NULL CHECK (octet_length(canonical_bytes) BETWEEN 2 AND 1048576),
    canonical_sha256 text GENERATED ALWAYS AS (recovery.successor_raw_sha256(canonical_bytes)) STORED,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (set_id, root_kind),
    CHECK (canonical_sha256 ~ '^[0-9a-f]{64}$')
);

CREATE OR REPLACE FUNCTION recovery.reject_successor_mutation()
RETURNS trigger LANGUAGE plpgsql
SET search_path = pg_catalog, recovery
AS $$
BEGIN
    RAISE EXCEPTION 'successor recovery evidence is immutable';
END
$$;

CREATE OR REPLACE FUNCTION recovery.lock_successor_generation()
RETURNS TABLE(incarnation_id uuid, revision bigint, policy_digest text)
LANGUAGE sql SECURITY DEFINER
SET search_path = pg_catalog, recovery
AS $$
    SELECT g.incarnation_id, g.revision, g.policy_digest
      FROM recovery.successor_trust_generation AS g
     WHERE g.singleton = true
     FOR UPDATE
$$;

CREATE OR REPLACE FUNCTION recovery.guard_successor_set_identity()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, recovery
AS $$
DECLARE existing_version smallint;
BEGIN
    INSERT INTO recovery.set_identity_registry(set_id, wire_version)
    VALUES (NEW.set_id, COALESCE(NULLIF(TG_ARGV[0], '')::smallint, 3))
    ON CONFLICT (set_id) DO NOTHING;
    SELECT wire_version INTO existing_version
      FROM recovery.set_identity_registry WHERE set_id = NEW.set_id;
    IF existing_version <> COALESCE(NULLIF(TG_ARGV[0], '')::smallint, 3) THEN
        RAISE EXCEPTION USING ERRCODE = '23505', MESSAGE = format('set identity %s already belongs to wire version %s', NEW.set_id, existing_version);
    END IF;
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION recovery.guard_successor_locator()
RETURNS trigger LANGUAGE plpgsql
SET search_path = pg_catalog, recovery
AS $$
DECLARE expected_length bigint; expected_hash text;
BEGIN
    SELECT byte_length, raw_sha256 INTO expected_length, expected_hash
      FROM recovery.successor_evidence_v2
     WHERE payload_family = NEW.payload_family
       AND payload_version = NEW.payload_version
       AND payload_digest = NEW.payload_digest;
    IF expected_length IS NULL OR NEW.byte_length <> expected_length OR NEW.raw_sha256 <> expected_hash THEN
        RAISE EXCEPTION 'successor locator does not match canonical evidence bytes';
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS successor_registry_immutable ON recovery.set_identity_registry;
CREATE TRIGGER successor_registry_immutable BEFORE UPDATE OR DELETE ON recovery.set_identity_registry
FOR EACH ROW EXECUTE FUNCTION recovery.reject_successor_mutation();

CREATE OR REPLACE FUNCTION recovery.guard_successor_set_complete()
RETURNS trigger LANGUAGE plpgsql
SET search_path = pg_catalog, recovery
AS $$
BEGIN
    IF (SELECT count(*) FROM recovery.recovery_set_v3_root WHERE set_id = NEW.set_id) <> 2
       OR (SELECT count(*) FROM recovery.recovery_set_v3_root WHERE set_id = NEW.set_id AND root_kind IN ('ducklake', 'serving-artifact')) <> 2 THEN
        RAISE EXCEPTION 'prepared successor recovery set requires exactly two canonical roots';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM recovery.successor_evidence_locator_v2 l JOIN recovery.recovery_set_v3 s ON s.set_id = NEW.set_id
                   WHERE l.payload_family = s.manifest_family AND l.payload_version = s.manifest_version AND l.payload_digest = s.manifest_digest)
       OR NOT EXISTS (SELECT 1 FROM recovery.successor_evidence_locator_v2 l JOIN recovery.recovery_set_v3 s ON s.set_id = NEW.set_id
                   WHERE l.payload_family = s.anchor_family AND l.payload_version = s.anchor_version AND l.payload_digest = s.anchor_digest)
       OR NOT EXISTS (SELECT 1 FROM recovery.successor_evidence_locator_v2 l JOIN recovery.recovery_set_v3 s ON s.set_id = NEW.set_id
                   WHERE l.payload_family = s.profile_family AND l.payload_version = s.profile_version AND l.payload_digest = s.profile_digest)
       OR NOT EXISTS (SELECT 1 FROM recovery.successor_evidence_locator_v2 l JOIN recovery.recovery_set_v3 s ON s.set_id = NEW.set_id
                   WHERE l.payload_family = s.receipt_family AND l.payload_version = s.receipt_version AND l.payload_digest = s.receipt_digest)
	   OR EXISTS (SELECT 1 FROM recovery.recovery_set_v3 s WHERE s.set_id = NEW.set_id AND s.capture_core_required)
	      AND NOT EXISTS (SELECT 1 FROM recovery.successor_evidence_locator_v2 l JOIN recovery.recovery_set_v3 s ON s.set_id = NEW.set_id
				   WHERE l.payload_family = 'core' AND l.payload_version = 2 AND l.payload_digest = s.capture_core_digest)
       OR NOT EXISTS (SELECT 1 FROM recovery.successor_evidence_locator_v2 l JOIN recovery.recovery_set_v3 s ON s.set_id = NEW.set_id
                   WHERE l.payload_family = s.authority_family AND l.payload_version = s.authority_version AND l.payload_digest = s.authority_digest) THEN
        RAISE EXCEPTION 'prepared successor recovery set requires exact locators for every selected evidence payload';
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS successor_evidence_immutable ON recovery.successor_evidence_v2;
CREATE TRIGGER successor_evidence_immutable BEFORE UPDATE OR DELETE ON recovery.successor_evidence_v2
FOR EACH ROW EXECUTE FUNCTION recovery.reject_successor_mutation();
DROP TRIGGER IF EXISTS successor_locator_immutable ON recovery.successor_evidence_locator_v2;
CREATE TRIGGER successor_locator_immutable BEFORE UPDATE OR DELETE ON recovery.successor_evidence_locator_v2
FOR EACH ROW EXECUTE FUNCTION recovery.reject_successor_mutation();
DROP TRIGGER IF EXISTS successor_manifest_binding_immutable ON recovery.successor_manifest_binding;
CREATE TRIGGER successor_manifest_binding_immutable BEFORE UPDATE OR DELETE ON recovery.successor_manifest_binding
FOR EACH ROW EXECUTE FUNCTION recovery.reject_successor_mutation();
DROP TRIGGER IF EXISTS successor_manifest_binding_identity ON recovery.successor_manifest_binding;
CREATE TRIGGER successor_manifest_binding_identity BEFORE INSERT ON recovery.successor_manifest_binding
FOR EACH ROW EXECUTE FUNCTION recovery.guard_successor_set_identity(3);
DROP TRIGGER IF EXISTS recovery_set_v3_insert_guard ON recovery.recovery_set_v3;
CREATE TRIGGER recovery_set_v3_insert_guard BEFORE INSERT ON recovery.recovery_set_v3
FOR EACH ROW EXECUTE FUNCTION recovery.guard_successor_set_identity(3);
DROP TRIGGER IF EXISTS recovery_set_v3_immutable ON recovery.recovery_set_v3;
CREATE TRIGGER recovery_set_v3_immutable BEFORE UPDATE OR DELETE ON recovery.recovery_set_v3
FOR EACH ROW EXECUTE FUNCTION recovery.reject_successor_mutation();
DROP TRIGGER IF EXISTS recovery_set_v3_root_immutable ON recovery.recovery_set_v3_root;
CREATE TRIGGER recovery_set_v3_root_immutable BEFORE UPDATE OR DELETE ON recovery.recovery_set_v3_root
FOR EACH ROW EXECUTE FUNCTION recovery.reject_successor_mutation();
DROP TRIGGER IF EXISTS successor_locator_guard ON recovery.successor_evidence_locator_v2;
CREATE TRIGGER successor_locator_guard BEFORE INSERT ON recovery.successor_evidence_locator_v2
FOR EACH ROW EXECUTE FUNCTION recovery.guard_successor_locator();
DROP TRIGGER IF EXISTS recovery_set_v3_complete ON recovery.recovery_set_v3;
CREATE CONSTRAINT TRIGGER recovery_set_v3_complete
AFTER INSERT ON recovery.recovery_set_v3 DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION recovery.guard_successor_set_complete();

-- A v1 row inserted after this migration also reserves its UUID in the same
-- registry.  Existing v1 rows are copied by migration 005 before this trigger
-- is installed; no v1 digest or payload is rewritten.
CREATE OR REPLACE FUNCTION recovery.guard_v1_set_identity()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, recovery
AS $$
DECLARE existing_version smallint;
BEGIN
    INSERT INTO recovery.set_identity_registry(set_id, wire_version)
    VALUES (NEW.set_id, 1)
    ON CONFLICT (set_id) DO NOTHING;
    SELECT wire_version INTO existing_version
      FROM recovery.set_identity_registry WHERE set_id = NEW.set_id;
    IF existing_version <> 1 THEN
        RAISE EXCEPTION USING ERRCODE = '23505', MESSAGE = format('set identity %s already belongs to wire version %s', NEW.set_id, existing_version);
    END IF;
    RETURN NEW;
END
$$;
DROP TRIGGER IF EXISTS recovery_set_identity_registry_v1 ON recovery.recovery_set;
CREATE TRIGGER recovery_set_identity_registry_v1 BEFORE INSERT ON recovery.recovery_set
FOR EACH ROW EXECUTE FUNCTION recovery.guard_v1_set_identity();

CREATE INDEX IF NOT EXISTS successor_locator_profile_idx
    ON recovery.successor_evidence_locator_v2(storage_profile_id, account_identity, endpoint, bucket);
CREATE INDEX IF NOT EXISTS recovery_set_v3_manifest_idx
    ON recovery.recovery_set_v3(manifest_digest, created_at DESC);

REVOKE ALL ON TABLE recovery.set_identity_registry, recovery.successor_evidence_v2,
    recovery.successor_evidence_locator_v2, recovery.recovery_set_v3,
    recovery.recovery_set_v3_root, recovery.successor_manifest_binding,
    recovery.successor_trust_generation FROM PUBLIC;
REVOKE ALL ON FUNCTION recovery.successor_raw_sha256(bytea), recovery.successor_domain_sha256(text, bytea),
    recovery.reject_successor_mutation(), recovery.guard_successor_set_identity(),
    recovery.guard_successor_locator(), recovery.guard_successor_set_complete(), recovery.guard_v1_set_identity(),
    recovery.lock_successor_generation() FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT SELECT ON TABLE recovery.set_identity_registry TO leapview_control_maintenance;
        GRANT SELECT ON TABLE recovery.successor_trust_generation TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION recovery.successor_raw_sha256(bytea), recovery.successor_domain_sha256(text, bytea) TO leapview_control_maintenance;
        GRANT SELECT, INSERT ON TABLE recovery.successor_evidence_v2,
            recovery.successor_evidence_locator_v2, recovery.recovery_set_v3,
            recovery.recovery_set_v3_root, recovery.successor_manifest_binding TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION recovery.lock_successor_generation() TO leapview_control_maintenance;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE recovery.set_identity_registry,
            recovery.successor_trust_generation FROM leapview_control_maintenance;
        REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE recovery.successor_evidence_v2,
            recovery.successor_evidence_locator_v2, recovery.recovery_set_v3,
            recovery.recovery_set_v3_root, recovery.successor_manifest_binding
            FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        REVOKE ALL ON TABLE recovery.set_identity_registry,
            recovery.successor_evidence_v2,
            recovery.successor_evidence_locator_v2, recovery.recovery_set_v3,
            recovery.recovery_set_v3_root, recovery.successor_manifest_binding,
            recovery.successor_trust_generation FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT SELECT ON TABLE recovery.set_identity_registry,
            recovery.successor_evidence_v2, recovery.successor_evidence_locator_v2,
            recovery.recovery_set_v3, recovery.recovery_set_v3_root,
            recovery.successor_manifest_binding, recovery.successor_trust_generation
            TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE recovery.set_identity_registry,
            recovery.successor_evidence_v2, recovery.successor_evidence_locator_v2,
            recovery.recovery_set_v3, recovery.recovery_set_v3_root,
            recovery.successor_manifest_binding, recovery.successor_trust_generation
            FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT SELECT ON TABLE recovery.set_identity_registry,
            recovery.successor_evidence_v2, recovery.successor_evidence_locator_v2,
            recovery.recovery_set_v3, recovery.recovery_set_v3_root,
            recovery.successor_manifest_binding, recovery.successor_trust_generation
            TO leapview_control_backup;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE recovery.set_identity_registry,
            recovery.successor_evidence_v2, recovery.successor_evidence_locator_v2,
            recovery.recovery_set_v3, recovery.recovery_set_v3_root,
            recovery.successor_manifest_binding, recovery.successor_trust_generation
            FROM leapview_control_backup;
    END IF;
END
$$;
