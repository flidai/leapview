-- +goose Up

-- LEA-411: one durable serialization point for every physical pool.  The
-- control store contains epochs and lease identity only; it never contains a
-- copy of DuckLake table or data-file membership.
CREATE TABLE delivery_pool_fences (
  physical_pool_id TEXT PRIMARY KEY REFERENCES physical_pools(id) ON DELETE CASCADE,
  writer_epoch INTEGER NOT NULL DEFAULT 0 CHECK (writer_epoch >= 0),
  gc_lease_epoch INTEGER NOT NULL DEFAULT 0 CHECK (gc_lease_epoch >= 0),
  root_revision INTEGER NOT NULL DEFAULT 0 CHECK (root_revision >= 0),
  gc_lease_id TEXT,
  gc_last_lease_id TEXT,
  gc_holder_id TEXT,
  gc_expires_at TEXT,
  gc_created_at TEXT,
  gc_root_revision INTEGER,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK ((gc_lease_id IS NULL AND gc_holder_id IS NULL AND gc_expires_at IS NULL AND gc_created_at IS NULL AND gc_root_revision IS NULL)
      OR (gc_lease_id IS NOT NULL AND gc_holder_id IS NOT NULL AND gc_expires_at IS NOT NULL AND gc_created_at IS NOT NULL AND gc_root_revision IS NOT NULL AND gc_root_revision >= 0))
);

CREATE TABLE delivery_gc_leases (
  id TEXT PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128 AND id = trim(id)),
  physical_pool_id TEXT NOT NULL REFERENCES physical_pools(id) ON DELETE CASCADE,
  holder_id TEXT NOT NULL CHECK (length(holder_id) BETWEEN 1 AND 128 AND holder_id = trim(holder_id)),
  epoch INTEGER NOT NULL CHECK (epoch > 0),
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('active','released','expired')),
  released_at TEXT,
  UNIQUE(physical_pool_id, epoch),
  CHECK (julianday(expires_at) > julianday(created_at)),
  CHECK ((status='active' AND released_at IS NULL) OR (status<>'active' AND released_at IS NOT NULL))
);

