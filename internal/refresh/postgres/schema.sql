-- Clean-slate refresh authority (ADR-0014/0016).
--
-- This capability deliberately has no refresh queue.  The canonical
-- platform jobs schema owns worker admission and leases; refresh.run.job_id
-- links a run to that job.  No analytical rows, Arrow payloads or result
-- bytes are stored here.
CREATE SCHEMA IF NOT EXISTS refresh;
REVOKE ALL ON SCHEMA refresh FROM PUBLIC;

CREATE TABLE IF NOT EXISTS refresh.schedule_revision (
    schedule_revision_id text PRIMARY KEY,
    project_id text NOT NULL,
    environment text NOT NULL,
    pipeline_id text NOT NULL,
    schedule_id text NOT NULL,
    semantic_model_id text NOT NULL,
    generation_id text NOT NULL,
    artifact_digest text NOT NULL,
    cron text NOT NULL,
    timezone text NOT NULL,
    starting_deadline interval NOT NULL DEFAULT interval '0 seconds',
    concurrency_policy text NOT NULL,
    schedule_digest text NOT NULL,
    next_run_at timestamptz NOT NULL,
    valid_from timestamptz NOT NULL DEFAULT clock_timestamp(),
    closed_at timestamptz,
    enabled boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (schedule_revision_id = btrim(schedule_revision_id) AND length(schedule_revision_id) BETWEEN 1 AND 256),
    CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128),
    CHECK (pipeline_id = btrim(pipeline_id) AND length(pipeline_id) BETWEEN 1 AND 255),
    CHECK (schedule_id = btrim(schedule_id) AND length(schedule_id) BETWEEN 1 AND 255),
    CHECK (semantic_model_id = btrim(semantic_model_id) AND length(semantic_model_id) BETWEEN 1 AND 255),
    CHECK (generation_id = btrim(generation_id) AND length(generation_id) BETWEEN 1 AND 255),
    CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (schedule_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (cron = btrim(cron) AND length(cron) BETWEEN 1 AND 255),
    CHECK (timezone = btrim(timezone) AND length(timezone) BETWEEN 1 AND 128),
    CHECK (starting_deadline >= interval '0 seconds' AND starting_deadline <= interval '366 days'),
    CHECK (concurrency_policy IN ('Forbid','Replace')),
    CHECK (closed_at IS NULL OR closed_at >= valid_from)
);
CREATE UNIQUE INDEX IF NOT EXISTS schedule_active_key
    ON refresh.schedule_revision(project_id, environment, pipeline_id, generation_id, schedule_id)
    WHERE closed_at IS NULL AND enabled;
CREATE INDEX IF NOT EXISTS schedule_due_idx
    ON refresh.schedule_revision(next_run_at, project_id, environment, pipeline_id)
    WHERE closed_at IS NULL AND enabled;

CREATE OR REPLACE FUNCTION refresh.guard_schedule_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.closed_at IS NOT NULL OR NOT NEW.enabled THEN RAISE EXCEPTION 'schedule revisions must begin active'; END IF;
    NEW.valid_from := clock_timestamp(); NEW.updated_at := NEW.valid_from;
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS schedule_insert_guard ON refresh.schedule_revision;
CREATE TRIGGER schedule_insert_guard BEFORE INSERT ON refresh.schedule_revision FOR EACH ROW EXECUTE FUNCTION refresh.guard_schedule_insert();

CREATE OR REPLACE FUNCTION refresh.guard_schedule_update() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.schedule_revision_id IS DISTINCT FROM OLD.schedule_revision_id OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.pipeline_id IS DISTINCT FROM OLD.pipeline_id OR NEW.schedule_id IS DISTINCT FROM OLD.schedule_id OR NEW.semantic_model_id IS DISTINCT FROM OLD.semantic_model_id OR NEW.generation_id IS DISTINCT FROM OLD.generation_id OR NEW.artifact_digest IS DISTINCT FROM OLD.artifact_digest OR NEW.cron IS DISTINCT FROM OLD.cron OR NEW.timezone IS DISTINCT FROM OLD.timezone OR NEW.starting_deadline IS DISTINCT FROM OLD.starting_deadline OR NEW.concurrency_policy IS DISTINCT FROM OLD.concurrency_policy OR NEW.schedule_digest IS DISTINCT FROM OLD.schedule_digest OR NEW.valid_from IS DISTINCT FROM OLD.valid_from THEN RAISE EXCEPTION 'schedule revision identity is immutable'; END IF;
    IF OLD.closed_at IS NOT NULL AND (NEW.closed_at IS DISTINCT FROM OLD.closed_at OR NEW.enabled IS DISTINCT FROM OLD.enabled) THEN RAISE EXCEPTION 'closed schedule revision is immutable'; END IF;
    IF OLD.closed_at IS NOT NULL AND NEW.next_run_at IS DISTINCT FROM OLD.next_run_at THEN RAISE EXCEPTION 'closed schedule revision is immutable'; END IF;
    IF NEW.closed_at IS NOT NULL AND OLD.closed_at IS NULL AND NEW.enabled THEN RAISE EXCEPTION 'closed schedule revision must be disabled'; END IF;
    IF NEW.closed_at IS NOT NULL AND OLD.closed_at IS NULL AND NEW.next_run_at IS DISTINCT FROM OLD.next_run_at THEN RAISE EXCEPTION 'schedule close cannot mutate next run time'; END IF;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS schedule_guard ON refresh.schedule_revision;
CREATE TRIGGER schedule_guard BEFORE UPDATE ON refresh.schedule_revision FOR EACH ROW EXECUTE FUNCTION refresh.guard_schedule_update();

CREATE OR REPLACE FUNCTION refresh.close_omitted_schedules(
    p_project_id text,
    p_environment text,
    p_generation_id text,
    p_pipelines text[],
    p_schedule_ids text[]
) RETURNS bigint
LANGUAGE plpgsql
SET search_path = pg_catalog, refresh AS $$
DECLARE affected bigint;
BEGIN
    UPDATE refresh.schedule_revision s
       SET closed_at=clock_timestamp(), enabled=false, updated_at=clock_timestamp()
     WHERE s.project_id=p_project_id
       AND s.environment=p_environment
       AND s.generation_id=p_generation_id
       AND s.closed_at IS NULL
       AND s.enabled
       AND NOT EXISTS (
           SELECT 1
             FROM unnest(p_pipelines, p_schedule_ids) AS omitted(pipeline_id, schedule_id)
            WHERE omitted.pipeline_id=s.pipeline_id
              AND omitted.schedule_id=s.schedule_id
       );
    GET DIAGNOSTICS affected = ROW_COUNT;
    RETURN affected;
END; $$;

