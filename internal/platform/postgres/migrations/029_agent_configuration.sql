-- +goose Up
SET LOCAL ROLE leapview_control_owner;

CREATE TABLE IF NOT EXISTS agent.configuration_revisions (
    revision bigint PRIMARY KEY CHECK (revision > 0),
    enabled boolean NOT NULL,
    config_json jsonb NOT NULL CHECK (jsonb_typeof(config_json) = 'object' AND COALESCE(config_json->>'APIKey', '') = ''),
    credential bytea NOT NULL CHECK (octet_length(credential) <= 16412),
    actor_id text NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 256),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE OR REPLACE TRIGGER configuration_revisions_immutable
BEFORE UPDATE OR DELETE ON agent.configuration_revisions
FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();

GRANT SELECT, INSERT ON agent.configuration_revisions TO leapview_control_runtime;
GRANT SELECT ON agent.configuration_revisions TO leapview_control_backup;

RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'agent configuration revisions are durable; destructive down is forbidden'; END $$;
-- +goose StatementEnd
RESET ROLE;
