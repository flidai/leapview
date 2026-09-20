-- Forward-only migration: credential-use evidence is retained for audit and
-- operational review. Existing rows remain unknown until first successful use.

-- +goose Up
ALTER TABLE service_principal_secrets ADD COLUMN last_used_at TEXT;

-- +goose Down
-- Platform migrations are forward-only; credential evidence is not removed.
