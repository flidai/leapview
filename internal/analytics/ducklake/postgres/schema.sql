-- Minimal PostgreSQL control-plane ledger for one DuckLake catalog per
-- admitted physical pool. DuckLake's own metadata remains authoritative in
-- the separately provisioned leapview_ducklake database.
CREATE SCHEMA IF NOT EXISTS ducklake;

CREATE TABLE IF NOT EXISTS ducklake.catalog_identity (
    physical_pool_id       text PRIMARY KEY
        CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    catalog_database       text NOT NULL
        CHECK (catalog_database = btrim(catalog_database) AND octet_length(catalog_database) BETWEEN 1 AND 255),
    catalog_id             text NOT NULL
        CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    catalog_uuid           text NOT NULL
        CHECK (catalog_uuid = btrim(catalog_uuid)
            AND catalog_uuid ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
    metadata_schema        text NOT NULL
        CHECK (metadata_schema = btrim(metadata_schema) AND metadata_schema ~ '^[A-Za-z_][A-Za-z0-9_]*$'),
    compatibility_digest   text NOT NULL
        CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    catalog_schema_version text NOT NULL
        CHECK (catalog_schema_version = btrim(catalog_schema_version) AND octet_length(catalog_schema_version) BETWEEN 1 AND 128),
    created_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (physical_pool_id, catalog_id)
);

CREATE TABLE IF NOT EXISTS ducklake.catalog_runtime_compatibility (
    physical_pool_id       text PRIMARY KEY,
    catalog_id             text NOT NULL,
    duckdb_runtime         text NOT NULL
        CHECK (duckdb_runtime = btrim(duckdb_runtime) AND octet_length(duckdb_runtime) BETWEEN 1 AND 255),
    ducklake_extension     text NOT NULL
        CHECK (ducklake_extension = btrim(ducklake_extension) AND octet_length(ducklake_extension) BETWEEN 1 AND 255),
    catalog_format         text NOT NULL
        CHECK (catalog_format = btrim(catalog_format) AND octet_length(catalog_format) BETWEEN 1 AND 255),
    compatibility_digest   text NOT NULL
        CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    catalog_schema_version text NOT NULL
        CHECK (catalog_schema_version = btrim(catalog_schema_version) AND octet_length(catalog_schema_version) BETWEEN 1 AND 128),
    updated_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (physical_pool_id, catalog_id)
        REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id)
);

CREATE OR REPLACE FUNCTION ducklake.reject_catalog_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, ducklake
AS $$
BEGIN
    RAISE EXCEPTION 'DuckLake catalog identity is immutable';
END;
$$;

DROP TRIGGER IF EXISTS catalog_identity_immutable ON ducklake.catalog_identity;
CREATE TRIGGER catalog_identity_immutable
    BEFORE UPDATE OR DELETE ON ducklake.catalog_identity
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_catalog_identity_mutation();

REVOKE ALL ON SCHEMA ducklake FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA ducklake FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA ducklake FROM PUBLIC;
