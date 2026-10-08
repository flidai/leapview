-- +goose Up
SET LOCAL ROLE leapview_control_owner;
CREATE TABLE IF NOT EXISTS dashboard.saved_visuals (
 project_id text NOT NULL,
 principal_id uuid NOT NULL,
 id uuid NOT NULL,
 source_key text NOT NULL CHECK (octet_length(source_key) BETWEEN 1 AND 512),
 title text NOT NULL CHECK (octet_length(title) BETWEEN 1 AND 512),
 semantic_model_id text NOT NULL,
 definition_json jsonb NOT NULL CHECK (jsonb_typeof(definition_json) = 'object'),
 saved_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (project_id, principal_id, id),
 UNIQUE (project_id, principal_id, source_key)
);

GRANT SELECT, INSERT, UPDATE ON dashboard.saved_visuals TO leapview_control_runtime;
GRANT SELECT ON dashboard.saved_visuals TO leapview_control_backup;
RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'saved visuals are durable; destructive down is forbidden'; END $$;
-- +goose StatementEnd
