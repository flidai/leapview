-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- A validation receipt is a short-lived statement that one immutable saved
-- draft was successfully observed against one exact target binding. It stores
-- only identifiers, configuration digests, and times.
CREATE TABLE IF NOT EXISTS credential.validation_receipt (
    receipt_id          text PRIMARY KEY,
    deployment_id       text NOT NULL,
    version_id          text NOT NULL,
    owner_id             text NOT NULL,
    scope_kind           text NOT NULL,
    target_id            text NOT NULL DEFAULT '',
    project_id           text NOT NULL DEFAULT '',
    environment          text NOT NULL DEFAULT '',
    resource_id          text NOT NULL,
    purpose              text NOT NULL,
    provider             text NOT NULL,
    destination          text NOT NULL,
    actor_id             text NOT NULL,
    binding_id           text NOT NULL,
    binding_revision     bigint NOT NULL,
    configuration_digest text NOT NULL,
    validated_at         timestamptz NOT NULL,
    expires_at           timestamptz NOT NULL,
    FOREIGN KEY (deployment_id, version_id)
        REFERENCES credential.draft_version (deployment_id, version_id),
    CHECK (receipt_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
    CHECK (deployment_id = btrim(deployment_id) AND octet_length(deployment_id) BETWEEN 1 AND 255),
    CHECK (version_id = btrim(version_id) AND octet_length(version_id) BETWEEN 1 AND 255),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255 AND owner_id !~ '[[:cntrl:]]'),
    CHECK (credential.valid_binding_fields(scope_kind, target_id, project_id, environment, resource_id)),
    CHECK (purpose = btrim(purpose) AND octet_length(purpose) BETWEEN 1 AND 255 AND purpose !~ '[[:cntrl:]]'),
    CHECK (provider = btrim(provider) AND octet_length(provider) BETWEEN 1 AND 255 AND provider !~ '[[:cntrl:]]'),
    CHECK (destination ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (actor_id = btrim(actor_id) AND octet_length(actor_id) BETWEEN 1 AND 255 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (binding_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$'),
    CHECK (binding_revision > 0),
    CHECK (configuration_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (validated_at > '-infinity'::timestamptz AND isfinite(validated_at)),
    CHECK (expires_at > '-infinity'::timestamptz AND isfinite(expires_at)),
    CHECK (expires_at = validated_at + interval '5 minutes')
);

DROP TRIGGER IF EXISTS validation_receipt_immutable ON credential.validation_receipt;
CREATE TRIGGER validation_receipt_immutable
    BEFORE UPDATE OR DELETE ON credential.validation_receipt
    FOR EACH ROW EXECUTE FUNCTION credential.reject_immutable_row_change();

REVOKE ALL ON credential.validation_receipt FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        REVOKE ALL ON credential.validation_receipt FROM leapview_control_runtime;
        GRANT INSERT ON credential.validation_receipt TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT SELECT ON credential.validation_receipt TO leapview_control_backup;
    END IF;
END;
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'credential validation receipts are forward-only; restore a coordinated backup to downgrade';
END $$;
-- +goose StatementEnd
