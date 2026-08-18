-- +goose Up

-- Persist the authenticated maintenance principal that owns each GC cycle so
-- lifecycle events remain attributable after a restart.
ALTER TABLE delivery_gc_cycles ADD COLUMN actor_id TEXT NOT NULL DEFAULT 'gc'
  CHECK (length(actor_id) BETWEEN 1 AND 128
    AND actor_id = trim(actor_id)
    AND actor_id NOT GLOB '*[^A-Za-z0-9._:/-]*');

-- +goose Down

-- SQLite cannot drop a column on all supported runtimes; this pre-release
-- migration is intentionally forward-only.
