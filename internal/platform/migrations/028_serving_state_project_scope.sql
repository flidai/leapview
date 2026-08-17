-- +goose Up
-- Serving-state project identity is part of the canonical baseline in 001.
SELECT 1;

-- +goose Down
SELECT 1;
