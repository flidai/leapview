-- LeapView PostgreSQL control-plane baseline (FAI-555).
--
-- This is a clean target baseline with no historical migration compatibility.
-- A dedicated
-- bootstrap/migrator connection applies this file once to leapview_control.
-- The application runtime only receives the grants at the end of the file.

-- Role and database provisioning is deliberately outside this migration.
-- Deployment creates these roles, assigns LOGIN/NOLOGIN and credentials, and
-- grants the migrator membership in the owner role.  This file only asserts
-- that the contract exists and grants object privileges.
DO $$
BEGIN
    IF (SELECT count(*) FROM pg_roles WHERE rolname IN (
        'leapview_control_owner', 'leapview_control_migrator',
        'leapview_control_runtime', 'leapview_control_readonly'
    )) <> 4 THEN
        RAISE EXCEPTION 'PostgreSQL control roles must be provisioned before applying the baseline';
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT pg_has_role(current_user, 'leapview_control_owner', 'member') THEN
        RAISE EXCEPTION 'current migration role % must be a member of leapview_control_owner', current_user;
    END IF;
END
$$;

CREATE SCHEMA IF NOT EXISTS platform AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS access AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS delivery AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS refresh AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS event AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS audit AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS lineage AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS cache AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS agent AUTHORIZATION leapview_control_owner;
-- Existing durable capability families retain explicit ownership boundaries;
-- their repository-specific tables arrive in forward capability migrations.
CREATE SCHEMA IF NOT EXISTS project AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS release AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS manageddata AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS dashboard AUTHORIZATION leapview_control_owner;
CREATE SCHEMA IF NOT EXISTS jobs AUTHORIZATION leapview_control_owner;

-- All baseline objects are owned by the owner role.  The migration authority
-- enters that role for DDL and grants; provisioning controls membership.
SET ROLE leapview_control_owner;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_type t
        JOIN pg_namespace n ON n.oid = t.typnamespace
        WHERE n.nspname = 'platform' AND t.typname = 'resource_id'
    ) THEN
        EXECUTE $q$CREATE DOMAIN platform.resource_id AS text
            CHECK (VALUE = btrim(VALUE) AND length(VALUE) BETWEEN 1 AND 255)$q$;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_type t
        JOIN pg_namespace n ON n.oid = t.typnamespace
        WHERE n.nspname = 'platform' AND t.typname = 'sha256_digest'
    ) THEN
        EXECUTE $q$CREATE DOMAIN platform.sha256_digest AS text
            CHECK (VALUE ~* '^(sha256:)?[0-9a-f]{64}$')$q$;
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS platform.schema_revision (
    revision       bigint PRIMARY KEY CHECK (revision > 0),
    migration_id   text NOT NULL UNIQUE CHECK (migration_id = btrim(migration_id) AND migration_id <> ''),
    checksum       platform.sha256_digest NOT NULL,
    applied_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE OR REPLACE FUNCTION platform.reject_schema_revision_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'schema revisions are append-only';
END;
$$;

DROP TRIGGER IF EXISTS schema_revision_append_only ON platform.schema_revision;
CREATE TRIGGER schema_revision_append_only
    BEFORE UPDATE OR DELETE ON platform.schema_revision
    FOR EACH ROW EXECUTE FUNCTION platform.reject_schema_revision_mutation();

-- The single baseline row is the schema contract checked during readiness.
-- The migration runner records the actual SHA-256 of this file after a
-- successful transaction.  Keeping checksum calculation in the runner avoids
-- a self-referential SQL literal and makes tampering detectable.

CREATE TABLE IF NOT EXISTS platform.operation (
    operation_id   uuid PRIMARY KEY,
    scope_id       platform.resource_id NOT NULL,
    operation_type platform.resource_id NOT NULL,
    idempotency_key text NOT NULL CHECK (idempotency_key = btrim(idempotency_key) AND length(idempotency_key) BETWEEN 1 AND 512),
    request_digest platform.sha256_digest NOT NULL,
    state          text NOT NULL CHECK (state IN ('pending', 'completed', 'failed', 'indeterminate')),
    outcome        jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(outcome) = 'object' AND octet_length(outcome::text) <= 32768),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at   timestamptz,
    UNIQUE (scope_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS access.principal (
    id             uuid PRIMARY KEY,
    principal_type text NOT NULL CHECK (principal_type IN ('user', 'service', 'system')),
    status         text NOT NULL CHECK (status IN ('active', 'disabled', 'pending')),
    external_subject platform.resource_id,
    display_name   text NOT NULL DEFAULT '' CHECK (length(display_name) <= 512),
    attributes     jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(attributes) = 'object' AND octet_length(attributes::text) <= 16384),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE UNIQUE INDEX IF NOT EXISTS principal_external_subject_idx
    ON access.principal (external_subject) WHERE external_subject IS NOT NULL;

CREATE TABLE IF NOT EXISTS access.access_group (
    id             uuid PRIMARY KEY,
    name           text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 255),
    provider       text NOT NULL DEFAULT '' CHECK (length(provider) <= 255),
    external_id    text NOT NULL DEFAULT '' CHECK (length(external_id) <= 512),
    attributes     jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(attributes) = 'object' AND octet_length(attributes::text) <= 16384),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (provider, external_id)
);