CREATE TABLE IF NOT EXISTS refresh.run (
    run_id text PRIMARY KEY,
    -- Immutable provenance pointer into platform.operation. It is intentionally
    -- text and has no cross-schema FK so operation retention can prune terminal
    -- rows without deleting historical refresh evidence.
    operation_id text,
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    parent_run_id text REFERENCES refresh.run(run_id) ON DELETE RESTRICT,
    pipeline_id text NOT NULL,
    semantic_model_id text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    target_revision bigint NOT NULL DEFAULT 0,
    trigger_type text NOT NULL,
    invocation_source text NOT NULL,
    trigger_id text NOT NULL DEFAULT '',
    concurrency_policy text NOT NULL DEFAULT 'Forbid',
    schedule_revision_id text NOT NULL DEFAULT '',
    occurrence_id text NOT NULL DEFAULT '',
    nominal_time timestamptz,
    plan_digest text NOT NULL,
    artifact_digest text NOT NULL,
    matching_schedule_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    materialization_scope jsonb NOT NULL DEFAULT '[]'::jsonb,
    principal_id text NOT NULL DEFAULT '',
    job_id text REFERENCES jobs.job_history(id) ON DELETE RESTRICT,
    status text NOT NULL DEFAULT 'queued',
    attempt_count bigint NOT NULL DEFAULT 0,
    fence_generation bigint NOT NULL DEFAULT 0,
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at timestamptz,
    finished_at timestamptz,
    CHECK (run_id = btrim(run_id) AND length(run_id) BETWEEN 1 AND 256),
    CHECK (operation_id IS NULL OR operation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128),
    CHECK (generation_id = btrim(generation_id) AND length(generation_id) BETWEEN 1 AND 255),
    CHECK (parent_run_id IS NULL OR (parent_run_id = btrim(parent_run_id) AND length(parent_run_id) BETWEEN 1 AND 256 AND parent_run_id <> run_id)),
    CHECK (pipeline_id = btrim(pipeline_id) AND length(pipeline_id) BETWEEN 1 AND 255),
    CHECK (semantic_model_id = btrim(semantic_model_id) AND length(semantic_model_id) BETWEEN 1 AND 255),
    CHECK (target_type IN ('refresh_pipeline','model')),
    CHECK (target_id = btrim(target_id) AND length(target_id) BETWEEN 1 AND 255),
    CHECK (target_revision >= 0),
    CHECK (trigger_type IN ('manual','schedule','dependency')),
    CHECK (invocation_source IN ('manual','schedule','external','backfill','dependency')),
    CHECK (trigger_id = btrim(trigger_id) AND length(trigger_id) <= 256),
    CHECK (concurrency_policy IN ('Forbid','Replace')),
    CHECK (schedule_revision_id = btrim(schedule_revision_id) AND length(schedule_revision_id) <= 256),
    CHECK (occurrence_id = btrim(occurrence_id) AND length(occurrence_id) <= 256),
    CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(matching_schedule_ids) = 'array' AND octet_length(matching_schedule_ids::text) <= 16384),
    CHECK (jsonb_typeof(materialization_scope) = 'array' AND octet_length(materialization_scope::text) <= 16384),
    CHECK (status IN ('queued','running','prepared','succeeded','failed','cancelled','superseded','skipped')),
    CHECK (attempt_count >= 0 AND fence_generation >= 0),
    CHECK ((status IN ('running','prepared') AND lease_owner <> '' AND lease_expires_at IS NOT NULL) OR (status NOT IN ('running','prepared') AND lease_owner = '' AND lease_expires_at IS NULL)),
    CHECK ((status IN ('succeeded','failed','cancelled','superseded','skipped') AND finished_at IS NOT NULL) OR (status IN ('queued','running','prepared') AND finished_at IS NULL))
);
CREATE INDEX IF NOT EXISTS run_scope_idx ON refresh.run(project_id, environment, created_at DESC, run_id DESC);
CREATE INDEX IF NOT EXISTS run_target_idx ON refresh.run(project_id, environment, target_type, target_id, created_at DESC, run_id DESC);
CREATE INDEX IF NOT EXISTS run_recovery_idx ON refresh.run(environment, created_at, run_id) WHERE job_id IS NOT NULL AND status IN ('queued','running','prepared');

CREATE OR REPLACE FUNCTION refresh.guard_run_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE parent_project text; parent_environment text; parent_generation text; parent_parent text;
BEGIN
    IF NEW.status <> 'queued' OR NEW.attempt_count <> 0 OR NEW.fence_generation <> 0 OR NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL OR NEW.started_at IS NOT NULL OR NEW.finished_at IS NOT NULL THEN RAISE EXCEPTION 'run inserts must begin as empty queued records'; END IF;
    IF NEW.parent_run_id IS NOT NULL THEN
        IF NEW.parent_run_id = NEW.run_id THEN RAISE EXCEPTION 'run cannot parent itself'; END IF;
        SELECT project_id,environment,generation_id,parent_run_id INTO parent_project,parent_environment,parent_generation,parent_parent FROM refresh.run WHERE run_id=NEW.parent_run_id;
        IF parent_project IS NULL OR parent_project IS DISTINCT FROM NEW.project_id OR parent_environment IS DISTINCT FROM NEW.environment OR parent_generation IS DISTINCT FROM NEW.generation_id OR parent_parent IS NOT NULL THEN
            RAISE EXCEPTION 'run parent must be an existing root in the same serving scope';
        END IF;
    END IF;
    NEW.created_at := clock_timestamp(); NEW.updated_at := NEW.created_at;
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS run_insert_guard ON refresh.run;
CREATE TRIGGER run_insert_guard BEFORE INSERT ON refresh.run FOR EACH ROW EXECUTE FUNCTION refresh.guard_run_insert();

-- Every committed root run must have one canonical platform job.  Dependency
-- children intentionally remain jobless: the root job owns their tree.  A
-- deferred constraint trigger permits the atomic insert-then-attach sequence
-- used by the jobs adapter while rejecting standalone/root rows that would be
-- invisible to queue recovery.
CREATE OR REPLACE FUNCTION refresh.guard_root_job_attachment() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE current_parent text; current_job text; job_kind text; job_workload text; job_resource_kind text; job_resource_id text; job_partition text; job_principal text; job_status text;
BEGIN
    SELECT parent_run_id, job_id INTO current_parent, current_job
      FROM refresh.run WHERE run_id = NEW.run_id;
    IF current_parent IS NULL AND current_job IS NULL THEN
        RAISE EXCEPTION 'root refresh run requires canonical platform job';
    END IF;
    IF current_parent IS NULL THEN
        SELECT kind,workload_class,resource_kind,resource_id,partition_key,principal_id,status
          INTO job_kind,job_workload,job_resource_kind,job_resource_id,job_partition,job_principal,job_status
          FROM jobs.job_history WHERE id=current_job;
        IF job_kind IS DISTINCT FROM 'refresh_pipeline' OR job_workload IS DISTINCT FROM 'background' OR job_resource_kind IS DISTINCT FROM 'refresh_run' OR job_resource_id IS DISTINCT FROM NEW.run_id OR job_partition IS DISTINCT FROM ('refresh:'||NEW.project_id||':'||NEW.environment) OR job_principal IS DISTINCT FROM NEW.principal_id OR job_status IS DISTINCT FROM 'queued' THEN
            RAISE EXCEPTION 'root refresh job does not match canonical queue identity';
        END IF;
    END IF;
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS run_root_job_guard ON refresh.run;
CREATE CONSTRAINT TRIGGER run_root_job_guard
    AFTER INSERT OR UPDATE OF parent_run_id,job_id ON refresh.run
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION refresh.guard_root_job_attachment();

