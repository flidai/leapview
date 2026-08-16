-- +goose Up
-- Forward-only migration: platform migrations do not rebuild SQLite tables for rollback.

-- Platform role bindings are defined in the canonical base schema. This
-- migration is retained as an empty historical slot for deployed databases.
