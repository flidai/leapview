-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Per-artifact migration capabilities are owner-signed, maintenance-published
-- inputs to the migration compatibility owners. The OCI admission foreign key
-- prevents a capability from naming an artifact that was never admitted.
-- Target and subsystem identities are part of both canonical documents and
-- the immutable key.
CREATE TABLE IF NOT EXISTS release.migration_capability (
    artifact_admission_digest text NOT NULL,
    target_identity_digest text NOT NULL,
    subsystem text NOT NULL,
    owner_identity text NOT NULL,
    owner_contract_version text NOT NULL,
    capability_version text NOT NULL,
    capability_digest text NOT NULL UNIQUE,
    capability_bytes bytea NOT NULL,
    owner_evidence_version text NOT NULL,
    owner_evidence_digest text NOT NULL UNIQUE,
    owner_evidence_bytes bytea NOT NULL,
    published_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (artifact_admission_digest, target_identity_digest, subsystem),
    FOREIGN KEY (artifact_admission_digest)
        REFERENCES release.oci_artifact_admission(admission_digest) ON DELETE RESTRICT,
    CHECK (artifact_admission_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (target_identity_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (capability_version = 'migration-capability/v1'),
    CHECK (capability_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (octet_length(capability_bytes) BETWEEN 1 AND 65536),
    CHECK (owner_evidence_version = 'migration-capability-owner-evidence/v1'),
    CHECK (owner_evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (octet_length(owner_evidence_bytes) BETWEEN 1 AND 131072),
    CHECK (
        (subsystem = 'goose' AND owner_identity = 'leapview.postgres.goose'
            AND owner_contract_version = 'goose-owner-capability/v1')
        OR (subsystem = 'river-jobs' AND owner_identity = 'river.postgres+leapview.jobs'
            AND owner_contract_version = 'river-jobs-owner-capability/v1')
        OR (subsystem = 'ducklake' AND owner_identity = 'leapview.ducklake.catalog'
            AND owner_contract_version = 'ducklake-owner-capability/v1')
        OR (subsystem = 'physical-pool' AND owner_identity = 'leapview.physical-pool'
            AND owner_contract_version = 'physical-pool-owner-capability/v1')
    )
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION release.reject_migration_capability_mutation()
RETURNS trigger LANGUAGE plpgsql
SET search_path = pg_catalog, release
AS $$
BEGIN
    RAISE EXCEPTION 'migration capability authority evidence is immutable';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS migration_capability_immutable ON release.migration_capability;
CREATE TRIGGER migration_capability_immutable
    BEFORE UPDATE OR DELETE ON release.migration_capability
    FOR EACH ROW EXECUTE FUNCTION release.reject_migration_capability_mutation();
DROP TRIGGER IF EXISTS migration_capability_no_truncate ON release.migration_capability;
CREATE TRIGGER migration_capability_no_truncate
    BEFORE TRUNCATE ON release.migration_capability
    FOR EACH STATEMENT EXECUTE FUNCTION release.reject_migration_capability_mutation();

REVOKE ALL ON TABLE release.migration_capability FROM PUBLIC;
REVOKE ALL ON FUNCTION release.reject_migration_capability_mutation() FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_owner;
        GRANT ALL ON release.migration_capability TO leapview_control_owner;
        GRANT EXECUTE ON FUNCTION release.reject_migration_capability_mutation() TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_migrator;
        GRANT ALL ON release.migration_capability TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION release.reject_migration_capability_mutation() TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_runtime;
        GRANT SELECT ON release.migration_capability TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.migration_capability FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_maintenance;
        GRANT SELECT, INSERT ON release.migration_capability TO leapview_control_maintenance;
        REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.migration_capability FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_readonly;
        GRANT SELECT ON release.migration_capability TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.migration_capability FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_backup;
        GRANT SELECT ON release.migration_capability TO leapview_control_backup;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.migration_capability FROM leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'migration capability authority migration is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