CREATE TABLE IF NOT EXISTS refresh.schedule_occurrence (
    occurrence_id text PRIMARY KEY,
    project_id text NOT NULL,
    environment text NOT NULL,
    pipeline_id text NOT NULL,
    nominal_time timestamptz NOT NULL,
    schedule_revision_id text NOT NULL REFERENCES refresh.schedule_revision(schedule_revision_id),
    matching_schedule_ids jsonb NOT NULL,
    semantic_model_id text NOT NULL,
    generation_id text NOT NULL,
    artifact_digest text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    run_id text REFERENCES refresh.run(run_id),
    fence_generation bigint NOT NULL DEFAULT 0,
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    claimed_at timestamptz,
    finished_at timestamptz,
    outcome jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (occurrence_id = btrim(occurrence_id) AND length(occurrence_id) BETWEEN 1 AND 256),
    CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128),
    CHECK (pipeline_id = btrim(pipeline_id) AND length(pipeline_id) BETWEEN 1 AND 255),
    CHECK (jsonb_typeof(matching_schedule_ids) = 'array' AND octet_length(matching_schedule_ids::text) <= 16384),
    CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (status IN ('pending','claimed','queued','running','succeeded','failed','cancelled','skipped','superseded')),
    CHECK (fence_generation >= 0),
    CHECK ((status = 'claimed' AND lease_owner <> '' AND lease_expires_at IS NOT NULL) OR (status <> 'claimed' AND lease_owner = '' AND lease_expires_at IS NULL)),
    CHECK (jsonb_typeof(outcome) = 'object' AND octet_length(outcome::text) <= 32768),
    UNIQUE (project_id, environment, pipeline_id, nominal_time)
);
CREATE INDEX IF NOT EXISTS occurrence_due_idx ON refresh.schedule_occurrence(project_id, environment, status, nominal_time);

CREATE OR REPLACE FUNCTION refresh.guard_occurrence_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.status <> 'pending' OR NEW.fence_generation <> 0 OR NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL OR NEW.finished_at IS NOT NULL THEN RAISE EXCEPTION 'occurrence inserts must begin pending and unfenced'; END IF;
    NEW.created_at := clock_timestamp();
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS occurrence_insert_guard ON refresh.schedule_occurrence;
CREATE TRIGGER occurrence_insert_guard BEFORE INSERT ON refresh.schedule_occurrence FOR EACH ROW EXECUTE FUNCTION refresh.guard_occurrence_insert();

CREATE TABLE IF NOT EXISTS refresh.attempt (
    run_id text NOT NULL REFERENCES refresh.run(run_id),
    attempt_number bigint NOT NULL,
    fence_generation bigint NOT NULL,
    owner_id text NOT NULL,
    lease_expires_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'running',
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    error text NOT NULL DEFAULT '',
    claimed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    PRIMARY KEY (run_id, attempt_number),
    UNIQUE (run_id, fence_generation),
    CHECK (attempt_number > 0 AND fence_generation > 0),
    CHECK (owner_id = btrim(owner_id) AND length(owner_id) BETWEEN 1 AND 256),
    CHECK (status IN ('running','succeeded','failed','cancelled','expired','indeterminate')),
    CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 65536),
    CHECK ((status = 'running') = (finished_at IS NULL))
);
CREATE INDEX IF NOT EXISTS attempt_lease_idx ON refresh.attempt(status, lease_expires_at);

CREATE OR REPLACE FUNCTION refresh.guard_attempt_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE run_owner text; run_fence bigint; run_status text; run_expiry timestamptz;
BEGIN
    IF NEW.status <> 'running' OR NEW.finished_at IS NOT NULL OR NEW.fence_generation <= 0
       OR NEW.owner_id = '' OR NEW.lease_expires_at <= clock_timestamp()
       OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours'
       OR NEW.evidence IS DISTINCT FROM '{}'::jsonb THEN
        RAISE EXCEPTION 'attempt inserts must begin as live empty evidence';
    END IF;
    SELECT lease_owner,fence_generation,status,lease_expires_at
      INTO run_owner,run_fence,run_status,run_expiry
      FROM refresh.run WHERE run_id=NEW.run_id;
    IF run_owner IS DISTINCT FROM NEW.owner_id OR run_fence IS DISTINCT FROM NEW.fence_generation
       OR run_status <> 'running' OR run_expiry IS NULL OR run_expiry <= clock_timestamp() THEN
        RAISE EXCEPTION 'attempt insert is not fenced by current live run';
    END IF;
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS attempt_insert_guard ON refresh.attempt;
CREATE TRIGGER attempt_insert_guard BEFORE INSERT ON refresh.attempt FOR EACH ROW EXECUTE FUNCTION refresh.guard_attempt_insert();

-- Tree terminalization is kept in capability-owned functions so the recursive
-- transition remains one atomic PostgreSQL statement while sqlc exposes a
-- typed scalar result (the exact number of rows changed).
CREATE OR REPLACE FUNCTION refresh.fail_child_runs(p_run_id text, p_error text)
RETURNS bigint
LANGUAGE plpgsql
SET search_path = pg_catalog, refresh AS $$
DECLARE affected bigint;
BEGIN
    WITH RECURSIVE tree(run_id) AS (
        SELECT r.run_id FROM refresh.run r WHERE r.run_id = p_run_id
        UNION ALL
        SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id = parent.run_id
    )
    UPDATE refresh.run
       SET status='failed', error=p_error, finished_at=clock_timestamp(), lease_owner='', lease_expires_at=NULL
     WHERE run_id IN (SELECT tree.run_id FROM tree WHERE tree.run_id <> p_run_id)
       AND status IN ('queued','running','prepared');
    GET DIAGNOSTICS affected = ROW_COUNT;
    RETURN affected;
END; $$;

CREATE OR REPLACE FUNCTION refresh.complete_child_runs(p_run_id text)
RETURNS bigint
LANGUAGE plpgsql
SET search_path = pg_catalog, refresh AS $$
DECLARE affected bigint;
BEGIN
    WITH RECURSIVE tree(run_id) AS (
        SELECT r.run_id FROM refresh.run r WHERE r.run_id = p_run_id
        UNION ALL
        SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id = parent.run_id
    )
    UPDATE refresh.run
       SET status='succeeded', finished_at=clock_timestamp(), lease_owner='', lease_expires_at=NULL
     WHERE run_id IN (SELECT tree.run_id FROM tree WHERE tree.run_id <> p_run_id)
       AND status IN ('queued','running','prepared');
    GET DIAGNOSTICS affected = ROW_COUNT;
    RETURN affected;
END; $$;