-- The registry is an exact, durable set of roots which do not have a useful
-- single row in one of the delivery tables (for example a quarantine hold).
-- source_id is an opaque control-plane identity; catalog_digest/object_key are
-- the complete catalog root, not a per-file manifest.
CREATE TABLE delivery_root_registry (
  physical_pool_id TEXT NOT NULL REFERENCES physical_pools(id) ON DELETE CASCADE,
  root_kind TEXT NOT NULL CHECK (root_kind IN ('candidate','build','published','rollback','retained','quarantined','lease')),
  source_id TEXT NOT NULL CHECK (length(source_id) BETWEEN 1 AND 128 AND source_id = trim(source_id) AND source_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  candidate_id TEXT,
  generation_id TEXT,
  lease_id TEXT,
  catalog_digest TEXT NOT NULL CHECK (length(catalog_digest) = 71 AND substr(catalog_digest, 1, 7) = 'sha256:' AND substr(catalog_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  object_key TEXT NOT NULL CHECK (length(object_key) BETWEEN 1 AND 1024 AND object_key = trim(object_key) AND object_key NOT LIKE '/%' AND object_key <> '.' AND object_key <> '..' AND object_key NOT LIKE './%' AND object_key NOT LIKE '../%' AND object_key NOT LIKE '%/../%' AND object_key NOT LIKE '%/./%' AND object_key NOT LIKE '%/.' AND object_key NOT LIKE '%/..' AND object_key NOT LIKE '%://%' AND object_key NOT LIKE '%//%'),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','retired','released','expired')),
  created_at TEXT NOT NULL,
  expires_at TEXT,
  retired_at TEXT,
  PRIMARY KEY (physical_pool_id, root_kind, source_id),
  CHECK (candidate_id IS NULL OR (length(candidate_id) BETWEEN 1 AND 128 AND candidate_id = trim(candidate_id))),
  CHECK (generation_id IS NULL OR (length(generation_id) BETWEEN 1 AND 128 AND generation_id = trim(generation_id))),
  CHECK (lease_id IS NULL OR (length(lease_id) BETWEEN 1 AND 128 AND lease_id = trim(lease_id))),
  CHECK (expires_at IS NULL OR julianday(expires_at) > julianday(created_at)),
  CHECK ((status = 'active' AND retired_at IS NULL) OR (status <> 'active' AND retired_at IS NOT NULL))
);
CREATE UNIQUE INDEX delivery_writer_leases_pool_epoch_idx ON delivery_writer_leases(physical_pool_id, epoch);
CREATE INDEX delivery_root_registry_digest_idx ON delivery_root_registry(physical_pool_id, catalog_digest, status);

-- A GC lease excludes every operation which can add or extend an immutable
-- catalog root. The checks are BEFORE triggers so an older repository cannot
-- bypass the pool fence by writing the base delivery table directly.
-- +goose StatementBegin
CREATE TRIGGER delivery_candidates_gc_guard
BEFORE INSERT ON delivery_candidates
BEGIN
  SELECT CASE WHEN EXISTS (SELECT 1 FROM delivery_pool_fences WHERE physical_pool_id=NEW.physical_pool_id AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(NEW.created_at)) THEN RAISE(ABORT,'GC lease excludes candidate root') END;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_candidates_gc_guard_update
BEFORE UPDATE OF status, ready_at, retired_at ON delivery_candidates
WHEN NEW.status IN ('preparing','ready')
BEGIN
  SELECT CASE WHEN EXISTS (SELECT 1 FROM delivery_pool_fences WHERE physical_pool_id=NEW.physical_pool_id AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(COALESCE(NEW.ready_at, NEW.created_at))) THEN RAISE(ABORT,'GC lease excludes candidate root') END;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_catalog_seals_gc_guard
BEFORE INSERT ON delivery_catalog_seals
BEGIN
  SELECT CASE WHEN EXISTS (SELECT 1 FROM delivery_pool_fences WHERE physical_pool_id=NEW.physical_pool_id AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(NEW.created_at)) THEN RAISE(ABORT,'GC lease excludes seal root') END;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_catalog_seals_gc_guard_update
BEFORE UPDATE OF status, verified_at ON delivery_catalog_seals
WHEN NEW.status IN ('preparing','uploaded','verified')
BEGIN
  SELECT CASE WHEN EXISTS (SELECT 1 FROM delivery_pool_fences WHERE physical_pool_id=NEW.physical_pool_id AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(NEW.created_at)) THEN RAISE(ABORT,'GC lease excludes seal root') END;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_generations_gc_guard
BEFORE INSERT ON delivery_generations
BEGIN
  SELECT CASE WHEN EXISTS (SELECT 1 FROM delivery_pool_fences WHERE physical_pool_id=NEW.physical_pool_id AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(NEW.created_at)) THEN RAISE(ABORT,'GC lease excludes generation root') END;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_generations_gc_guard_update
BEFORE UPDATE OF status, activated_at, rollback_until ON delivery_generations
WHEN NEW.status IN ('prepared','active')
BEGIN
  SELECT CASE WHEN EXISTS (SELECT 1 FROM delivery_pool_fences WHERE physical_pool_id=NEW.physical_pool_id AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(NEW.created_at)) THEN RAISE(ABORT,'GC lease excludes generation root') END;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_publications_gc_guard
BEFORE INSERT ON delivery_publications
BEGIN
  SELECT CASE WHEN EXISTS (SELECT 1 FROM delivery_pool_fences WHERE physical_pool_id=(SELECT physical_pool_id FROM delivery_candidates WHERE id=NEW.candidate_id) AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(NEW.created_at)) THEN RAISE(ABORT,'GC lease excludes publication root') END;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_retention_gc_guard
BEFORE INSERT ON delivery_retention_exceptions
BEGIN
  SELECT CASE WHEN EXISTS (SELECT 1 FROM delivery_pool_fences WHERE physical_pool_id=NEW.physical_pool_id AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(NEW.created_at)) THEN RAISE(ABORT,'GC lease excludes retention root') END;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_retention_gc_guard_update
BEFORE UPDATE OF status, expires_at ON delivery_retention_exceptions
WHEN NEW.status='active'
BEGIN
  SELECT CASE WHEN EXISTS (SELECT 1 FROM delivery_pool_fences WHERE physical_pool_id=NEW.physical_pool_id AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(NEW.created_at)) THEN RAISE(ABORT,'GC lease excludes retention root') END;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_query_leases_gc_guard
BEFORE INSERT ON delivery_query_leases
BEGIN
  SELECT CASE WHEN EXISTS (SELECT 1 FROM delivery_pool_fences WHERE physical_pool_id=NEW.physical_pool_id AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(NEW.created_at)) THEN RAISE(ABORT,'GC lease excludes query root') END;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_query_leases_gc_guard_update
BEFORE UPDATE OF status, expires_at ON delivery_query_leases
WHEN NEW.status='active'
BEGIN
  SELECT CASE WHEN EXISTS (SELECT 1 FROM delivery_pool_fences WHERE physical_pool_id=NEW.physical_pool_id AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(NEW.created_at)) THEN RAISE(ABORT,'GC lease excludes query root') END;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_root_registry_gc_guard
BEFORE INSERT ON delivery_root_registry
BEGIN
  SELECT CASE WHEN EXISTS (SELECT 1 FROM delivery_pool_fences WHERE physical_pool_id=NEW.physical_pool_id AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(NEW.created_at)) THEN RAISE(ABORT,'GC lease excludes explicit root') END;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_root_registry_gc_guard_update
BEFORE UPDATE OF status, expires_at ON delivery_root_registry
WHEN NEW.status='active'
BEGIN
  SELECT CASE WHEN EXISTS (SELECT 1 FROM delivery_pool_fences WHERE physical_pool_id=NEW.physical_pool_id AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(NEW.created_at)) THEN RAISE(ABORT,'GC lease excludes explicit root') END;
END;
-- +goose StatementEnd

-- Explicit registry changes participate in the same revision as delivery
-- lifecycle rows, including writes made by recovery tooling.
-- +goose StatementBegin
CREATE TRIGGER delivery_root_registry_revision_insert
AFTER INSERT ON delivery_root_registry
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision=root_revision+1,updated_at=CURRENT_TIMESTAMP WHERE physical_pool_id=NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_root_registry_revision_update
AFTER UPDATE OF status, expires_at, retired_at, catalog_digest, object_key ON delivery_root_registry
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision=root_revision+1,updated_at=CURRENT_TIMESTAMP WHERE physical_pool_id=NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_root_registry_revision_delete
AFTER DELETE ON delivery_root_registry
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (OLD.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision=root_revision+1,updated_at=CURRENT_TIMESTAMP WHERE physical_pool_id=OLD.physical_pool_id;
END;
-- +goose StatementEnd

-- Existing lifecycle rows are roots too.  These triggers make direct writes
-- through older repositories visible to the same durable root revision used
-- by the fencing repository.  INSERT OR IGNORE is intentionally idempotent
-- for databases upgraded from before LEA-411.
-- +goose StatementBegin
CREATE TRIGGER delivery_candidates_root_revision_insert
AFTER INSERT ON delivery_candidates
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_candidates_root_revision_update
AFTER UPDATE OF status, catalog_digest, catalog_object_key, retired_at ON delivery_candidates
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_candidates_root_revision_delete
AFTER DELETE ON delivery_candidates
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (OLD.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = OLD.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_build_attempts_root_revision_insert
AFTER INSERT ON delivery_build_attempts
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_build_attempts_root_revision_update
AFTER UPDATE OF status, terminal_at ON delivery_build_attempts
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_build_attempts_root_revision_delete
AFTER DELETE ON delivery_build_attempts
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (OLD.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = OLD.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_catalog_seals_root_revision_insert
AFTER INSERT ON delivery_catalog_seals
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_catalog_seals_root_revision_update
AFTER UPDATE OF status, verified_at ON delivery_catalog_seals
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_catalog_seals_root_revision_delete
AFTER DELETE ON delivery_catalog_seals
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (OLD.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = OLD.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_generations_root_revision_insert
AFTER INSERT ON delivery_generations
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_generations_root_revision_update
AFTER UPDATE OF status, retired_at, rollback_until ON delivery_generations
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_generations_root_revision_delete
AFTER DELETE ON delivery_generations
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (OLD.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = OLD.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_publications_root_revision_insert
AFTER INSERT ON delivery_publications
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id)
    SELECT c.physical_pool_id FROM delivery_candidates c WHERE c.id = NEW.candidate_id;
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1
    WHERE physical_pool_id = (SELECT c.physical_pool_id FROM delivery_candidates c WHERE c.id = NEW.candidate_id);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_publications_root_revision_update
AFTER UPDATE OF status, completed_at ON delivery_publications
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id)
    SELECT c.physical_pool_id FROM delivery_candidates c WHERE c.id = NEW.candidate_id;
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1
    WHERE physical_pool_id = (SELECT c.physical_pool_id FROM delivery_candidates c WHERE c.id = NEW.candidate_id);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_publications_root_revision_delete
AFTER DELETE ON delivery_publications
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id)
    SELECT c.physical_pool_id FROM delivery_candidates c WHERE c.id = OLD.candidate_id;
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1
    WHERE physical_pool_id = (SELECT c.physical_pool_id FROM delivery_candidates c WHERE c.id = OLD.candidate_id);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_retention_roots_revision_insert
AFTER INSERT ON delivery_retention_exceptions
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_retention_roots_revision_update
AFTER UPDATE OF status, released_at, expires_at ON delivery_retention_exceptions
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_retention_roots_revision_delete
AFTER DELETE ON delivery_retention_exceptions
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (OLD.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = OLD.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_query_leases_root_revision_insert
AFTER INSERT ON delivery_query_leases
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_query_leases_root_revision_update
AFTER UPDATE OF status, released_at, expires_at ON delivery_query_leases
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_query_leases_root_revision_delete
AFTER DELETE ON delivery_query_leases
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (OLD.physical_pool_id);
  UPDATE delivery_pool_fences SET root_revision = root_revision + 1, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = OLD.physical_pool_id;
END;
-- +goose StatementEnd

-- Legacy delivery writer leases are accepted only when no GC owns the pool;
-- the adapter routes normal creation through the global writer epoch fence.
-- This trigger prevents a direct writer insert from bypassing the GC fence.
-- +goose StatementBegin
CREATE TRIGGER delivery_writer_leases_fence_guard
BEFORE INSERT ON delivery_writer_leases
BEGIN
  SELECT CASE WHEN EXISTS (
    SELECT 1 FROM delivery_pool_fences
    WHERE physical_pool_id = NEW.physical_pool_id
      AND gc_lease_id IS NOT NULL AND julianday(gc_expires_at) > julianday(NEW.created_at)
  ) THEN RAISE(ABORT, 'GC lease excludes writer') END;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_writer_leases_epoch_observe
AFTER INSERT ON delivery_writer_leases
BEGIN
  INSERT OR IGNORE INTO delivery_pool_fences(physical_pool_id) VALUES (NEW.physical_pool_id);
  UPDATE delivery_pool_fences SET writer_epoch = CASE WHEN writer_epoch < NEW.epoch THEN NEW.epoch ELSE writer_epoch END, updated_at = CURRENT_TIMESTAMP WHERE physical_pool_id = NEW.physical_pool_id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER delivery_query_leases_root_revision_update;
DROP TRIGGER delivery_query_leases_root_revision_insert;
DROP TRIGGER delivery_root_registry_gc_guard;
DROP TRIGGER delivery_root_registry_gc_guard_update;
DROP TRIGGER delivery_query_leases_gc_guard;
DROP TRIGGER delivery_query_leases_gc_guard_update;
DROP TRIGGER delivery_retention_gc_guard;
DROP TRIGGER delivery_retention_gc_guard_update;
DROP TRIGGER delivery_publications_gc_guard;
DROP TRIGGER delivery_generations_gc_guard;
DROP TRIGGER delivery_generations_gc_guard_update;
DROP TRIGGER delivery_catalog_seals_gc_guard;
DROP TRIGGER delivery_catalog_seals_gc_guard_update;
DROP TRIGGER delivery_candidates_gc_guard;
DROP TRIGGER delivery_candidates_gc_guard_update;
DROP TRIGGER delivery_query_leases_root_revision_delete;
DROP TRIGGER delivery_root_registry_revision_delete;
DROP TRIGGER delivery_root_registry_revision_update;
DROP TRIGGER delivery_root_registry_revision_insert;
DROP TRIGGER delivery_retention_roots_revision_delete;
DROP TRIGGER delivery_publications_root_revision_delete;
DROP TRIGGER delivery_generations_root_revision_delete;
DROP TRIGGER delivery_catalog_seals_root_revision_delete;
DROP TRIGGER delivery_build_attempts_root_revision_delete;
DROP TRIGGER delivery_candidates_root_revision_delete;
DROP TRIGGER delivery_writer_leases_epoch_observe;
DROP TRIGGER delivery_writer_leases_fence_guard;
DROP TRIGGER delivery_retention_roots_revision_update;
DROP TRIGGER delivery_retention_roots_revision_insert;
DROP TRIGGER delivery_publications_root_revision_update;
DROP TRIGGER delivery_publications_root_revision_insert;
DROP TRIGGER delivery_generations_root_revision_update;
DROP TRIGGER delivery_generations_root_revision_insert;
DROP TRIGGER delivery_catalog_seals_root_revision_update;
DROP TRIGGER delivery_catalog_seals_root_revision_insert;
DROP TRIGGER delivery_build_attempts_root_revision_update;
DROP TRIGGER delivery_build_attempts_root_revision_insert;
DROP TRIGGER delivery_candidates_root_revision_update;
DROP TRIGGER delivery_candidates_root_revision_insert;
DROP INDEX delivery_root_registry_digest_idx;
DROP TABLE delivery_root_registry;
DROP INDEX delivery_writer_leases_pool_epoch_idx;
DROP TABLE delivery_gc_leases;
DROP TABLE delivery_pool_fences;
