-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Immutable OCI admission evidence is published by maintenance and resolved
-- read-only at runtime. Canonical bytes, not denormalized columns, remain the
-- authority and are reverified by the release repository on every read.
CREATE TABLE IF NOT EXISTS release.oci_artifact_admission (
    artifact_reference text PRIMARY KEY,
    repository_identity text NOT NULL,
    oci_digest text NOT NULL,
    admission_version text NOT NULL,
    admission_digest text NOT NULL UNIQUE,
    admission_bytes bytea NOT NULL,
    admitted_at timestamptz NOT NULL,
    published_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (artifact_reference = repository_identity || '@' || oci_digest),
    CHECK (oci_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (admission_version = 'oci-artifact-admission/v1'),
    CHECK (admission_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (octet_length(admission_bytes) BETWEEN 1 AND 262144),
    UNIQUE (artifact_reference, admission_digest)
);

-- Revocation is a separate append-only fact. It never rewrites the admitted
-- evidence and therefore preserves the exact decision bytes for audit.
CREATE TABLE IF NOT EXISTS release.oci_artifact_admission_revocation (
    artifact_reference text PRIMARY KEY,
    admission_digest text NOT NULL,
    revoked_at timestamptz NOT NULL,
    reason text NOT NULL,
    CHECK (admission_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (octet_length(reason) BETWEEN 1 AND 1024),
    FOREIGN KEY (artifact_reference, admission_digest)
        REFERENCES release.oci_artifact_admission(artifact_reference, admission_digest)
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION release.reject_oci_admission_mutation()
RETURNS trigger LANGUAGE plpgsql
SET search_path = pg_catalog, release
AS $$
BEGIN
    RAISE EXCEPTION 'OCI artifact admission authority evidence is immutable';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS oci_artifact_admission_immutable ON release.oci_artifact_admission;
CREATE TRIGGER oci_artifact_admission_immutable
    BEFORE UPDATE OR DELETE ON release.oci_artifact_admission
    FOR EACH ROW EXECUTE FUNCTION release.reject_oci_admission_mutation();
DROP TRIGGER IF EXISTS oci_artifact_admission_no_truncate ON release.oci_artifact_admission;
CREATE TRIGGER oci_artifact_admission_no_truncate
    BEFORE TRUNCATE ON release.oci_artifact_admission
    FOR EACH STATEMENT EXECUTE FUNCTION release.reject_oci_admission_mutation();
DROP TRIGGER IF EXISTS oci_artifact_admission_revocation_immutable ON release.oci_artifact_admission_revocation;
CREATE TRIGGER oci_artifact_admission_revocation_immutable
    BEFORE UPDATE OR DELETE ON release.oci_artifact_admission_revocation
    FOR EACH ROW EXECUTE FUNCTION release.reject_oci_admission_mutation();
DROP TRIGGER IF EXISTS oci_artifact_admission_revocation_no_truncate ON release.oci_artifact_admission_revocation;
CREATE TRIGGER oci_artifact_admission_revocation_no_truncate
    BEFORE TRUNCATE ON release.oci_artifact_admission_revocation
    FOR EACH STATEMENT EXECUTE FUNCTION release.reject_oci_admission_mutation();

REVOKE ALL ON TABLE release.oci_artifact_admission, release.oci_artifact_admission_revocation FROM PUBLIC;
REVOKE ALL ON FUNCTION release.reject_oci_admission_mutation() FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_owner;
        GRANT ALL ON release.oci_artifact_admission, release.oci_artifact_admission_revocation TO leapview_control_owner;
        GRANT EXECUTE ON FUNCTION release.reject_oci_admission_mutation() TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_migrator;
        GRANT ALL ON release.oci_artifact_admission, release.oci_artifact_admission_revocation TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION release.reject_oci_admission_mutation() TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_runtime;
        GRANT SELECT ON release.oci_artifact_admission, release.oci_artifact_admission_revocation TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.oci_artifact_admission, release.oci_artifact_admission_revocation FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_maintenance;
        GRANT SELECT, INSERT ON release.oci_artifact_admission, release.oci_artifact_admission_revocation TO leapview_control_maintenance;
        REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.oci_artifact_admission, release.oci_artifact_admission_revocation FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_readonly;
        GRANT SELECT ON release.oci_artifact_admission, release.oci_artifact_admission_revocation TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.oci_artifact_admission, release.oci_artifact_admission_revocation FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_backup;
        GRANT SELECT ON release.oci_artifact_admission, release.oci_artifact_admission_revocation TO leapview_control_backup;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.oci_artifact_admission, release.oci_artifact_admission_revocation FROM leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'OCI artifact admission authority migration is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
