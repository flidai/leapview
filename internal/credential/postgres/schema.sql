-- PostgreSQL storage for encrypted credential drafts. Logical metadata and
-- encrypted envelopes are distinct so plaintext is never represented here.
CREATE SCHEMA IF NOT EXISTS credential;

CREATE OR REPLACE FUNCTION credential.reject_immutable_row_change()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'credential draft rows are immutable';
END;
$$;

CREATE OR REPLACE FUNCTION credential.guard_encryption_budget()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.uses <> 1 THEN
            RAISE EXCEPTION 'credential encryption budget must begin at one';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.deployment_id IS DISTINCT FROM OLD.deployment_id
       OR NEW.key_id IS DISTINCT FROM OLD.key_id
       OR NEW.key_commitment IS DISTINCT FROM OLD.key_commitment
       OR NEW.uses <> OLD.uses + 1 THEN
        RAISE EXCEPTION 'credential encryption reservations must increase by one';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TABLE IF NOT EXISTS credential.encryption_budget (
    deployment_id  text NOT NULL,
    key_id         text NOT NULL,
    key_commitment bytea NOT NULL,
    uses           bigint NOT NULL,
    PRIMARY KEY (deployment_id, key_id),
    UNIQUE (deployment_id, key_commitment),
    CHECK (deployment_id = btrim(deployment_id) AND octet_length(deployment_id) BETWEEN 1 AND 255),
    CHECK (key_id = btrim(key_id) AND octet_length(key_id) BETWEEN 1 AND 255),
    CHECK (octet_length(key_commitment) = 32),
    CHECK (uses BETWEEN 1 AND 2147483648)
);

DROP TRIGGER IF EXISTS encryption_budget_reservation_guard ON credential.encryption_budget;
CREATE TRIGGER encryption_budget_reservation_guard
    BEFORE INSERT OR UPDATE ON credential.encryption_budget
    FOR EACH ROW EXECUTE FUNCTION credential.guard_encryption_budget();

DROP TRIGGER IF EXISTS encryption_budget_no_delete ON credential.encryption_budget;
CREATE TRIGGER encryption_budget_no_delete
    BEFORE DELETE ON credential.encryption_budget
    FOR EACH ROW EXECUTE FUNCTION credential.reject_immutable_row_change();

CREATE OR REPLACE FUNCTION credential.valid_binding_fields(
    scope_kind text,
    target_id text,
    project_id text,
    environment text,
    resource_id text
) RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
SET search_path = pg_catalog
AS $$
    SELECT CASE scope_kind
        WHEN 'connection' THEN target_id <> '' AND project_id <> '' AND environment <> '' AND resource_id <> ''
        WHEN 'agent' THEN target_id = '' AND project_id = '' AND environment = '' AND resource_id <> ''
        ELSE false
    END
       AND target_id = btrim(target_id)
       AND project_id = btrim(project_id)
       AND environment = btrim(environment)
       AND resource_id = btrim(resource_id)
       AND octet_length(target_id) <= 255
       AND octet_length(project_id) <= 255
       AND octet_length(environment) <= 255
       AND octet_length(resource_id) BETWEEN 1 AND 255
       AND target_id !~ '[[:cntrl:]]'
       AND project_id !~ '[[:cntrl:]]'
       AND environment !~ '[[:cntrl:]]'
       AND resource_id !~ '[[:cntrl:]]';
$$;