CREATE TABLE IF NOT EXISTS refresh.publication_link (
    publication_id text PRIMARY KEY,
    run_id text NOT NULL REFERENCES refresh.run(run_id),
    base_generation_id text NOT NULL,
    result_generation_id text NOT NULL,
    plan_digest text NOT NULL,
    artifact_digest text NOT NULL,
    physical_pool_id text NOT NULL,
    catalog_id text NOT NULL,
    expected_target_revision bigint NOT NULL CHECK (expected_target_revision > 0),
    result_target_revision bigint NOT NULL CHECK (result_target_revision > expected_target_revision),
    snapshot_id bigint,
    state text NOT NULL DEFAULT 'pending',
    fence_generation bigint NOT NULL,
    owner_id text NOT NULL,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    committed_at timestamptz,
    CHECK (publication_id = btrim(publication_id) AND length(publication_id) BETWEEN 1 AND 256),
    CHECK (base_generation_id = btrim(base_generation_id) AND length(base_generation_id) BETWEEN 1 AND 255),
    CHECK (result_generation_id = btrim(result_generation_id) AND length(result_generation_id) BETWEEN 1 AND 255),
    CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND length(catalog_id) BETWEEN 1 AND 255),
    CHECK (snapshot_id IS NULL OR snapshot_id > 0),
    CHECK (state IN ('pending','committed','failed','fenced')),
    CHECK (fence_generation > 0),
    CHECK (owner_id = btrim(owner_id) AND length(owner_id) BETWEEN 1 AND 256),
    CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 65536),
    CHECK ((state = 'committed') = (committed_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS publication_run_idx ON refresh.publication_link(run_id) WHERE state IN ('pending','committed');

CREATE OR REPLACE FUNCTION refresh.guard_publication_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE run_generation text; run_plan text; run_artifact text; run_owner text; run_fence bigint; run_status text; run_expiry timestamptz;
BEGIN
    IF NEW.state <> 'pending' OR NEW.snapshot_id IS NOT NULL OR NEW.committed_at IS NOT NULL
       OR NEW.evidence = '{}'::jsonb THEN
        RAISE EXCEPTION 'publication links must begin pending without commit evidence';
    END IF;
    SELECT generation_id,plan_digest,artifact_digest,lease_owner,fence_generation,status,lease_expires_at
      INTO run_generation,run_plan,run_artifact,run_owner,run_fence,run_status,run_expiry
      FROM refresh.run WHERE run_id=NEW.run_id;
    IF run_generation IS DISTINCT FROM NEW.base_generation_id OR NEW.result_generation_id = '' OR run_plan IS DISTINCT FROM NEW.plan_digest
       OR run_artifact IS DISTINCT FROM NEW.artifact_digest OR run_owner IS DISTINCT FROM NEW.owner_id
       OR run_fence IS DISTINCT FROM NEW.fence_generation OR run_status NOT IN ('running','prepared')
       OR run_expiry IS NULL OR run_expiry <= clock_timestamp() THEN
        RAISE EXCEPTION 'publication link is not fenced by current live run';
    END IF;
    NEW.created_at := clock_timestamp();
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS publication_insert_guard ON refresh.publication_link;
CREATE TRIGGER publication_insert_guard BEFORE INSERT ON refresh.publication_link FOR EACH ROW EXECUTE FUNCTION refresh.guard_publication_insert();

CREATE TABLE IF NOT EXISTS refresh.recovery_state (
    run_id text PRIMARY KEY REFERENCES refresh.run(run_id),
    state text NOT NULL DEFAULT 'unreconciled',
    reconciliation_fence bigint NOT NULL DEFAULT 0,
    owner_id text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    exact_external_identity text NOT NULL DEFAULT '',
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_error text NOT NULL DEFAULT '',
    next_reconcile_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (state IN ('unreconciled','pending','reconciled','indeterminate','quarantined')),
    CHECK (reconciliation_fence >= 0),
    CHECK ((reconciliation_fence > 0 AND owner_id <> '' AND lease_expires_at IS NOT NULL) OR (reconciliation_fence = 0 AND owner_id = '' AND lease_expires_at IS NULL)),
    CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 65536)
);

CREATE OR REPLACE FUNCTION refresh.guard_recovery_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE run_status text;
BEGIN
    IF NEW.state NOT IN ('unreconciled','pending','reconciled','indeterminate','quarantined')
       OR NEW.reconciliation_fence <> 1
       OR NEW.lease_expires_at IS NULL OR NEW.lease_expires_at <= clock_timestamp()
       OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours'
       OR NEW.owner_id <> btrim(NEW.owner_id) OR length(NEW.owner_id) > 256
       OR NEW.exact_external_identity <> btrim(NEW.exact_external_identity) OR length(NEW.exact_external_identity) > 256
       OR length(NEW.last_error) > 4096
       OR (NEW.reconciliation_fence > 0 AND NEW.evidence = '{}'::jsonb)
       OR (NEW.state IN ('reconciled','indeterminate') AND NEW.exact_external_identity = '')
       OR NEW.evidence IS DISTINCT FROM '{}'::jsonb AND jsonb_typeof(NEW.evidence) <> 'object' THEN
        RAISE EXCEPTION 'recovery inserts must begin with canonical state, fence and evidence';
    END IF;
    SELECT status INTO run_status FROM refresh.run WHERE run_id=NEW.run_id;
    IF run_status NOT IN ('failed','indeterminate') THEN RAISE EXCEPTION 'recovery requires failed or indeterminate run'; END IF;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS recovery_insert_guard ON refresh.recovery_state;
CREATE TRIGGER recovery_insert_guard BEFORE INSERT ON refresh.recovery_state FOR EACH ROW EXECUTE FUNCTION refresh.guard_recovery_insert();

-- A compact serving watermark, not analytical data.  Snapshot identity points
-- into the DuckLake authority and is never a container for result bytes.
CREATE TABLE IF NOT EXISTS refresh.data_version (
    project_id text NOT NULL,
    environment text NOT NULL,
    semantic_model_id text NOT NULL,
    generation_id text NOT NULL,
    snapshot_id bigint NOT NULL CHECK (snapshot_id > 0),
    refreshed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    source text NOT NULL CHECK (source IN ('publish','refresh')),
    physical_pool_id text NOT NULL,
    catalog_id text NOT NULL,
    pipeline_id text NOT NULL DEFAULT '',
    run_id text NOT NULL DEFAULT '',
    target_revision bigint NOT NULL DEFAULT 0 CHECK (target_revision >= 0),
    lease_owner text NOT NULL DEFAULT '',
    lease_revision bigint NOT NULL DEFAULT 0 CHECK (lease_revision >= 0),
    PRIMARY KEY (project_id, environment, semantic_model_id, generation_id),
    CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128),
    CHECK (semantic_model_id = btrim(semantic_model_id) AND length(semantic_model_id) BETWEEN 1 AND 255),
    CHECK (generation_id = btrim(generation_id) AND length(generation_id) BETWEEN 1 AND 255),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND length(catalog_id) BETWEEN 1 AND 255),
    CHECK (pipeline_id = btrim(pipeline_id) AND length(pipeline_id) <= 255),
    CHECK (run_id = btrim(run_id) AND length(run_id) <= 256),
    CHECK ((lease_owner = '') = (lease_revision = 0))
);

