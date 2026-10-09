-- +goose Up
SET LOCAL ROLE leapview_control_owner;
-- Preserve unsupported legacy ciphertext for explicit operator recovery. New
-- revisions reference only the exact customer-owned encrypted version verified
-- by the activation transaction; no automatic ciphertext import is performed.
ALTER TABLE agent.configuration_revisions
    ADD COLUMN credential_version_id text
    CHECK (credential_version_id IS NULL OR (credential_version_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND credential_version_id <> '00000000-0000-0000-0000-000000000000'
        AND octet_length(credential) = 0));
-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS agent.configuration_candidates (
    candidate_id uuid PRIMARY KEY CHECK (candidate_id <> '00000000-0000-0000-0000-000000000000'),
    expected_revision bigint NOT NULL CHECK (expected_revision >= 0),
    enabled boolean NOT NULL,
    config_json jsonb NOT NULL CHECK (jsonb_typeof(config_json) = 'object' AND COALESCE(config_json->>'APIKey', '') = ''),
    actor_id text NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 256),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE OR REPLACE TRIGGER configuration_candidates_immutable
BEFORE UPDATE OR DELETE ON agent.configuration_candidates
FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT SELECT, INSERT ON agent.configuration_candidates TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT SELECT ON agent.configuration_candidates TO leapview_control_backup;
    END IF;
END $$;

-- +goose StatementEnd
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'agent credential history is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
