-- +goose Up
-- Snapshot leases are canonical in the serving-state baseline.
SELECT 1;

-- +goose Down
SELECT 1;
