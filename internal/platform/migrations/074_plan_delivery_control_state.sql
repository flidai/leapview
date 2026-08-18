-- +goose Up

-- LEA-404: target-owned plan/build/seal/publication control state. The
-- immutable DuckLake catalog remains authoritative for table and file
-- membership; this migration intentionally contains no per-file membership
-- or reference-count tables.

CREATE TABLE delivery_target_revisions (
  target_id TEXT PRIMARY KEY
    CHECK (length(target_id) BETWEEN 1 AND 128
      AND target_id = trim(target_id)
      AND target_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  project_id TEXT NOT NULL
    CHECK (length(project_id) BETWEEN 1 AND 128
      AND project_id = trim(project_id)
      AND project_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  environment TEXT NOT NULL
    CHECK (length(environment) BETWEEN 1 AND 128
      AND environment = trim(environment)
      AND environment NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  target_revision INTEGER NOT NULL DEFAULT 0 CHECK (target_revision >= 0),
  active_generation_id TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(project_id, environment),
  UNIQUE(target_id, project_id, environment),
  CHECK (active_generation_id IS NULL OR (
    length(active_generation_id) BETWEEN 1 AND 128
    AND active_generation_id = trim(active_generation_id)
    AND active_generation_id NOT GLOB '*[^A-Za-z0-9._:/-]*'))
);

CREATE TABLE delivery_plans (
  id TEXT PRIMARY KEY
    CHECK (length(id) BETWEEN 1 AND 128
      AND id = trim(id)
      AND id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  target_id TEXT NOT NULL REFERENCES delivery_target_revisions(target_id),
  project_id TEXT NOT NULL
    CHECK (length(project_id) BETWEEN 1 AND 128
      AND project_id = trim(project_id)
      AND project_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  environment TEXT NOT NULL
    CHECK (length(environment) BETWEEN 1 AND 128
      AND environment = trim(environment)
      AND environment NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  operation_kind TEXT NOT NULL CHECK (operation_kind IN ('code_change', 'restatement', 'binding_change', 'policy_change')),
  source_digest TEXT NOT NULL CHECK (length(source_digest) = 71
    AND substr(source_digest, 1, 7) = 'sha256:'
    AND substr(source_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  base_generation_id TEXT,
  base_target_revision INTEGER NOT NULL CHECK (base_target_revision >= 0),
  execution_digest TEXT NOT NULL CHECK (length(execution_digest) = 71
    AND substr(execution_digest, 1, 7) = 'sha256:'
    AND substr(execution_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  execution_inputs_json TEXT NOT NULL CHECK (json_valid(execution_inputs_json) AND json_type(execution_inputs_json) = 'object'),
  -- Canonical non-secret evidence is retained so a plan can be reconstructed;
  -- the corresponding digests remain the immutable identity fields.
  provenance_json TEXT NOT NULL CHECK (json_valid(provenance_json) AND json_type(provenance_json) = 'object'),
  governance_json TEXT NOT NULL CHECK (json_valid(governance_json) AND json_type(governance_json) = 'object'),
  provenance_digest TEXT NOT NULL CHECK (length(provenance_digest) = 71
    AND substr(provenance_digest, 1, 7) = 'sha256:'
    AND substr(provenance_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  governance_digest TEXT NOT NULL CHECK (length(governance_digest) = 71
    AND substr(governance_digest, 1, 7) = 'sha256:'
    AND substr(governance_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  plan_digest TEXT NOT NULL CHECK (length(plan_digest) = 71
    AND substr(plan_digest, 1, 7) = 'sha256:'
    AND substr(plan_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  status TEXT NOT NULL DEFAULT 'planned' CHECK (status IN ('planned', 'expired')),
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(id, plan_digest),
  UNIQUE(id, plan_digest, source_digest, execution_digest),
  UNIQUE(target_id, plan_digest),
  FOREIGN KEY (target_id, project_id, environment)
    REFERENCES delivery_target_revisions(target_id, project_id, environment),
  CHECK (base_generation_id IS NULL OR (
    length(base_generation_id) BETWEEN 1 AND 128
    AND base_generation_id = trim(base_generation_id)
    AND base_generation_id NOT GLOB '*[^A-Za-z0-9._:/-]*'))
);

CREATE TABLE delivery_writer_leases (
  id TEXT PRIMARY KEY
    CHECK (length(id) BETWEEN 1 AND 128
      AND id = trim(id)
      AND id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  attempt_id TEXT NOT NULL CHECK (length(attempt_id) BETWEEN 1 AND 128
    AND attempt_id = trim(attempt_id)
    AND attempt_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  physical_pool_id TEXT NOT NULL REFERENCES physical_pools(id),
  owner_id TEXT NOT NULL
    CHECK (length(owner_id) BETWEEN 1 AND 128
      AND owner_id = trim(owner_id)
      AND owner_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  epoch INTEGER NOT NULL CHECK (epoch > 0),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'released', 'expired')),
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  released_at TEXT
);

CREATE UNIQUE INDEX delivery_writer_leases_identity_idx
  ON delivery_writer_leases(id, attempt_id, physical_pool_id);

CREATE UNIQUE INDEX delivery_writer_leases_active_attempt_idx
  ON delivery_writer_leases(attempt_id)
  WHERE status = 'active';

CREATE TABLE delivery_build_attempts (
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
  CHECK (base_generation_id IS NULL OR (
    length(base_generation_id) BETWEEN 1 AND 128
    AND base_generation_id = trim(base_generation_id)
    AND base_generation_id NOT GLOB '*[^A-Za-z0-9._:/-]*')),
  CHECK ((base_generation_id IS NULL AND base_catalog_digest IS NULL AND base_physical_pool_id IS NULL)
    OR (base_generation_id IS NOT NULL AND base_catalog_digest IS NOT NULL AND base_physical_pool_id IS NOT NULL
      AND length(base_catalog_digest) = 71
      AND substr(base_catalog_digest, 1, 7) = 'sha256:'
      AND substr(base_catalog_digest, 8) NOT GLOB '*[^0-9a-f]*'
      AND base_physical_pool_id = trim(base_physical_pool_id)
      AND base_physical_pool_id = physical_pool_id)),
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

CREATE UNIQUE INDEX delivery_build_attempts_sealed_plan_idx
  ON delivery_build_attempts(plan_id)
  WHERE status = 'sealed';

CREATE TABLE delivery_catalog_seals (
  id TEXT PRIMARY KEY
    CHECK (length(id) BETWEEN 1 AND 128
      AND id = trim(id)
      AND id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  attempt_id TEXT NOT NULL REFERENCES delivery_build_attempts(id),
  plan_id TEXT NOT NULL REFERENCES delivery_plans(id),
  plan_digest TEXT NOT NULL CHECK (length(plan_digest) = 71
    AND substr(plan_digest, 1, 7) = 'sha256:'
    AND substr(plan_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  execution_digest TEXT NOT NULL CHECK (length(execution_digest) = 71
    AND substr(execution_digest, 1, 7) = 'sha256:'
    AND substr(execution_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  physical_pool_id TEXT NOT NULL REFERENCES physical_pools(id),
  catalog_digest TEXT NOT NULL CHECK (length(catalog_digest) = 71
    AND substr(catalog_digest, 1, 7) = 'sha256:'
    AND substr(catalog_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  base_catalog_digest TEXT,
  base_physical_pool_id TEXT REFERENCES physical_pools(id),
  compatibility_digest TEXT NOT NULL CHECK (length(compatibility_digest) = 71
    AND substr(compatibility_digest, 1, 7) = 'sha256:'
    AND substr(compatibility_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  object_key TEXT NOT NULL CHECK (object_key = trim(object_key) AND length(object_key) BETWEEN 1 AND 1024
    AND object_key NOT LIKE '/%' AND object_key <> '.' AND object_key <> '..'
    AND object_key NOT LIKE './%' AND object_key NOT LIKE '../%'
    AND object_key NOT LIKE '%/./%' AND object_key NOT LIKE '%/../%'
    AND object_key NOT LIKE '%/.' AND object_key NOT LIKE '%/..'
    AND object_key NOT LIKE '%//%' AND object_key NOT LIKE '%://%'),
  object_size INTEGER NOT NULL CHECK (object_size > 0),
  closure_digest TEXT,
  qualification_digest TEXT,
  status TEXT NOT NULL CHECK (status IN ('preparing', 'uploaded', 'verified', 'failed')),
  failure_code TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  verified_at TEXT,
  UNIQUE(attempt_id),
  FOREIGN KEY (attempt_id, plan_id, plan_digest, execution_digest, physical_pool_id)
    REFERENCES delivery_build_attempts(id, plan_id, plan_digest, execution_digest, physical_pool_id),
  FOREIGN KEY (attempt_id, plan_id, plan_digest, execution_digest, physical_pool_id, base_catalog_digest, base_physical_pool_id)
    REFERENCES delivery_build_attempts(id, plan_id, plan_digest, execution_digest, physical_pool_id, base_catalog_digest, base_physical_pool_id),
  UNIQUE(object_key),
  UNIQUE(id, plan_digest, physical_pool_id, catalog_digest, compatibility_digest),
  UNIQUE(id, plan_digest, execution_digest, physical_pool_id, catalog_digest, compatibility_digest, object_key),
  UNIQUE(id, plan_digest, physical_pool_id, catalog_digest, compatibility_digest, base_catalog_digest, base_physical_pool_id, qualification_digest),
  FOREIGN KEY (plan_id, plan_digest)
    REFERENCES delivery_plans(id, plan_digest),
  CHECK ((base_catalog_digest IS NULL AND base_physical_pool_id IS NULL) OR
    (base_catalog_digest IS NOT NULL AND base_physical_pool_id IS NOT NULL
      AND length(base_catalog_digest) = 71 AND substr(base_catalog_digest, 1, 7) = 'sha256:'
      AND substr(base_catalog_digest, 8) NOT GLOB '*[^0-9a-f]*'
      AND base_physical_pool_id = trim(base_physical_pool_id)
      AND base_physical_pool_id = physical_pool_id)),
  CHECK ((status IN ('preparing', 'uploaded') AND closure_digest IS NULL AND qualification_digest IS NULL AND verified_at IS NULL AND failure_code = '')
    OR (status = 'verified' AND closure_digest IS NOT NULL AND qualification_digest IS NOT NULL AND verified_at IS NOT NULL AND failure_code = '')
    OR (status = 'failed' AND closure_digest IS NULL AND qualification_digest IS NULL AND verified_at IS NULL AND failure_code <> '')),
  CHECK (closure_digest IS NULL OR (length(closure_digest) = 71 AND substr(closure_digest, 1, 7) = 'sha256:' AND substr(closure_digest, 8) NOT GLOB '*[^0-9a-f]*')),
  CHECK (qualification_digest IS NULL OR (length(qualification_digest) = 71 AND substr(qualification_digest, 1, 7) = 'sha256:' AND substr(qualification_digest, 8) NOT GLOB '*[^0-9a-f]*'))
);

CREATE TABLE delivery_candidates (
  id TEXT PRIMARY KEY
    CHECK (length(id) BETWEEN 1 AND 128
      AND id = trim(id)
      AND id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  plan_id TEXT NOT NULL REFERENCES delivery_plans(id),
  plan_digest TEXT NOT NULL CHECK (length(plan_digest) = 71
    AND substr(plan_digest, 1, 7) = 'sha256:'
    AND substr(plan_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  target_id TEXT NOT NULL REFERENCES delivery_target_revisions(target_id),
  project_id TEXT NOT NULL CHECK (length(project_id) BETWEEN 1 AND 128
    AND project_id = trim(project_id)
    AND project_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  environment TEXT NOT NULL CHECK (length(environment) BETWEEN 1 AND 128
    AND environment = trim(environment)
    AND environment NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  source_digest TEXT NOT NULL CHECK (length(source_digest) = 71
    AND substr(source_digest, 1, 7) = 'sha256:'
    AND substr(source_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  execution_digest TEXT NOT NULL CHECK (length(execution_digest) = 71
    AND substr(execution_digest, 1, 7) = 'sha256:'
    AND substr(execution_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  base_generation_id TEXT,
  base_target_revision INTEGER NOT NULL CHECK (base_target_revision >= 0),
  seal_id TEXT NOT NULL REFERENCES delivery_catalog_seals(id),
  catalog_digest TEXT NOT NULL CHECK (length(catalog_digest) = 71
    AND substr(catalog_digest, 1, 7) = 'sha256:'
    AND substr(catalog_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  base_catalog_digest TEXT,
  base_physical_pool_id TEXT REFERENCES physical_pools(id),
  compatibility_digest TEXT NOT NULL CHECK (length(compatibility_digest) = 71
    AND substr(compatibility_digest, 1, 7) = 'sha256:'
    AND substr(compatibility_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  catalog_object_key TEXT NOT NULL CHECK (catalog_object_key = trim(catalog_object_key) AND length(catalog_object_key) BETWEEN 1 AND 1024
    AND catalog_object_key NOT LIKE '/%' AND catalog_object_key <> '.' AND catalog_object_key <> '..'
    AND catalog_object_key NOT LIKE './%' AND catalog_object_key NOT LIKE '../%'
    AND catalog_object_key NOT LIKE '%/./%' AND catalog_object_key NOT LIKE '%/../%'
    AND catalog_object_key NOT LIKE '%/.' AND catalog_object_key NOT LIKE '%/..'
    AND catalog_object_key NOT LIKE '%//%' AND catalog_object_key NOT LIKE '%://%'),
  physical_pool_id TEXT NOT NULL REFERENCES physical_pools(id),
  qualification_digest TEXT,
  status TEXT NOT NULL CHECK (status IN ('preparing', 'ready', 'failed', 'retired')),
  failure_code TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  ready_at TEXT,
  retired_at TEXT,
  UNIQUE(plan_id, seal_id),
  UNIQUE(id, plan_id, plan_digest, target_id, project_id, environment, catalog_digest, physical_pool_id, compatibility_digest),
  UNIQUE(id, plan_id, plan_digest, target_id),
  UNIQUE(id, catalog_digest, physical_pool_id),
  UNIQUE(id, plan_id, plan_digest, target_id, project_id, environment, catalog_digest, catalog_object_key, physical_pool_id),
  FOREIGN KEY (plan_id, plan_digest)
    REFERENCES delivery_plans(id, plan_digest),
  FOREIGN KEY (plan_id, plan_digest, source_digest, execution_digest)
    REFERENCES delivery_plans(id, plan_digest, source_digest, execution_digest),
  FOREIGN KEY (seal_id, plan_digest, physical_pool_id, catalog_digest, compatibility_digest)
    REFERENCES delivery_catalog_seals(id, plan_digest, physical_pool_id, catalog_digest, compatibility_digest),
  FOREIGN KEY (seal_id, plan_digest, execution_digest, physical_pool_id, catalog_digest, compatibility_digest, catalog_object_key)
    REFERENCES delivery_catalog_seals(id, plan_digest, execution_digest, physical_pool_id, catalog_digest, compatibility_digest, object_key),
  FOREIGN KEY (seal_id, plan_digest, physical_pool_id, catalog_digest, compatibility_digest, base_catalog_digest, base_physical_pool_id, qualification_digest)
    REFERENCES delivery_catalog_seals(id, plan_digest, physical_pool_id, catalog_digest, compatibility_digest, base_catalog_digest, base_physical_pool_id, qualification_digest),
  CHECK (base_generation_id IS NULL OR (length(base_generation_id) BETWEEN 1 AND 128
    AND base_generation_id = trim(base_generation_id)
    AND base_generation_id NOT GLOB '*[^A-Za-z0-9._:/-]*')),
  CHECK ((base_catalog_digest IS NULL AND base_physical_pool_id IS NULL) OR
    (base_catalog_digest IS NOT NULL AND base_physical_pool_id IS NOT NULL
      AND length(base_catalog_digest) = 71 AND substr(base_catalog_digest, 1, 7) = 'sha256:'
      AND substr(base_catalog_digest, 8) NOT GLOB '*[^0-9a-f]*'
      AND base_physical_pool_id = trim(base_physical_pool_id)
      AND base_physical_pool_id = physical_pool_id)),
  CHECK ((status = 'preparing' AND qualification_digest IS NULL AND ready_at IS NULL AND retired_at IS NULL AND failure_code = '')
    OR (status = 'ready' AND qualification_digest IS NOT NULL AND ready_at IS NOT NULL AND retired_at IS NULL AND failure_code = '')
    OR (status = 'failed' AND qualification_digest IS NULL AND ready_at IS NULL AND retired_at IS NULL AND failure_code <> '')
    OR (status = 'retired' AND qualification_digest IS NOT NULL AND ready_at IS NOT NULL AND retired_at IS NOT NULL AND failure_code = '')),
  CHECK (qualification_digest IS NULL OR (length(qualification_digest) = 71 AND substr(qualification_digest, 1, 7) = 'sha256:' AND substr(qualification_digest, 8) NOT GLOB '*[^0-9a-f]*'))
);

CREATE TABLE delivery_generations (
  id TEXT PRIMARY KEY
    CHECK (length(id) BETWEEN 1 AND 128
      AND id = trim(id)
      AND id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  candidate_id TEXT NOT NULL REFERENCES delivery_candidates(id),
  plan_id TEXT NOT NULL REFERENCES delivery_plans(id),
  plan_digest TEXT NOT NULL CHECK (length(plan_digest) = 71
    AND substr(plan_digest, 1, 7) = 'sha256:'
    AND substr(plan_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  target_id TEXT NOT NULL REFERENCES delivery_target_revisions(target_id),
  project_id TEXT NOT NULL CHECK (length(project_id) BETWEEN 1 AND 128
    AND project_id = trim(project_id)
    AND project_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  environment TEXT NOT NULL CHECK (length(environment) BETWEEN 1 AND 128
    AND environment = trim(environment)
    AND environment NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  catalog_digest TEXT NOT NULL CHECK (length(catalog_digest) = 71
    AND substr(catalog_digest, 1, 7) = 'sha256:'
    AND substr(catalog_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  catalog_object_key TEXT NOT NULL CHECK (catalog_object_key = trim(catalog_object_key) AND length(catalog_object_key) BETWEEN 1 AND 1024
    AND catalog_object_key NOT LIKE '/%' AND catalog_object_key <> '.' AND catalog_object_key <> '..'
    AND catalog_object_key NOT LIKE './%' AND catalog_object_key NOT LIKE '../%'
    AND catalog_object_key NOT LIKE '%/./%' AND catalog_object_key NOT LIKE '%/../%'
    AND catalog_object_key NOT LIKE '%/.' AND catalog_object_key NOT LIKE '%/..'
    AND catalog_object_key NOT LIKE '%//%' AND catalog_object_key NOT LIKE '%://%'),
  physical_pool_id TEXT NOT NULL REFERENCES physical_pools(id),
  rollback_class TEXT NOT NULL CHECK (rollback_class IN ('rollback_safe', 'serving_safe', 'non_reversible')),
  status TEXT NOT NULL CHECK (status IN ('prepared', 'active', 'retired')),
  created_at TEXT NOT NULL,
  activated_at TEXT,
  retired_at TEXT,
  rollback_until TEXT,
  UNIQUE(candidate_id),
  UNIQUE(id, candidate_id, plan_id, plan_digest, target_id, project_id, environment, catalog_digest, catalog_object_key, physical_pool_id),
  UNIQUE(id, plan_id, plan_digest, target_id),
  UNIQUE(id, catalog_digest, physical_pool_id),
  UNIQUE(id, candidate_id, plan_id, plan_digest, target_id),
  FOREIGN KEY (plan_id, plan_digest)
    REFERENCES delivery_plans(id, plan_digest),
  FOREIGN KEY (candidate_id, plan_id, plan_digest, target_id, project_id, environment, catalog_digest, catalog_object_key, physical_pool_id)
    REFERENCES delivery_candidates(id, plan_id, plan_digest, target_id, project_id, environment, catalog_digest, catalog_object_key, physical_pool_id),
  CHECK ((status = 'prepared' AND activated_at IS NULL AND retired_at IS NULL)
    OR (status = 'active' AND activated_at IS NOT NULL AND retired_at IS NULL)
    OR (status = 'retired' AND activated_at IS NOT NULL AND retired_at IS NOT NULL))
);

CREATE TABLE delivery_retention_exceptions (
  id TEXT PRIMARY KEY
    CHECK (length(id) BETWEEN 1 AND 128
      AND id = trim(id)
      AND id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  physical_pool_id TEXT NOT NULL REFERENCES physical_pools(id),
  candidate_id TEXT REFERENCES delivery_candidates(id),
  generation_id TEXT REFERENCES delivery_generations(id),
  catalog_digest TEXT NOT NULL CHECK (length(catalog_digest) = 71
    AND substr(catalog_digest, 1, 7) = 'sha256:'
    AND substr(catalog_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  reason TEXT NOT NULL CHECK (reason = trim(reason) AND length(reason) BETWEEN 1 AND 1024),
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  released_at TEXT,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'released')),
  CHECK ((candidate_id IS NOT NULL AND generation_id IS NULL) OR (candidate_id IS NULL AND generation_id IS NOT NULL)),
  FOREIGN KEY (candidate_id, catalog_digest, physical_pool_id)
    REFERENCES delivery_candidates(id, catalog_digest, physical_pool_id),
  FOREIGN KEY (generation_id, catalog_digest, physical_pool_id)
    REFERENCES delivery_generations(id, catalog_digest, physical_pool_id),
  CHECK ((status = 'active' AND released_at IS NULL) OR (status = 'released' AND released_at IS NOT NULL))
);

CREATE TABLE delivery_publications (
  id TEXT PRIMARY KEY
    CHECK (length(id) BETWEEN 1 AND 128
      AND id = trim(id)
      AND id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  request_digest TEXT NOT NULL CHECK (length(request_digest) = 71
    AND substr(request_digest, 1, 7) = 'sha256:'
    AND substr(request_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  target_id TEXT NOT NULL REFERENCES delivery_target_revisions(target_id),
  project_id TEXT NOT NULL CHECK (length(project_id) BETWEEN 1 AND 128
    AND project_id = trim(project_id)
    AND project_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  environment TEXT NOT NULL CHECK (length(environment) BETWEEN 1 AND 128
    AND environment = trim(environment)
    AND environment NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  plan_id TEXT NOT NULL REFERENCES delivery_plans(id),
  plan_digest TEXT NOT NULL CHECK (length(plan_digest) = 71
    AND substr(plan_digest, 1, 7) = 'sha256:'
    AND substr(plan_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  candidate_id TEXT NOT NULL REFERENCES delivery_candidates(id),
  generation_id TEXT NOT NULL REFERENCES delivery_generations(id),
  expected_base_generation_id TEXT,
  expected_target_revision INTEGER NOT NULL CHECK (expected_target_revision >= 0),
  result_target_revision INTEGER NOT NULL DEFAULT 0 CHECK (result_target_revision >= 0),
  status TEXT NOT NULL CHECK (status IN ('pending', 'committed', 'rejected', 'indeterminate')),
  reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  completed_at TEXT,
  UNIQUE(target_id, request_digest),
  FOREIGN KEY (target_id, project_id, environment)
    REFERENCES delivery_target_revisions(target_id, project_id, environment),
  FOREIGN KEY (plan_id, plan_digest)
    REFERENCES delivery_plans(id, plan_digest),
  FOREIGN KEY (candidate_id, plan_id, plan_digest, target_id)
    REFERENCES delivery_candidates(id, plan_id, plan_digest, target_id),
  FOREIGN KEY (generation_id, plan_id, plan_digest, target_id)
    REFERENCES delivery_generations(id, plan_id, plan_digest, target_id),
  FOREIGN KEY (generation_id, candidate_id, plan_id, plan_digest, target_id)
    REFERENCES delivery_generations(id, candidate_id, plan_id, plan_digest, target_id),
  CHECK (expected_base_generation_id IS NULL OR (length(expected_base_generation_id) BETWEEN 1 AND 128
    AND expected_base_generation_id = trim(expected_base_generation_id)
    AND expected_base_generation_id NOT GLOB '*[^A-Za-z0-9._:/-]*')),
  CHECK ((status = 'pending' AND completed_at IS NULL AND result_target_revision = 0 AND reason = '')
    OR (status = 'indeterminate' AND completed_at IS NOT NULL AND result_target_revision = 0)
    OR (status = 'committed' AND completed_at IS NOT NULL AND result_target_revision = expected_target_revision + 1 AND reason = '')
    OR (status = 'rejected' AND completed_at IS NOT NULL AND result_target_revision = 0 AND reason <> ''))
);

CREATE TABLE delivery_query_leases (
  id TEXT PRIMARY KEY
    CHECK (length(id) BETWEEN 1 AND 128
      AND id = trim(id)
      AND id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  holder_id TEXT NOT NULL
    CHECK (length(holder_id) BETWEEN 1 AND 128
      AND holder_id = trim(holder_id)
      AND holder_id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  candidate_id TEXT REFERENCES delivery_candidates(id),
  generation_id TEXT REFERENCES delivery_generations(id),
  catalog_digest TEXT NOT NULL CHECK (length(catalog_digest) = 71
    AND substr(catalog_digest, 1, 7) = 'sha256:'
    AND substr(catalog_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  physical_pool_id TEXT NOT NULL REFERENCES physical_pools(id),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'released', 'expired')),
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  released_at TEXT,
  CHECK ((candidate_id IS NOT NULL AND generation_id IS NULL) OR (candidate_id IS NULL AND generation_id IS NOT NULL)),
  FOREIGN KEY (candidate_id, catalog_digest, physical_pool_id)
    REFERENCES delivery_candidates(id, catalog_digest, physical_pool_id),
  FOREIGN KEY (generation_id, catalog_digest, physical_pool_id)
    REFERENCES delivery_generations(id, catalog_digest, physical_pool_id),
  CHECK ((status = 'active' AND released_at IS NULL) OR (status IN ('released', 'expired') AND released_at IS NOT NULL))
);

CREATE INDEX delivery_query_leases_expiry_idx
  ON delivery_query_leases(physical_pool_id, expires_at)
  WHERE status = 'active';

CREATE TABLE delivery_gc_cycles (
  id TEXT PRIMARY KEY
    CHECK (length(id) BETWEEN 1 AND 128
      AND id = trim(id)
      AND id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  physical_pool_id TEXT NOT NULL REFERENCES physical_pools(id),
  epoch INTEGER NOT NULL CHECK (epoch > 0),
  root_revision INTEGER NOT NULL CHECK (root_revision >= 0),
  mark_digest TEXT CHECK (mark_digest IS NULL OR (length(mark_digest) = 71
    AND substr(mark_digest, 1, 7) = 'sha256:'
    AND substr(mark_digest, 8) NOT GLOB '*[^0-9a-f]*')),
  status TEXT NOT NULL CHECK (status IN ('running', 'marked', 'deleting', 'complete', 'aborted')),
  abort_reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  completed_at TEXT,
  UNIQUE(physical_pool_id, epoch),
  UNIQUE(id, physical_pool_id),
  CHECK ((status = 'running' AND mark_digest IS NULL AND completed_at IS NULL AND abort_reason = '')
    OR (status IN ('marked', 'deleting') AND mark_digest IS NOT NULL AND completed_at IS NULL AND abort_reason = '')
    OR (status = 'complete' AND mark_digest IS NOT NULL AND completed_at IS NOT NULL AND abort_reason = '')
    OR (status = 'aborted' AND completed_at IS NOT NULL AND abort_reason <> ''))
);

CREATE TABLE delivery_gc_delete_intents (
  id TEXT PRIMARY KEY
    CHECK (length(id) BETWEEN 1 AND 128
      AND id = trim(id)
      AND id NOT GLOB '*[^A-Za-z0-9._:/-]*'),
  cycle_id TEXT NOT NULL REFERENCES delivery_gc_cycles(id),
  physical_pool_id TEXT NOT NULL REFERENCES physical_pools(id),
  object_key TEXT NOT NULL CHECK (object_key = trim(object_key) AND length(object_key) BETWEEN 1 AND 1024
    AND object_key NOT LIKE '/%' AND object_key <> '.' AND object_key <> '..'
    AND object_key NOT LIKE './%' AND object_key NOT LIKE '../%'
    AND object_key NOT LIKE '%/./%' AND object_key NOT LIKE '%/../%'
    AND object_key NOT LIKE '%/.' AND object_key NOT LIKE '%/..'
    AND object_key NOT LIKE '%//%' AND object_key NOT LIKE '%://%'),
  object_digest TEXT NOT NULL CHECK (length(object_digest) = 71
    AND substr(object_digest, 1, 7) = 'sha256:'
    AND substr(object_digest, 8) NOT GLOB '*[^0-9a-f]*'),
  status TEXT NOT NULL CHECK (status IN ('pending', 'deleted', 'ambiguous')),
  created_at TEXT NOT NULL,
  completed_at TEXT,
  UNIQUE(cycle_id, object_key),
  FOREIGN KEY (cycle_id, physical_pool_id)
    REFERENCES delivery_gc_cycles(id, physical_pool_id),
  CHECK ((status = 'pending' AND completed_at IS NULL)
    OR (status IN ('deleted', 'ambiguous') AND completed_at IS NOT NULL))
);

-- +goose Down

DROP TABLE delivery_gc_delete_intents;
DROP TABLE delivery_gc_cycles;
DROP INDEX delivery_query_leases_expiry_idx;
DROP TABLE delivery_query_leases;
DROP TABLE delivery_publications;
DROP TABLE delivery_retention_exceptions;
DROP TABLE delivery_generations;
DROP TABLE delivery_candidates;
DROP TABLE delivery_catalog_seals;
DROP INDEX delivery_build_attempts_sealed_plan_idx;
DROP TABLE delivery_build_attempts;
DROP INDEX delivery_writer_leases_active_attempt_idx;
DROP INDEX delivery_writer_leases_identity_idx;
DROP TABLE delivery_writer_leases;
DROP TABLE delivery_plans;
DROP TABLE delivery_target_revisions;
