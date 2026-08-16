-- +goose Up

CREATE TABLE oauth_device_authorizations (
  id TEXT PRIMARY KEY,
  client_id TEXT NOT NULL,
  device_code_hash TEXT NOT NULL UNIQUE,
  user_code_hash TEXT NOT NULL UNIQUE,
  target_id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  capabilities_json TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'approved', 'denied', 'consumed')),
  principal_id TEXT REFERENCES principals(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  poll_interval_seconds INTEGER NOT NULL CHECK (poll_interval_seconds > 0),
  last_polled_at TEXT,
  created_at TEXT NOT NULL,
  approved_at TEXT,
  denied_at TEXT,
  consumed_at TEXT
);

CREATE INDEX oauth_device_authorizations_expiry_idx
  ON oauth_device_authorizations(expires_at);

CREATE TABLE oauth_authoring_sessions (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('human_cli', 'workload')),
  client_id TEXT NOT NULL,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  target_id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  capabilities_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  last_used_at TEXT,
  expires_at TEXT NOT NULL,
  revoked_at TEXT
);

CREATE INDEX oauth_authoring_sessions_principal_idx
  ON oauth_authoring_sessions(principal_id, created_at DESC);

CREATE INDEX oauth_authoring_sessions_expiry_idx
  ON oauth_authoring_sessions(expires_at);

CREATE TABLE oauth_authoring_credentials (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES oauth_authoring_sessions(id) ON DELETE CASCADE,
  access_token_hash TEXT NOT NULL UNIQUE,
  refresh_token_hash TEXT UNIQUE,
  access_expires_at TEXT NOT NULL,
  refresh_expires_at TEXT,
  active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
  created_at TEXT NOT NULL,
  replaced_at TEXT,
  CHECK (
    (refresh_token_hash IS NULL AND refresh_expires_at IS NULL)
    OR (refresh_token_hash IS NOT NULL AND refresh_expires_at IS NOT NULL)
  )
);

CREATE UNIQUE INDEX oauth_authoring_credentials_active_session_idx
  ON oauth_authoring_credentials(session_id)
  WHERE active = 1;

CREATE INDEX oauth_authoring_credentials_access_expiry_idx
  ON oauth_authoring_credentials(access_expires_at);

CREATE INDEX oauth_authoring_credentials_refresh_expiry_idx
  ON oauth_authoring_credentials(refresh_expires_at)
  WHERE refresh_expires_at IS NOT NULL;

-- +goose Down

DROP INDEX oauth_authoring_credentials_refresh_expiry_idx;
DROP INDEX oauth_authoring_credentials_access_expiry_idx;
DROP INDEX oauth_authoring_credentials_active_session_idx;
DROP TABLE oauth_authoring_credentials;
DROP INDEX oauth_authoring_sessions_expiry_idx;
DROP INDEX oauth_authoring_sessions_principal_idx;
DROP TABLE oauth_authoring_sessions;
DROP INDEX oauth_device_authorizations_expiry_idx;
DROP TABLE oauth_device_authorizations;
