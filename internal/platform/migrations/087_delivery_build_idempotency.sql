-- +goose Up

-- Bind the build operation key to the durable attempt. Empty values preserve
-- compatibility for pre-canonical callers; canonical delivery always sends a
-- non-empty key. The partial unique index makes retries converge atomically
-- while allowing legacy rows without operation keys.
ALTER TABLE delivery_build_attempts ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT ''
  CHECK (idempotency_key = trim(idempotency_key));

CREATE UNIQUE INDEX delivery_build_attempts_plan_idempotency_idx
  ON delivery_build_attempts(plan_id, idempotency_key)
  WHERE idempotency_key <> '';

-- +goose Down

DROP INDEX delivery_build_attempts_plan_idempotency_idx;
