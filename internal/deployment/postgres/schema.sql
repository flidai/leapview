-- Canonical PostgreSQL delivery authority (FAI-565).
--
-- This is a capability-owned, clean-slate schema.  It deliberately contains
-- no SQLite compatibility tables and does not duplicate DuckLake metadata.
-- The package is applied as one clean baseline by the delivery capability.

CREATE SCHEMA IF NOT EXISTS delivery;

CREATE TABLE IF NOT EXISTS delivery.delivery_target (
    target_id text PRIMARY KEY,
    project_id text NOT NULL,
    environment text NOT NULL,
    target_revision bigint NOT NULL DEFAULT 1 CHECK (target_revision > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (target_id = btrim(target_id) AND octet_length(target_id) BETWEEN 1 AND 255),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 255),
    UNIQUE (project_id, environment)
);

-- Target-owned fencing counter. Keeping the counter in a separate row avoids
-- conflating serving selection with lease state while preserving one lock
-- scope for epoch allocation.
CREATE TABLE IF NOT EXISTS delivery.delivery_target_fence (
    target_id text PRIMARY KEY REFERENCES delivery.delivery_target(target_id),
    next_fencing_epoch bigint NOT NULL DEFAULT 1 CHECK (next_fencing_epoch > 0)
);

-- Target-owned immutable revision allocators.  Each counter is advanced while
-- the row is locked by the admitting transaction, so plan/candidate/generation
-- revisions are serialized per target without MAX scans or advisory locks.
-- The counters are deliberately independent: a failed admission rolls back
-- only the caller transaction and therefore cannot consume a revision.
CREATE TABLE IF NOT EXISTS delivery.delivery_target_revision (
    target_id text PRIMARY KEY REFERENCES delivery.delivery_target(target_id),
    next_plan_revision bigint NOT NULL DEFAULT 1 CHECK (next_plan_revision > 0),
    next_candidate_revision bigint NOT NULL DEFAULT 1 CHECK (next_candidate_revision > 0),
    next_generation_revision bigint NOT NULL DEFAULT 1 CHECK (next_generation_revision > 0)
);

CREATE OR REPLACE FUNCTION delivery.create_target_revision_row()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO delivery.delivery_target_revision(target_id)
    VALUES (NEW.target_id)
    ON CONFLICT (target_id) DO NOTHING;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS delivery_target_revision_after_insert ON delivery.delivery_target;
CREATE TRIGGER delivery_target_revision_after_insert
AFTER INSERT ON delivery.delivery_target
FOR EACH ROW EXECUTE FUNCTION delivery.create_target_revision_row();

