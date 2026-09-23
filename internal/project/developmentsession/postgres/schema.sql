CREATE SCHEMA IF NOT EXISTS project;

CREATE TABLE project.development_session (
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
    UNIQUE (owner_id, checkout_id, worktree_id, project_id, target_id, environment)
);
