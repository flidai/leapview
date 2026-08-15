-- +goose Up
-- Serving-state lifecycle metadata is canonical in the serving-state baseline.
SELECT 1;

-- +goose Down
SELECT 1;
