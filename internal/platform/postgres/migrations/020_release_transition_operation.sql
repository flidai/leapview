-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- One immutable transition request is retained with its canonical preflight
-- evidence. Phase rows are append-only outcomes and the singleton fence is
-- the durable, process-wide transition lease.
CREATE TABLE IF NOT EXISTS release.release_transition_operation (
    operation_id uuid PRIMARY KEY,
    target_identity_digest text NOT NULL CHECK (target_identity_digest ~ '^sha256:[0-9a-f]{64}$'),
    predecessor_artifact_digest text NOT NULL CHECK (predecessor_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    candidate_artifact_digest text NOT NULL CHECK (candidate_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    recovery_frontier_id text NOT NULL CHECK (recovery_frontier_id = btrim(recovery_frontier_id) AND octet_length(recovery_frontier_id) BETWEEN 1 AND 255),
    recovery_frontier_digest text NOT NULL CHECK (recovery_frontier_digest ~ '^sha256:[0-9a-f]{64}$'),
    preflight_evidence_digest text NOT NULL CHECK (preflight_evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
    preflight_evidence bytea NOT NULL CHECK (octet_length(preflight_evidence) BETWEEN 1 AND 1048576),
    idempotency_key text NOT NULL CHECK (idempotency_key = btrim(idempotency_key) AND octet_length(idempotency_key) BETWEEN 1 AND 512),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','completed','failed','indeterminate')),
    current_phase text NOT NULL DEFAULT 'preflight' CHECK (current_phase IN ('preflight','migrations','candidate-staged','candidate-activated','candidate-restarted','post-validated','success')),
    owner_id text NOT NULL DEFAULT '' CHECK (octet_length(owner_id) <= 255),
    fencing_generation bigint NOT NULL DEFAULT 0 CHECK (fencing_generation >= 0),
    lease_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    terminal_at timestamptz,
    CHECK (predecessor_artifact_digest <> candidate_artifact_digest),
    CHECK ((status IN ('pending','running') AND terminal_at IS NULL) OR (status IN ('completed','failed','indeterminate') AND terminal_at IS NOT NULL)),
    CHECK ((owner_id = '' AND lease_expires_at IS NULL) OR (owner_id <> '' AND lease_expires_at IS NOT NULL)),
    UNIQUE (target_identity_digest, idempotency_key)
);

CREATE TABLE IF NOT EXISTS release.release_transition_phase_result (
    operation_id uuid NOT NULL REFERENCES release.release_transition_operation(operation_id) ON DELETE RESTRICT,
    phase text NOT NULL CHECK (phase IN ('preflight','migrations','candidate-staged','candidate-activated','candidate-restarted','post-validated','success')),
    result_status text NOT NULL CHECK (result_status IN ('succeeded','failed','indeterminate')),
    result_digest text NOT NULL CHECK (result_digest ~ '^sha256:[0-9a-f]{64}$'),
    result_bytes bytea NOT NULL CHECK (octet_length(result_bytes) BETWEEN 1 AND 1048576),
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (operation_id, phase),
    CHECK (completed_at >= started_at)
);

CREATE TABLE IF NOT EXISTS release.release_transition_fence (
    target_identity_digest text PRIMARY KEY CHECK (target_identity_digest ~ '^sha256:[0-9a-f]{64}$'),
    operation_id uuid REFERENCES release.release_transition_operation(operation_id) ON DELETE RESTRICT,
    owner_id text NOT NULL DEFAULT '' CHECK (octet_length(owner_id) <= 255),
    fencing_generation bigint NOT NULL DEFAULT 0 CHECK (fencing_generation >= 0),
    lease_expires_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((operation_id IS NULL AND owner_id = '' AND lease_expires_at IS NULL) OR (operation_id IS NOT NULL AND owner_id <> '' AND lease_expires_at IS NOT NULL))
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION release.reject_transition_operation_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, release AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'release transition operation evidence cannot be deleted';
    END IF;
    IF NEW.operation_id IS DISTINCT FROM OLD.operation_id
       OR NEW.target_identity_digest IS DISTINCT FROM OLD.target_identity_digest
       OR NEW.predecessor_artifact_digest IS DISTINCT FROM OLD.predecessor_artifact_digest
       OR NEW.candidate_artifact_digest IS DISTINCT FROM OLD.candidate_artifact_digest
       OR NEW.recovery_frontier_id IS DISTINCT FROM OLD.recovery_frontier_id
       OR NEW.recovery_frontier_digest IS DISTINCT FROM OLD.recovery_frontier_digest
       OR NEW.preflight_evidence_digest IS DISTINCT FROM OLD.preflight_evidence_digest
       OR NEW.preflight_evidence IS DISTINCT FROM OLD.preflight_evidence
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.request_digest IS DISTINCT FROM OLD.request_digest
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR (OLD.status IN ('completed','failed','indeterminate') AND (NEW.status IS DISTINCT FROM OLD.status OR NEW.terminal_at IS DISTINCT FROM OLD.terminal_at))
       OR (OLD.status = 'pending' AND NEW.status NOT IN ('pending','running'))
       OR (OLD.status = 'running' AND NEW.status NOT IN ('running','completed','failed','indeterminate'))
       OR (NEW.current_phase <> OLD.current_phase AND NOT ((OLD.current_phase = 'preflight' AND NEW.current_phase = 'migrations') OR (OLD.current_phase = 'migrations' AND NEW.current_phase = 'candidate-staged') OR (OLD.current_phase = 'candidate-staged' AND NEW.current_phase = 'candidate-activated') OR (OLD.current_phase = 'candidate-activated' AND NEW.current_phase = 'candidate-restarted') OR (OLD.current_phase = 'candidate-restarted' AND NEW.current_phase = 'post-validated') OR (OLD.current_phase = 'post-validated' AND NEW.current_phase = 'success'))) THEN
        RAISE EXCEPTION 'release transition operation identity or phase is immutable/non-monotonic';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS release_transition_operation_guard ON release.release_transition_operation;
CREATE TRIGGER release_transition_operation_guard BEFORE UPDATE OR DELETE ON release.release_transition_operation FOR EACH ROW EXECUTE FUNCTION release.reject_transition_operation_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION release.reject_transition_phase_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, release AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'release transition phase result is immutable';
    END IF;
    IF NEW.operation_id IS DISTINCT FROM OLD.operation_id OR NEW.phase IS DISTINCT FROM OLD.phase
       OR NEW.result_digest IS DISTINCT FROM OLD.result_digest OR NEW.result_bytes IS DISTINCT FROM OLD.result_bytes
       OR NEW.started_at IS DISTINCT FROM OLD.started_at OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
       OR NEW.result_status IS DISTINCT FROM OLD.result_status THEN
        RAISE EXCEPTION 'release transition phase result is immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS release_transition_phase_guard ON release.release_transition_phase_result;
CREATE TRIGGER release_transition_phase_guard BEFORE UPDATE OR DELETE ON release.release_transition_phase_result FOR EACH ROW EXECUTE FUNCTION release.reject_transition_phase_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION release.reject_transition_fence_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, release AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'release transition fence cannot be deleted';
    END IF;
    IF NEW.target_identity_digest IS DISTINCT FROM OLD.target_identity_digest OR NEW.fencing_generation < OLD.fencing_generation THEN
        RAISE EXCEPTION 'release transition fence identity or generation rewound';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS release_transition_fence_guard ON release.release_transition_fence;
CREATE TRIGGER release_transition_fence_guard BEFORE UPDATE OR DELETE ON release.release_transition_fence FOR EACH ROW EXECUTE FUNCTION release.reject_transition_fence_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION release.reject_transition_truncate()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, release AS $$
BEGIN
    RAISE EXCEPTION 'release transition evidence cannot be truncated';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER release_transition_operation_no_truncate BEFORE TRUNCATE ON release.release_transition_operation FOR EACH STATEMENT EXECUTE FUNCTION release.reject_transition_truncate();
CREATE TRIGGER release_transition_phase_no_truncate BEFORE TRUNCATE ON release.release_transition_phase_result FOR EACH STATEMENT EXECUTE FUNCTION release.reject_transition_truncate();
CREATE TRIGGER release_transition_fence_no_truncate BEFORE TRUNCATE ON release.release_transition_fence FOR EACH STATEMENT EXECUTE FUNCTION release.reject_transition_truncate();

REVOKE ALL ON release.release_transition_operation, release.release_transition_phase_result, release.release_transition_fence FROM PUBLIC;
REVOKE ALL ON FUNCTION release.reject_transition_operation_mutation(), release.reject_transition_phase_mutation(), release.reject_transition_fence_mutation(), release.reject_transition_truncate() FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_owner;
        GRANT ALL ON release.release_transition_operation, release.release_transition_phase_result, release.release_transition_fence TO leapview_control_owner;
        GRANT EXECUTE ON FUNCTION release.reject_transition_operation_mutation(), release.reject_transition_phase_mutation(), release.reject_transition_fence_mutation(), release.reject_transition_truncate() TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_migrator;
        GRANT ALL ON release.release_transition_operation, release.release_transition_phase_result, release.release_transition_fence TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION release.reject_transition_operation_mutation(), release.reject_transition_phase_mutation(), release.reject_transition_fence_mutation(), release.reject_transition_truncate() TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_runtime;
        GRANT SELECT ON release.release_transition_operation, release.release_transition_phase_result, release.release_transition_fence TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_readonly;
        GRANT SELECT ON release.release_transition_operation, release.release_transition_phase_result, release.release_transition_fence TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_backup;
        GRANT SELECT ON release.release_transition_operation, release.release_transition_phase_result, release.release_transition_fence TO leapview_control_backup;
    END IF;
END $$;
-- +goose StatementEnd
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'release transition operation migration is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
