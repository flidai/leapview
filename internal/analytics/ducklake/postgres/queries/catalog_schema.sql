-- sqlc analysis declarations for PostgreSQL system catalogs used by the
-- metadata-owner verification query. These declarations are parser-only and
-- are never applied at runtime.
CREATE TABLE pg_namespace (nspname text NOT NULL, nspowner oid NOT NULL);
CREATE TABLE pg_roles (oid oid NOT NULL, rolname text NOT NULL);
