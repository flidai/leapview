-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- PostgreSQL storage for encrypted credential drafts. Logical metadata and
-- encrypted envelopes are distinct so plaintext is never represented here.
CREATE SCHEMA IF NOT EXISTS credential;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION credential.reject_immutable_row_change()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'credential draft rows are immutable';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION credential.guard_encryption_budget()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.uses <> 1 THEN
            RAISE EXCEPTION 'credential encryption budget must begin at one';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.deployment_id IS DISTINCT FROM OLD.deployment_id
       OR NEW.key_id IS DISTINCT FROM OLD.key_id
       OR NEW.key_commitment IS DISTINCT FROM OLD.key_commitment
       OR NEW.uses <> OLD.uses + 1 THEN
        RAISE EXCEPTION 'credential encryption reservations must increase by one';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS credential.encryption_budget (
    deployment_id  text NOT NULL,
    key_id         text NOT NULL,
    key_commitment bytea NOT NULL,
    uses           bigint NOT NULL,
    PRIMARY KEY (deployment_id, key_id),
    UNIQUE (deployment_id, key_commitment),
    CHECK (deployment_id = btrim(deployment_id) AND octet_length(deployment_id) BETWEEN 1 AND 255),
    CHECK (key_id = btrim(key_id) AND octet_length(key_id) BETWEEN 1 AND 255),
    CHECK (octet_length(key_commitment) = 32),
    CHECK (uses BETWEEN 1 AND 2147483648)
);

DROP TRIGGER IF EXISTS encryption_budget_reservation_guard ON credential.encryption_budget;
CREATE TRIGGER encryption_budget_reservation_guard
    BEFORE INSERT OR UPDATE ON credential.encryption_budget
    FOR EACH ROW EXECUTE FUNCTION credential.guard_encryption_budget();

DROP TRIGGER IF EXISTS encryption_budget_no_delete ON credential.encryption_budget;
CREATE TRIGGER encryption_budget_no_delete
    BEFORE DELETE ON credential.encryption_budget
    FOR EACH ROW EXECUTE FUNCTION credential.reject_immutable_row_change();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION credential.valid_binding_fields(
    scope_kind text,
    target_id text,
    project_id text,
    environment text,
    resource_id text
) RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
SET search_path = pg_catalog
AS $$
    SELECT CASE scope_kind
        WHEN 'connection' THEN target_id <> '' AND project_id <> '' AND environment <> '' AND resource_id <> ''
        WHEN 'agent' THEN target_id = '' AND project_id = '' AND environment = '' AND resource_id <> ''
        ELSE false
    END
       AND target_id = btrim(target_id)
       AND project_id = btrim(project_id)
       AND environment = btrim(environment)
       AND resource_id = btrim(resource_id)
       AND octet_length(target_id) <= 255
       AND octet_length(project_id) <= 255
       AND octet_length(environment) <= 255
       AND octet_length(resource_id) BETWEEN 1 AND 255
       AND target_id !~ '[[:cntrl:]]'
       AND project_id !~ '[[:cntrl:]]'
       AND environment !~ '[[:cntrl:]]'
       AND resource_id !~ '[[:cntrl:]]';
$$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS credential.draft_version (
    version_id    text PRIMARY KEY,
    deployment_id text NOT NULL,
    owner_id      text NOT NULL,
    scope_kind    text NOT NULL,
    target_id     text NOT NULL DEFAULT '',
    project_id    text NOT NULL DEFAULT '',
    environment   text NOT NULL DEFAULT '',
    resource_id   text NOT NULL,
    purpose       text NOT NULL,
    provider      text NOT NULL,
    destination   text NOT NULL,
    actor_id      text NOT NULL,
    created_at    timestamptz NOT NULL,
    UNIQUE (deployment_id, version_id),
    CHECK (version_id = btrim(version_id) AND octet_length(version_id) BETWEEN 1 AND 255),
    CHECK (deployment_id = btrim(deployment_id) AND octet_length(deployment_id) BETWEEN 1 AND 255),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255 AND owner_id !~ '[[:cntrl:]]'),
    CHECK (credential.valid_binding_fields(scope_kind, target_id, project_id, environment, resource_id)),
    CHECK (purpose = btrim(purpose) AND octet_length(purpose) BETWEEN 1 AND 255 AND purpose !~ '[[:cntrl:]]'),
    CHECK (provider = btrim(provider) AND octet_length(provider) BETWEEN 1 AND 255 AND provider !~ '[[:cntrl:]]'),
    CHECK (destination ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (actor_id = btrim(actor_id) AND octet_length(actor_id) BETWEEN 1 AND 255 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (created_at > '-infinity'::timestamptz AND isfinite(created_at))
);

CREATE INDEX IF NOT EXISTS credential_draft_scope_created_version_idx
    ON credential.draft_version (
        deployment_id, owner_id, scope_kind, target_id, project_id,
        environment, resource_id, created_at DESC, version_id DESC
    );

CREATE TABLE IF NOT EXISTS credential.envelope (
    version_id    text PRIMARY KEY,
    deployment_id text NOT NULL,
    key_id        text NOT NULL,
    format        text NOT NULL,
    ciphertext    bytea NOT NULL,
    envelope_revision bigint NOT NULL DEFAULT 1,
    FOREIGN KEY (deployment_id, version_id)
        REFERENCES credential.draft_version (deployment_id, version_id),
    FOREIGN KEY (deployment_id, key_id)
        REFERENCES credential.encryption_budget (deployment_id, key_id),
    CHECK (format = 'aes-256-gcm-random-nonce-v1'),
    CHECK (octet_length(ciphertext) BETWEEN 28 AND 16412),
    CHECK (envelope_revision > 0)
);

DROP TRIGGER IF EXISTS draft_version_immutable ON credential.draft_version;
CREATE TRIGGER draft_version_immutable
    BEFORE UPDATE OR DELETE ON credential.draft_version
    FOR EACH ROW EXECUTE FUNCTION credential.reject_immutable_row_change();

DROP TRIGGER IF EXISTS envelope_immutable ON credential.envelope;
CREATE TRIGGER envelope_immutable
    BEFORE UPDATE OR DELETE ON credential.envelope
    FOR EACH ROW EXECUTE FUNCTION credential.reject_immutable_row_change();

REVOKE ALL ON SCHEMA credential FROM PUBLIC;
REVOKE ALL ON credential.encryption_budget, credential.draft_version, credential.envelope FROM PUBLIC;
REVOKE ALL ON FUNCTION credential.reject_immutable_row_change() FROM PUBLIC;
REVOKE ALL ON FUNCTION credential.guard_encryption_budget() FROM PUBLIC;
REVOKE ALL ON FUNCTION credential.valid_binding_fields(text, text, text, text, text) FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA credential TO leapview_control_runtime;
        GRANT SELECT, INSERT ON credential.draft_version, credential.envelope TO leapview_control_runtime;
        GRANT SELECT, INSERT ON credential.encryption_budget TO leapview_control_runtime;
        GRANT UPDATE (uses) ON credential.encryption_budget TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION credential.valid_binding_fields(text, text, text, text, text) TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA credential TO leapview_control_backup;
        GRANT SELECT ON credential.encryption_budget, credential.draft_version, credential.envelope TO leapview_control_backup;
    END IF;
END;
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'credential draft storage is forward-only; restore a coordinated backup to downgrade';
END $$;
-- +goose StatementEnd
