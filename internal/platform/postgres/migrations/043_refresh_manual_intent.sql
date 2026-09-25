-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- capability source: internal/refresh/postgres/schema.sql
-- Accepted manual refresh requests remain distinct from immutable run rows
-- until a worker has won the scoped claim and atomically attached a job.
CREATE TABLE IF NOT EXISTS refresh.manual_intent (
    intent_id text PRIMARY KEY,
    reserved_run_id text NOT NULL UNIQUE,
    project_id text NOT NULL,
    environment text NOT NULL,
    pipeline_id text NOT NULL,
    target_id text NOT NULL,
    principal_id text NOT NULL,
    source_digest text NOT NULL,
    idempotency_key text NOT NULL,
    request_digest text NOT NULL,
    audit_intent jsonb NOT NULL DEFAULT '{}'::jsonb,
    authority_envelope jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'waiting',
    stale_reason text NOT NULL DEFAULT '',
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    fence_generation bigint NOT NULL DEFAULT 0,
    claimed_at timestamptz,
    attached_run_id text REFERENCES refresh.run(run_id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(project_id, environment, principal_id, idempotency_key),
    CHECK (intent_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    CHECK (reserved_run_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128),
    CHECK (pipeline_id = btrim(pipeline_id) AND length(pipeline_id) BETWEEN 1 AND 255),
    CHECK (target_id = btrim(target_id) AND length(target_id) BETWEEN 1 AND 255),
    CHECK (principal_id = btrim(principal_id) AND length(principal_id) BETWEEN 1 AND 255),
    CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (idempotency_key = btrim(idempotency_key) AND length(idempotency_key) BETWEEN 1 AND 256),
    CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(audit_intent) = 'object' AND octet_length(audit_intent::text) <= 65536),
    CHECK (jsonb_typeof(authority_envelope) = 'object' AND octet_length(authority_envelope::text) <= 65536),
    CHECK (status IN ('waiting','claimed','attached','cancelled','stale')),
    CHECK ((status = 'stale' AND length(stale_reason) BETWEEN 1 AND 256) OR (status <> 'stale' AND stale_reason = '')),
    CHECK (fence_generation >= 0),
    CHECK ((status = 'claimed' AND lease_owner <> '' AND lease_expires_at IS NOT NULL) OR (status <> 'claimed' AND lease_owner = '' AND lease_expires_at IS NULL)),
    CHECK ((status = 'attached' AND attached_run_id = reserved_run_id) OR (status <> 'attached' AND attached_run_id IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS manual_intent_one_claim_per_scope
    ON refresh.manual_intent(project_id, environment) WHERE status = 'claimed';
CREATE INDEX IF NOT EXISTS manual_intent_waiting_fifo_idx
    ON refresh.manual_intent(project_id, environment, created_at, intent_id)
    WHERE status IN ('waiting','claimed');
CREATE INDEX IF NOT EXISTS manual_intent_pipeline_idx
    ON refresh.manual_intent(project_id, environment, pipeline_id, created_at, intent_id);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION refresh.guard_manual_intent_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.status <> 'waiting' OR NEW.stale_reason <> '' OR NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL OR NEW.fence_generation <> 0 OR NEW.claimed_at IS NOT NULL OR NEW.attached_run_id IS NOT NULL THEN
        RAISE EXCEPTION 'manual intent inserts must begin waiting and unclaimed';
    END IF;
    NEW.created_at := clock_timestamp();
    NEW.updated_at := NEW.created_at;
    RETURN NEW;
END; $$;
-- +goose StatementEnd

CREATE TRIGGER manual_intent_insert_guard BEFORE INSERT ON refresh.manual_intent
FOR EACH ROW EXECUTE FUNCTION refresh.guard_manual_intent_insert();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION refresh.guard_manual_intent_update() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.intent_id IS DISTINCT FROM OLD.intent_id OR NEW.reserved_run_id IS DISTINCT FROM OLD.reserved_run_id
       OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.environment IS DISTINCT FROM OLD.environment
       OR NEW.pipeline_id IS DISTINCT FROM OLD.pipeline_id OR NEW.target_id IS DISTINCT FROM OLD.target_id
       OR NEW.principal_id IS DISTINCT FROM OLD.principal_id OR NEW.source_digest IS DISTINCT FROM OLD.source_digest
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key OR NEW.request_digest IS DISTINCT FROM OLD.request_digest
       OR NEW.audit_intent IS DISTINCT FROM OLD.audit_intent OR NEW.authority_envelope IS DISTINCT FROM OLD.authority_envelope
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'manual intent identity is immutable';
    END IF;
    IF OLD.status IN ('attached','cancelled','stale') AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal manual intent is immutable';
    END IF;
    IF OLD.status = 'waiting' AND NEW.status NOT IN ('waiting','claimed','cancelled','stale') THEN
        RAISE EXCEPTION 'illegal waiting manual intent transition';
    END IF;
    IF OLD.status = 'claimed' AND NEW.status NOT IN ('claimed','waiting','attached','cancelled','stale') THEN
        RAISE EXCEPTION 'illegal claimed manual intent transition';
    END IF;
    IF OLD.status = 'claimed' AND NEW.status IN ('waiting','attached','stale')
       AND (OLD.lease_expires_at <= clock_timestamp() OR NEW.fence_generation <> OLD.fence_generation) THEN
        RAISE EXCEPTION 'manual intent transition requires current live fence';
    END IF;
    IF NEW.status = 'claimed' THEN
        IF NEW.lease_owner = '' OR NEW.lease_expires_at IS NULL OR NEW.lease_expires_at <= clock_timestamp()
           OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours' OR NEW.claimed_at IS NULL THEN
            RAISE EXCEPTION 'claimed manual intent requires a live bounded lease';
        END IF;
        IF OLD.status = 'waiting' AND NEW.fence_generation <> OLD.fence_generation + 1 THEN
            RAISE EXCEPTION 'manual intent claim must advance fence';
        END IF;
        IF OLD.status = 'claimed' AND NEW.fence_generation > OLD.fence_generation
           AND (NEW.fence_generation <> OLD.fence_generation + 1 OR OLD.lease_expires_at > clock_timestamp()) THEN
            RAISE EXCEPTION 'manual intent takeover requires expired lease and next fence';
        END IF;
        IF OLD.status = 'claimed' AND NEW.fence_generation = OLD.fence_generation
           AND NEW.lease_owner IS DISTINCT FROM OLD.lease_owner THEN
            RAISE EXCEPTION 'manual intent owner change requires a new fence';
        END IF;
    ELSE
        IF NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL THEN
            RAISE EXCEPTION 'non-claimed manual intent cannot hold a lease';
        END IF;
        IF NEW.status = 'attached' AND NEW.attached_run_id IS DISTINCT FROM NEW.reserved_run_id THEN
            RAISE EXCEPTION 'attached manual intent must use its reserved run id';
        END IF;
        IF NEW.status = 'attached' AND NOT EXISTS (
            SELECT 1 FROM refresh.run r
             WHERE r.run_id=NEW.reserved_run_id AND r.project_id=NEW.project_id AND r.environment=NEW.environment
               AND r.pipeline_id=NEW.pipeline_id AND r.target_type='refresh_pipeline' AND r.target_id=NEW.pipeline_id
               AND r.principal_id=NEW.principal_id AND r.trigger_type='manual' AND r.invocation_source='manual'
               AND r.parent_run_id IS NULL AND r.job_id IS NOT NULL
               AND r.status IN ('queued','running','prepared','succeeded','failed','cancelled','superseded','skipped')
        ) THEN
            RAISE EXCEPTION 'attached manual intent requires its matching persisted root run';
        END IF;
        IF NEW.status <> 'attached' AND NEW.attached_run_id IS NOT NULL THEN
            RAISE EXCEPTION 'only attached manual intents may name a run';
        END IF;
    END IF;
    IF NEW.fence_generation < OLD.fence_generation THEN
        RAISE EXCEPTION 'manual intent fence cannot decrease';
    END IF;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END; $$;
-- +goose StatementEnd

CREATE TRIGGER manual_intent_guard BEFORE UPDATE ON refresh.manual_intent
FOR EACH ROW EXECUTE FUNCTION refresh.guard_manual_intent_update();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION refresh.notify_manual_intent_change() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF TG_OP = 'INSERT' OR NEW.status IS DISTINCT FROM OLD.status
       OR NEW.lease_owner IS DISTINCT FROM OLD.lease_owner
       OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
       OR NEW.fence_generation IS DISTINCT FROM OLD.fence_generation THEN
        PERFORM pg_notify('leapview_refresh_changed', json_build_object('projectId', NEW.project_id, 'environment', NEW.environment)::text);
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd

CREATE TRIGGER manual_intent_notify_change AFTER INSERT OR UPDATE OF status ON refresh.manual_intent
FOR EACH ROW EXECUTE FUNCTION refresh.notify_manual_intent_change();

-- Every root shares the project/environment lock used by manual claims. This
-- is the current single DuckLake publication target, so different pipeline
-- ids cannot admit concurrent roots either.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION refresh.guard_run_insert() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE parent_project text; parent_environment text; parent_generation text; parent_parent text;
BEGIN
    IF NEW.status <> 'queued' OR NEW.attempt_count <> 0 OR NEW.fence_generation <> 0 OR NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL OR NEW.started_at IS NOT NULL OR NEW.finished_at IS NOT NULL THEN RAISE EXCEPTION 'run inserts must begin as empty queued records'; END IF;
    IF NEW.parent_run_id IS NULL THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('manual-intent-scope:' || length(NEW.project_id)::text || ':' || NEW.project_id || '|' || length(NEW.environment)::text || ':' || NEW.environment, 0));
        IF EXISTS (
            SELECT 1 FROM refresh.manual_intent i
             WHERE i.reserved_run_id=NEW.run_id AND i.status<>'claimed'
        ) THEN
            RAISE EXCEPTION USING ERRCODE='23505', MESSAGE='reserved manual run id is no longer claimable';
        END IF;
        IF EXISTS (
            SELECT 1 FROM refresh.manual_intent i
             WHERE i.project_id=NEW.project_id AND i.environment=NEW.environment
               AND i.status='claimed' AND i.reserved_run_id<>NEW.run_id
        ) THEN
            RAISE EXCEPTION USING ERRCODE='23505', MESSAGE='project environment has a claimed manual refresh intent';
        END IF;
        IF EXISTS (
            SELECT 1 FROM refresh.run r
             WHERE r.project_id=NEW.project_id AND r.environment=NEW.environment
               AND r.parent_run_id IS NULL AND r.status IN ('queued','running','prepared')
               AND r.run_id<>NEW.run_id
        ) THEN
            RAISE EXCEPTION USING ERRCODE='23505', MESSAGE='project environment already has an active root refresh run';
        END IF;
    END IF;
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
-- +goose StatementEnd

-- A claimed scheduled occurrence may become a terminal policy skip after a
-- competing manual/backfill root wins scope admission.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION refresh.guard_occurrence_update() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.occurrence_id IS DISTINCT FROM OLD.occurrence_id OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.pipeline_id IS DISTINCT FROM OLD.pipeline_id OR NEW.nominal_time IS DISTINCT FROM OLD.nominal_time OR NEW.schedule_revision_id IS DISTINCT FROM OLD.schedule_revision_id OR NEW.matching_schedule_ids IS DISTINCT FROM OLD.matching_schedule_ids OR NEW.generation_id IS DISTINCT FROM OLD.generation_id OR NEW.artifact_digest IS DISTINCT FROM OLD.artifact_digest OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.fence_generation < OLD.fence_generation THEN RAISE EXCEPTION 'occurrence identity is immutable'; END IF;
    IF OLD.run_id IS NOT NULL AND NEW.run_id IS DISTINCT FROM OLD.run_id THEN RAISE EXCEPTION 'occurrence run binding is immutable'; END IF;
    IF OLD.run_id IS NULL AND NEW.run_id IS NOT NULL AND NOT (OLD.status='claimed' AND NEW.status='queued') THEN RAISE EXCEPTION 'occurrence run binding requires claimed to queued transition'; END IF;
    IF NEW.status IN ('queued','running','succeeded','failed','cancelled','superseded') AND NEW.run_id IS NULL THEN RAISE EXCEPTION 'bound occurrence status requires run id'; END IF;
    IF OLD.status IN ('succeeded','failed','cancelled','skipped','superseded') AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'terminal occurrence is immutable'; END IF;
    IF OLD.status='pending' AND NEW.status NOT IN ('pending','claimed','skipped','superseded') THEN RAISE EXCEPTION 'illegal pending occurrence transition'; END IF;
    IF OLD.status='claimed' AND NEW.status NOT IN ('claimed','pending','queued','skipped') THEN RAISE EXCEPTION 'illegal claimed occurrence transition'; END IF;
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
-- +goose StatementEnd

REVOKE ALL ON TABLE refresh.manual_intent FROM PUBLIC;
REVOKE ALL ON FUNCTION refresh.guard_manual_intent_insert() FROM PUBLIC;
REVOKE ALL ON FUNCTION refresh.guard_manual_intent_update() FROM PUBLIC;
REVOKE ALL ON FUNCTION refresh.notify_manual_intent_change() FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE ON refresh.manual_intent TO leapview_control_runtime;
REVOKE DELETE ON refresh.manual_intent FROM leapview_control_runtime;
GRANT SELECT ON refresh.manual_intent TO leapview_control_readonly, leapview_control_backup;

RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'accepted manual refresh intents are durable evidence; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
