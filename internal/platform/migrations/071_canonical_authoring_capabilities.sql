-- +goose Up
-- Canonicalize the remaining identity persistence. Project authorization is
-- immutable and generation-scoped; the historical mutable roles, role
-- bindings, and role-grant templates are no longer part of the schema.

ALTER TABLE oauth_device_authorizations RENAME COLUMN privileges_json TO capabilities_json;
ALTER TABLE oauth_authoring_sessions RENAME COLUMN privileges_json TO capabilities_json;

CREATE TABLE platform_role_bindings_canonical (
  id TEXT PRIMARY KEY,
  role TEXT NOT NULL CHECK(role = 'platform_admin'),
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO platform_role_bindings_canonical (id, role, principal_id)
SELECT b.id, r.name, b.principal_id
FROM platform_role_bindings b
JOIN roles r ON r.id = b.role_id
WHERE r.name = 'platform_admin';
DROP TABLE platform_role_bindings;
ALTER TABLE platform_role_bindings_canonical RENAME TO platform_role_bindings;
CREATE UNIQUE INDEX platform_role_bindings_principal_unique_idx
  ON platform_role_bindings(principal_id);

DROP TABLE role_bindings;
DROP TABLE role_grant_templates;
DROP TABLE roles;

CREATE TABLE api_tokens_canonical (
  id TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  token_fingerprint TEXT NOT NULL UNIQUE,
  token_verifier TEXT NOT NULL,
  expires_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at TEXT,
  revoked_at TEXT
);
INSERT INTO api_tokens_canonical
  (id, principal_id, name, token_fingerprint, token_verifier, expires_at, created_at, last_used_at, revoked_at)
SELECT id, principal_id, name, token_fingerprint, token_verifier, expires_at, created_at, last_used_at, revoked_at
FROM api_tokens;
DROP TABLE api_tokens;
ALTER TABLE api_tokens_canonical RENAME TO api_tokens;
CREATE INDEX api_tokens_principal_idx ON api_tokens(principal_id, created_at DESC);

-- +goose Down
-- Forward-only: canonical identity storage is not downgraded to mutable role
-- templates or token capability columns.