CREATE TABLE IF NOT EXISTS access.principal_group (
    principal_id   uuid NOT NULL REFERENCES access.principal(id) ON DELETE CASCADE,
    group_id       uuid NOT NULL REFERENCES access.access_group(id) ON DELETE CASCADE,
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (principal_id, group_id)
);

CREATE TABLE IF NOT EXISTS access.access_grant (
    id             uuid PRIMARY KEY,
    principal_id   uuid NOT NULL REFERENCES access.principal(id) ON DELETE CASCADE,
    project_id     platform.resource_id,
    capability     platform.resource_id NOT NULL,
    effect         text NOT NULL CHECK (effect IN ('allow', 'deny')),
    predicates     jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(predicates) = 'object' AND octet_length(predicates::text) <= 32768),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (principal_id, project_id, capability)
);

CREATE TABLE IF NOT EXISTS access.session (
    id             uuid PRIMARY KEY,
    principal_id   uuid NOT NULL REFERENCES access.principal(id) ON DELETE CASCADE,
    token_fingerprint bytea NOT NULL UNIQUE,
    expires_at     timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_seen_at   timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS access.credential (
    id             uuid PRIMARY KEY,
    principal_id   uuid NOT NULL REFERENCES access.principal(id) ON DELETE CASCADE,
    credential_type text NOT NULL CHECK (credential_type IN ('password', 'oauth', 'api_token', 'service')),
    verifier       bytea NOT NULL,
    metadata       jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(metadata) = 'object' AND octet_length(metadata::text) <= 16384),
    expires_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS delivery.delivery_target (
    target_id      platform.resource_id PRIMARY KEY,
    project_id     platform.resource_id NOT NULL,
    environment    platform.resource_id NOT NULL,
    target_revision bigint NOT NULL DEFAULT 1 CHECK (target_revision > 0),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (project_id, environment),
    UNIQUE (target_id, project_id, environment)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_plan (
    plan_id        uuid PRIMARY KEY,
    target_id      platform.resource_id NOT NULL REFERENCES delivery.delivery_target(target_id),
    plan_revision  bigint NOT NULL CHECK (plan_revision > 0),
    plan_digest    platform.sha256_digest NOT NULL,
    artifact_digest platform.sha256_digest NOT NULL,
    evidence       jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 65536),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (target_id, plan_revision)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_candidate (
    candidate_id   uuid PRIMARY KEY,
    target_id      platform.resource_id NOT NULL REFERENCES delivery.delivery_target(target_id),
    plan_id        uuid NOT NULL REFERENCES delivery.delivery_plan(plan_id),
    status         text NOT NULL CHECK (status IN ('building', 'ready', 'admitted', 'rejected', 'retired')),
    candidate_revision bigint NOT NULL CHECK (candidate_revision > 0),
    artifact_digest platform.sha256_digest NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (target_id, candidate_revision),
    UNIQUE (candidate_id, target_id)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_build_attempt (
    attempt_id     uuid PRIMARY KEY,
    candidate_id   uuid NOT NULL REFERENCES delivery.delivery_candidate(candidate_id),
    fencing_epoch  bigint NOT NULL CHECK (fencing_epoch > 0),
    request_digest platform.sha256_digest NOT NULL,
    plan_digest    platform.sha256_digest NOT NULL,
    status         text NOT NULL CHECK (status IN ('running', 'committed', 'failed', 'indeterminate', 'fenced')),
    namespace      text NOT NULL CHECK (namespace = btrim(namespace) AND length(namespace) <= 512),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at    timestamptz
);

CREATE TABLE IF NOT EXISTS delivery.delivery_snapshot_seal (
    seal_id        uuid PRIMARY KEY,
    attempt_id     uuid NOT NULL REFERENCES delivery.delivery_build_attempt(attempt_id),
    physical_pool_id platform.resource_id NOT NULL,
    catalog_identity platform.resource_id NOT NULL,
    snapshot_id    bigint NOT NULL CHECK (snapshot_id > 0),
    relation_manifest_digest platform.sha256_digest NOT NULL,
    compatibility_digest platform.sha256_digest NOT NULL,
    serving_artifact_digest platform.sha256_digest NOT NULL,
    duckdb_version text NOT NULL CHECK (length(duckdb_version) <= 128),
    ducklake_version text NOT NULL CHECK (length(ducklake_version) <= 128),
    evidence       jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    qualified_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (attempt_id),
    UNIQUE (physical_pool_id, snapshot_id, attempt_id)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_generation (
    generation_id  uuid PRIMARY KEY,
    target_id      platform.resource_id NOT NULL REFERENCES delivery.delivery_target(target_id),
    candidate_id   uuid NOT NULL REFERENCES delivery.delivery_candidate(candidate_id),
    snapshot_seal_id uuid NOT NULL REFERENCES delivery.delivery_snapshot_seal(seal_id),
    status         text NOT NULL CHECK (status IN ('qualified', 'published', 'superseded', 'retired')),
    generation_revision bigint NOT NULL CHECK (generation_revision > 0),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (generation_id, target_id),
    UNIQUE (target_id, generation_revision)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_publication (
    publication_id uuid PRIMARY KEY,
    target_id      platform.resource_id NOT NULL REFERENCES delivery.delivery_target(target_id),
    generation_id  uuid NOT NULL,
    expected_target_revision bigint NOT NULL CHECK (expected_target_revision > 0),
    published_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (generation_id, target_id)
        REFERENCES delivery.delivery_generation(generation_id, target_id)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_active_pointer (
    target_id      platform.resource_id PRIMARY KEY REFERENCES delivery.delivery_target(target_id),
    generation_id  uuid NOT NULL,
    target_revision bigint NOT NULL CHECK (target_revision > 0),
    changed_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (generation_id, target_id)
        REFERENCES delivery.delivery_generation(generation_id, target_id)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_approval (
    approval_id    uuid PRIMARY KEY,
    candidate_id   uuid NOT NULL REFERENCES delivery.delivery_candidate(candidate_id),
    principal_id   uuid NOT NULL REFERENCES access.principal(id),
    decision       text NOT NULL CHECK (decision IN ('approved', 'denied', 'withdrawn')),
    evidence       jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 16384),
    decided_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS delivery.delivery_lease (
    lease_id       uuid PRIMARY KEY,
    target_id      platform.resource_id NOT NULL REFERENCES delivery.delivery_target(target_id),
    owner_id       platform.resource_id NOT NULL,
    fencing_epoch  bigint NOT NULL CHECK (fencing_epoch > 0),
    expires_at     timestamptz NOT NULL,
    acquired_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    released_at    timestamptz,
    UNIQUE (target_id, owner_id, fencing_epoch)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_retention_root (
    root_id        uuid PRIMARY KEY,
    target_id      platform.resource_id NOT NULL REFERENCES delivery.delivery_target(target_id),
    snapshot_seal_id uuid REFERENCES delivery.delivery_snapshot_seal(seal_id),
    root_kind      text NOT NULL CHECK (root_kind IN ('candidate', 'generation', 'rollback', 'recovery', 'query')),
    state          text NOT NULL CHECK (state IN ('live', 'retiring', 'expired')),
    expires_at     timestamptz,
    evidence       jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 16384),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- Physical snapshot lifecycle is separate from logical delivery roots and
-- immutable qualification evidence.  A retiring row rejects new roots/leases
-- in the control transaction while existing query leases drain.
CREATE TABLE IF NOT EXISTS delivery.delivery_snapshot_retention (
    physical_pool_id platform.resource_id NOT NULL,
    snapshot_id    bigint NOT NULL CHECK (snapshot_id > 0),
    state          text NOT NULL CHECK (state IN ('live', 'retiring', 'expired')),
    protected_until timestamptz,
    retired_at     timestamptz,
    expired_at     timestamptz,
    evidence       jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 16384),
    PRIMARY KEY (physical_pool_id, snapshot_id)
);

CREATE TABLE IF NOT EXISTS refresh.refresh_job (
    job_id         uuid PRIMARY KEY,
    target_id      platform.resource_id NOT NULL REFERENCES delivery.delivery_target(target_id),
    pipeline_id    platform.resource_id NOT NULL,
    requested_by   uuid REFERENCES access.principal(id),
    idempotency_key text NOT NULL CHECK (idempotency_key = btrim(idempotency_key) AND length(idempotency_key) BETWEEN 1 AND 512),
    request_digest platform.sha256_digest NOT NULL,
    status         text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    parameters     jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(parameters) = 'object' AND octet_length(parameters::text) <= 65536),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (target_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS refresh.refresh_job_run (
    run_id         uuid PRIMARY KEY,
    job_id         uuid NOT NULL REFERENCES refresh.refresh_job(job_id) ON DELETE CASCADE,
    attempt        bigint NOT NULL CHECK (attempt > 0),
    status         text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'cancelled')),
    fencing_epoch  bigint NOT NULL CHECK (fencing_epoch > 0),
    started_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at    timestamptz,
    result         jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(result) = 'object' AND octet_length(result::text) <= 32768),
    UNIQUE (job_id, attempt)
);

CREATE TABLE IF NOT EXISTS refresh.refresh_lease (
    lease_id       uuid PRIMARY KEY,
    job_id         uuid NOT NULL REFERENCES refresh.refresh_job(job_id) ON DELETE CASCADE,
    owner_id       platform.resource_id NOT NULL,
    fencing_epoch  bigint NOT NULL CHECK (fencing_epoch > 0),
    expires_at     timestamptz NOT NULL,
    acquired_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (job_id, owner_id, fencing_epoch)
);

CREATE TABLE IF NOT EXISTS event.event_log (
    event_id       uuid PRIMARY KEY,
    scope_id       platform.resource_id NOT NULL,
    aggregate_type platform.resource_id NOT NULL,
    aggregate_id   platform.resource_id NOT NULL,
    aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
    event_type     platform.resource_id NOT NULL,
    schema_version bigint NOT NULL CHECK (schema_version > 0),
    occurred_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    correlation_id uuid,
    payload        jsonb NOT NULL
        CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 65536),
    UNIQUE (scope_id, aggregate_type, aggregate_id, aggregate_version)
);

-- Producers lock this row and increment next_version; MAX(sequence)+1 is not
-- a valid allocator under concurrent transactions.
CREATE TABLE IF NOT EXISTS event.event_aggregate (
    scope_id       platform.resource_id NOT NULL,
    aggregate_type platform.resource_id NOT NULL,
    aggregate_id   platform.resource_id NOT NULL,
    next_version   bigint NOT NULL DEFAULT 1 CHECK (next_version > 0),
    updated_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (scope_id, aggregate_type, aggregate_id)
);

CREATE INDEX IF NOT EXISTS event_log_scope_occurred_idx
    ON event.event_log (scope_id, occurred_at, event_id);

CREATE TABLE IF NOT EXISTS event.event_fanout_registry (
    registry_id    boolean PRIMARY KEY DEFAULT true CHECK (registry_id),
    updated_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

INSERT INTO event.event_fanout_registry (registry_id) VALUES (true)
ON CONFLICT (registry_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS event.event_consumer (
    consumer_id    uuid PRIMARY KEY,
    consumer_key   platform.resource_id NOT NULL UNIQUE,
    lifecycle      text NOT NULL CHECK (lifecycle IN ('backfilling', 'enabled', 'paused', 'retired')),
    replay_from    timestamptz NOT NULL,
    frontier_event_id uuid,
    metadata       jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(metadata) = 'object' AND octet_length(metadata::text) <= 16384),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS event.event_delivery (
    consumer_id    uuid NOT NULL REFERENCES event.event_consumer(consumer_id) ON DELETE CASCADE,
    event_id       uuid NOT NULL REFERENCES event.event_log(event_id) ON DELETE CASCADE,
    status         text NOT NULL CHECK (status IN ('pending', 'claimed', 'succeeded', 'dead_letter', 'waived')),
    attempts       bigint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    claimed_by     platform.resource_id,
    claimed_until  timestamptz,
    terminal_at    timestamptz,
    evidence       jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    PRIMARY KEY (consumer_id, event_id)
);

CREATE INDEX IF NOT EXISTS event_delivery_claim_idx
    ON event.event_delivery (consumer_id, status, available_at);

CREATE TABLE IF NOT EXISTS event.event_retention_root (
    root_id        uuid PRIMARY KEY,
    consumer_id    uuid REFERENCES event.event_consumer(consumer_id) ON DELETE CASCADE,
    replay_from    timestamptz NOT NULL,
    replay_until   timestamptz,
    state          text NOT NULL CHECK (state IN ('live', 'retiring', 'expired')),
    frontier_event_id uuid,
    evidence       jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 16384),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS audit.audit_event (
    audit_id       uuid PRIMARY KEY,
    scope_id       platform.resource_id,
    -- Actor identity is immutable audit evidence.  It is intentionally not a
    -- foreign key: deleting a principal must never rewrite historical rows.
    principal_id   uuid,
    source         platform.resource_id NOT NULL,
    operation      platform.resource_id NOT NULL,
    action         platform.resource_id NOT NULL,
    resource_kind  platform.resource_id,
    resource_id    platform.resource_id,
    capability     text NOT NULL CHECK (capability = btrim(capability) AND length(capability) <= 128),
    outcome        text NOT NULL CHECK (outcome IN ('success', 'failure', 'denied')),
    request_id     uuid,
    correlation_id uuid,
    aggregate_key  text NOT NULL CHECK (aggregate_key = btrim(aggregate_key) AND length(aggregate_key) BETWEEN 1 AND 512),
    aggregate_sequence bigint NOT NULL CHECK (aggregate_sequence >= 0),
    intent_digest  platform.sha256_digest NOT NULL,
    metadata       jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(metadata) = 'object' AND octet_length(metadata::text) <= 32768),
    occurred_at    timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE OR REPLACE FUNCTION audit.reject_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit history is append-only';
END;
$$;

DROP TRIGGER IF EXISTS audit_event_append_only ON audit.audit_event;
CREATE TRIGGER audit_event_append_only
    BEFORE UPDATE OR DELETE ON audit.audit_event
    FOR EACH ROW EXECUTE FUNCTION audit.reject_audit_mutation();

CREATE TABLE IF NOT EXISTS lineage.lineage_graph (
    graph_digest   platform.sha256_digest PRIMARY KEY,
    project_id     platform.resource_id NOT NULL,
    compiler_version text NOT NULL CHECK (length(compiler_version) <= 128),
    artifact_digest platform.sha256_digest NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS lineage.lineage_node (
    graph_digest   platform.sha256_digest NOT NULL REFERENCES lineage.lineage_graph(graph_digest) ON DELETE CASCADE,
    resource_id    platform.resource_id NOT NULL,
    resource_kind  platform.resource_id NOT NULL,
    identity_digest platform.sha256_digest NOT NULL,
    properties     jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(properties) = 'object' AND octet_length(properties::text) <= 16384),
    PRIMARY KEY (graph_digest, resource_id)
);

CREATE TABLE IF NOT EXISTS lineage.lineage_edge (
    graph_digest   platform.sha256_digest NOT NULL,
    from_resource_id platform.resource_id NOT NULL,
    to_resource_id platform.resource_id NOT NULL,
    edge_kind      platform.resource_id NOT NULL,
    PRIMARY KEY (graph_digest, from_resource_id, to_resource_id, edge_kind),
    CHECK (from_resource_id <> to_resource_id),
    FOREIGN KEY (graph_digest, from_resource_id)
        REFERENCES lineage.lineage_node(graph_digest, resource_id) ON DELETE CASCADE,
    FOREIGN KEY (graph_digest, to_resource_id)
        REFERENCES lineage.lineage_node(graph_digest, resource_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS lineage_edge_upstream_idx
    ON lineage.lineage_edge (graph_digest, to_resource_id);
CREATE INDEX IF NOT EXISTS lineage_edge_downstream_idx
    ON lineage.lineage_edge (graph_digest, from_resource_id);

CREATE TABLE IF NOT EXISTS lineage.lineage_closure (
    graph_digest   platform.sha256_digest NOT NULL,
    ancestor_id    platform.resource_id NOT NULL,
    descendant_id  platform.resource_id NOT NULL,
    minimum_depth  bigint NOT NULL CHECK (minimum_depth >= 0),
    PRIMARY KEY (graph_digest, ancestor_id, descendant_id),
    FOREIGN KEY (graph_digest, ancestor_id)
        REFERENCES lineage.lineage_node(graph_digest, resource_id) ON DELETE CASCADE,
    FOREIGN KEY (graph_digest, descendant_id)
        REFERENCES lineage.lineage_node(graph_digest, resource_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS lineage.lineage_active_graph (
    project_id     platform.resource_id NOT NULL,
    environment    platform.resource_id NOT NULL,
    graph_digest   platform.sha256_digest NOT NULL REFERENCES lineage.lineage_graph(graph_digest),
    changed_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, environment)
);

CREATE TABLE IF NOT EXISTS cache.cache_manifest (
    manifest_id    uuid PRIMARY KEY,
    partition_kind text NOT NULL CHECK (partition_kind IN ('production', 'candidate')),
    partition_id   platform.resource_id NOT NULL,
    dependency_digest platform.sha256_digest NOT NULL,
    policy_fingerprint platform.sha256_digest NOT NULL,
    canonical_query_digest platform.sha256_digest NOT NULL,
    key_format_version bigint NOT NULL CHECK (key_format_version > 0),
    storage_security_domain platform.resource_id NOT NULL,
    object_digest  platform.sha256_digest NOT NULL,
    object_key     text NOT NULL CHECK (object_key = btrim(object_key) AND length(object_key) BETWEEN 1 AND 2048),
    byte_size      bigint NOT NULL CHECK (byte_size > 0),
    metadata       jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(metadata) = 'object' AND octet_length(metadata::text) <= 16384),
    state          text NOT NULL CHECK (state IN ('admitted', 'retiring', 'expired')),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at     timestamptz,
    UNIQUE (partition_kind, partition_id, dependency_digest, policy_fingerprint, canonical_query_digest, key_format_version)
);

CREATE INDEX IF NOT EXISTS cache_manifest_lookup_idx
    ON cache.cache_manifest (partition_kind, partition_id, dependency_digest, policy_fingerprint, canonical_query_digest);

CREATE TABLE IF NOT EXISTS cache.cache_fill_lease (
    lease_id       uuid PRIMARY KEY,
    manifest_id    uuid REFERENCES cache.cache_manifest(manifest_id) ON DELETE CASCADE,
    cache_key      platform.sha256_digest NOT NULL,
    owner_id       platform.resource_id NOT NULL,
    fencing_epoch  bigint NOT NULL CHECK (fencing_epoch > 0),
    expires_at     timestamptz NOT NULL,
    acquired_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (cache_key),
    UNIQUE (owner_id, fencing_epoch)
);

CREATE TABLE IF NOT EXISTS cache.cache_retention_root (
    root_id        uuid PRIMARY KEY,
    manifest_id    uuid NOT NULL REFERENCES cache.cache_manifest(manifest_id) ON DELETE CASCADE,
    state          text NOT NULL CHECK (state IN ('live', 'retiring', 'expired')),
    reason         platform.resource_id NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS agent.agent_conversation (
    conversation_id uuid PRIMARY KEY,
    principal_id   uuid REFERENCES access.principal(id) ON DELETE SET NULL,
    status         text NOT NULL CHECK (status IN ('active', 'closed', 'expired')),
    context        jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(context) = 'object' AND octet_length(context::text) <= 32768),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS agent.agent_operation (
    operation_id   uuid PRIMARY KEY,
    conversation_id uuid REFERENCES agent.agent_conversation(conversation_id) ON DELETE CASCADE,
    idempotency_key text NOT NULL CHECK (idempotency_key = btrim(idempotency_key) AND length(idempotency_key) BETWEEN 1 AND 512),
    request_digest platform.sha256_digest NOT NULL,
    state          text NOT NULL CHECK (state IN ('pending', 'completed', 'failed', 'indeterminate')),
    outcome        jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(outcome) = 'object' AND octet_length(outcome::text) <= 32768),
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at   timestamptz,
    UNIQUE (conversation_id, idempotency_key)
);

-- Remove ambient PUBLIC access before granting capability-specific access.
REVOKE ALL ON ALL TABLES IN SCHEMA platform, access, delivery, refresh, event, audit, lineage, cache, agent, project, release, manageddata, dashboard, jobs FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA platform, access, delivery, refresh, event, audit, lineage, cache, agent, project, release, manageddata, dashboard, jobs FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA platform, access, delivery, refresh, event, audit, lineage, cache, agent, project, release, manageddata, dashboard, jobs FROM PUBLIC;

GRANT USAGE ON SCHEMA platform, access, delivery, refresh, event, audit, lineage, cache, agent
    TO leapview_control_migrator, leapview_control_runtime, leapview_control_readonly;
GRANT USAGE ON SCHEMA project, release, manageddata, dashboard, jobs
    TO leapview_control_migrator, leapview_control_runtime, leapview_control_readonly;
GRANT USAGE ON DOMAIN platform.resource_id, platform.sha256_digest
    TO leapview_control_migrator, leapview_control_runtime, leapview_control_readonly;

-- The migrator has explicit DDL/data authority through its owner membership;
-- runtime owns normal control mutations; read-only is SELECT-only.
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA platform, access, delivery, refresh, event, audit, lineage, cache, agent, project, release, manageddata, dashboard, jobs
    TO leapview_control_migrator;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA access, delivery, refresh, event, lineage, cache, agent, project, release, manageddata, dashboard, jobs
    TO leapview_control_runtime;
GRANT SELECT, INSERT ON audit.audit_event TO leapview_control_runtime;
GRANT SELECT ON ALL TABLES IN SCHEMA platform, access, delivery, refresh, event, audit, lineage, cache, agent, project, release, manageddata, dashboard, jobs
    TO leapview_control_readonly;

-- Audit history is append-only even if a future grant is accidentally widened.
REVOKE UPDATE, DELETE ON audit.audit_event FROM leapview_control_runtime, leapview_control_readonly;
REVOKE INSERT, UPDATE, DELETE ON platform.schema_revision FROM leapview_control_runtime, leapview_control_readonly;
REVOKE ALL ON FUNCTION audit.reject_audit_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.reject_schema_revision_mutation() FROM PUBLIC;

-- Preserve isolation for objects created by future forward migrations.
ALTER DEFAULT PRIVILEGES FOR ROLE leapview_control_owner
    REVOKE ALL ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE leapview_control_owner IN SCHEMA access, delivery, refresh, event, lineage, cache, agent
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO leapview_control_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE leapview_control_owner IN SCHEMA audit
    GRANT SELECT, INSERT ON TABLES TO leapview_control_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE leapview_control_owner IN SCHEMA platform, access, delivery, refresh, event, audit, lineage, cache, agent, project, release, manageddata, dashboard, jobs
    GRANT SELECT ON TABLES TO leapview_control_readonly;

RESET ROLE;
