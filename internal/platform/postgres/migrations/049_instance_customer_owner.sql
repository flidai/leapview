-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- The declared customer owner is instance setup authority, distinct from the
-- administrator principal and the claimed Project.
CREATE TABLE IF NOT EXISTS platform.instance_customer_owner (
    singleton_id smallint PRIMARY KEY CHECK (singleton_id = 1),
    instance_id  text NOT NULL UNIQUE REFERENCES platform.instance_identity(instance_id),
    owner_id     text NOT NULL,
    declared_at  timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (owner_id = btrim(owner_id)
           AND octet_length(owner_id) BETWEEN 1 AND 255
           AND owner_id !~ U&'[\0009-\000D\0020\0085\00A0\1680\2000-\200A\2028\2029\202F\205F\3000\0001-\001F\007F-\009F]')
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION platform.reject_bootstrap_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'platform bootstrap identity and claims are immutable';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS instance_customer_owner_immutable ON platform.instance_customer_owner;
CREATE TRIGGER instance_customer_owner_immutable
    BEFORE UPDATE OR DELETE ON platform.instance_customer_owner
    FOR EACH ROW EXECUTE FUNCTION platform.reject_bootstrap_immutable_mutation();

REVOKE ALL ON TABLE platform.instance_customer_owner FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT SELECT, INSERT ON platform.instance_customer_owner TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT SELECT ON platform.instance_customer_owner TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT SELECT ON platform.instance_customer_owner TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'instance customer owner declaration is forward-only';
END $$;
-- +goose StatementEnd
