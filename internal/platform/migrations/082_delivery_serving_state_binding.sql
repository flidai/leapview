-- +goose Up

-- Delivery roots retain the exact compiled serving-state identity and the
-- physical-pool compatibility tuple used to qualify their sealed catalog.
-- Empty values are retained for pre-migration rows; sealed serving rejects
-- those rows rather than inferring identity from a runtime request.
ALTER TABLE delivery_candidates ADD COLUMN serving_state_id TEXT NOT NULL DEFAULT '';
ALTER TABLE delivery_catalog_seals ADD COLUMN serving_state_id TEXT NOT NULL DEFAULT '';
ALTER TABLE delivery_generations ADD COLUMN serving_state_id TEXT NOT NULL DEFAULT '';
ALTER TABLE delivery_generations ADD COLUMN compatibility_digest TEXT NOT NULL DEFAULT '';

CREATE INDEX delivery_candidates_serving_state_idx ON delivery_candidates(serving_state_id);
CREATE INDEX delivery_generations_serving_state_idx ON delivery_generations(serving_state_id);

-- +goose Down
DROP INDEX IF EXISTS delivery_generations_serving_state_idx;
DROP INDEX IF EXISTS delivery_candidates_serving_state_idx;
ALTER TABLE delivery_generations DROP COLUMN compatibility_digest;
ALTER TABLE delivery_generations DROP COLUMN serving_state_id;
ALTER TABLE delivery_candidates DROP COLUMN serving_state_id;
ALTER TABLE delivery_catalog_seals DROP COLUMN serving_state_id;
