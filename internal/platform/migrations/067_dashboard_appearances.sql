-- +goose Up
-- Dashboard appearance metadata is canonical in the serving-state baseline.
SELECT 1;

-- +goose Down
SELECT 1;
