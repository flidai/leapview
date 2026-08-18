-- +goose Up

-- Preserve the provider generation observed when a bounded delete intent is
-- persisted. Digest remains the stable content identity; version prevents a
-- same-key replacement from being deleted after a crash/retry.
ALTER TABLE delivery_gc_delete_intents ADD COLUMN object_version TEXT;

-- +goose Down
ALTER TABLE delivery_gc_delete_intents DROP COLUMN object_version;
