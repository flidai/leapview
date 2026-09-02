-- +goose Up

-- Physical pools are control/admission records only. DuckLake remains the
-- authority for table and data/delete-file membership; these tables never
-- store a physical manifest or secret values.
CREATE TABLE physical_pools (
  id TEXT PRIMARY KEY CHECK (length(id) = 71 AND substr(id, 1, 7) = 'sha256:' AND substr(id, 8) NOT GLOB '*[^0-9a-f]*'),
  identity_digest TEXT NOT NULL UNIQUE CHECK (length(identity_digest) = 71 AND substr(identity_digest, 1, 7) = 'sha256:' AND substr(identity_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  storage_location TEXT NOT NULL,
  storage_namespace TEXT NOT NULL,
  storage_implementation TEXT NOT NULL,
  object_naming_contract TEXT NOT NULL,
  region TEXT NOT NULL DEFAULT '',
  tenant TEXT NOT NULL DEFAULT '',
  isolation_boundary TEXT NOT NULL,
  encryption_key_ref TEXT NOT NULL DEFAULT '',
  credential_reference TEXT NOT NULL DEFAULT '',
  retention_authority TEXT NOT NULL,
  retention_policy_json TEXT NOT NULL CHECK (json_valid(retention_policy_json) AND json_type(retention_policy_json) = 'object'),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (storage_implementation, storage_location, storage_namespace),
  CHECK (id = identity_digest)
);

CREATE TABLE physical_pool_admissions (
  pool_id TEXT NOT NULL CHECK (length(pool_id) = 71 AND substr(pool_id, 1, 7) = 'sha256:' AND substr(pool_id, 8) NOT GLOB '*[^0-9a-f]*') REFERENCES physical_pools(id) ON DELETE CASCADE,
  compatibility_json TEXT NOT NULL CHECK (json_valid(compatibility_json) AND json_type(compatibility_json) = 'object'),
  -- Full non-secret conformance evidence is durable so admission can be
  -- reconstructed and verified after a process restart. It contains only the
  -- compatibility tuple, check IDs/outcomes/observation digests, version,
  -- and the content digest; no logs, paths, credentials, or file membership.
  evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json) AND json_type(evidence_json) = 'object'),
  evidence_digest TEXT NOT NULL CHECK (length(evidence_digest) = 71 AND substr(evidence_digest, 1, 7) = 'sha256:' AND substr(evidence_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  compatibility_digest TEXT NOT NULL CHECK (length(compatibility_digest) = 71 AND substr(compatibility_digest, 1, 7) = 'sha256:' AND substr(compatibility_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  conformance_version TEXT NOT NULL,
  admitted_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (pool_id, evidence_digest),
  UNIQUE (pool_id, compatibility_digest, evidence_digest)
);

-- A pool identity is content-addressed and cannot be retargeted or have its
-- retention authority/compatibility changed in place. Admission history is
-- append-only in physical_pool_admissions.
-- +goose StatementBegin
CREATE TRIGGER physical_pools_identity_immutable
BEFORE UPDATE OF id, identity_digest, storage_location, storage_namespace,
  storage_implementation, region, tenant, isolation_boundary,
  encryption_key_ref, credential_reference, retention_authority,
  retention_policy_json, object_naming_contract
ON physical_pools
BEGIN
  SELECT RAISE(ABORT, 'physical pool identity is immutable');
END;
-- +goose StatementEnd

-- Admission records are immutable evidence and may only be appended. A newer
-- runtime/extension/catalog tuple therefore joins the same stable pool without
-- replacing or rewriting an earlier admission.
-- +goose StatementBegin
CREATE TRIGGER physical_pool_admissions_append_only_update
BEFORE UPDATE ON physical_pool_admissions
BEGIN
  SELECT RAISE(ABORT, 'physical pool admissions are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER physical_pool_admissions_append_only_delete
BEFORE DELETE ON physical_pool_admissions
BEGIN
  SELECT RAISE(ABORT, 'physical pool admissions are append-only');
END;
-- +goose StatementEnd

-- A catalog binding is immutable after state changes to sealed. It stores
-- artifact identity and compatibility evidence, never table/file membership.
CREATE TABLE physical_catalog_bindings (
  id TEXT PRIMARY KEY,
  physical_pool_id TEXT NOT NULL CHECK (length(physical_pool_id) = 71 AND substr(physical_pool_id, 1, 7) = 'sha256:' AND substr(physical_pool_id, 8) NOT GLOB '*[^0-9a-f]*') REFERENCES physical_pools(id) ON DELETE RESTRICT,
  catalog_digest TEXT NOT NULL CHECK (length(catalog_digest) = 71 AND substr(catalog_digest, 1, 7) = 'sha256:' AND substr(catalog_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  object_key TEXT NOT NULL UNIQUE,
  size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
  compatibility_digest TEXT NOT NULL CHECK (length(compatibility_digest) = 71 AND substr(compatibility_digest, 1, 7) = 'sha256:' AND substr(compatibility_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  catalog_format TEXT NOT NULL,
  base_catalog_digest TEXT NOT NULL DEFAULT '' CHECK (base_catalog_digest = '' OR (length(base_catalog_digest) = 71 AND substr(base_catalog_digest, 1, 7) = 'sha256:' AND substr(base_catalog_digest, 8) NOT GLOB '*[^0-9a-f]*')),
  base_physical_pool_id TEXT NOT NULL DEFAULT '' CHECK (base_physical_pool_id = '' OR (length(base_physical_pool_id) = 71 AND substr(base_physical_pool_id, 1, 7) = 'sha256:' AND substr(base_physical_pool_id, 8) NOT GLOB '*[^0-9a-f]*')),
  evidence_digest TEXT NOT NULL CHECK (length(evidence_digest) = 71 AND substr(evidence_digest, 1, 7) = 'sha256:' AND substr(evidence_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  state TEXT NOT NULL CHECK (state IN ('working', 'sealed')),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  sealed_at TEXT,
  CHECK ((state = 'sealed') = (sealed_at IS NOT NULL)),
  CHECK (state <> 'sealed' OR size_bytes > 0),
  CHECK ((base_catalog_digest = '' AND base_physical_pool_id = '') OR (base_catalog_digest <> '' AND base_physical_pool_id <> '' AND base_physical_pool_id = physical_pool_id)),
  UNIQUE (physical_pool_id, catalog_digest),
  FOREIGN KEY (physical_pool_id, compatibility_digest, evidence_digest)
    REFERENCES physical_pool_admissions(pool_id, compatibility_digest, evidence_digest)
    ON DELETE RESTRICT
);

CREATE INDEX physical_catalog_bindings_pool_idx
  ON physical_catalog_bindings(physical_pool_id, state);

-- A sealed artifact's pool, bytes, key, size, and compatibility cannot be
-- changed in place. Retiring roots is a separate control-plane operation.
-- +goose StatementBegin
CREATE TRIGGER physical_catalog_bindings_sealed_immutable
BEFORE UPDATE ON physical_catalog_bindings
WHEN OLD.state = 'sealed'
BEGIN
  SELECT RAISE(ABORT, 'sealed physical catalog binding is immutable');
END;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER physical_catalog_bindings_sealed_immutable;
DROP TRIGGER physical_pool_admissions_append_only_delete;
DROP TRIGGER physical_pool_admissions_append_only_update;
DROP TRIGGER physical_pools_identity_immutable;
DROP INDEX physical_catalog_bindings_pool_idx;
DROP TABLE physical_catalog_bindings;
DROP TABLE physical_pool_admissions;
DROP TABLE physical_pools;
