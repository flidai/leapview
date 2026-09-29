-- +goose Up
SET LOCAL ROLE leapview_control_owner;

CREATE TABLE project.saved_exploration (
    id              uuid PRIMARY KEY,
    project_id      text NOT NULL,
    environment     text NOT NULL,
    principal_id    text NOT NULL,
    title           text NOT NULL,
    command_json    jsonb NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 255),
    CHECK (principal_id = btrim(principal_id) AND octet_length(principal_id) BETWEEN 1 AND 255),
    CHECK (title = btrim(title) AND octet_length(title) BETWEEN 1 AND 255),
    CHECK (jsonb_typeof(command_json) = 'object'),
    CHECK (octet_length(command_json::text) BETWEEN 1 AND 65536),
    CHECK (updated_at >= created_at)
);
CREATE INDEX saved_exploration_owner_recent_idx
    ON project.saved_exploration(project_id, environment, principal_id, updated_at DESC, id);

REVOKE ALL ON TABLE project.saved_exploration FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT ALL ON TABLE project.saved_exploration TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT SELECT, INSERT ON TABLE project.saved_exploration TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT SELECT ON TABLE project.saved_exploration TO leapview_control_readonly;
    END IF;
END $$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'saved exploration persistence is forward-only; restore a coordinated backup to downgrade';
END $$;
-- +goose StatementEnd