CREATE OR REPLACE FUNCTION refresh.guard_data_version_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE pub_generation text; pub_pool text; pub_catalog text; pub_snapshot bigint; pub_run text; pub_project text; pub_environment text; pub_model text;
BEGIN
    IF NEW.refreshed_at IS NULL OR NEW.snapshot_id <= 0 OR NEW.source NOT IN ('publish','refresh')
       OR (NEW.lease_revision = 0 AND NEW.lease_owner <> '')
       OR (NEW.lease_revision > 0 AND NEW.lease_owner = '') THEN
        RAISE EXCEPTION 'data version insert is not canonical';
    END IF;
    SELECT p.result_generation_id,p.physical_pool_id,p.catalog_id,p.snapshot_id,p.run_id,r.project_id,r.environment,r.semantic_model_id
      INTO pub_generation,pub_pool,pub_catalog,pub_snapshot,pub_run,pub_project,pub_environment,pub_model
      FROM refresh.publication_link p JOIN refresh.run r ON r.run_id=p.run_id
     WHERE p.run_id=NEW.run_id AND p.state='committed' AND p.result_generation_id=NEW.generation_id
       AND p.physical_pool_id=NEW.physical_pool_id AND p.catalog_id=NEW.catalog_id
       AND p.snapshot_id=NEW.snapshot_id AND (NEW.source='publish' OR (p.fence_generation=NEW.lease_revision AND p.owner_id=NEW.lease_owner));
    IF pub_run IS NULL OR pub_project IS DISTINCT FROM NEW.project_id OR pub_environment IS DISTINCT FROM NEW.environment
       OR pub_model IS DISTINCT FROM NEW.semantic_model_id OR pub_generation IS DISTINCT FROM NEW.generation_id THEN
        RAISE EXCEPTION 'data version must reference exact committed publication';
    END IF;
    IF NEW.source='publish' AND NEW.lease_revision <> 0 THEN
        RAISE EXCEPTION 'published data versions cannot carry a refresh lease';
    END IF;
    IF NEW.source='refresh' AND NEW.lease_revision > 0 THEN
        IF NOT EXISTS (SELECT 1 FROM refresh.run r JOIN refresh.publication_link p ON p.run_id=r.run_id WHERE r.run_id=NEW.run_id AND r.project_id=NEW.project_id AND r.environment=NEW.environment AND p.base_generation_id=r.generation_id AND p.result_generation_id=NEW.generation_id AND p.state='committed' AND p.fence_generation=NEW.lease_revision AND p.owner_id=NEW.lease_owner AND r.status IN ('running','prepared','succeeded')) THEN
            RAISE EXCEPTION 'refresh data version lease is not tied to current run';
        END IF;
    END IF;
    NEW.refreshed_at := clock_timestamp();
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS data_version_insert_guard ON refresh.data_version;
CREATE TRIGGER data_version_insert_guard BEFORE INSERT ON refresh.data_version FOR EACH ROW EXECUTE FUNCTION refresh.guard_data_version_insert();

CREATE OR REPLACE FUNCTION refresh.guard_data_version_update() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.semantic_model_id IS DISTINCT FROM OLD.semantic_model_id OR NEW.generation_id IS DISTINCT FROM OLD.generation_id THEN RAISE EXCEPTION 'data version identity is immutable'; END IF;
    IF NEW.lease_revision < OLD.lease_revision THEN RAISE EXCEPTION 'data version fence cannot decrease'; END IF;
    IF NEW.lease_revision = OLD.lease_revision AND (NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id OR NEW.source IS DISTINCT FROM OLD.source OR NEW.physical_pool_id IS DISTINCT FROM OLD.physical_pool_id OR NEW.catalog_id IS DISTINCT FROM OLD.catalog_id OR NEW.pipeline_id IS DISTINCT FROM OLD.pipeline_id OR NEW.run_id IS DISTINCT FROM OLD.run_id OR NEW.target_revision IS DISTINCT FROM OLD.target_revision OR NEW.lease_owner IS DISTINCT FROM OLD.lease_owner) THEN RAISE EXCEPTION 'equal-fence data version replay conflicts'; END IF;
    IF NEW.lease_revision > OLD.lease_revision THEN
		IF NEW.source='publish' AND NEW.lease_revision <> 0 THEN RAISE EXCEPTION 'published data versions cannot carry a refresh lease'; END IF;
        IF NOT EXISTS (SELECT 1 FROM refresh.publication_link p JOIN refresh.run r ON r.run_id=p.run_id WHERE p.run_id=NEW.run_id AND p.state='committed' AND p.result_generation_id=NEW.generation_id AND p.physical_pool_id=NEW.physical_pool_id AND p.catalog_id=NEW.catalog_id AND p.snapshot_id=NEW.snapshot_id AND p.fence_generation=NEW.lease_revision AND r.project_id=NEW.project_id AND r.environment=NEW.environment AND r.semantic_model_id=NEW.semantic_model_id) THEN
            RAISE EXCEPTION 'higher-fence data version must reference exact committed publication';
        END IF;
        IF NEW.source='refresh' AND NOT EXISTS (SELECT 1 FROM refresh.run r JOIN refresh.publication_link p ON p.run_id=r.run_id WHERE r.run_id=NEW.run_id AND r.project_id=NEW.project_id AND r.environment=NEW.environment AND p.base_generation_id=r.generation_id AND p.result_generation_id=NEW.generation_id AND p.state='committed' AND p.fence_generation=NEW.lease_revision AND p.owner_id=NEW.lease_owner AND r.status IN ('running','prepared','succeeded')) THEN
            RAISE EXCEPTION 'higher-fence data version lease is not tied to current run';
        END IF;
    END IF;
    NEW.refreshed_at := clock_timestamp();
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS data_version_guard ON refresh.data_version;
CREATE TRIGGER data_version_guard BEFORE UPDATE ON refresh.data_version FOR EACH ROW EXECUTE FUNCTION refresh.guard_data_version_update();

