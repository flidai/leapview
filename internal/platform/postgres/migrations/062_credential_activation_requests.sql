-- +goose Up
SET LOCAL ROLE leapview_control_owner;
ALTER TABLE credential.validation_receipt DROP CONSTRAINT validation_receipt_binding_revision_check;
ALTER TABLE credential.validation_receipt ADD CONSTRAINT validation_receipt_binding_revision_check CHECK ((scope_kind = 'connection' AND binding_revision > 0) OR (scope_kind = 'agent' AND binding_revision >= 0));
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS credential.activation_request (
    operation_id text PRIMARY KEY,
    deployment_id text NOT NULL,
    receipt_id text NOT NULL UNIQUE,
    version_id text NOT NULL,
    expected_binding_revision bigint NOT NULL CHECK (expected_binding_revision >= 0),
    state text NOT NULL CHECK (state IN ('preparing','prepared','switching','committed','completed','aborted')),
    revision bigint NOT NULL CHECK (revision > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    plan_id text NOT NULL DEFAULT '',
    candidate_id text NOT NULL DEFAULT '',
    generation_id text NOT NULL DEFAULT '',
    publication_id text NOT NULL DEFAULT '',
    configuration_revision bigint NOT NULL DEFAULT 0 CHECK (configuration_revision >= 0),
    FOREIGN KEY (deployment_id, receipt_id) REFERENCES credential.validation_receipt (deployment_id,receipt_id),
    FOREIGN KEY (deployment_id, version_id) REFERENCES credential.draft_version (deployment_id,version_id),
    CHECK (operation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' AND operation_id <> '00000000-0000-0000-0000-000000000000'),
    CHECK (isfinite(created_at) AND isfinite(updated_at) AND updated_at >= created_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS activation_request_one_pending_deployment_idx
    ON credential.activation_request (deployment_id) WHERE state NOT IN ('completed','aborted');

CREATE TABLE IF NOT EXISTS credential.activation_request_receipt (
    operation_id text NOT NULL REFERENCES credential.activation_request (operation_id),
    deployment_id text NOT NULL,
    receipt_id text PRIMARY KEY,
    observed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (deployment_id,receipt_id) REFERENCES credential.validation_receipt (deployment_id,receipt_id),
    CHECK (isfinite(observed_at))
);

CREATE OR REPLACE FUNCTION credential.guard_activation_request() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'credential activation requests cannot be deleted';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'preparing' OR NEW.revision <> 1
           OR NEW.plan_id <> '' OR NEW.candidate_id <> '' OR NEW.generation_id <> '' OR NEW.publication_id <> ''
           OR NEW.configuration_revision <> 0
           OR NOT EXISTS (
               SELECT 1 FROM credential.validation_receipt AS receipt
               WHERE receipt.deployment_id=NEW.deployment_id AND receipt.receipt_id=NEW.receipt_id
                 AND receipt.version_id=NEW.version_id AND receipt.binding_revision=NEW.expected_binding_revision
                 AND receipt.validated_at <= clock_timestamp() AND receipt.expires_at > clock_timestamp()
           ) THEN RAISE EXCEPTION 'credential activation request requires a fresh exact receipt';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.state IN ('completed','aborted') OR NEW.operation_id IS DISTINCT FROM OLD.operation_id
       OR NEW.deployment_id IS DISTINCT FROM OLD.deployment_id OR NEW.receipt_id IS DISTINCT FROM OLD.receipt_id
       OR NEW.version_id IS DISTINCT FROM OLD.version_id OR NEW.expected_binding_revision IS DISTINCT FROM OLD.expected_binding_revision
       OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.revision <> OLD.revision + 1
       OR NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'credential activation request identity is immutable';
    END IF;
    IF OLD.state <> 'preparing' AND (
       NEW.plan_id IS DISTINCT FROM OLD.plan_id OR NEW.candidate_id IS DISTINCT FROM OLD.candidate_id
       OR NEW.generation_id IS DISTINCT FROM OLD.generation_id OR NEW.publication_id IS DISTINCT FROM OLD.publication_id
       OR NEW.configuration_revision IS DISTINCT FROM OLD.configuration_revision) THEN
        RAISE EXCEPTION 'prepared credential activation identity is immutable';
    END IF;
    IF NEW.state = OLD.state
       OR (OLD.state='preparing' AND NEW.state='prepared')
       OR (OLD.state='prepared' AND NEW.state='switching')
       OR (OLD.state='switching' AND NEW.state='committed')
       OR (OLD.state='committed' AND NEW.state='completed')
       OR (OLD.state IN ('preparing','prepared','switching') AND NEW.state='aborted') THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'credential activation transition is invalid';
END;
$$;

CREATE OR REPLACE FUNCTION credential.guard_activation_request_receipt() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP <> 'INSERT' OR NOT EXISTS (
        SELECT 1 FROM credential.activation_request AS operation
        JOIN credential.validation_receipt AS original ON original.receipt_id=operation.receipt_id
        JOIN credential.validation_receipt AS receipt ON receipt.receipt_id=NEW.receipt_id
        WHERE operation.operation_id=NEW.operation_id AND operation.deployment_id=NEW.deployment_id
          AND operation.state IN ('preparing','prepared','switching')
          AND receipt.deployment_id=original.deployment_id AND receipt.version_id=original.version_id
          AND receipt.owner_id=original.owner_id AND receipt.scope_kind=original.scope_kind
          AND receipt.target_id=original.target_id AND receipt.project_id=original.project_id
          AND receipt.environment=original.environment AND receipt.resource_id=original.resource_id
          AND receipt.purpose=original.purpose AND receipt.provider=original.provider AND receipt.destination=original.destination
          AND receipt.actor_id=original.actor_id AND receipt.binding_id=original.binding_id
          AND receipt.binding_revision=original.binding_revision AND receipt.configuration_digest=original.configuration_digest
          AND receipt.validated_at <= clock_timestamp() AND receipt.expires_at > clock_timestamp()
          AND NOT EXISTS (SELECT 1 FROM credential.activation_request AS other
              WHERE other.receipt_id=NEW.receipt_id AND other.operation_id<>NEW.operation_id)
    ) THEN RAISE EXCEPTION 'activation retry receipt must be fresh and match the exact original authority';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS activation_request_guard ON credential.activation_request;
CREATE TRIGGER activation_request_guard BEFORE INSERT OR UPDATE OR DELETE ON credential.activation_request
    FOR EACH ROW EXECUTE FUNCTION credential.guard_activation_request();
DROP TRIGGER IF EXISTS activation_request_receipt_guard ON credential.activation_request_receipt;
CREATE TRIGGER activation_request_receipt_guard BEFORE INSERT OR UPDATE OR DELETE ON credential.activation_request_receipt
    FOR EACH ROW EXECUTE FUNCTION credential.guard_activation_request_receipt();

REVOKE ALL ON credential.activation_request, credential.activation_request_receipt FROM PUBLIC;
REVOKE ALL ON FUNCTION credential.guard_activation_request(), credential.guard_activation_request_receipt() FROM PUBLIC;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        GRANT SELECT,INSERT ON credential.activation_request,credential.activation_request_receipt TO leapview_control_runtime;
        GRANT UPDATE (state,revision,updated_at,plan_id,candidate_id,generation_id,publication_id,configuration_revision)
            ON credential.activation_request TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
        GRANT SELECT ON credential.activation_request,credential.activation_request_receipt TO leapview_control_backup;
    END IF;
END $$;


-- Preserve the immutable original intent while an explicitly retried operation
-- consumes its latest exact successful validation. Historical journals that
-- predate activation_request retain the original receipt's expiry contract.
CREATE OR REPLACE FUNCTION credential.activation_receipt_is_fresh(
    deployment text, operation text, original_receipt text, observed_now timestamptz
) RETURNS boolean LANGUAGE sql SET search_path = pg_catalog AS $$
    SELECT CASE WHEN EXISTS (
        SELECT 1 FROM credential.activation_request WHERE deployment_id=deployment AND operation_id=operation
    ) THEN COALESCE((
        SELECT proof.validated_at <= observed_now AND proof.expires_at > observed_now
        FROM credential.activation_request AS request
        JOIN credential.activation_request_receipt AS attempt USING(deployment_id,operation_id)
        JOIN credential.validation_receipt AS proof ON proof.deployment_id=attempt.deployment_id AND proof.receipt_id=attempt.receipt_id
        WHERE request.deployment_id=deployment AND request.operation_id=operation
          AND request.receipt_id=original_receipt AND request.state IN ('preparing','prepared','switching')
        ORDER BY attempt.observed_at DESC,attempt.receipt_id DESC LIMIT 1
    ),false) ELSE EXISTS (
        SELECT 1 FROM credential.validation_receipt
        WHERE deployment_id=deployment AND receipt_id=original_receipt
          AND validated_at <= observed_now AND expires_at > observed_now
    ) END;
$$;
REVOKE ALL ON FUNCTION credential.activation_receipt_is_fresh(text,text,text,timestamptz) FROM PUBLIC;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        GRANT EXECUTE ON FUNCTION credential.activation_receipt_is_fresh(text,text,text,timestamptz) TO leapview_control_runtime;
    END IF;
END $$;

CREATE OR REPLACE FUNCTION credential.guard_activation_preparation_transition() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    commit_now timestamptz;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.completed_at IS NOT NULL OR NEW.switching_at IS NOT NULL OR NEW.committed_at IS NOT NULL OR NEW.aborted_at IS NOT NULL OR NEW.aborted_by IS NOT NULL
	       OR NOT EXISTS (
	            SELECT 1 FROM credential.validation_receipt AS receipt
	            WHERE receipt.deployment_id = NEW.deployment_id
	              AND receipt.receipt_id = NEW.receipt_id
	              AND receipt.target_id = NEW.deployment_id
	       ) THEN
	        RAISE EXCEPTION 'credential activation preparation must be active and target-bound when inserted';
	    END IF;
	    RETURN NEW;
	END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'credential activation preparations cannot be deleted';
    END IF;
    IF NEW.operation_id IS DISTINCT FROM OLD.operation_id
       OR NEW.deployment_id IS DISTINCT FROM OLD.deployment_id
       OR NEW.receipt_id IS DISTINCT FROM OLD.receipt_id
       OR NEW.expected_target_revision IS DISTINCT FROM OLD.expected_target_revision
       OR NEW.predecessor_generation_id IS DISTINCT FROM OLD.predecessor_generation_id
       OR NEW.candidate_id IS DISTINCT FROM OLD.candidate_id
       OR NEW.generation_id IS DISTINCT FROM OLD.generation_id
       OR NEW.publication_id IS DISTINCT FROM OLD.publication_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'credential activation preparation intent is immutable';
    END IF;
    -- Completion is a separate durable transition after exact runtime readiness.
    -- No historical identity, receipt, pointer or commit timestamp may change.
    IF OLD.committed_at IS NOT NULL AND OLD.completed_at IS NULL
       AND OLD.aborted_at IS NULL AND OLD.aborted_by IS NULL
       AND NEW.switching_at IS NOT DISTINCT FROM OLD.switching_at
       AND NEW.committed_at IS NOT DISTINCT FROM OLD.committed_at
       AND NEW.aborted_at IS NULL AND NEW.aborted_by IS NULL
       AND NEW.completed_at IS NOT NULL AND isfinite(NEW.completed_at)
       AND NEW.completed_at >= OLD.committed_at
       AND NEW.completed_at <= clock_timestamp() THEN
        RETURN NEW;
    END IF;
    IF NEW.completed_at IS NOT NULL OR OLD.completed_at IS NOT NULL THEN
        RAISE EXCEPTION 'credential activation completion is terminal';
    END IF;
    IF OLD.aborted_at IS NOT NULL OR OLD.aborted_by IS NOT NULL OR OLD.committed_at IS NOT NULL THEN
        RAISE EXCEPTION 'credential activation preparation cannot transition after abort or commit';
    END IF;

    -- The conditional update performs a preliminary freshness check. Sample
    -- again after acquiring this row and stamp the authoritative commit time.
    IF OLD.switching_at IS NOT NULL
       AND NEW.switching_at IS NOT DISTINCT FROM OLD.switching_at
       AND NEW.aborted_at IS NULL AND NEW.aborted_by IS NULL
       AND NEW.committed_at IS NOT NULL
       AND isfinite(NEW.committed_at) THEN
        commit_now := clock_timestamp();
        IF NEW.committed_at >= OLD.switching_at
           AND NEW.committed_at <= commit_now
           AND commit_now >= OLD.switching_at THEN
            IF NOT credential.activation_receipt_is_fresh(OLD.deployment_id, OLD.operation_id, OLD.receipt_id, commit_now) THEN
                RAISE EXCEPTION 'credential activation receipt is no longer fresh'
                    USING ERRCODE = '40001', CONSTRAINT = 'activation_preparation_receipt_freshness';
            END IF;
            NEW.committed_at := commit_now;
            RETURN NEW;
        END IF;
    END IF;

    IF NEW.committed_at IS NULL
       AND NEW.aborted_at IS NULL AND NEW.aborted_by IS NULL
       AND OLD.switching_at IS NULL AND NEW.switching_at IS NOT NULL
       AND NEW.switching_at >= OLD.created_at AND isfinite(NEW.switching_at) THEN
        RETURN NEW;
    END IF;

    IF NEW.committed_at IS NULL
       AND NEW.aborted_at IS NOT NULL AND NEW.aborted_by IS NOT NULL
       AND NEW.switching_at IS NOT DISTINCT FROM OLD.switching_at
       AND NEW.aborted_at >= COALESCE(OLD.switching_at, OLD.created_at)
       AND isfinite(NEW.aborted_at)
       AND NEW.aborted_by = btrim(NEW.aborted_by)
       AND octet_length(NEW.aborted_by) BETWEEN 1 AND 255
       AND NEW.aborted_by !~ '[[:cntrl:]]' THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'credential activation preparation transition is invalid';
END;
$$;


-- +goose StatementEnd
RESET ROLE;
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'credential activation request history is forward-only'; END $$;
-- +goose StatementEnd
