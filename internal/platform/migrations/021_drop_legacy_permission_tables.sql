-- +goose Up
-- Remove obsolete string permission tables from early development schemas.
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS permissions;

-- +goose Down
CREATE TABLE IF NOT EXISTS permissions (
  name TEXT PRIMARY KEY,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS role_permissions (
  role_id TEXT NOT NULL,
  permission_name TEXT NOT NULL REFERENCES permissions(name) ON DELETE CASCADE,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(role_id, permission_name)
);