-- Database-owned timestamps and monotonic fencing are defence in depth for
-- roles that accidentally receive a wider UPDATE privilege.
CREATE OR REPLACE FUNCTION refresh.guard_updated_at() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE recovery_run_status text;
BEGIN
    NEW.updated_at := clock_timestamp();
    IF TG_TABLE_NAME = 'run' THEN
        IF NEW.run_id IS DISTINCT FROM OLD.run_id OR NEW.operation_id IS DISTINCT FROM OLD.operation_id OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.generation_id IS DISTINCT FROM OLD.generation_id OR NEW.parent_run_id IS DISTINCT FROM OLD.parent_run_id OR NEW.pipeline_id IS DISTINCT FROM OLD.pipeline_id OR NEW.semantic_model_id IS DISTINCT FROM OLD.semantic_model_id OR NEW.target_type IS DISTINCT FROM OLD.target_type OR NEW.target_id IS DISTINCT FROM OLD.target_id OR NEW.target_revision IS DISTINCT FROM OLD.target_revision OR NEW.trigger_type IS DISTINCT FROM OLD.trigger_type OR NEW.invocation_source IS DISTINCT FROM OLD.invocation_source OR NEW.trigger_id IS DISTINCT FROM OLD.trigger_id OR NEW.concurrency_policy IS DISTINCT FROM OLD.concurrency_policy OR NEW.schedule_revision_id IS DISTINCT FROM OLD.schedule_revision_id OR NEW.occurrence_id IS DISTINCT FROM OLD.occurrence_id OR NEW.nominal_time IS DISTINCT FROM OLD.nominal_time OR NEW.plan_digest IS DISTINCT FROM OLD.plan_digest OR NEW.artifact_digest IS DISTINCT FROM OLD.artifact_digest OR NEW.matching_schedule_ids IS DISTINCT FROM OLD.matching_schedule_ids OR NEW.materialization_scope IS DISTINCT FROM OLD.materialization_scope OR NEW.principal_id IS DISTINCT FROM OLD.principal_id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN RAISE EXCEPTION 'run identity is immutable'; END IF;
        IF NEW.job_id IS DISTINCT FROM OLD.job_id AND (OLD.job_id <> '' OR NEW.job_id = '') THEN RAISE EXCEPTION 'run job attachment is immutable after first bind'; END IF;
        IF OLD.status IN ('succeeded','failed','cancelled','superseded','skipped') AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'terminal run is immutable'; END IF;
        IF OLD.status='queued' AND NEW.status NOT IN ('queued','running','succeeded','cancelled','failed','superseded','skipped') THEN RAISE EXCEPTION 'illegal queued run transition'; END IF;
		IF OLD.status='running' AND NEW.status NOT IN ('running','prepared','succeeded','failed','cancelled','superseded') THEN RAISE EXCEPTION 'illegal running run transition'; END IF;
		IF OLD.status='prepared' AND NEW.status NOT IN ('running','prepared','succeeded','failed','cancelled','superseded') THEN RAISE EXCEPTION 'illegal prepared run transition'; END IF;
		IF NEW.status='running' THEN
			IF NEW.lease_owner = '' OR NEW.lease_expires_at IS NULL OR NEW.lease_expires_at <= clock_timestamp() OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours' THEN RAISE EXCEPTION 'running run requires a live bounded lease'; END IF;
			IF OLD.status='queued' AND (NEW.fence_generation <> OLD.fence_generation + 1 OR NEW.attempt_count <> OLD.attempt_count + 1 OR NEW.started_at IS NULL) THEN RAISE EXCEPTION 'queued run claim must advance fence and attempt'; END IF;
			IF OLD.status='running' AND NEW.fence_generation > OLD.fence_generation AND (OLD.lease_expires_at IS NULL OR OLD.lease_expires_at > clock_timestamp() OR NEW.attempt_count <> OLD.attempt_count + 1) THEN RAISE EXCEPTION 'run takeover requires expired lease and next attempt'; END IF;
			IF OLD.status='prepared' AND (NEW.fence_generation <> OLD.fence_generation + 1 OR NEW.attempt_count <> OLD.attempt_count + 1 OR OLD.lease_expires_at IS NULL OR OLD.lease_expires_at > clock_timestamp()) THEN RAISE EXCEPTION 'prepared run takeover requires expired lease and next attempt'; END IF;
			IF OLD.status='running' AND NEW.fence_generation = OLD.fence_generation AND NEW.lease_owner IS DISTINCT FROM OLD.lease_owner THEN RAISE EXCEPTION 'run owner change requires a new fence'; END IF;
		END IF;
		IF NEW.status='prepared' AND (NEW.lease_owner = '' OR NEW.lease_expires_at IS NULL OR NEW.lease_expires_at <= clock_timestamp() OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours') THEN RAISE EXCEPTION 'prepared run requires a live bounded lease'; END IF;
		IF OLD.status IN ('running','prepared') AND NEW.status='prepared' AND NEW.fence_generation > OLD.fence_generation AND (OLD.lease_expires_at IS NULL OR OLD.lease_expires_at > clock_timestamp() OR NEW.attempt_count <> OLD.attempt_count + 1) THEN RAISE EXCEPTION 'run takeover requires expired lease and next attempt'; END IF;
		IF OLD.status IN ('running','prepared') AND NEW.status='prepared' AND NEW.fence_generation = OLD.fence_generation AND NEW.lease_owner IS DISTINCT FROM OLD.lease_owner THEN RAISE EXCEPTION 'run owner change requires a new fence'; END IF;
		IF OLD.status='running' AND NEW.status IN ('succeeded','failed','cancelled','superseded') AND (NEW.finished_at IS NULL OR NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL) THEN RAISE EXCEPTION 'terminal run requires closed lease and finish time'; END IF;
		IF OLD.status='queued' AND NEW.status IN ('succeeded','cancelled','failed','superseded','skipped') AND (NEW.finished_at IS NULL OR NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL) THEN RAISE EXCEPTION 'queued terminal run requires finish time'; END IF;
		IF OLD.status='prepared' AND NEW.status IN ('succeeded','failed','cancelled','superseded') AND (NEW.finished_at IS NULL OR NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL) THEN RAISE EXCEPTION 'terminal prepared run requires closed lease and finish time'; END IF;
		IF NEW.status='superseded' AND NEW.trigger_type <> 'schedule' THEN RAISE EXCEPTION 'only scheduled runs may be superseded by overlap replacement'; END IF;
	ELSIF TG_TABLE_NAME = 'recovery_state' THEN
		SELECT status INTO recovery_run_status FROM refresh.run WHERE run_id=NEW.run_id;
		IF recovery_run_status NOT IN ('failed','indeterminate') THEN RAISE EXCEPTION 'recovery requires failed or indeterminate run'; END IF;
		IF NEW.run_id IS DISTINCT FROM OLD.run_id THEN RAISE EXCEPTION 'recovery run identity is immutable'; END IF;
		IF NEW.reconciliation_fence = OLD.reconciliation_fence AND (NEW.state IS DISTINCT FROM OLD.state OR NEW.owner_id IS DISTINCT FROM OLD.owner_id OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at OR NEW.exact_external_identity IS DISTINCT FROM OLD.exact_external_identity OR NEW.last_error IS DISTINCT FROM OLD.last_error OR NEW.evidence IS DISTINCT FROM OLD.evidence OR NEW.next_reconcile_at IS DISTINCT FROM OLD.next_reconcile_at) THEN RAISE EXCEPTION 'equal-fence recovery replay conflicts'; END IF;
		IF NEW.reconciliation_fence > OLD.reconciliation_fence AND (NEW.reconciliation_fence <> OLD.reconciliation_fence + 1 OR NEW.owner_id = '' OR NEW.lease_expires_at IS NULL OR NEW.lease_expires_at <= clock_timestamp() OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours') THEN RAISE EXCEPTION 'recovery fence must advance one live lease at a time'; END IF;
		IF NEW.reconciliation_fence > OLD.reconciliation_fence AND (OLD.lease_expires_at IS NULL OR OLD.lease_expires_at > clock_timestamp()) THEN RAISE EXCEPTION 'recovery takeover requires expired authority lease'; END IF;
		IF NEW.state IN ('reconciled','indeterminate') AND NEW.exact_external_identity = '' THEN RAISE EXCEPTION 'terminal recovery requires exact external identity'; END IF;
    END IF;
    IF TG_TABLE_NAME = 'run' THEN
        IF NEW.fence_generation < OLD.fence_generation THEN RAISE EXCEPTION 'run fence cannot decrease'; END IF;
    ELSIF TG_TABLE_NAME = 'recovery_state' THEN
        IF NEW.reconciliation_fence < OLD.reconciliation_fence THEN RAISE EXCEPTION 'reconciliation fence cannot decrease'; END IF;
    END IF;
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS run_updated_at ON refresh.run;
CREATE TRIGGER run_updated_at BEFORE UPDATE ON refresh.run FOR EACH ROW EXECUTE FUNCTION refresh.guard_updated_at();

CREATE OR REPLACE FUNCTION refresh.guard_run_claim_evidence() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM refresh.attempt a WHERE a.run_id=NEW.run_id AND a.attempt_number=NEW.attempt_count AND a.fence_generation=NEW.fence_generation) THEN
        RAISE EXCEPTION 'running run must have matching durable attempt evidence';
    END IF;
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS run_claim_evidence_guard ON refresh.run;
CREATE CONSTRAINT TRIGGER run_claim_evidence_guard
    AFTER UPDATE OF status ON refresh.run
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (NEW.status='running' AND OLD.status IN ('queued','prepared'))
    EXECUTE FUNCTION refresh.guard_run_claim_evidence();

DROP TRIGGER IF EXISTS recovery_updated_at ON refresh.recovery_state;
CREATE TRIGGER recovery_updated_at BEFORE UPDATE ON refresh.recovery_state FOR EACH ROW EXECUTE FUNCTION refresh.guard_updated_at();

CREATE OR REPLACE FUNCTION refresh.guard_attempt_update() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE run_owner text; run_fence bigint; run_status text; run_expiry timestamptz;
BEGIN
    IF NEW.run_id IS DISTINCT FROM OLD.run_id OR NEW.attempt_number IS DISTINCT FROM OLD.attempt_number OR NEW.fence_generation < OLD.fence_generation OR NEW.owner_id IS DISTINCT FROM OLD.owner_id OR NEW.claimed_at IS DISTINCT FROM OLD.claimed_at OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN RAISE EXCEPTION 'attempt identity is immutable'; END IF;
    IF OLD.status <> 'running' AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'terminal attempt is immutable'; END IF;
    SELECT lease_owner,fence_generation,status,lease_expires_at INTO run_owner,run_fence,run_status,run_expiry FROM refresh.run WHERE run_id=NEW.run_id;
    IF NEW.status IN ('succeeded','failed','cancelled','indeterminate') AND NEW.evidence = '{}'::jsonb THEN RAISE EXCEPTION 'terminal attempt requires evidence'; END IF;
    IF NEW.status IN ('succeeded','failed','cancelled','indeterminate') AND (run_owner IS DISTINCT FROM NEW.owner_id OR run_fence IS DISTINCT FROM NEW.fence_generation OR run_status NOT IN ('running','prepared') OR run_expiry IS NULL OR run_expiry <= clock_timestamp()) THEN
        RAISE EXCEPTION 'attempt terminal transition is not fenced by current live run';
    END IF;
    IF NEW.status='expired' AND (run_fence IS DISTINCT FROM NEW.fence_generation OR run_status NOT IN ('running','prepared','failed','superseded') OR run_expiry IS NULL OR run_expiry > clock_timestamp()) THEN
        RAISE EXCEPTION 'attempt expiry is not fenced by expired run';
    END IF;
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS attempt_guard ON refresh.attempt;
CREATE TRIGGER attempt_guard BEFORE UPDATE ON refresh.attempt FOR EACH ROW EXECUTE FUNCTION refresh.guard_attempt_update();

CREATE OR REPLACE FUNCTION refresh.guard_publication_update() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE run_owner text; run_fence bigint; run_status text; run_expiry timestamptz;
BEGIN
    IF NEW.publication_id IS DISTINCT FROM OLD.publication_id OR NEW.run_id IS DISTINCT FROM OLD.run_id OR NEW.base_generation_id IS DISTINCT FROM OLD.base_generation_id OR NEW.result_generation_id IS DISTINCT FROM OLD.result_generation_id OR NEW.plan_digest IS DISTINCT FROM OLD.plan_digest OR NEW.artifact_digest IS DISTINCT FROM OLD.artifact_digest OR NEW.physical_pool_id IS DISTINCT FROM OLD.physical_pool_id OR NEW.catalog_id IS DISTINCT FROM OLD.catalog_id OR NEW.expected_target_revision IS DISTINCT FROM OLD.expected_target_revision OR NEW.result_target_revision IS DISTINCT FROM OLD.result_target_revision OR NEW.fence_generation IS DISTINCT FROM OLD.fence_generation OR NEW.owner_id IS DISTINCT FROM OLD.owner_id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN RAISE EXCEPTION 'publication identity is immutable'; END IF;
    IF OLD.state <> 'pending' AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'terminal publication is immutable'; END IF;
    IF NEW.state <> 'committed' OR NEW.snapshot_id IS NULL OR NEW.snapshot_id <= 0 OR NEW.committed_at IS NULL OR NEW.evidence = '{}'::jsonb THEN RAISE EXCEPTION 'publication transition requires committed physical evidence'; END IF;
    SELECT lease_owner,fence_generation,status,lease_expires_at INTO run_owner,run_fence,run_status,run_expiry FROM refresh.run WHERE run_id=NEW.run_id;
    IF run_owner IS DISTINCT FROM NEW.owner_id OR run_fence IS DISTINCT FROM NEW.fence_generation OR run_status NOT IN ('running','prepared') OR run_expiry IS NULL OR run_expiry <= clock_timestamp() THEN RAISE EXCEPTION 'publication transition is not fenced by current live run'; END IF;
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS publication_guard ON refresh.publication_link;
CREATE TRIGGER publication_guard BEFORE UPDATE ON refresh.publication_link FOR EACH ROW EXECUTE FUNCTION refresh.guard_publication_update();

CREATE OR REPLACE FUNCTION refresh.guard_occurrence_update() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.occurrence_id IS DISTINCT FROM OLD.occurrence_id OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.pipeline_id IS DISTINCT FROM OLD.pipeline_id OR NEW.nominal_time IS DISTINCT FROM OLD.nominal_time OR NEW.schedule_revision_id IS DISTINCT FROM OLD.schedule_revision_id OR NEW.matching_schedule_ids IS DISTINCT FROM OLD.matching_schedule_ids OR NEW.generation_id IS DISTINCT FROM OLD.generation_id OR NEW.artifact_digest IS DISTINCT FROM OLD.artifact_digest OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.fence_generation < OLD.fence_generation THEN RAISE EXCEPTION 'occurrence identity is immutable'; END IF;
	IF OLD.run_id IS NOT NULL AND NEW.run_id IS DISTINCT FROM OLD.run_id THEN RAISE EXCEPTION 'occurrence run binding is immutable'; END IF;
	IF OLD.run_id IS NULL AND NEW.run_id IS NOT NULL AND NOT (OLD.status='claimed' AND NEW.status='queued') THEN RAISE EXCEPTION 'occurrence run binding requires claimed to queued transition'; END IF;
    IF NEW.status IN ('queued','running','succeeded','failed','cancelled','superseded') AND NEW.run_id IS NULL THEN RAISE EXCEPTION 'bound occurrence status requires run id'; END IF;
    IF OLD.status IN ('succeeded','failed','cancelled','skipped','superseded') AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'terminal occurrence is immutable'; END IF;
    IF OLD.status='pending' AND NEW.status NOT IN ('pending','claimed','skipped','superseded') THEN RAISE EXCEPTION 'illegal pending occurrence transition'; END IF;
    IF OLD.status='claimed' AND NEW.status NOT IN ('claimed','pending','queued') THEN RAISE EXCEPTION 'illegal claimed occurrence transition'; END IF;
    IF NEW.status='claimed' AND (NEW.lease_owner = '' OR NEW.lease_expires_at IS NULL OR NEW.lease_expires_at <= clock_timestamp() OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours' OR NEW.claimed_at IS NULL) THEN RAISE EXCEPTION 'claimed occurrence requires a live bounded lease'; END IF;
    IF OLD.status='pending' AND NEW.status='claimed' AND NEW.fence_generation <> OLD.fence_generation + 1 THEN RAISE EXCEPTION 'occurrence claim must advance fence'; END IF;
    IF OLD.status='claimed' AND NEW.status='claimed' AND NEW.fence_generation <> OLD.fence_generation THEN RAISE EXCEPTION 'occurrence heartbeat cannot change fence'; END IF;
    IF NEW.status IN ('queued','running','succeeded','failed','cancelled','skipped','superseded') AND (NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL) THEN RAISE EXCEPTION 'non-claimed occurrence cannot hold a lease'; END IF;
	IF OLD.status='queued' AND NEW.status NOT IN ('queued','running','succeeded','failed','cancelled','superseded') THEN RAISE EXCEPTION 'illegal queued occurrence transition'; END IF;
	IF OLD.status='running' AND NEW.status NOT IN ('running','succeeded','failed','cancelled','superseded') THEN RAISE EXCEPTION 'illegal running occurrence transition'; END IF;
	IF NEW.status IN ('pending','claimed','queued','running') AND NEW.finished_at IS NOT NULL THEN RAISE EXCEPTION 'active occurrence cannot have finished time'; END IF;
	IF NEW.status IN ('succeeded','failed','cancelled','skipped','superseded') AND (NEW.finished_at IS NULL OR NEW.outcome = '{}'::jsonb) THEN RAISE EXCEPTION 'terminal occurrence requires finish time and outcome'; END IF;
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS occurrence_guard ON refresh.schedule_occurrence;
CREATE TRIGGER occurrence_guard BEFORE UPDATE ON refresh.schedule_occurrence FOR EACH ROW EXECUTE FUNCTION refresh.guard_occurrence_update();

-- Runtime roles never receive direct DELETE.  This bounded maintenance
-- function only expires abandoned leases and leaves all evidence queryable.
CREATE OR REPLACE FUNCTION refresh.maintenance(p_limit integer)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, refresh AS $$
DECLARE v_count bigint := 0; v_affected bigint := 0; v_limit integer;
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN RAISE EXCEPTION 'refresh maintenance limit must be between 1 and 1000'; END IF;
    v_limit := p_limit;
    WITH stale AS (
        SELECT run_id, attempt_number FROM refresh.attempt
        WHERE status = 'running' AND lease_expires_at <= clock_timestamp()
        ORDER BY lease_expires_at, run_id, attempt_number LIMIT v_limit
        FOR UPDATE SKIP LOCKED
    )
    UPDATE refresh.attempt a SET status='expired', finished_at=clock_timestamp(), error='lease expired'
    FROM stale s WHERE a.run_id=s.run_id AND a.attempt_number=s.attempt_number;
    GET DIAGNOSTICS v_affected = ROW_COUNT;
    v_count := v_affected;
    v_limit := v_limit - v_affected::integer;
    IF v_limit <= 0 THEN
        RETURN v_count;
    END IF;
    WITH stale_runs AS (
        SELECT r.run_id FROM refresh.run r
        WHERE r.status IN ('running','prepared') AND r.lease_expires_at <= clock_timestamp()
        ORDER BY r.lease_expires_at, r.run_id LIMIT v_limit FOR UPDATE SKIP LOCKED
    )
    UPDATE refresh.run r SET status='failed', error='lease expired', finished_at=clock_timestamp(), lease_owner='', lease_expires_at=NULL
    FROM stale_runs s WHERE r.run_id=s.run_id AND r.status IN ('running','prepared');
    GET DIAGNOSTICS v_affected = ROW_COUNT;
    RETURN v_count + v_affected;
END; $$;

-- Capability grants are conditional so SchemaSQL remains independently
-- applicable to an empty conformance database.  Production provisioning
-- creates these roles before applying the control baseline.
DO $$
BEGIN
	REVOKE ALL ON ALL TABLES IN SCHEMA refresh FROM PUBLIC;
	REVOKE ALL ON ALL SEQUENCES IN SCHEMA refresh FROM PUBLIC;
	REVOKE ALL ON FUNCTION refresh.maintenance(integer) FROM PUBLIC;
	REVOKE ALL ON FUNCTION refresh.fail_child_runs(text,text) FROM PUBLIC;
	REVOKE ALL ON FUNCTION refresh.complete_child_runs(text) FROM PUBLIC;
	REVOKE ALL ON FUNCTION refresh.close_omitted_schedules(text,text,text,text[],text[]) FROM PUBLIC;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN
        GRANT ALL ON ALL TABLES IN SCHEMA refresh TO leapview_control_owner;
        GRANT ALL ON ALL SEQUENCES IN SCHEMA refresh TO leapview_control_owner;
        GRANT ALL ON ALL FUNCTIONS IN SCHEMA refresh TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_migrator') THEN
        GRANT ALL ON ALL TABLES IN SCHEMA refresh TO leapview_control_migrator;
        GRANT ALL ON ALL SEQUENCES IN SCHEMA refresh TO leapview_control_migrator;
        GRANT ALL ON ALL FUNCTIONS IN SCHEMA refresh TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION refresh.maintenance(integer) TO leapview_control_maintenance;
        REVOKE ALL ON FUNCTION refresh.fail_child_runs(text,text), refresh.complete_child_runs(text), refresh.close_omitted_schedules(text,text,text,text[],text[]) FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON refresh.schedule_revision, refresh.run, refresh.schedule_occurrence, refresh.attempt, refresh.publication_link, refresh.recovery_state, refresh.data_version TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION refresh.fail_child_runs(text,text), refresh.complete_child_runs(text), refresh.close_omitted_schedules(text,text,text,text[],text[]) TO leapview_control_runtime;
        REVOKE DELETE ON refresh.schedule_revision, refresh.run, refresh.schedule_occurrence, refresh.attempt, refresh.publication_link, refresh.recovery_state, refresh.data_version FROM leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION refresh.maintenance(integer) FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA refresh TO leapview_control_readonly;
        REVOKE ALL ON FUNCTION refresh.fail_child_runs(text,text), refresh.complete_child_runs(text), refresh.close_omitted_schedules(text,text,text,text[],text[]) FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA refresh TO leapview_control_backup;
        REVOKE ALL ON FUNCTION refresh.fail_child_runs(text,text), refresh.complete_child_runs(text), refresh.close_omitted_schedules(text,text,text,text[],text[]) FROM leapview_control_backup;
    END IF;
END $$;
