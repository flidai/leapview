-- +goose Up
ALTER TABLE delivery_generations
  ADD COLUMN rollback_external_effects_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(rollback_external_effects_json) AND json_type(rollback_external_effects_json) = 'array');

-- +goose Down
ALTER TABLE delivery_generations DROP COLUMN rollback_external_effects_json;

