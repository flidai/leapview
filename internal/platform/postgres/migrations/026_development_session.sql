-- +goose Up

SET LOCAL ROLE leapview_control_owner;

-- The session is a pointer and an orchestration checkpoint, not a candidate
-- authority. Candidate/artifact/graph identities remain independently owned by
-- project and deployment authorities.
CREATE SCHEMA IF NOT EXISTS project;
CREATE TABLE IF NOT EXISTS project.development_session (
    id                          text PRIMARY KEY,
    owner_id                    text NOT NULL,
    checkout_id                 text NOT NULL,
    worktree_id                 text NOT NULL,
    project_id                  text NOT NULL,
    target_id                   text NOT NULL,
    environment                 text NOT NULL,
    attempted_candidate_id      text NOT NULL DEFAULT '',
    attempted_artifact_digest   text NOT NULL DEFAULT '',
    attempted_graph_digest      text NOT NULL DEFAULT '',
    attempted_preview_url       text NOT NULL DEFAULT '',
    last_valid_candidate_id     text NOT NULL DEFAULT '',
    last_valid_artifact_digest  text NOT NULL DEFAULT '',
    last_valid_graph_digest     text NOT NULL DEFAULT '',
    last_valid_preview_url      text NOT NULL DEFAULT '',
    diagnostics_json            jsonb NOT NULL DEFAULT '[]'::jsonb,
    revision                    bigint NOT NULL DEFAULT 1,
    created_at                  timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at                  timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (id = btrim(id) AND octet_length(id) BETWEEN 1 AND 256 AND id ~ '^devsess_[0-9a-f]{64}$'),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 256 AND owner_id !~ '[[:space:][:cntrl:]]'),
    CHECK (checkout_id = btrim(checkout_id) AND octet_length(checkout_id) BETWEEN 1 AND 256 AND checkout_id !~ '[[:space:][:cntrl:]]'),
    CHECK (worktree_id = btrim(worktree_id) AND octet_length(worktree_id) BETWEEN 1 AND 256 AND worktree_id !~ '[[:space:][:cntrl:]]'),
    CHECK (project_id = btrim(project_id) AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (target_id = btrim(target_id) AND target_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,159}$'),
    CHECK (environment = btrim(environment) AND environment ~ '^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,159}$'),
    CHECK (attempted_candidate_id = btrim(attempted_candidate_id) AND octet_length(attempted_candidate_id) <= 256 AND attempted_candidate_id !~ '[[:space:][:cntrl:]]'),
    CHECK (last_valid_candidate_id = btrim(last_valid_candidate_id) AND octet_length(last_valid_candidate_id) <= 256 AND last_valid_candidate_id !~ '[[:space:][:cntrl:]]'),
    CHECK (attempted_artifact_digest = '' OR attempted_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (attempted_graph_digest = '' OR attempted_graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (last_valid_artifact_digest = '' OR last_valid_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (last_valid_graph_digest = '' OR last_valid_graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (attempted_preview_url = '' OR (attempted_preview_url ~ '^https?://[^@/?#[:space:]]+(/[^?#[:space:]]*)?$')),
    CHECK (last_valid_preview_url = '' OR (last_valid_preview_url ~ '^https?://[^@/?#[:space:]]+(/[^?#[:space:]]*)?$')),
    CHECK (attempted_candidate_id <> '' OR attempted_preview_url = ''),
    CHECK ((attempted_candidate_id = '' OR attempted_artifact_digest <> '') AND
           ((last_valid_candidate_id = '' AND last_valid_artifact_digest = '' AND last_valid_graph_digest = '' AND last_valid_preview_url = '') OR
            (last_valid_candidate_id <> '' AND last_valid_artifact_digest <> '' AND last_valid_preview_url <> ''))),
    CHECK (jsonb_typeof(diagnostics_json) = 'array' AND jsonb_array_length(diagnostics_json) <= 64 AND octet_length(diagnostics_json::text) <= 262144),
    CHECK (revision > 0),
    CHECK (updated_at >= created_at),
    UNIQUE (owner_id, checkout_id, worktree_id, project_id, target_id, environment)
);

CREATE INDEX IF NOT EXISTS development_session_owner_lookup_idx
    ON project.development_session (owner_id, project_id, target_id, environment);

-- Session identity and revisions are database-enforced. A stale process may
-- never replace a newer valid pointer by racing an update in another process.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION project.reject_development_session_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, project
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'development session records are durable and cannot be deleted';
    END IF;
    IF NEW.id <> OLD.id OR NEW.owner_id <> OLD.owner_id OR NEW.checkout_id <> OLD.checkout_id OR NEW.worktree_id <> OLD.worktree_id OR NEW.project_id <> OLD.project_id OR
       NEW.target_id <> OLD.target_id OR NEW.environment <> OLD.environment THEN
        RAISE EXCEPTION 'development session scope is immutable';
    END IF;
    IF NEW.revision <> OLD.revision + 1 THEN
        RAISE EXCEPTION 'development session revision must increase monotonically';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS development_session_mutation_guard ON project.development_session;
CREATE TRIGGER development_session_mutation_guard
    BEFORE UPDATE OR DELETE ON project.development_session
    FOR EACH ROW EXECUTE FUNCTION project.reject_development_session_mutation();

REVOKE ALL ON project.development_session FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE ON project.development_session TO leapview_control_owner;
GRANT SELECT, INSERT, UPDATE ON project.development_session TO leapview_control_runtime;
GRANT SELECT ON project.development_session TO leapview_control_readonly;
GRANT SELECT ON project.development_session TO leapview_control_backup;

RESET ROLE;

-- +goose Down

SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'development session authority migration is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd

RESET ROLE;
