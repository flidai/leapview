-- +goose Up
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
-- Immutable installation-operator admission, independent from audit retention.
-- One target can admit its first source once; exact retries retain its receipt.
CREATE TABLE IF NOT EXISTS credential.first_source_admission (
    target_id text PRIMARY KEY CHECK (target_id = btrim(target_id) AND length(target_id) BETWEEN 1 AND 255),
    operation_id text NOT NULL UNIQUE CHECK (operation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' AND operation_id <> '00000000-0000-0000-0000-000000000000'),
    intent_digest text NOT NULL CHECK (intent_digest ~ '^sha256:[0-9a-f]{64}$'),
    intent_document jsonb NOT NULL CHECK (jsonb_typeof(intent_document) = 'object' AND octet_length(intent_document::text) BETWEEN 2 AND 32768),
    binding_digest text NOT NULL CHECK (binding_digest ~ '^sha256:[0-9a-f]{64}$'),
    policy_revision bigint NOT NULL CHECK (policy_revision > 1),
    policy_digest text NOT NULL CHECK (policy_digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(created_at)),
    CHECK (intent_document->>'targetId' = target_id AND intent_document->>'operationId' = operation_id)
);

CREATE OR REPLACE FUNCTION credential.guard_first_source_admission() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'first-source admission identity is immutable';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM audit.audit_event
        WHERE audit_id = NEW.operation_id::uuid AND scope_id = NEW.target_id
          AND actor_id = 'offline_operator' AND source = 'credential'
          AND operation = 'admitFirstSource' AND action = 'credential.first_source.admitted'
          AND resource_kind = 'connection' AND resource_id = NEW.intent_document->>'connectionId'
          AND outcome = 'success' AND metadata->>'intentDigest' = NEW.intent_digest
    ) THEN
        RAISE EXCEPTION 'first-source admission requires exact atomic operator audit';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS first_source_admission_guard ON credential.first_source_admission;
CREATE TRIGGER first_source_admission_guard BEFORE INSERT OR UPDATE OR DELETE ON credential.first_source_admission
    FOR EACH ROW EXECUTE FUNCTION credential.guard_first_source_admission();
REVOKE ALL ON credential.first_source_admission FROM PUBLIC;
REVOKE ALL ON FUNCTION credential.guard_first_source_admission() FROM PUBLIC;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT SELECT, INSERT ON credential.first_source_admission TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT SELECT ON credential.first_source_admission TO leapview_control_backup;
    END IF;
END $$;
-- +goose StatementEnd
RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'credential first-source admission is forward-only; preserve exact operator and binding authority and restore a coordinated backup';
END $$;
-- +goose StatementEnd