CREATE TABLE IF NOT EXISTS credential.draft_version (
    version_id    text PRIMARY KEY,
    deployment_id text NOT NULL,
    owner_id      text NOT NULL,
    scope_kind    text NOT NULL,
    target_id     text NOT NULL DEFAULT '',
    project_id    text NOT NULL DEFAULT '',
    environment   text NOT NULL DEFAULT '',
    resource_id   text NOT NULL,
    purpose       text NOT NULL,
    provider      text NOT NULL,
    destination   text NOT NULL,
    actor_id      text NOT NULL,
    created_at    timestamptz NOT NULL,
    UNIQUE (deployment_id, version_id),
    CHECK (version_id = btrim(version_id) AND octet_length(version_id) BETWEEN 1 AND 255),
    CHECK (deployment_id = btrim(deployment_id) AND octet_length(deployment_id) BETWEEN 1 AND 255),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255 AND owner_id !~ '[[:cntrl:]]'),
    CHECK (credential.valid_binding_fields(scope_kind, target_id, project_id, environment, resource_id)),
    CHECK (purpose = btrim(purpose) AND octet_length(purpose) BETWEEN 1 AND 255 AND purpose !~ '[[:cntrl:]]'),
    CHECK (provider = btrim(provider) AND octet_length(provider) BETWEEN 1 AND 255 AND provider !~ '[[:cntrl:]]'),
    CHECK (destination ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (actor_id = btrim(actor_id) AND octet_length(actor_id) BETWEEN 1 AND 255 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (created_at > '-infinity'::timestamptz AND isfinite(created_at))
);

CREATE INDEX IF NOT EXISTS credential_draft_scope_created_version_idx
    ON credential.draft_version (
        deployment_id, owner_id, scope_kind, target_id, project_id,
        environment, resource_id, created_at DESC, version_id DESC
    );

CREATE TABLE IF NOT EXISTS credential.envelope (
    version_id    text PRIMARY KEY,
    deployment_id text NOT NULL,
    key_id        text NOT NULL,
    format        text NOT NULL,
    ciphertext    bytea NOT NULL,
    envelope_revision bigint NOT NULL DEFAULT 1,
    FOREIGN KEY (deployment_id, version_id)
        REFERENCES credential.draft_version (deployment_id, version_id),
    FOREIGN KEY (deployment_id, key_id)
        REFERENCES credential.encryption_budget (deployment_id, key_id),
    CHECK (format = 'aes-256-gcm-random-nonce-v1'),
    CHECK (octet_length(ciphertext) BETWEEN 28 AND 16412),
    CHECK (envelope_revision > 0)
);

DROP TRIGGER IF EXISTS draft_version_immutable ON credential.draft_version;
CREATE TRIGGER draft_version_immutable
    BEFORE UPDATE OR DELETE ON credential.draft_version
    FOR EACH ROW EXECUTE FUNCTION credential.reject_immutable_row_change();

DROP TRIGGER IF EXISTS envelope_immutable ON credential.envelope;
CREATE TRIGGER envelope_immutable
    BEFORE UPDATE OR DELETE ON credential.envelope
    FOR EACH ROW EXECUTE FUNCTION credential.reject_immutable_row_change();

-- A validation receipt records a successful observation of one exact saved
-- credential version and target binding. It contains only identities and
-- digests; credential fields and ciphertext are never copied here.
CREATE TABLE IF NOT EXISTS credential.validation_receipt (
    receipt_id          text PRIMARY KEY,
    deployment_id       text NOT NULL,
    version_id          text NOT NULL,
    owner_id             text NOT NULL,
    scope_kind           text NOT NULL,
    target_id            text NOT NULL DEFAULT '',
    project_id           text NOT NULL DEFAULT '',
    environment          text NOT NULL DEFAULT '',
    resource_id          text NOT NULL,
    purpose              text NOT NULL,
    provider             text NOT NULL,
    destination          text NOT NULL,
    actor_id             text NOT NULL,
    binding_id           text NOT NULL,
    binding_revision     bigint NOT NULL,
    configuration_digest text NOT NULL,
    validated_at         timestamptz NOT NULL,
    expires_at           timestamptz NOT NULL,
    FOREIGN KEY (deployment_id, version_id)
        REFERENCES credential.draft_version (deployment_id, version_id),
    UNIQUE (deployment_id, receipt_id),
    CHECK (receipt_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
    CHECK (deployment_id = btrim(deployment_id) AND octet_length(deployment_id) BETWEEN 1 AND 255),
    CHECK (version_id = btrim(version_id) AND octet_length(version_id) BETWEEN 1 AND 255),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255 AND owner_id !~ '[[:cntrl:]]'),
    CHECK (credential.valid_binding_fields(scope_kind, target_id, project_id, environment, resource_id)),
    CHECK (purpose = btrim(purpose) AND octet_length(purpose) BETWEEN 1 AND 255 AND purpose !~ '[[:cntrl:]]'),
    CHECK (provider = btrim(provider) AND octet_length(provider) BETWEEN 1 AND 255 AND provider !~ '[[:cntrl:]]'),
    CHECK (destination ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (actor_id = btrim(actor_id) AND octet_length(actor_id) BETWEEN 1 AND 255 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (binding_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$'),
    CHECK ((scope_kind = 'connection' AND binding_revision > 0) OR (scope_kind = 'agent' AND binding_revision >= 0)),
    CHECK (configuration_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (validated_at > '-infinity'::timestamptz AND isfinite(validated_at)),
    CHECK (expires_at > '-infinity'::timestamptz AND isfinite(expires_at)),
    CHECK (expires_at = validated_at + interval '5 minutes')
);

DROP TRIGGER IF EXISTS validation_receipt_immutable ON credential.validation_receipt;
CREATE TRIGGER validation_receipt_immutable
    BEFORE UPDATE OR DELETE ON credential.validation_receipt
    FOR EACH ROW EXECUTE FUNCTION credential.reject_immutable_row_change();

-- A preparation reserves one immutable receipt for one intended activation.
-- The unique deployment key deliberately permits only one outstanding
-- preparation in this bounded primitive; lifecycle completion can relax it.
CREATE TABLE IF NOT EXISTS credential.activation_preparation (
    operation_id             text PRIMARY KEY,
    deployment_id            text NOT NULL,
    receipt_id               text NOT NULL,
    expected_target_revision bigint NOT NULL,
    predecessor_generation_id text,
    candidate_id             text NOT NULL,
    generation_id            text NOT NULL,
    publication_id           text NOT NULL,
    created_at               timestamptz NOT NULL,
    switching_at             timestamptz,
    committed_at             timestamptz,
    completed_at             timestamptz,
    CHECK (completed_at IS NULL OR (committed_at IS NOT NULL AND completed_at >= committed_at AND isfinite(completed_at))),
    aborted_at               timestamptz,
    aborted_by               text,
    UNIQUE (receipt_id),
    FOREIGN KEY (deployment_id, receipt_id)
        REFERENCES credential.validation_receipt (deployment_id, receipt_id),
    CHECK (operation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND operation_id <> '00000000-0000-0000-0000-000000000000'),
    CHECK (deployment_id = btrim(deployment_id) AND octet_length(deployment_id) BETWEEN 1 AND 255),
    CHECK (receipt_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND receipt_id <> '00000000-0000-0000-0000-000000000000'),
    CHECK (expected_target_revision > 0),
    CHECK (predecessor_generation_id IS NULL OR (
        predecessor_generation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND predecessor_generation_id <> '00000000-0000-0000-0000-000000000000')),
    CHECK (candidate_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND candidate_id <> '00000000-0000-0000-0000-000000000000'),
    CHECK (generation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND generation_id <> '00000000-0000-0000-0000-000000000000'),
    CHECK (publication_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND publication_id <> '00000000-0000-0000-0000-000000000000'),
    CHECK (created_at > '-infinity'::timestamptz AND isfinite(created_at)),
    CHECK (switching_at IS NULL OR (
        switching_at > '-infinity'::timestamptz AND isfinite(switching_at) AND switching_at >= created_at)),
    CHECK (committed_at IS NULL OR (
        switching_at IS NOT NULL AND committed_at > '-infinity'::timestamptz
        AND isfinite(committed_at) AND committed_at >= switching_at)),
    CHECK ((aborted_at IS NULL AND aborted_by IS NULL) OR (
        aborted_at IS NOT NULL AND aborted_by IS NOT NULL
        AND aborted_at > '-infinity'::timestamptz AND isfinite(aborted_at)
        AND aborted_at >= COALESCE(switching_at, created_at)
        AND aborted_by = btrim(aborted_by) AND octet_length(aborted_by) BETWEEN 1 AND 255
        AND aborted_by !~ '[[:cntrl:]]'))
);

CREATE UNIQUE INDEX IF NOT EXISTS activation_preparation_one_pending_deployment_idx
    ON credential.activation_preparation (deployment_id) WHERE aborted_at IS NULL AND completed_at IS NULL;

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

DROP TRIGGER IF EXISTS activation_preparation_immutable ON credential.activation_preparation;
DROP TRIGGER IF EXISTS activation_preparation_abort_guard ON credential.activation_preparation;
DROP TRIGGER IF EXISTS activation_preparation_transition_guard ON credential.activation_preparation;
CREATE TRIGGER activation_preparation_transition_guard
    BEFORE INSERT OR UPDATE OR DELETE ON credential.activation_preparation
    FOR EACH ROW EXECUTE FUNCTION credential.guard_activation_preparation_transition();

REVOKE ALL ON SCHEMA credential FROM PUBLIC;
REVOKE ALL ON credential.encryption_budget, credential.draft_version, credential.envelope, credential.validation_receipt, credential.activation_preparation FROM PUBLIC;
REVOKE ALL ON FUNCTION credential.reject_immutable_row_change() FROM PUBLIC;
REVOKE ALL ON FUNCTION credential.guard_activation_preparation_transition() FROM PUBLIC;
REVOKE ALL ON FUNCTION credential.guard_encryption_budget() FROM PUBLIC;
REVOKE ALL ON FUNCTION credential.valid_binding_fields(text, text, text, text, text) FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA credential TO leapview_control_runtime;
        GRANT SELECT, INSERT ON credential.draft_version, credential.envelope TO leapview_control_runtime;
        GRANT INSERT ON credential.validation_receipt TO leapview_control_runtime;
        GRANT SELECT ON credential.validation_receipt TO leapview_control_runtime;
        GRANT SELECT, INSERT ON credential.activation_preparation TO leapview_control_runtime;
        GRANT UPDATE (switching_at, committed_at, completed_at, aborted_at, aborted_by) ON credential.activation_preparation TO leapview_control_runtime;
        GRANT SELECT, INSERT ON credential.encryption_budget TO leapview_control_runtime;
        GRANT UPDATE (uses) ON credential.encryption_budget TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION credential.valid_binding_fields(text, text, text, text, text) TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA credential TO leapview_control_backup;
        GRANT SELECT ON credential.encryption_budget, credential.draft_version, credential.envelope, credential.validation_receipt, credential.activation_preparation TO leapview_control_backup;
    END IF;
END;
$$;
