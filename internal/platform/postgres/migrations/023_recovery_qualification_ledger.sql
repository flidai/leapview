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
CREATE OR REPLACE FUNCTION refresh.retain_recovery_qualification_occurrence(
    p_occurrence_id text, p_active_at timestamptz, p_finished_before timestamptz
) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, refresh AS $$
DECLARE affected bigint;
BEGIN
    IF p_occurrence_id IS NULL OR p_active_at IS NULL OR p_finished_before IS NULL THEN
        RAISE EXCEPTION 'recovery qualification retention identity and boundaries are required';
    END IF;
    DELETE FROM refresh.recovery_qualification_occurrence
     WHERE occurrence_id = p_occurrence_id
       AND status IN ('succeeded','failed','canceled','expired')
       AND (evidence_status <> 'claimed' OR evidence_lease_expires_at <= p_active_at)
       AND finished_at < p_finished_before;
    GET DIAGNOSTICS affected = ROW_COUNT;
    RETURN affected = 1;
END; $$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    REVOKE ALL ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt FROM PUBLIC;
    REVOKE ALL ON FUNCTION refresh.retain_recovery_qualification_occurrence(text,timestamptz,timestamptz) FROM PUBLIC;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN
        GRANT ALL ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt TO leapview_control_owner;
        GRANT EXECUTE ON FUNCTION refresh.retain_recovery_qualification_occurrence(text,timestamptz,timestamptz) TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_migrator') THEN
        GRANT ALL ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION refresh.retain_recovery_qualification_occurrence(text,timestamptz,timestamptz) TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION refresh.retain_recovery_qualification_occurrence(text,timestamptz,timestamptz) TO leapview_control_runtime;
        REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_readonly;
        GRANT SELECT ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_backup;
        GRANT SELECT ON refresh.recovery_qualification_schedule, refresh.recovery_qualification_occurrence, refresh.recovery_qualification_attempt, refresh.recovery_qualification_evidence_attempt TO leapview_control_backup;
    END IF;
END $$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
DROP FUNCTION IF EXISTS refresh.retain_recovery_qualification_occurrence(text,timestamptz,timestamptz);
DROP TABLE IF EXISTS refresh.recovery_qualification_evidence_attempt;
DROP TABLE IF EXISTS refresh.recovery_qualification_attempt;
DROP TABLE IF EXISTS refresh.recovery_qualification_occurrence;
DROP TABLE IF EXISTS refresh.recovery_qualification_schedule;

RESET ROLE;
