-- PostgreSQL cursor-signing authority. Keys are append-only except for the
-- active/verification state transitions performed by the owning repository.
CREATE SCHEMA IF NOT EXISTS platform;

CREATE TABLE IF NOT EXISTS platform.api_cursor_signing_keys (
    key_id     text PRIMARY KEY CHECK (key_id = btrim(key_id) AND length(key_id) BETWEEN 1 AND 128),
    secret     bytea NOT NULL CHECK (octet_length(secret) >= 32 AND octet_length(secret) <= 4096),
    active     boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    verify_until timestamptz,
    CHECK ((active AND verify_until IS NULL) OR (NOT active AND verify_until IS NOT NULL))
    -- verify_until is the verification-until instant; expired rows are
    -- removed by the bounded security-definer prune function.
);

CREATE UNIQUE INDEX IF NOT EXISTS api_cursor_signing_keys_active_idx
    ON platform.api_cursor_signing_keys (active) WHERE active;

-- Key material is mutable only through the rotation transition. Retired keys
-- remain immutable until the bounded SECURITY DEFINER prune removes them.
CREATE OR REPLACE FUNCTION platform.guard_api_cursor_signing_key_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF NEW.active IS DISTINCT FROM true
       OR NEW.verify_until IS NOT NULL THEN
        RAISE EXCEPTION 'cursor signing key inserts must be active with no verification expiry';
    END IF;
    NEW.created_at := clock_timestamp();
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION platform.guard_api_cursor_signing_key_update()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF NEW.key_id IS DISTINCT FROM OLD.key_id
       OR NEW.secret IS DISTINCT FROM OLD.secret
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'cursor signing key identity is immutable';
    END IF;
    IF NOT OLD.active THEN
        RAISE EXCEPTION 'retired cursor signing keys are immutable';
    END IF;
    IF OLD.active AND NEW.active THEN
        IF NEW.verify_until IS DISTINCT FROM OLD.verify_until THEN
            RAISE EXCEPTION 'active cursor signing key verification window is immutable';
        END IF;
    ELSIF OLD.active AND NOT NEW.active THEN
        IF NEW.verify_until IS NULL
           OR NEW.verify_until < clock_timestamp() + interval '15 minutes'
           OR NEW.verify_until > clock_timestamp() + interval '24 hours' THEN
            RAISE EXCEPTION 'retired cursor signing key verification window is invalid';
        END IF;
    ELSE
        RAISE EXCEPTION 'invalid cursor signing key state transition';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS api_cursor_signing_keys_insert_guard ON platform.api_cursor_signing_keys;
CREATE TRIGGER api_cursor_signing_keys_insert_guard
    BEFORE INSERT ON platform.api_cursor_signing_keys
    FOR EACH ROW EXECUTE FUNCTION platform.guard_api_cursor_signing_key_insert();

DROP TRIGGER IF EXISTS api_cursor_signing_keys_update_guard ON platform.api_cursor_signing_keys;
CREATE TRIGGER api_cursor_signing_keys_update_guard
    BEFORE UPDATE ON platform.api_cursor_signing_keys
    FOR EACH ROW EXECUTE FUNCTION platform.guard_api_cursor_signing_key_update();

CREATE OR REPLACE FUNCTION platform.prune_expired_cursor_signing_keys(p_limit integer)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, platform
AS $$
DECLARE
    removed bigint;
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'cursor signing key prune limit must be between 1 and 1000';
    END IF;
    WITH doomed AS (
        SELECT key_id
        FROM platform.api_cursor_signing_keys
        WHERE NOT active
          AND verify_until IS NOT NULL
          AND verify_until <= clock_timestamp()
        ORDER BY verify_until, key_id
        LIMIT p_limit
    )
    DELETE FROM platform.api_cursor_signing_keys
    WHERE key_id IN (SELECT key_id FROM doomed);
    GET DIAGNOSTICS removed = ROW_COUNT;
    RETURN removed;
END;
$$;

-- Metadata excludes secret bytes so readonly diagnostics cannot exfiltrate
-- HMAC material. Runtime reads the base table for process configuration.
CREATE OR REPLACE VIEW platform.api_cursor_signing_key_metadata AS
SELECT key_id, active, created_at, verify_until
FROM platform.api_cursor_signing_keys;

COMMENT ON TABLE platform.api_cursor_signing_keys IS
    'Durable HMAC cursor-signing key ring; exactly one active key signs new cursors';

-- Keep cursor key material out of ambient PUBLIC access. Runtime owns key
-- rotation/configuration; readonly consumers may inspect metadata only.
REVOKE ALL ON SCHEMA platform FROM PUBLIC;
REVOKE ALL ON TABLE platform.api_cursor_signing_keys FROM PUBLIC;
REVOKE ALL ON TABLE platform.api_cursor_signing_key_metadata FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.prune_expired_cursor_signing_keys(integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_api_cursor_signing_key_insert() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_api_cursor_signing_key_update() FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT ALL ON TABLE platform.api_cursor_signing_keys TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT ALL ON TABLE platform.api_cursor_signing_keys TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON TABLE platform.api_cursor_signing_keys TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION platform.prune_expired_cursor_signing_keys(integer) TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_readonly;
        GRANT SELECT ON TABLE platform.api_cursor_signing_key_metadata TO leapview_control_readonly;
    END IF;
END
$$;
