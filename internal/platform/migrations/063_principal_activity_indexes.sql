-- +goose Up
CREATE INDEX sessions_principal_last_seen_idx
  ON sessions(principal_id, last_seen_at DESC);

CREATE INDEX oauth_authoring_sessions_human_activity_idx
  ON oauth_authoring_sessions(principal_id, last_used_at DESC)
  WHERE kind = 'human_cli' AND last_used_at IS NOT NULL;

-- +goose Down
DROP INDEX oauth_authoring_sessions_human_activity_idx;
DROP INDEX sessions_principal_last_seen_idx;
