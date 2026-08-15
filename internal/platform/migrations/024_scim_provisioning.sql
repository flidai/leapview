-- +goose Up
ALTER TABLE principals ADD COLUMN disabled_at TEXT;

-- SCIM principals and groups are global. Group membership carries no
-- workspace scope; the base schema already provides these tables.

-- +goose Down
SELECT 1;
