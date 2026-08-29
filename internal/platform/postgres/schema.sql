CREATE SCHEMA platform;

CREATE TABLE platform.schema_revision (
    revision bigint PRIMARY KEY,
    migration_id text NOT NULL,
    checksum text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