CREATE TABLE IF NOT EXISTS delivery.delivery_plan (
    plan_id uuid PRIMARY KEY,
    target_id text NOT NULL REFERENCES delivery.delivery_target(target_id),
    plan_revision bigint NOT NULL CHECK (plan_revision > 0),
    plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_graph_digest text NOT NULL CHECK (compiled_graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_config_digest text NOT NULL CHECK (compiled_config_digest ~ '^sha256:[0-9a-f]{64}$'),
    security_domain_fingerprint text NOT NULL CHECK (security_domain_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    artifact_digest text NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    qualification_digest text NOT NULL CHECK (qualification_digest ~ '^sha256:[0-9a-f]{64}$'),
    qualification_required boolean NOT NULL,
    approval_required boolean NOT NULL,
    approval_policy_revision bigint NOT NULL CHECK (approval_policy_revision > 0),
    -- The complete canonical deployment.DeliveryPlan document is the
    -- execution contract. Digest/evidence columns above remain relational
    -- projections for indexed authority checks, but this document is what a
    -- native build rehydrates when it executes a persisted plan.
    plan_document jsonb NOT NULL
        CHECK (jsonb_typeof(plan_document) = 'object' AND octet_length(plan_document::text) <= 1048576),
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 65536),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (target_id, plan_revision),
    UNIQUE (plan_id, target_id)
);

-- The candidate table is the admission record. It stores no mutable serving
-- pointer; only a qualified candidate can be used to create a generation.
CREATE TABLE IF NOT EXISTS delivery.delivery_candidate (
    candidate_id uuid PRIMARY KEY,
    target_id text NOT NULL REFERENCES delivery.delivery_target(target_id),
    plan_id uuid NOT NULL REFERENCES delivery.delivery_plan(plan_id),
    snapshot_seal_id uuid,
    status text NOT NULL DEFAULT 'building' CHECK (status = btrim(status) AND octet_length(status) BETWEEN 1 AND 32 AND status IN ('building','qualified','ready','admitted','rejected','retired')),
    candidate_revision bigint NOT NULL CHECK (candidate_revision > 0),
    artifact_digest text NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    qualification_digest text CHECK (qualification_digest IS NULL OR qualification_digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    qualified_at timestamptz,
    retired_at timestamptz,
    UNIQUE (target_id, candidate_revision),
    UNIQUE (candidate_id, plan_id),
    UNIQUE (candidate_id, target_id, plan_id),
    UNIQUE (candidate_id, target_id, snapshot_seal_id),
    FOREIGN KEY (plan_id, target_id) REFERENCES delivery.delivery_plan(plan_id, target_id),
    CHECK ((status IN ('building','ready') AND snapshot_seal_id IS NULL AND qualification_digest IS NULL AND qualified_at IS NULL)
        OR (status IN ('qualified','admitted') AND snapshot_seal_id IS NOT NULL AND qualification_digest IS NOT NULL AND qualified_at IS NOT NULL)
        OR (status = 'rejected')
        OR (status = 'retired' AND retired_at IS NOT NULL)),
    CHECK (retired_at IS NULL OR status = 'retired')
);

CREATE TABLE IF NOT EXISTS delivery.delivery_build_attempt (
    attempt_id uuid PRIMARY KEY,
    plan_id uuid NOT NULL REFERENCES delivery.delivery_plan(plan_id),
    candidate_id uuid REFERENCES delivery.delivery_candidate(candidate_id),
    owner_id text NOT NULL,
    physical_pool_id text NOT NULL CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    fencing_epoch bigint NOT NULL CHECK (fencing_epoch > 0),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    state text NOT NULL CHECK (state = btrim(state) AND octet_length(state) BETWEEN 1 AND 32 AND state IN ('running','committed','aborted','indeterminate','fenced')),
    namespace text NOT NULL CHECK (namespace = btrim(namespace) AND octet_length(namespace) BETWEEN 1 AND 512),
    lease_expires_at timestamptz NOT NULL,
    session_identity text NOT NULL CHECK (session_identity = btrim(session_identity) AND octet_length(session_identity) BETWEEN 1 AND 512),
    snapshot_id bigint CHECK (snapshot_id IS NULL OR snapshot_id > 0),
    commit_marker jsonb,
    termination_evidence jsonb,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK (commit_marker IS NULL OR (jsonb_typeof(commit_marker) = 'object' AND octet_length(commit_marker::text) <= 4096)),
    CHECK (termination_evidence IS NULL OR (jsonb_typeof(termination_evidence) = 'object' AND octet_length(termination_evidence::text) <= 32768)),
    CHECK ((state = 'running' AND finished_at IS NULL) OR (state <> 'running' AND finished_at IS NOT NULL)),
    CHECK ((state = 'running' AND snapshot_id IS NULL AND commit_marker IS NULL AND termination_evidence IS NULL)
        OR (state = 'committed' AND snapshot_id IS NOT NULL AND commit_marker IS NOT NULL AND termination_evidence IS NULL)
        OR (state IN ('aborted','indeterminate','fenced') AND snapshot_id IS NULL AND commit_marker IS NULL AND termination_evidence IS NOT NULL)),
    UNIQUE (attempt_id, candidate_id),
    FOREIGN KEY (candidate_id, plan_id) REFERENCES delivery.delivery_candidate(candidate_id, plan_id)
);

-- A successor is admitted only from an explicitly reconciled predecessor.
-- Keeping the edge in its own immutable table means the predecessor attempt
-- row remains append-only after it is fenced, while normal commit/reconcile
-- transitions can reject late writes by checking this edge.
CREATE TABLE IF NOT EXISTS delivery.delivery_build_attempt_successor (
    predecessor_attempt_id uuid PRIMARY KEY REFERENCES delivery.delivery_build_attempt(attempt_id) ON DELETE RESTRICT,
    successor_attempt_id uuid NOT NULL UNIQUE REFERENCES delivery.delivery_build_attempt(attempt_id) ON DELETE RESTRICT,
    resolution_evidence jsonb NOT NULL
        CHECK (jsonb_typeof(resolution_evidence) = 'object' AND octet_length(resolution_evidence::text) <= 32768),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (predecessor_attempt_id <> successor_attempt_id)
);

-- A build artifact binding is the immutable hand-off from a durable build
-- attempt to the serving-state artifact that was produced by that attempt.
-- The attempt UUID is the sole identity: a retry may replay the exact row,
-- but can never replace its artifact or serving-state identity.
CREATE TABLE IF NOT EXISTS delivery.delivery_build_artifact_binding (
    attempt_id uuid PRIMARY KEY REFERENCES delivery.delivery_build_attempt(attempt_id) ON DELETE RESTRICT,
    serving_artifact_id text NOT NULL CHECK (serving_artifact_id = btrim(serving_artifact_id) AND octet_length(serving_artifact_id) BETWEEN 1 AND 255 AND serving_artifact_id ~ '^[A-Za-z0-9._:/-]+$'),
    serving_artifact_digest text NOT NULL CHECK (serving_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    serving_state_id text NOT NULL CHECK (serving_state_id = btrim(serving_state_id) AND octet_length(serving_state_id) BETWEEN 1 AND 255 AND serving_state_id ~ '^[A-Za-z0-9._:/-]+$'),
    bound_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS delivery.delivery_snapshot_seal (
    seal_id uuid PRIMARY KEY,
    attempt_id uuid NOT NULL REFERENCES delivery.delivery_build_attempt(attempt_id),
    candidate_id uuid REFERENCES delivery.delivery_candidate(candidate_id),
    physical_pool_id text NOT NULL CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    tenant_domain text NOT NULL CHECK (tenant_domain = btrim(tenant_domain) AND octet_length(tenant_domain) BETWEEN 1 AND 255),
    region text NOT NULL CHECK (region = btrim(region) AND octet_length(region) BETWEEN 1 AND 128),
    encryption_domain text NOT NULL CHECK (encryption_domain = btrim(encryption_domain) AND octet_length(encryption_domain) BETWEEN 1 AND 255),
    object_namespace text NOT NULL CHECK (object_namespace = btrim(object_namespace) AND octet_length(object_namespace) BETWEEN 1 AND 255),
    catalog_database text NOT NULL CHECK (catalog_database = btrim(catalog_database) AND octet_length(catalog_database) BETWEEN 1 AND 255),
    catalog_id text NOT NULL CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    catalog_uuid text NOT NULL CHECK (catalog_uuid = btrim(catalog_uuid) AND octet_length(catalog_uuid) BETWEEN 1 AND 255),
    catalog_version bigint NOT NULL CHECK (catalog_version > 0),
    ducklake_snapshot_id bigint NOT NULL CHECK (ducklake_snapshot_id > 0),
    relation_namespace text NOT NULL CHECK (relation_namespace = btrim(relation_namespace) AND octet_length(relation_namespace) BETWEEN 1 AND 512),
    relation_manifest_digest text NOT NULL CHECK (relation_manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    closure_digest text NOT NULL CHECK (closure_digest ~ '^sha256:[0-9a-f]{64}$'),
    object_root text NOT NULL CHECK (object_root = btrim(object_root) AND octet_length(object_root) BETWEEN 1 AND 512),
    object_root_digest text NOT NULL CHECK (object_root_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_root text NOT NULL CHECK (artifact_root = btrim(artifact_root) AND octet_length(artifact_root) BETWEEN 1 AND 512),
    artifact_root_digest text NOT NULL CHECK (artifact_root_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_graph_digest text NOT NULL CHECK (compiled_graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_config_digest text NOT NULL CHECK (compiled_config_digest ~ '^sha256:[0-9a-f]{64}$'),
    security_domain_fingerprint text NOT NULL CHECK (security_domain_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    compatibility_digest text NOT NULL CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    serving_artifact_id text NOT NULL CHECK (serving_artifact_id = btrim(serving_artifact_id) AND octet_length(serving_artifact_id) BETWEEN 1 AND 255),
    serving_artifact_digest text NOT NULL CHECK (serving_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    duckdb_version text NOT NULL CHECK (duckdb_version = btrim(duckdb_version) AND octet_length(duckdb_version) BETWEEN 1 AND 128),
    runtime_version text NOT NULL CHECK (runtime_version = btrim(runtime_version) AND octet_length(runtime_version) BETWEEN 1 AND 128),
    ducklake_extension_version text NOT NULL CHECK (ducklake_extension_version = btrim(ducklake_extension_version) AND octet_length(ducklake_extension_version) BETWEEN 1 AND 128),
    ducklake_spec_version text NOT NULL CHECK (ducklake_spec_version = btrim(ducklake_spec_version) AND octet_length(ducklake_spec_version) BETWEEN 1 AND 128),
    catalog_schema_version text NOT NULL CHECK (catalog_schema_version = btrim(catalog_schema_version) AND octet_length(catalog_schema_version) BETWEEN 1 AND 128),
    qualification_evidence jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(qualification_evidence) = 'object' AND octet_length(qualification_evidence::text) <= 32768),
    qualified_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (attempt_id),
    UNIQUE (seal_id, candidate_id),
    UNIQUE (physical_pool_id, catalog_id, catalog_database, catalog_uuid, ducklake_snapshot_id),
    FOREIGN KEY (attempt_id, candidate_id) REFERENCES delivery.delivery_build_attempt(attempt_id, candidate_id)
);

-- Candidate and seal reference one another during the lifecycle.  Install the
-- nullable candidate->seal edge after both tables exist, preserving the clean
-- baseline while avoiding a circular CREATE TABLE dependency.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'delivery_candidate_snapshot_seal_fk'
           AND conrelid = 'delivery.delivery_candidate'::regclass
    ) THEN
        ALTER TABLE delivery.delivery_candidate
            ADD CONSTRAINT delivery_candidate_snapshot_seal_fk
            FOREIGN KEY (snapshot_seal_id) REFERENCES delivery.delivery_snapshot_seal(seal_id);
    END IF;
END;
$$;

CREATE TABLE IF NOT EXISTS delivery.delivery_generation (
    generation_id uuid PRIMARY KEY,
    target_id text NOT NULL REFERENCES delivery.delivery_target(target_id),
    candidate_id uuid NOT NULL REFERENCES delivery.delivery_candidate(candidate_id),
    snapshot_seal_id uuid NOT NULL REFERENCES delivery.delivery_snapshot_seal(seal_id),
    plan_id uuid NOT NULL REFERENCES delivery.delivery_plan(plan_id),
    plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_root text NOT NULL CHECK (artifact_root = btrim(artifact_root) AND octet_length(artifact_root) BETWEEN 1 AND 512),
    artifact_root_digest text NOT NULL CHECK (artifact_root_digest ~ '^sha256:[0-9a-f]{64}$'),
    serving_artifact_digest text NOT NULL CHECK (serving_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_graph_digest text NOT NULL CHECK (compiled_graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_config_digest text NOT NULL CHECK (compiled_config_digest ~ '^sha256:[0-9a-f]{64}$'),
    security_domain_fingerprint text NOT NULL CHECK (security_domain_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    generation_revision bigint NOT NULL CHECK (generation_revision > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (target_id, generation_revision),
    UNIQUE (generation_id, target_id, candidate_id, snapshot_seal_id),
    FOREIGN KEY (candidate_id, target_id, plan_id) REFERENCES delivery.delivery_candidate(candidate_id, target_id, plan_id),
    FOREIGN KEY (snapshot_seal_id, candidate_id) REFERENCES delivery.delivery_snapshot_seal(seal_id, candidate_id)
);

-- Explicit-revision APIs remain supported.  Keep allocator counters ahead of
-- any explicitly supplied revision so a later allocated admission cannot
-- collide with legacy/caller-assigned rows.
CREATE OR REPLACE FUNCTION delivery.advance_target_revision_counter()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_ARGV[0] = 'plan' THEN
        UPDATE delivery.delivery_target_revision
           SET next_plan_revision = GREATEST(next_plan_revision, NEW.plan_revision + 1)
         WHERE target_id = NEW.target_id;
    ELSIF TG_ARGV[0] = 'candidate' THEN
        UPDATE delivery.delivery_target_revision
           SET next_candidate_revision = GREATEST(next_candidate_revision, NEW.candidate_revision + 1)
         WHERE target_id = NEW.target_id;
    ELSIF TG_ARGV[0] = 'generation' THEN
        UPDATE delivery.delivery_target_revision
           SET next_generation_revision = GREATEST(next_generation_revision, NEW.generation_revision + 1)
         WHERE target_id = NEW.target_id;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS delivery_plan_revision_counter ON delivery.delivery_plan;
CREATE TRIGGER delivery_plan_revision_counter
AFTER INSERT ON delivery.delivery_plan
FOR EACH ROW EXECUTE FUNCTION delivery.advance_target_revision_counter('plan');

DROP TRIGGER IF EXISTS delivery_candidate_revision_counter ON delivery.delivery_candidate;
CREATE TRIGGER delivery_candidate_revision_counter
AFTER INSERT ON delivery.delivery_candidate
FOR EACH ROW EXECUTE FUNCTION delivery.advance_target_revision_counter('candidate');

DROP TRIGGER IF EXISTS delivery_generation_revision_counter ON delivery.delivery_generation;
CREATE TRIGGER delivery_generation_revision_counter
AFTER INSERT ON delivery.delivery_generation
FOR EACH ROW EXECUTE FUNCTION delivery.advance_target_revision_counter('generation');

CREATE TABLE IF NOT EXISTS delivery.delivery_publication (
    publication_id uuid PRIMARY KEY,
    target_id text NOT NULL REFERENCES delivery.delivery_target(target_id),
    generation_id uuid NOT NULL,
    expected_base_generation_id uuid,
    candidate_id uuid NOT NULL REFERENCES delivery.delivery_candidate(candidate_id),
    snapshot_seal_id uuid NOT NULL REFERENCES delivery.delivery_snapshot_seal(seal_id),
    expected_target_revision bigint NOT NULL CHECK (expected_target_revision > 0),
    result_target_revision bigint,
    actor_id text NOT NULL CHECK (actor_id = btrim(actor_id) AND octet_length(actor_id) BETWEEN 1 AND 255),
    state text NOT NULL CHECK (state = btrim(state) AND octet_length(state) BETWEEN 1 AND 32 AND state IN ('pending','committed','rejected','indeterminate')),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    committed_at timestamptz,
    CHECK ((state = 'pending' AND result_target_revision IS NULL AND committed_at IS NULL)
        OR (state = 'committed' AND result_target_revision IS NOT NULL AND committed_at IS NOT NULL)
        OR (state IN ('rejected','indeterminate') AND result_target_revision IS NULL AND committed_at IS NULL)),
    FOREIGN KEY (generation_id) REFERENCES delivery.delivery_generation(generation_id),
    FOREIGN KEY (expected_base_generation_id) REFERENCES delivery.delivery_generation(generation_id),
    FOREIGN KEY (generation_id, target_id, candidate_id, snapshot_seal_id) REFERENCES delivery.delivery_generation(generation_id, target_id, candidate_id, snapshot_seal_id),
    FOREIGN KEY (candidate_id, target_id, snapshot_seal_id) REFERENCES delivery.delivery_candidate(candidate_id, target_id, snapshot_seal_id),
    FOREIGN KEY (snapshot_seal_id, candidate_id) REFERENCES delivery.delivery_snapshot_seal(seal_id, candidate_id),
    UNIQUE (target_id, request_digest)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_active_pointer (
    target_id text PRIMARY KEY REFERENCES delivery.delivery_target(target_id),
    generation_id uuid NOT NULL REFERENCES delivery.delivery_generation(generation_id),
    publication_id uuid NOT NULL REFERENCES delivery.delivery_publication(publication_id),
    changed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- A pointer carries only serving selection.  This deferred consistency check
-- keeps the selected publication/generation pair bound to one target and one
-- qualified candidate without duplicating candidate or seal columns here.

-- Native publication approval authority.  Approval requests are immutable
-- evidence for one exact pending publication; decisions (including
-- revocations) are append-only child rows. There is deliberately no candidate-
-- scoped approval projection in the clean-slate schema.
CREATE TABLE IF NOT EXISTS delivery.delivery_approval_request (
    request_id uuid PRIMARY KEY,
    publication_id uuid NOT NULL REFERENCES delivery.delivery_publication(publication_id) ON DELETE RESTRICT,
    target_id text NOT NULL CHECK (target_id = btrim(target_id) AND octet_length(target_id) BETWEEN 1 AND 255),
    candidate_id uuid NOT NULL,
    generation_id uuid NOT NULL,
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    expected_target_revision bigint NOT NULL CHECK (expected_target_revision > 0),
    policy_revision bigint NOT NULL CHECK (policy_revision > 0),
    requested_by text NOT NULL CHECK (requested_by = btrim(requested_by) AND octet_length(requested_by) BETWEEN 1 AND 255),
    request_credential_class text NOT NULL CHECK (request_credential_class IN ('human','workload','api_token','session')),
    request_credential_id text NOT NULL CHECK (request_credential_id = btrim(request_credential_id) AND octet_length(request_credential_id) BETWEEN 1 AND 255),
    request_credential_expires_at timestamptz NOT NULL,
    requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    operation_id uuid NOT NULL,
    event_id uuid NOT NULL,
    audit_id uuid NOT NULL,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    CHECK (request_credential_expires_at > requested_at),
    CHECK (expires_at > requested_at AND expires_at <= requested_at + interval '24 hours'),
    UNIQUE (publication_id)
);

-- Per-request allocator for append-only decision revisions. The row is the
-- sole mutable counter; decisions themselves remain immutable history.
CREATE TABLE IF NOT EXISTS delivery.delivery_approval_revision (
    request_id uuid PRIMARY KEY REFERENCES delivery.delivery_approval_request(request_id) ON DELETE RESTRICT,
    next_revision bigint NOT NULL DEFAULT 1 CHECK (next_revision > 0)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_approval_decision (
    decision_id uuid PRIMARY KEY,
    request_id uuid NOT NULL REFERENCES delivery.delivery_approval_request(request_id) ON DELETE RESTRICT,
    decision_revision bigint NOT NULL CHECK (decision_revision > 0),
    decision text NOT NULL CHECK (decision IN ('approved','denied','revoked')),
    decided_by text NOT NULL CHECK (decided_by = btrim(decided_by) AND octet_length(decided_by) BETWEEN 1 AND 255),
    decision_credential_class text NOT NULL CHECK (decision_credential_class IN ('human','workload','api_token','session')),
    decision_credential_id text NOT NULL CHECK (decision_credential_id = btrim(decision_credential_id) AND octet_length(decision_credential_id) BETWEEN 1 AND 255),
    decision_credential_expires_at timestamptz NOT NULL,
    decided_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    operation_id uuid NOT NULL,
    event_id uuid NOT NULL,
    audit_id uuid NOT NULL,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    UNIQUE (request_id, decision_revision)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_lease (
    lease_id uuid PRIMARY KEY,
    target_id text NOT NULL REFERENCES delivery.delivery_target(target_id),
    owner_id text NOT NULL CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    fencing_epoch bigint NOT NULL CHECK (fencing_epoch > 0),
    state text NOT NULL DEFAULT 'active' CHECK (state = btrim(state) AND octet_length(state) BETWEEN 1 AND 32 AND state IN ('active','released','expired')),
    expires_at timestamptz NOT NULL,
    acquired_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    released_at timestamptz,
    UNIQUE (target_id, fencing_epoch),
    CHECK (expires_at > acquired_at),
    CHECK ((state = 'active' AND released_at IS NULL)
        OR (state IN ('released','expired') AND released_at IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS delivery.delivery_retention_root (
    root_id uuid PRIMARY KEY,
    target_id text NOT NULL REFERENCES delivery.delivery_target(target_id),
    candidate_id uuid REFERENCES delivery.delivery_candidate(candidate_id),
    generation_id uuid REFERENCES delivery.delivery_generation(generation_id),
    snapshot_seal_id uuid REFERENCES delivery.delivery_snapshot_seal(seal_id),
    root_kind text NOT NULL CHECK (root_kind = btrim(root_kind) AND octet_length(root_kind) BETWEEN 1 AND 32 AND root_kind IN ('candidate','generation','rollback','recovery','query')),
    state text NOT NULL CHECK (state = btrim(state) AND octet_length(state) BETWEEN 1 AND 32 AND state IN ('live','retiring','expired')),
    expires_at timestamptz,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 16384),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    retired_at timestamptz,
    expired_at timestamptz,
    CHECK ((state = 'live' AND retired_at IS NULL AND expired_at IS NULL)
        OR (state = 'retiring' AND retired_at IS NOT NULL AND expired_at IS NULL)
        OR (state = 'expired' AND expired_at IS NOT NULL)),
    CHECK ((root_kind = 'candidate' AND candidate_id IS NOT NULL)
        OR (root_kind = 'generation' AND generation_id IS NOT NULL)
        OR (root_kind NOT IN ('candidate','generation')))
);

CREATE OR REPLACE FUNCTION delivery.reject_authority_history_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'delivery authority history is immutable';
END;
$$;

CREATE OR REPLACE FUNCTION delivery.reject_target_identity_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.target_id <> OLD.target_id OR NEW.project_id <> OLD.project_id
       OR NEW.environment <> OLD.environment THEN
        RAISE EXCEPTION 'delivery target identity is immutable';
    END IF;
    IF NEW.target_revision < OLD.target_revision THEN
        RAISE EXCEPTION 'delivery target revision cannot move backwards';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION delivery.reject_fence_counter_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.target_id <> OLD.target_id OR NEW.next_fencing_epoch < OLD.next_fencing_epoch THEN
        RAISE EXCEPTION 'delivery fencing counter is monotonic and owned by the authority';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION delivery.reject_target_revision_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.target_id <> OLD.target_id
       OR NEW.next_plan_revision < OLD.next_plan_revision
       OR NEW.next_candidate_revision < OLD.next_candidate_revision
       OR NEW.next_generation_revision < OLD.next_generation_revision THEN
        RAISE EXCEPTION 'delivery target revision counters are monotonic and immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION delivery.reject_attempt_identity_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.attempt_id <> OLD.attempt_id OR NEW.plan_id <> OLD.plan_id
       OR NEW.candidate_id IS DISTINCT FROM OLD.candidate_id
       OR NEW.owner_id <> OLD.owner_id OR NEW.physical_pool_id <> OLD.physical_pool_id OR NEW.fencing_epoch <> OLD.fencing_epoch
       OR NEW.request_digest <> OLD.request_digest OR NEW.plan_digest <> OLD.plan_digest
       OR NEW.namespace <> OLD.namespace OR NEW.session_identity <> OLD.session_identity
       OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'delivery build attempt identity is immutable';
    END IF;
    IF OLD.state <> 'running' AND NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at THEN
        RAISE EXCEPTION 'terminal build attempt lease expiry is immutable';
    ELSIF OLD.state = 'running' AND NEW.state = 'running'
          AND NEW.lease_expires_at < OLD.lease_expires_at THEN
        RAISE EXCEPTION 'running build attempt lease expiry cannot move backwards';
    ELSIF OLD.state = 'running' AND NEW.state <> 'running'
          AND NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at THEN
        RAISE EXCEPTION 'terminal build attempt lease expiry is immutable';
    END IF;
    IF OLD.state = 'indeterminate' AND NEW.state NOT IN ('indeterminate','committed','aborted') THEN
        RAISE EXCEPTION 'indeterminate build attempt may only be reconciled to committed or aborted';
    END IF;
    IF OLD.state NOT IN ('running','indeterminate') AND (NEW.state <> OLD.state OR NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id
       OR NEW.commit_marker IS DISTINCT FROM OLD.commit_marker OR NEW.termination_evidence IS DISTINCT FROM OLD.termination_evidence
       OR NEW.finished_at IS DISTINCT FROM OLD.finished_at OR NEW.updated_at <> OLD.updated_at) THEN
        RAISE EXCEPTION 'terminal build attempt evidence is immutable';
    END IF;
    IF OLD.state IN ('running','indeterminate') AND NEW.state = OLD.state
       AND (NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id
            OR NEW.commit_marker IS DISTINCT FROM OLD.commit_marker
            OR NEW.termination_evidence IS DISTINCT FROM OLD.termination_evidence
            OR NEW.finished_at IS DISTINCT FROM OLD.finished_at) THEN
        RAISE EXCEPTION 'running build attempt evidence is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION delivery.reject_build_artifact_binding_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'delivery build artifact binding is immutable';
END;
$$;

CREATE OR REPLACE FUNCTION delivery.reject_build_attempt_successor_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'delivery build attempt successor link is immutable';
END;
$$;

DROP TRIGGER IF EXISTS delivery_build_artifact_binding_immutable ON delivery.delivery_build_artifact_binding;
CREATE TRIGGER delivery_build_artifact_binding_immutable
BEFORE UPDATE OR DELETE ON delivery.delivery_build_artifact_binding
FOR EACH ROW EXECUTE FUNCTION delivery.reject_build_artifact_binding_mutation();

DROP TRIGGER IF EXISTS delivery_build_attempt_successor_immutable ON delivery.delivery_build_attempt_successor;
CREATE TRIGGER delivery_build_attempt_successor_immutable
BEFORE UPDATE OR DELETE ON delivery.delivery_build_attempt_successor
FOR EACH ROW EXECUTE FUNCTION delivery.reject_build_attempt_successor_mutation();

CREATE OR REPLACE FUNCTION delivery.reject_publication_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.publication_id <> OLD.publication_id
       OR NEW.target_id <> OLD.target_id
       OR NEW.generation_id <> OLD.generation_id
       OR NEW.candidate_id <> OLD.candidate_id
       OR NEW.snapshot_seal_id <> OLD.snapshot_seal_id
       OR NEW.expected_target_revision <> OLD.expected_target_revision
       OR NEW.actor_id <> OLD.actor_id
       OR NEW.request_digest <> OLD.request_digest
       OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'delivery publication identity is immutable';
    END IF;
    IF OLD.state <> 'pending' THEN
        IF NEW.state <> OLD.state OR NEW.result_target_revision IS DISTINCT FROM OLD.result_target_revision
           OR NEW.committed_at IS DISTINCT FROM OLD.committed_at THEN
            RAISE EXCEPTION 'terminal publication is immutable';
        END IF;
    ELSIF NEW.state NOT IN ('pending','committed','rejected','indeterminate') THEN
        RAISE EXCEPTION 'invalid publication transition';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION delivery.reject_candidate_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.candidate_id <> OLD.candidate_id OR NEW.target_id <> OLD.target_id
       OR NEW.plan_id <> OLD.plan_id OR NEW.candidate_revision <> OLD.candidate_revision
       OR NEW.artifact_digest <> OLD.artifact_digest OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'delivery candidate identity is immutable';
    END IF;
    IF OLD.status = 'building' AND NEW.status NOT IN ('building','ready','qualified','rejected') THEN
        RAISE EXCEPTION 'invalid candidate transition';
    ELSIF OLD.status = 'ready' AND NEW.status NOT IN ('ready','qualified','rejected') THEN
        RAISE EXCEPTION 'invalid candidate transition';
    ELSIF OLD.status = 'qualified' AND NEW.status NOT IN ('qualified','admitted','rejected','retired') THEN
        RAISE EXCEPTION 'invalid candidate transition';
    ELSIF OLD.status = 'admitted' AND NEW.status NOT IN ('admitted','retired') THEN
        RAISE EXCEPTION 'invalid candidate transition';
    ELSIF OLD.status = 'rejected' AND NEW.status NOT IN ('rejected','retired') THEN
        RAISE EXCEPTION 'invalid candidate transition';
    ELSIF OLD.status = 'retired' AND NEW.status <> 'retired' THEN
        RAISE EXCEPTION 'invalid candidate transition';
    END IF;
    IF OLD.status NOT IN ('building','ready') AND (NEW.snapshot_seal_id IS DISTINCT FROM OLD.snapshot_seal_id
       OR NEW.qualification_digest IS DISTINCT FROM OLD.qualification_digest
       OR NEW.qualified_at IS DISTINCT FROM OLD.qualified_at
       OR NEW.retired_at IS DISTINCT FROM OLD.retired_at) THEN
        RAISE EXCEPTION 'candidate qualification evidence is immutable';
    END IF;
    IF OLD.status IN ('building','ready') AND NEW.status <> 'qualified'
       AND (NEW.snapshot_seal_id IS DISTINCT FROM OLD.snapshot_seal_id
            OR NEW.qualification_digest IS DISTINCT FROM OLD.qualification_digest
            OR NEW.qualified_at IS DISTINCT FROM OLD.qualified_at) THEN
        RAISE EXCEPTION 'candidate qualification evidence requires qualification transition';
    END IF;
    IF OLD.retired_at IS NOT NULL AND NEW.retired_at IS DISTINCT FROM OLD.retired_at THEN
        RAISE EXCEPTION 'candidate retirement timestamp is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION delivery.guard_approval_request_insert()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    publication delivery.delivery_publication%ROWTYPE;
    durable_policy_revision bigint;
    now_ts timestamptz := clock_timestamp();
BEGIN
    SELECT * INTO STRICT publication
      FROM delivery.delivery_publication
     WHERE publication_id = NEW.publication_id
     FOR UPDATE;
    IF publication.state <> 'pending'
       OR publication.target_id <> NEW.target_id
       OR publication.generation_id <> NEW.generation_id
       OR publication.candidate_id <> NEW.candidate_id
       OR publication.request_digest <> NEW.request_digest
       OR publication.expected_target_revision <> NEW.expected_target_revision THEN
        RAISE EXCEPTION 'approval request must bind the exact pending publication';
    END IF;
    SELECT plan.approval_policy_revision
      INTO STRICT durable_policy_revision
      FROM delivery.delivery_generation g
      JOIN delivery.delivery_plan plan ON plan.plan_id = g.plan_id
     WHERE g.generation_id = publication.generation_id;
    IF NEW.policy_revision <> durable_policy_revision THEN
        RAISE EXCEPTION 'approval request policy revision differs from durable plan';
    END IF;
    IF NEW.expires_at <= now_ts OR NEW.expires_at > now_ts + interval '24 hours'
       OR NEW.expires_at > NEW.request_credential_expires_at THEN
        RAISE EXCEPTION 'approval request expiry is outside the database-clock window';
    END IF;
    IF NEW.request_credential_expires_at <= now_ts THEN
        RAISE EXCEPTION 'approval request credential is expired';
    END IF;
    IF NEW.requested_at IS DISTINCT FROM now_ts THEN
        NEW.requested_at := now_ts;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION delivery.create_approval_revision_row()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO delivery.delivery_approval_revision(request_id)
    VALUES (NEW.request_id)
    ON CONFLICT (request_id) DO NOTHING;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION delivery.guard_approval_decision_insert()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    request delivery.delivery_approval_request%ROWTYPE;
    publication_state text;
    now_ts timestamptz := clock_timestamp();
BEGIN
    SELECT p.state INTO STRICT publication_state
      FROM delivery.delivery_publication p
      JOIN delivery.delivery_approval_request r ON r.publication_id = p.publication_id
     WHERE r.request_id = NEW.request_id
     FOR UPDATE OF p;
    IF publication_state <> 'pending' THEN
        RAISE EXCEPTION 'approval decision requires a pending publication';
    END IF;
    SELECT * INTO STRICT request
      FROM delivery.delivery_approval_request
     WHERE request_id = NEW.request_id
     FOR UPDATE;
    IF request.expires_at <= now_ts OR NEW.decided_at > request.expires_at THEN
        RAISE EXCEPTION 'approval request is expired';
    END IF;
    IF NEW.decision IN ('approved','denied') AND NEW.decided_by = request.requested_by THEN
        RAISE EXCEPTION 'approval separation of duty violated';
    END IF;
    IF NEW.decision_credential_expires_at <= now_ts THEN
        RAISE EXCEPTION 'approval decision credential is expired';
    END IF;
    IF NEW.decided_at IS DISTINCT FROM now_ts THEN
        NEW.decided_at := now_ts;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION delivery.reject_approval_request_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'approval request identity is immutable';
END;
$$;

CREATE OR REPLACE FUNCTION delivery.reject_approval_decision_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'approval decision evidence is immutable';
END;
$$;

CREATE OR REPLACE FUNCTION delivery.guard_approval_revision_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.request_id <> OLD.request_id OR NEW.next_revision <> OLD.next_revision + 1 THEN
        RAISE EXCEPTION 'approval decision revision allocator is monotonic';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION delivery.check_active_pointer_consistency()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    pub_target text;
    pub_generation uuid;
    pub_candidate uuid;
    pub_seal uuid;
    gen_target text;
    gen_candidate uuid;
    gen_seal uuid;
    candidate_target text;
BEGIN
    SELECT target_id,generation_id,candidate_id,snapshot_seal_id
      INTO pub_target,pub_generation,pub_candidate,pub_seal
      FROM delivery.delivery_publication
     WHERE publication_id=NEW.publication_id;
    IF NOT FOUND OR pub_target <> NEW.target_id OR pub_generation <> NEW.generation_id THEN
        RAISE EXCEPTION 'active pointer publication identity differs';
    END IF;
    SELECT target_id,candidate_id,snapshot_seal_id
      INTO gen_target,gen_candidate,gen_seal
      FROM delivery.delivery_generation
     WHERE generation_id=NEW.generation_id;
    IF NOT FOUND OR gen_target <> NEW.target_id OR gen_candidate <> pub_candidate OR gen_seal <> pub_seal THEN
        RAISE EXCEPTION 'active pointer generation identity differs';
    END IF;
    SELECT target_id INTO candidate_target FROM delivery.delivery_candidate WHERE candidate_id=pub_candidate;
    IF NOT FOUND OR candidate_target <> NEW.target_id THEN
        RAISE EXCEPTION 'active pointer candidate identity differs';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION delivery.reject_lease_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.lease_id <> OLD.lease_id OR NEW.target_id <> OLD.target_id
       OR NEW.owner_id <> OLD.owner_id OR NEW.fencing_epoch <> OLD.fencing_epoch
       OR NEW.acquired_at <> OLD.acquired_at THEN
        RAISE EXCEPTION 'delivery lease identity is immutable';
    END IF;
    IF OLD.state <> 'active' AND (NEW.state <> OLD.state OR NEW.expires_at <> OLD.expires_at
       OR NEW.released_at IS DISTINCT FROM OLD.released_at) THEN
        RAISE EXCEPTION 'terminal lease is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION delivery.reject_root_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.root_id <> OLD.root_id OR NEW.target_id <> OLD.target_id
       OR NEW.candidate_id IS DISTINCT FROM OLD.candidate_id
       OR NEW.generation_id IS DISTINCT FROM OLD.generation_id
       OR NEW.snapshot_seal_id IS DISTINCT FROM OLD.snapshot_seal_id
       OR NEW.root_kind <> OLD.root_kind OR NEW.created_at <> OLD.created_at
       OR NEW.evidence IS DISTINCT FROM OLD.evidence THEN
        RAISE EXCEPTION 'delivery retention root identity is immutable';
    END IF;
    IF OLD.state = 'live' AND NEW.state NOT IN ('live','retiring') THEN
        RAISE EXCEPTION 'delivery retention root lifecycle is monotonic';
    ELSIF OLD.state = 'retiring' AND NEW.state NOT IN ('retiring','expired') THEN
        RAISE EXCEPTION 'delivery retention root lifecycle is monotonic';
    ELSIF OLD.state = 'expired' AND NEW.state <> 'expired' THEN
        RAISE EXCEPTION 'delivery retention root lifecycle is monotonic';
    END IF;
    IF NEW.state = 'live' AND (NEW.retired_at IS NOT NULL OR NEW.expired_at IS NOT NULL) THEN
        RAISE EXCEPTION 'live retention root cannot have terminal timestamps';
    ELSIF NEW.state = 'retiring' AND (NEW.retired_at IS NULL OR NEW.expired_at IS NOT NULL) THEN
        RAISE EXCEPTION 'retiring retention root requires retirement timestamp only';
    ELSIF NEW.state = 'expired' AND NEW.expired_at IS NULL THEN
        RAISE EXCEPTION 'expired retention root requires expiry timestamp';
    END IF;
    IF OLD.state IN ('retiring','expired') AND NEW.retired_at IS DISTINCT FROM OLD.retired_at THEN
        RAISE EXCEPTION 'retention root retirement timestamp is immutable';
    END IF;
    IF OLD.state = 'expired' AND NEW.expired_at IS DISTINCT FROM OLD.expired_at THEN
        RAISE EXCEPTION 'retention root expiry timestamp is immutable';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS delivery_target_identity_immutable ON delivery.delivery_target;
CREATE TRIGGER delivery_target_identity_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_target
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_target_identity_mutation();
DROP TRIGGER IF EXISTS delivery_fence_counter_monotonic ON delivery.delivery_target_fence;
CREATE TRIGGER delivery_fence_counter_monotonic BEFORE UPDATE OR DELETE ON delivery.delivery_target_fence
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_fence_counter_mutation();
DROP TRIGGER IF EXISTS delivery_target_revision_monotonic ON delivery.delivery_target_revision;
CREATE TRIGGER delivery_target_revision_monotonic BEFORE UPDATE OR DELETE ON delivery.delivery_target_revision
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_target_revision_mutation();
DROP TRIGGER IF EXISTS delivery_plan_history_immutable ON delivery.delivery_plan;
CREATE TRIGGER delivery_plan_history_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_plan
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_authority_history_mutation();
DROP TRIGGER IF EXISTS delivery_seal_history_immutable ON delivery.delivery_snapshot_seal;
CREATE TRIGGER delivery_seal_history_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_snapshot_seal
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_authority_history_mutation();
DROP TRIGGER IF EXISTS delivery_generation_history_immutable ON delivery.delivery_generation;
CREATE TRIGGER delivery_generation_history_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_generation
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_authority_history_mutation();
DROP TRIGGER IF EXISTS delivery_publication_history_immutable ON delivery.delivery_publication;
CREATE TRIGGER delivery_publication_history_immutable BEFORE DELETE ON delivery.delivery_publication
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_authority_history_mutation();
DROP TRIGGER IF EXISTS delivery_attempt_identity_immutable ON delivery.delivery_build_attempt;
CREATE TRIGGER delivery_attempt_identity_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_build_attempt
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_attempt_identity_mutation();
DROP TRIGGER IF EXISTS delivery_publication_immutable ON delivery.delivery_publication;
CREATE TRIGGER delivery_publication_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_publication
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_publication_mutation();
DROP TRIGGER IF EXISTS delivery_candidate_immutable ON delivery.delivery_candidate;
CREATE TRIGGER delivery_candidate_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_candidate
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_candidate_mutation();
DROP TRIGGER IF EXISTS delivery_lease_immutable ON delivery.delivery_lease;
CREATE TRIGGER delivery_lease_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_lease
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_lease_mutation();
DROP TRIGGER IF EXISTS delivery_root_immutable ON delivery.delivery_retention_root;
CREATE TRIGGER delivery_root_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_retention_root
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_root_mutation();
DROP TRIGGER IF EXISTS delivery_approval_request_insert_guard ON delivery.delivery_approval_request;
CREATE TRIGGER delivery_approval_request_insert_guard BEFORE INSERT ON delivery.delivery_approval_request
    FOR EACH ROW EXECUTE FUNCTION delivery.guard_approval_request_insert();
DROP TRIGGER IF EXISTS delivery_approval_revision_after_insert ON delivery.delivery_approval_request;
CREATE TRIGGER delivery_approval_revision_after_insert AFTER INSERT ON delivery.delivery_approval_request
    FOR EACH ROW EXECUTE FUNCTION delivery.create_approval_revision_row();
DROP TRIGGER IF EXISTS delivery_approval_request_immutable ON delivery.delivery_approval_request;
CREATE TRIGGER delivery_approval_request_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_approval_request
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_approval_request_mutation();
DROP TRIGGER IF EXISTS delivery_approval_decision_insert_guard ON delivery.delivery_approval_decision;
CREATE TRIGGER delivery_approval_decision_insert_guard BEFORE INSERT ON delivery.delivery_approval_decision
    FOR EACH ROW EXECUTE FUNCTION delivery.guard_approval_decision_insert();
DROP TRIGGER IF EXISTS delivery_approval_decision_immutable ON delivery.delivery_approval_decision;
CREATE TRIGGER delivery_approval_decision_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_approval_decision
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_approval_decision_mutation();
DROP TRIGGER IF EXISTS delivery_approval_revision_monotonic ON delivery.delivery_approval_revision;
CREATE TRIGGER delivery_approval_revision_monotonic BEFORE UPDATE OR DELETE ON delivery.delivery_approval_revision
    FOR EACH ROW EXECUTE FUNCTION delivery.guard_approval_revision_mutation();
DROP TRIGGER IF EXISTS delivery_active_pointer_consistency ON delivery.delivery_active_pointer;
CREATE CONSTRAINT TRIGGER delivery_active_pointer_consistency AFTER INSERT OR UPDATE ON delivery.delivery_active_pointer
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION delivery.check_active_pointer_consistency();
CREATE INDEX IF NOT EXISTS delivery_lease_active_idx ON delivery.delivery_lease(target_id, state, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS delivery_lease_one_active_idx ON delivery.delivery_lease(target_id) WHERE state = 'active';
CREATE INDEX IF NOT EXISTS delivery_generation_target_idx ON delivery.delivery_generation(target_id, generation_revision);
CREATE INDEX IF NOT EXISTS delivery_generation_candidate_idx ON delivery.delivery_generation(candidate_id);
CREATE INDEX IF NOT EXISTS delivery_seal_attempt_idx ON delivery.delivery_snapshot_seal(attempt_id);
CREATE INDEX IF NOT EXISTS delivery_root_snapshot_idx ON delivery.delivery_retention_root(snapshot_seal_id, state);
CREATE INDEX IF NOT EXISTS delivery_approval_request_publication_idx ON delivery.delivery_approval_request(publication_id, requested_at DESC, request_id DESC);
CREATE INDEX IF NOT EXISTS delivery_approval_decision_request_idx ON delivery.delivery_approval_decision(request_id, decision_revision DESC, decision_id DESC);

-- Delivery authority evidence is never reachable through PUBLIC defaults.  The
-- applying role remains the owner and therefore retains full control; deploy
-- roles must be granted the minimum required privileges explicitly by the
-- surrounding migration.
REVOKE ALL ON SCHEMA delivery FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA delivery FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA delivery FROM PUBLIC;
GRANT USAGE ON SCHEMA delivery TO CURRENT_USER;
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA delivery TO CURRENT_USER;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA delivery TO CURRENT_USER;
REVOKE UPDATE, DELETE ON delivery.delivery_build_attempt_successor FROM CURRENT_USER;
GRANT SELECT, INSERT ON delivery.delivery_build_attempt_successor TO CURRENT_USER;
