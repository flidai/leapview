-- +goose Up
-- DuckLake snapshot identity is canonical in the serving-state baseline.
SELECT 1;

-- +goose Down
SELECT 1;
