-- +goose NO TRANSACTION

-- +goose Up
-- +goose StatementBegin

-- SQLite cannot alter a table CHECK constraint in place.  Rebuild only the
-- build-attempt table while foreign-key enforcement is temporarily disabled;
-- every dependent table keeps its original table name and therefore retains
-- its foreign keys when the replacement is renamed into place.  This
-- migration is intentionally forward-only below: a down migration that
-- reinstates the old CHECK would reject valid full-refresh rows.
PRAGMA foreign_keys = OFF;

CREATE TABLE delivery_build_attempts_088_new (
  id TEXT PRIMARY KEY
    CHECK (length(id) BETWEEN 1 AND 128
      AND id = trim(id)
      AND id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  plan_id TEXT NOT NULL REFERENCES delivery_plans(id),
  plan_digest TEXT NOT NULL CHECK (length(plan_digest) = 71
    AND substr(plan_digest, 1, 7) = 'sha256:'
    AND substr(plan_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  source_digest TEXT NOT NULL CHECK (length(source_digest) = 71
    AND substr(source_digest, 1, 7) = 'sha256:'
    AND substr(source_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  execution_digest TEXT NOT NULL CHECK (length(execution_digest) = 71
    AND substr(execution_digest, 1, 7) = 'sha256:'
    AND substr(execution_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  base_generation_id TEXT,
  base_catalog_digest TEXT,
  base_physical_pool_id TEXT REFERENCES physical_pools(id),
  physical_pool_id TEXT NOT NULL REFERENCES physical_pools(id),
  writer_lease_id TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('building', 'normalizing', 'validating', 'sealing', 'sealed', 'failed', 'abandoned')),
  seal_id TEXT,
  candidate_id TEXT,
  failure_code TEXT NOT NULL DEFAULT '',
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  terminal_at TEXT,
  idempotency_key TEXT NOT NULL DEFAULT ''
    CHECK (idempotency_key = trim(idempotency_key)),
  CHECK (base_generation_id IS NULL OR (
    length(base_generation_id) BETWEEN 1 AND 128
    AND base_generation_id = trim(base_generation_id)
    AND base_generation_id NOT GLOB '*[^A-Za-z0-9._:/-]*')),
  -- A base generation fences publication for both full refreshes and
  -- retained-base builds.  Retained catalog identity is optional, but when
  -- present its digest/pool pair remains all-or-nothing and exact to the
  -- build's physical pool.
  CHECK ((base_generation_id IS NULL AND base_catalog_digest IS NULL AND base_physical_pool_id IS NULL)
    OR (base_generation_id IS NOT NULL
      AND ((base_catalog_digest IS NULL AND base_physical_pool_id IS NULL)
        OR (base_catalog_digest IS NOT NULL AND base_physical_pool_id IS NOT NULL
          AND length(base_catalog_digest) = 71
          AND substr(base_catalog_digest, 1, 7) = 'sha256:'
          AND substr(base_catalog_digest, 8) NOT GLOB '*[^0-9a-f]*'
          AND base_physical_pool_id = trim(base_physical_pool_id)
          AND base_physical_pool_id = physical_pool_id)))),
  FOREIGN KEY (writer_lease_id, id, physical_pool_id)
    REFERENCES delivery_writer_leases(id, attempt_id, physical_pool_id),
  FOREIGN KEY (plan_id, plan_digest)
    REFERENCES delivery_plans(id, plan_digest),
  CHECK ((status IN ('building', 'normalizing', 'validating', 'sealing') AND terminal_at IS NULL AND failure_code = '' AND seal_id IS NULL AND candidate_id IS NULL)
    OR (status = 'sealed' AND terminal_at IS NOT NULL AND failure_code = '' AND seal_id IS NOT NULL AND candidate_id IS NOT NULL)
    OR (status IN ('failed', 'abandoned') AND terminal_at IS NOT NULL AND failure_code <> '' AND seal_id IS NULL AND candidate_id IS NULL)),
  UNIQUE(plan_id, id),
  UNIQUE(id, plan_id, plan_digest, execution_digest, physical_pool_id),
  UNIQUE(id, plan_id, plan_digest, execution_digest, physical_pool_id, base_catalog_digest, base_physical_pool_id)
);

INSERT INTO delivery_build_attempts_088_new (
  id, plan_id, plan_digest, source_digest, execution_digest,
  base_generation_id, base_catalog_digest, base_physical_pool_id,
  physical_pool_id, writer_lease_id, status, seal_id, candidate_id,
  failure_code, revision, created_at, updated_at, terminal_at, idempotency_key
)
SELECT
  id, plan_id, plan_digest, source_digest, execution_digest,
  base_generation_id, base_catalog_digest, base_physical_pool_id,
  physical_pool_id, writer_lease_id, status, seal_id, candidate_id,
  failure_code, revision, created_at, updated_at, terminal_at, idempotency_key
FROM delivery_build_attempts;

DROP INDEX delivery_build_attempts_sealed_plan_idx;
DROP INDEX delivery_build_attempts_plan_idempotency_idx;
DROP TABLE delivery_build_attempts;
ALTER TABLE delivery_build_attempts_088_new RENAME TO delivery_build_attempts;

CREATE UNIQUE INDEX delivery_build_attempts_sealed_plan_idx
  ON delivery_build_attempts(plan_id)
  WHERE status = 'sealed';

CREATE UNIQUE INDEX delivery_build_attempts_plan_idempotency_idx
  ON delivery_build_attempts(plan_id, idempotency_key)
  WHERE idempotency_key <> '';

-- Recreate the pool-fence triggers owned by the rebuilt table.  All other
-- delivery triggers remain attached to their original tables.
CREATE TRIGGER delivery_build_attempts_root_revision_insert
AFTER INSERT ON delivery_build_attempts
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
CREATE TRIGGER delivery_build_attempts_root_revision_update
AFTER UPDATE OF status, terminal_at ON delivery_build_attempts
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
CREATE TRIGGER delivery_build_attempts_root_revision_delete
AFTER DELETE ON delivery_build_attempts
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (OLD.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = OLD.physical_pool_id;
END;

PRAGMA foreign_keys = ON;
-- +goose StatementEnd

-- +goose Down

SELECT 1;
