-- LeapView PostgreSQL clean-slate foundation (ADR-0016).
--
-- Capability packages own their tables, functions, triggers, and grants. The
-- migration runner applies this foundation and then the ordered capability
-- SchemaSQL components in one caller-owned transaction. Keep this file small:
-- it contains only role/ownership checks and the append-only revision ledger.

DO $$
BEGIN
    IF (SELECT count(*) FROM pg_roles WHERE rolname IN (
        'leapview_control_owner', 'leapview_control_migrator',
        'leapview_control_runtime', 'leapview_control_readonly',
        'leapview_control_backup'
    )) <> 5 THEN
        RAISE EXCEPTION 'PostgreSQL control roles must be provisioned before applying the baseline';
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT pg_has_role(current_user, 'leapview_control_owner', 'member') THEN
        RAISE EXCEPTION 'current migration role % must be a member of leapview_control_owner', current_user;
    END IF;
END
$$;

-- Apply has already assumed the NOLOGIN owner role. Objects therefore remain
-- owned by durable authority rather than by the migrator login whose
-- credentials can be rotated independently.
CREATE SCHEMA IF NOT EXISTS platform;

CREATE TABLE IF NOT EXISTS platform.schema_revision (
    revision       bigint PRIMARY KEY CHECK (revision > 0),
    migration_id   text NOT NULL UNIQUE CHECK (migration_id = btrim(migration_id) AND migration_id <> ''),
    checksum       text NOT NULL CHECK (checksum ~ '^[0-9a-f]{64}$'),
    applied_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE OR REPLACE FUNCTION platform.reject_schema_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, platform
AS $$
BEGIN
    RAISE EXCEPTION 'schema revisions are append-only';
END;
$$;

DROP TRIGGER IF EXISTS schema_revision_append_only ON platform.schema_revision;
CREATE TRIGGER schema_revision_append_only
    BEFORE UPDATE OR DELETE ON platform.schema_revision
    FOR EACH ROW EXECUTE FUNCTION platform.reject_schema_revision_mutation();

REVOKE ALL ON SCHEMA platform FROM PUBLIC;
REVOKE ALL ON TABLE platform.schema_revision FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.reject_schema_revision_mutation() FROM PUBLIC;

GRANT USAGE ON SCHEMA platform TO leapview_control_migrator;
GRANT ALL ON TABLE platform.schema_revision TO leapview_control_owner, leapview_control_migrator;
GRANT SELECT ON TABLE platform.schema_revision TO
    leapview_control_runtime,
    leapview_control_readonly,
    leapview_control_backup;
