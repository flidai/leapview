-- +goose Up
-- disabled_at is owned by provisioning (for example SCIM active=false).
-- blocked_at is an independent LeapView administrator override and must not be
-- cleared by a later provisioning update.
ALTER TABLE principals ADD COLUMN blocked_at TEXT;

-- +goose Down
ALTER TABLE principals DROP COLUMN blocked_at;
