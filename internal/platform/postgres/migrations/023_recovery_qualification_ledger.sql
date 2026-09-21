-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- capability source: internal/refresh/postgres/schema.sql
-- PostgreSQL-era durable recovery qualification ledger (FAI-1001). Scenario
-- owners execute backup, restore, upgrade, and rollback work; this ledger owns
-- scheduling, durable attempts, fencing, phase evidence, publication retry,
-- and retention metadata only.
CREATE TABLE IF NOT EXISTS refresh.recovery_qualification_schedule (
    schedule_revision_id text PRIMARY KEY,
    schedule_id text NOT NULL,
    scenario text NOT NULL,
    operation text NOT NULL CHECK (operation IN ('backup','restore','upgrade','rollback')),
    policy_version text NOT NULL,
    policy_sha256 text NOT NULL CHECK (policy_sha256 ~ '^[0-9a-f]{64}$'),
    target_scope text NOT NULL,
    artifact_identity text NOT NULL CHECK (artifact_identity ~ 'sha256:[0-9a-f]{64}$'),
    cron text NOT NULL,
    timezone text NOT NULL,
    stale_after interval NOT NULL CHECK (stale_after >= interval '1 second' AND stale_after <= interval '366 days'),
    next_run_at timestamptz NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    valid_from timestamptz NOT NULL,
    closed_at timestamptz,
    updated_at timestamptz NOT NULL,
    CHECK (schedule_id = btrim(schedule_id) AND length(schedule_id) BETWEEN 1 AND 256),
    CHECK (scenario = btrim(scenario) AND length(scenario) BETWEEN 1 AND 256),
    CHECK (target_scope = btrim(target_scope) AND length(target_scope) BETWEEN 1 AND 256),
    CHECK (closed_at IS NULL OR closed_at >= valid_from)
);
CREATE UNIQUE INDEX IF NOT EXISTS recovery_qualification_schedule_active_idx
    ON refresh.recovery_qualification_schedule(schedule_id) WHERE closed_at IS NULL;
CREATE INDEX IF NOT EXISTS recovery_qualification_schedule_due_idx
    ON refresh.recovery_qualification_schedule(next_run_at, schedule_id) WHERE enabled AND closed_at IS NULL;

