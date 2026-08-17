-- +goose Up
-- Serving-state environment and the project active pointer are canonical in 001.
SELECT 1;

-- +goose Down
SELECT 1;
