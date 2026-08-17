-- +goose Up
-- Production access primitives: group membership,
-- binding subjects, revocable sessions/tokens, and audit listing support.

CREATE TABLE IF NOT EXISTS group_members (
  group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(group_id, principal_id)
);

ALTER TABLE sessions ADD COLUMN revoked_at TEXT;
ALTER TABLE api_tokens ADD COLUMN revoked_at TEXT;

CREATE INDEX IF NOT EXISTS group_members_principal_idx ON group_members(principal_id);
CREATE INDEX IF NOT EXISTS api_tokens_principal_idx ON api_tokens(principal_id, created_at DESC);