CREATE TABLE IF NOT EXISTS refresh.recovery_qualification_enqueue_cursor (
    singleton_id boolean PRIMARY KEY DEFAULT true CHECK (singleton_id),
    last_schedule_id text NOT NULL DEFAULT '',
    last_schedule_revision_id text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'
);
INSERT INTO refresh.recovery_qualification_enqueue_cursor(singleton_id)
VALUES (true) ON CONFLICT (singleton_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS refresh.recovery_qualification_occurrence (
    occurrence_id text PRIMARY KEY,
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    schedule_id text NOT NULL,
    schedule_revision_id text NOT NULL,
    scenario text NOT NULL,
    operation text NOT NULL CHECK (operation IN ('backup','restore','upgrade','rollback')),
    policy_version text NOT NULL,
    policy_sha256 text NOT NULL CHECK (policy_sha256 ~ '^[0-9a-f]{64}$'),
    target_scope text NOT NULL,
    artifact_identity text NOT NULL CHECK (artifact_identity ~ 'sha256:[0-9a-f]{64}$'),
    planned_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','claimed','running','succeeded','failed','canceled','expired')),
    result text NOT NULL DEFAULT 'pending' CHECK (result IN ('pending','success','failure','canceled','expired')),
    attempt_count bigint NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    fence_generation bigint NOT NULL DEFAULT 0 CHECK (fence_generation >= 0),
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    actor text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    claimed_at timestamptz,
    started_at timestamptz,
    restore_started_at timestamptz,
    restore_completed_at timestamptz,
    readiness_started_at timestamptz,
    readiness_completed_at timestamptz,
    finished_at timestamptz,
    recovery_point_at timestamptz,
    recovery_point_age_seconds bigint CHECK (recovery_point_age_seconds IS NULL OR recovery_point_age_seconds >= 0),
    restore_duration_millis bigint CHECK (restore_duration_millis IS NULL OR restore_duration_millis >= 0),
    readiness_duration_millis bigint CHECK (readiness_duration_millis IS NULL OR readiness_duration_millis >= 0),
    qualification_duration_millis bigint CHECK (qualification_duration_millis IS NULL OR qualification_duration_millis >= 0),
    failure_reason_redacted text NOT NULL DEFAULT '',
    failure_code text NOT NULL DEFAULT '' CHECK (length(failure_code) <= 64),
    evidence_refs jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence_refs) = 'array'),
    evidence_status text NOT NULL DEFAULT 'pending' CHECK (evidence_status IN ('pending','claimed','published','failed','none')),
    evidence_attempt_count bigint NOT NULL DEFAULT 0 CHECK (evidence_attempt_count >= 0),
    evidence_fence_generation bigint NOT NULL DEFAULT 0 CHECK (evidence_fence_generation >= 0),
    evidence_lease_owner text NOT NULL DEFAULT '',
    evidence_lease_expires_at timestamptz,
    evidence_next_attempt_at timestamptz,
    evidence_published_at timestamptz,
    evidence_failure_reason_redacted text NOT NULL DEFAULT '',
    evidence_failure_code text NOT NULL DEFAULT '' CHECK (length(evidence_failure_code) <= 64),
    CHECK (expires_at > planned_at),
    CHECK ((status IN ('claimed','running')) = (lease_owner <> '' AND lease_expires_at IS NOT NULL)),
    CHECK ((status IN ('succeeded','failed','canceled','expired')) = (finished_at IS NOT NULL)),
    CHECK ((evidence_status = 'claimed') = (evidence_lease_owner <> '' AND evidence_lease_expires_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS recovery_qualification_occurrence_claim_idx
    ON refresh.recovery_qualification_occurrence(planned_at, occurrence_id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS recovery_qualification_occurrence_retention_idx
    ON refresh.recovery_qualification_occurrence(scenario, operation, status, finished_at, occurrence_id);
CREATE INDEX IF NOT EXISTS recovery_qualification_evidence_claim_idx
    ON refresh.recovery_qualification_occurrence(evidence_next_attempt_at, finished_at, occurrence_id)
    WHERE evidence_status IN ('pending','failed');

CREATE TABLE IF NOT EXISTS refresh.recovery_qualification_attempt (
    occurrence_id text NOT NULL REFERENCES refresh.recovery_qualification_occurrence(occurrence_id) ON DELETE CASCADE,
    attempt_number bigint NOT NULL CHECK (attempt_number > 0),
    fence_generation bigint NOT NULL CHECK (fence_generation > 0),
    worker_id text NOT NULL,
    actor text NOT NULL,
    status text NOT NULL CHECK (status IN ('claimed','running','succeeded','failed','canceled','abandoned')),
    claimed_at timestamptz NOT NULL,
    started_at timestamptz,
    lease_expires_at timestamptz NOT NULL,
    finished_at timestamptz,
    failure_reason_redacted text NOT NULL DEFAULT '',
    failure_code text NOT NULL DEFAULT '' CHECK (length(failure_code) <= 64),
    PRIMARY KEY (occurrence_id, attempt_number),
    UNIQUE (occurrence_id, fence_generation),
    CHECK (worker_id = btrim(worker_id) AND length(worker_id) BETWEEN 1 AND 256),
    CHECK (actor = btrim(actor) AND length(actor) BETWEEN 1 AND 256)
);

CREATE TABLE IF NOT EXISTS refresh.recovery_qualification_evidence_attempt (
    occurrence_id text NOT NULL REFERENCES refresh.recovery_qualification_occurrence(occurrence_id) ON DELETE CASCADE,
    attempt_number bigint NOT NULL CHECK (attempt_number > 0),
    fence_generation bigint NOT NULL CHECK (fence_generation > 0),
    publisher_id text NOT NULL,
    status text NOT NULL CHECK (status IN ('claimed','published','failed','abandoned')),
    claimed_at timestamptz NOT NULL,
    lease_expires_at timestamptz NOT NULL,
    finished_at timestamptz,
    failure_reason_redacted text NOT NULL DEFAULT '',
    failure_code text NOT NULL DEFAULT '' CHECK (length(failure_code) <= 64),
    PRIMARY KEY (occurrence_id, attempt_number),
    UNIQUE (occurrence_id, fence_generation),
    CHECK (publisher_id = btrim(publisher_id) AND length(publisher_id) BETWEEN 1 AND 256)
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION refresh.retain_recovery_qualification_occurrences(
    p_active_at timestamptz, p_finished_before timestamptz, p_limit integer
) RETURNS TABLE(occurrence_id text)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF p_active_at IS NULL OR p_finished_before IS NULL OR p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'recovery qualification retention boundaries and limit between 1 and 1000 are required';
    END IF;
    RETURN QUERY
    WITH candidates AS (
        SELECT o.occurrence_id
        FROM refresh.recovery_qualification_occurrence o
        WHERE o.status IN ('succeeded','failed','canceled','expired')
          AND (o.evidence_status <> 'claimed' OR o.evidence_lease_expires_at <= p_active_at)
          AND o.finished_at < p_finished_before
          AND NOT (o.status='succeeded' AND NOT EXISTS (
              SELECT 1 FROM refresh.recovery_qualification_occurrence newer
              WHERE newer.scenario=o.scenario AND newer.operation=o.operation AND newer.status='succeeded'
                AND (newer.finished_at,newer.occurrence_id) > (o.finished_at,o.occurrence_id)))
          AND NOT (o.status IN ('failed','expired') AND NOT EXISTS (
              SELECT 1 FROM refresh.recovery_qualification_occurrence newer
              WHERE newer.scenario=o.scenario AND newer.operation=o.operation AND newer.status IN ('failed','expired')
                AND (newer.finished_at,newer.occurrence_id) > (o.finished_at,o.occurrence_id)))
        ORDER BY o.finished_at,o.occurrence_id
        LIMIT p_limit FOR UPDATE SKIP LOCKED
    ), deleted AS (
        DELETE FROM refresh.recovery_qualification_occurrence o USING candidates c
        WHERE o.occurrence_id=c.occurrence_id RETURNING o.occurrence_id
    )
    SELECT deleted.occurrence_id FROM deleted ORDER BY deleted.occurrence_id;
END; $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION refresh.guard_recovery_qualification_schedule_update() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.schedule_revision_id IS DISTINCT FROM OLD.schedule_revision_id OR NEW.schedule_id IS DISTINCT FROM OLD.schedule_id OR NEW.scenario IS DISTINCT FROM OLD.scenario OR NEW.operation IS DISTINCT FROM OLD.operation OR NEW.policy_version IS DISTINCT FROM OLD.policy_version OR NEW.policy_sha256 IS DISTINCT FROM OLD.policy_sha256 OR NEW.target_scope IS DISTINCT FROM OLD.target_scope OR NEW.artifact_identity IS DISTINCT FROM OLD.artifact_identity OR NEW.cron IS DISTINCT FROM OLD.cron OR NEW.timezone IS DISTINCT FROM OLD.timezone OR NEW.stale_after IS DISTINCT FROM OLD.stale_after OR NEW.valid_from IS DISTINCT FROM OLD.valid_from THEN RAISE EXCEPTION 'recovery qualification schedule identity is immutable'; END IF;
    IF OLD.closed_at IS NOT NULL AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'closed recovery qualification schedule is immutable'; END IF;
    IF NEW.next_run_at < OLD.next_run_at THEN RAISE EXCEPTION 'recovery qualification next run cannot move backward'; END IF;
    IF NEW.closed_at IS NOT NULL AND (NEW.enabled OR NEW.closed_at < NEW.valid_from) THEN RAISE EXCEPTION 'closed recovery qualification schedule is invalid'; END IF;
    RETURN NEW;
END; $$;

CREATE OR REPLACE FUNCTION refresh.guard_recovery_qualification_occurrence_update() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.occurrence_id IS DISTINCT FROM OLD.occurrence_id OR NEW.request_digest IS DISTINCT FROM OLD.request_digest OR NEW.schedule_id IS DISTINCT FROM OLD.schedule_id OR NEW.schedule_revision_id IS DISTINCT FROM OLD.schedule_revision_id OR NEW.scenario IS DISTINCT FROM OLD.scenario OR NEW.operation IS DISTINCT FROM OLD.operation OR NEW.policy_version IS DISTINCT FROM OLD.policy_version OR NEW.policy_sha256 IS DISTINCT FROM OLD.policy_sha256 OR NEW.target_scope IS DISTINCT FROM OLD.target_scope OR NEW.artifact_identity IS DISTINCT FROM OLD.artifact_identity OR NEW.planned_at IS DISTINCT FROM OLD.planned_at OR NEW.expires_at IS DISTINCT FROM OLD.expires_at OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN RAISE EXCEPTION 'recovery qualification occurrence identity is immutable'; END IF;
    IF NEW.fence_generation < OLD.fence_generation OR NEW.evidence_fence_generation < OLD.evidence_fence_generation THEN RAISE EXCEPTION 'recovery qualification fence cannot decrease'; END IF;
    IF NEW.fence_generation > OLD.fence_generation AND NOT (OLD.status='pending' AND NEW.status='claimed' AND NEW.fence_generation=OLD.fence_generation+1 AND NEW.attempt_count=OLD.attempt_count+1) THEN RAISE EXCEPTION 'recovery qualification claim must advance one fence and attempt'; END IF;
    IF NEW.fence_generation = OLD.fence_generation AND NEW.attempt_count <> OLD.attempt_count THEN RAISE EXCEPTION 'recovery qualification attempt count requires a new fence'; END IF;
    IF OLD.status='pending' AND NEW.status NOT IN ('pending','claimed','expired') THEN RAISE EXCEPTION 'illegal pending recovery qualification transition'; END IF;
    IF OLD.status='claimed' AND NEW.status NOT IN ('claimed','running','pending','failed','canceled') THEN RAISE EXCEPTION 'illegal claimed recovery qualification transition'; END IF;
    IF OLD.status='running' AND NEW.status NOT IN ('running','pending','succeeded','failed','canceled') THEN RAISE EXCEPTION 'illegal running recovery qualification transition'; END IF;
    IF OLD.status IN ('succeeded','failed','canceled','expired') AND (NEW.status IS DISTINCT FROM OLD.status OR NEW.result IS DISTINCT FROM OLD.result OR NEW.fence_generation IS DISTINCT FROM OLD.fence_generation OR NEW.attempt_count IS DISTINCT FROM OLD.attempt_count OR NEW.lease_owner IS DISTINCT FROM OLD.lease_owner OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at OR NEW.actor IS DISTINCT FROM OLD.actor OR NEW.claimed_at IS DISTINCT FROM OLD.claimed_at OR NEW.started_at IS DISTINCT FROM OLD.started_at OR NEW.restore_started_at IS DISTINCT FROM OLD.restore_started_at OR NEW.restore_completed_at IS DISTINCT FROM OLD.restore_completed_at OR NEW.readiness_started_at IS DISTINCT FROM OLD.readiness_started_at OR NEW.readiness_completed_at IS DISTINCT FROM OLD.readiness_completed_at OR NEW.finished_at IS DISTINCT FROM OLD.finished_at OR NEW.recovery_point_at IS DISTINCT FROM OLD.recovery_point_at OR NEW.recovery_point_age_seconds IS DISTINCT FROM OLD.recovery_point_age_seconds OR NEW.restore_duration_millis IS DISTINCT FROM OLD.restore_duration_millis OR NEW.readiness_duration_millis IS DISTINCT FROM OLD.readiness_duration_millis OR NEW.qualification_duration_millis IS DISTINCT FROM OLD.qualification_duration_millis OR NEW.failure_reason_redacted IS DISTINCT FROM OLD.failure_reason_redacted OR NEW.failure_code IS DISTINCT FROM OLD.failure_code OR NEW.evidence_refs IS DISTINCT FROM OLD.evidence_refs) THEN RAISE EXCEPTION 'terminal recovery qualification outcome is immutable'; END IF;
    IF NEW.status='claimed' AND (NEW.lease_owner='' OR NEW.claimed_at IS NULL OR NEW.lease_expires_at IS NULL OR NEW.lease_expires_at<=NEW.claimed_at) THEN RAISE EXCEPTION 'claimed recovery qualification requires a valid lease'; END IF;
    IF NEW.claimed_at IS NOT NULL AND NEW.claimed_at < NEW.planned_at THEN RAISE EXCEPTION 'recovery qualification claim precedes plan'; END IF;
    IF NEW.started_at IS NOT NULL AND (NEW.claimed_at IS NULL OR NEW.started_at < NEW.claimed_at) THEN RAISE EXCEPTION 'recovery qualification start precedes claim'; END IF;
    IF NEW.restore_started_at IS NOT NULL AND (NEW.started_at IS NULL OR NEW.restore_started_at < NEW.started_at) THEN RAISE EXCEPTION 'recovery restore phase precedes execution'; END IF;
    IF NEW.restore_completed_at IS NOT NULL AND (NEW.restore_started_at IS NULL OR NEW.restore_completed_at < NEW.restore_started_at) THEN RAISE EXCEPTION 'recovery restore completion precedes start'; END IF;
    IF NEW.readiness_started_at IS NOT NULL AND (NEW.started_at IS NULL OR NEW.readiness_started_at < NEW.started_at) THEN RAISE EXCEPTION 'recovery readiness phase precedes execution'; END IF;
    IF NEW.readiness_completed_at IS NOT NULL AND (NEW.readiness_started_at IS NULL OR NEW.readiness_completed_at < NEW.readiness_started_at) THEN RAISE EXCEPTION 'recovery readiness completion precedes start'; END IF;
    IF NEW.evidence_fence_generation > OLD.evidence_fence_generation AND NOT (OLD.evidence_status IN ('pending','failed') AND NEW.evidence_status='claimed' AND NEW.evidence_fence_generation=OLD.evidence_fence_generation+1 AND NEW.evidence_attempt_count=OLD.evidence_attempt_count+1) THEN RAISE EXCEPTION 'recovery evidence claim must advance one fence and attempt'; END IF;
    IF NEW.evidence_fence_generation = OLD.evidence_fence_generation AND NEW.evidence_attempt_count <> OLD.evidence_attempt_count THEN RAISE EXCEPTION 'recovery evidence attempt count requires a new fence'; END IF;
    IF OLD.evidence_status='published' AND NEW.evidence_status IS DISTINCT FROM OLD.evidence_status THEN RAISE EXCEPTION 'published recovery evidence is immutable'; END IF;
    IF OLD.evidence_status='none' AND NEW.evidence_status IS DISTINCT FROM OLD.evidence_status THEN RAISE EXCEPTION 'disabled recovery evidence is immutable'; END IF;
    RETURN NEW;
END; $$;

CREATE OR REPLACE FUNCTION refresh.guard_recovery_qualification_attempt_write() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE occurrence refresh.recovery_qualification_occurrence%ROWTYPE;
BEGIN
    SELECT * INTO occurrence FROM refresh.recovery_qualification_occurrence WHERE occurrence_id=NEW.occurrence_id;
    IF TG_OP='INSERT' THEN
        IF occurrence.status<>'claimed' OR NEW.status<>'claimed' OR NEW.attempt_number<>occurrence.attempt_count OR NEW.fence_generation<>occurrence.fence_generation OR NEW.worker_id<>occurrence.lease_owner OR NEW.actor<>occurrence.actor OR NEW.claimed_at IS DISTINCT FROM occurrence.claimed_at THEN RAISE EXCEPTION 'recovery qualification attempt is not tied to current claim'; END IF;
    ELSE
        IF NEW.occurrence_id IS DISTINCT FROM OLD.occurrence_id OR NEW.attempt_number IS DISTINCT FROM OLD.attempt_number OR NEW.fence_generation IS DISTINCT FROM OLD.fence_generation OR NEW.worker_id IS DISTINCT FROM OLD.worker_id OR NEW.actor IS DISTINCT FROM OLD.actor OR NEW.claimed_at IS DISTINCT FROM OLD.claimed_at THEN RAISE EXCEPTION 'recovery qualification attempt identity is immutable'; END IF;
        IF OLD.status IN ('succeeded','failed','canceled','abandoned') AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'terminal recovery qualification attempt is immutable'; END IF;
        IF OLD.status='claimed' AND NEW.status NOT IN ('claimed','running','failed','canceled','abandoned') THEN RAISE EXCEPTION 'illegal claimed recovery qualification attempt transition'; END IF;
        IF OLD.status='running' AND NEW.status NOT IN ('running','succeeded','failed','canceled','abandoned') THEN RAISE EXCEPTION 'illegal running recovery qualification attempt transition'; END IF;
        IF NEW.started_at IS NOT NULL AND NEW.started_at < NEW.claimed_at THEN RAISE EXCEPTION 'recovery qualification attempt start precedes claim'; END IF;
        IF NEW.finished_at IS NOT NULL AND NEW.finished_at < COALESCE(NEW.started_at,NEW.claimed_at) THEN RAISE EXCEPTION 'recovery qualification attempt finish precedes execution'; END IF;
        IF NEW.status IN ('claimed','running') AND (occurrence.fence_generation<>NEW.fence_generation OR occurrence.attempt_count<>NEW.attempt_number OR occurrence.lease_owner<>NEW.worker_id OR occurrence.status NOT IN ('claimed','running')) THEN RAISE EXCEPTION 'recovery qualification attempt is not fenced by current occurrence'; END IF;
        IF NEW.status='succeeded' AND occurrence.status<>'succeeded' THEN RAISE EXCEPTION 'successful recovery attempt requires successful occurrence'; END IF;
        IF NEW.status='failed' AND occurrence.status<>'failed' THEN RAISE EXCEPTION 'failed recovery attempt requires failed occurrence'; END IF;
        IF NEW.status='canceled' AND occurrence.status<>'canceled' THEN RAISE EXCEPTION 'canceled recovery attempt requires canceled occurrence'; END IF;
        IF NEW.status='abandoned' AND (occurrence.status NOT IN ('claimed','running') OR occurrence.lease_expires_at IS NULL OR NEW.finished_at < occurrence.lease_expires_at) THEN RAISE EXCEPTION 'abandoned recovery attempt requires expired occurrence lease'; END IF;
    END IF;
    RETURN NEW;
END; $$;

CREATE OR REPLACE FUNCTION refresh.guard_recovery_evidence_attempt_write() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE occurrence refresh.recovery_qualification_occurrence%ROWTYPE;
BEGIN
    SELECT * INTO occurrence FROM refresh.recovery_qualification_occurrence WHERE occurrence_id=NEW.occurrence_id;
    IF TG_OP='INSERT' THEN
        IF occurrence.evidence_status<>'claimed' OR NEW.status<>'claimed' OR NEW.attempt_number<>occurrence.evidence_attempt_count OR NEW.fence_generation<>occurrence.evidence_fence_generation OR NEW.publisher_id<>occurrence.evidence_lease_owner THEN RAISE EXCEPTION 'recovery evidence attempt is not tied to current claim'; END IF;
    ELSE
        IF NEW.occurrence_id IS DISTINCT FROM OLD.occurrence_id OR NEW.attempt_number IS DISTINCT FROM OLD.attempt_number OR NEW.fence_generation IS DISTINCT FROM OLD.fence_generation OR NEW.publisher_id IS DISTINCT FROM OLD.publisher_id OR NEW.claimed_at IS DISTINCT FROM OLD.claimed_at THEN RAISE EXCEPTION 'recovery evidence attempt identity is immutable'; END IF;
        IF OLD.status IN ('published','failed','abandoned') AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'terminal recovery evidence attempt is immutable'; END IF;
        IF NEW.finished_at IS NOT NULL AND NEW.finished_at < NEW.claimed_at THEN RAISE EXCEPTION 'recovery evidence attempt finish precedes claim'; END IF;
        IF NEW.status='published' AND occurrence.evidence_status<>'published' THEN RAISE EXCEPTION 'published recovery evidence attempt requires published occurrence evidence'; END IF;
        IF NEW.status='failed' AND occurrence.evidence_status<>'failed' THEN RAISE EXCEPTION 'failed recovery evidence attempt requires failed occurrence evidence'; END IF;
        IF NEW.status='abandoned' AND (occurrence.evidence_status<>'claimed' OR occurrence.evidence_lease_expires_at IS NULL OR NEW.finished_at < occurrence.evidence_lease_expires_at) THEN RAISE EXCEPTION 'abandoned recovery evidence attempt requires expired occurrence evidence lease'; END IF;
    END IF;
    RETURN NEW;
END; $$;

CREATE OR REPLACE FUNCTION refresh.guard_recovery_qualification_attempt_evidence() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.status='claimed' AND OLD.status='pending' AND NOT EXISTS (SELECT 1 FROM refresh.recovery_qualification_attempt a WHERE a.occurrence_id=NEW.occurrence_id AND a.attempt_number=NEW.attempt_count AND a.fence_generation=NEW.fence_generation AND a.status IN ('claimed','running')) THEN RAISE EXCEPTION 'recovery qualification claim requires durable attempt evidence'; END IF;
    IF NEW.status IN ('succeeded','failed','canceled') AND NEW.status IS DISTINCT FROM OLD.status AND NOT EXISTS (SELECT 1 FROM refresh.recovery_qualification_attempt a WHERE a.occurrence_id=NEW.occurrence_id AND a.attempt_number=NEW.attempt_count AND a.fence_generation=NEW.fence_generation AND a.status=NEW.status) THEN RAISE EXCEPTION 'terminal recovery qualification requires matching attempt evidence'; END IF;
    IF NEW.status='pending' AND OLD.status IN ('claimed','running') AND NOT EXISTS (SELECT 1 FROM refresh.recovery_qualification_attempt a WHERE a.occurrence_id=NEW.occurrence_id AND a.attempt_number=NEW.attempt_count AND a.fence_generation=NEW.fence_generation AND a.status='abandoned') THEN RAISE EXCEPTION 'requeued recovery qualification requires abandoned attempt evidence'; END IF;
    IF NEW.evidence_status='claimed' AND OLD.evidence_status IN ('pending','failed') AND NOT EXISTS (SELECT 1 FROM refresh.recovery_qualification_evidence_attempt a WHERE a.occurrence_id=NEW.occurrence_id AND a.attempt_number=NEW.evidence_attempt_count AND a.fence_generation=NEW.evidence_fence_generation AND a.status='claimed') THEN RAISE EXCEPTION 'recovery evidence claim requires durable attempt evidence'; END IF;
    IF NEW.evidence_status IN ('published','failed') AND OLD.evidence_status='claimed' AND NOT EXISTS (SELECT 1 FROM refresh.recovery_qualification_evidence_attempt a WHERE a.occurrence_id=NEW.occurrence_id AND a.attempt_number=NEW.evidence_attempt_count AND a.fence_generation=NEW.evidence_fence_generation AND ((NEW.evidence_status='published' AND a.status='published') OR (NEW.evidence_status='failed' AND a.status IN ('failed','abandoned')))) THEN RAISE EXCEPTION 'terminal recovery evidence state requires matching attempt evidence'; END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS recovery_qualification_schedule_guard ON refresh.recovery_qualification_schedule;
CREATE TRIGGER recovery_qualification_schedule_guard BEFORE UPDATE ON refresh.recovery_qualification_schedule FOR EACH ROW EXECUTE FUNCTION refresh.guard_recovery_qualification_schedule_update();
DROP TRIGGER IF EXISTS recovery_qualification_occurrence_guard ON refresh.recovery_qualification_occurrence;
CREATE TRIGGER recovery_qualification_occurrence_guard BEFORE UPDATE ON refresh.recovery_qualification_occurrence FOR EACH ROW EXECUTE FUNCTION refresh.guard_recovery_qualification_occurrence_update();
DROP TRIGGER IF EXISTS recovery_qualification_attempt_guard ON refresh.recovery_qualification_attempt;
CREATE TRIGGER recovery_qualification_attempt_guard BEFORE INSERT OR UPDATE ON refresh.recovery_qualification_attempt FOR EACH ROW EXECUTE FUNCTION refresh.guard_recovery_qualification_attempt_write();
DROP TRIGGER IF EXISTS recovery_qualification_evidence_attempt_guard ON refresh.recovery_qualification_evidence_attempt;
CREATE TRIGGER recovery_qualification_evidence_attempt_guard BEFORE INSERT OR UPDATE ON refresh.recovery_qualification_evidence_attempt FOR EACH ROW EXECUTE FUNCTION refresh.guard_recovery_evidence_attempt_write();
DROP TRIGGER IF EXISTS recovery_qualification_attempt_evidence_guard ON refresh.recovery_qualification_occurrence;
CREATE CONSTRAINT TRIGGER recovery_qualification_attempt_evidence_guard AFTER UPDATE ON refresh.recovery_qualification_occurrence DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION refresh.guard_recovery_qualification_attempt_evidence();

-- +goose StatementBegin
DO $$
BEGIN
    REVOKE ALL ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_enqueue_cursor, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt FROM PUBLIC;
    REVOKE ALL ON FUNCTION refresh.retain_recovery_qualification_occurrences(timestamptz,timestamptz,integer) FROM PUBLIC;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN
        GRANT ALL ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_enqueue_cursor, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt TO leapview_control_owner;
        GRANT EXECUTE ON FUNCTION refresh.retain_recovery_qualification_occurrences(timestamptz,timestamptz,integer) TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_migrator') THEN
        GRANT ALL ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_enqueue_cursor, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION refresh.retain_recovery_qualification_occurrences(timestamptz,timestamptz,integer) TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_enqueue_cursor, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION refresh.retain_recovery_qualification_occurrences(timestamptz,timestamptz,integer) TO leapview_control_runtime;
        REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_enqueue_cursor, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_readonly;
        GRANT SELECT ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_enqueue_cursor, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_backup;
        GRANT SELECT ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_enqueue_cursor, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt TO leapview_control_backup;
    END IF;
END $$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
DROP TRIGGER IF EXISTS recovery_qualification_attempt_evidence_guard ON refresh.recovery_qualification_occurrence;
DROP TRIGGER IF EXISTS recovery_qualification_evidence_attempt_guard ON refresh.recovery_qualification_evidence_attempt;
DROP TRIGGER IF EXISTS recovery_qualification_attempt_guard ON refresh.recovery_qualification_attempt;
DROP TRIGGER IF EXISTS recovery_qualification_occurrence_guard ON refresh.recovery_qualification_occurrence;
DROP TRIGGER IF EXISTS recovery_qualification_schedule_guard ON refresh.recovery_qualification_schedule;
DROP FUNCTION IF EXISTS refresh.retain_recovery_qualification_occurrences(timestamptz,timestamptz,integer);
DROP FUNCTION IF EXISTS refresh.guard_recovery_evidence_attempt_write();
DROP FUNCTION IF EXISTS refresh.guard_recovery_qualification_attempt_write();
DROP FUNCTION IF EXISTS refresh.guard_recovery_qualification_attempt_evidence();
DROP FUNCTION IF EXISTS refresh.guard_recovery_qualification_occurrence_update();
DROP FUNCTION IF EXISTS refresh.guard_recovery_qualification_schedule_update();
DROP TABLE IF EXISTS refresh.recovery_qualification_evidence_attempt;
DROP TABLE IF EXISTS refresh.recovery_qualification_attempt;
DROP TABLE IF EXISTS refresh.recovery_qualification_occurrence;
DROP TABLE IF EXISTS refresh.recovery_qualification_enqueue_cursor;
DROP TABLE IF EXISTS refresh.recovery_qualification_schedule;

RESET ROLE;
