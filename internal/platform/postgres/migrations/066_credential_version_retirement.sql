-- +goose Up
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
-- Retirement preserves encrypted history and key recovery obligations. A
-- version can never be re-enabled; save a new logical version instead.
CREATE TABLE IF NOT EXISTS credential.version_retirement (
    deployment_id text NOT NULL,
    version_id text NOT NULL,
    retired_by text NOT NULL CHECK (retired_by=btrim(retired_by) AND length(retired_by) BETWEEN 1 AND 255),
    audit_id uuid NOT NULL UNIQUE,
    retired_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(retired_at)),
    PRIMARY KEY(deployment_id,version_id),
    FOREIGN KEY(deployment_id,version_id) REFERENCES credential.draft_version(deployment_id,version_id)
);

-- Runtime has no UPDATE privilege on immutable draft rows. This narrow helper
-- supplies only a row lock, never a mutation or a secret read. Every dependency
-- insertion and retirement takes this same lock, after the publication fence.
CREATE OR REPLACE FUNCTION credential.lock_version(p_deployment text,p_version text)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
    IF current_setting('transaction_isolation') <> 'read committed' THEN
        RAISE EXCEPTION 'credential version fencing requires read committed';
    END IF;
    PERFORM 1 FROM credential.draft_version
      WHERE deployment_id=p_deployment AND version_id=p_version FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'credential version is unavailable'; END IF;
END;
$$;

CREATE OR REPLACE FUNCTION credential.require_available_version(p_deployment text,p_version text)
RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    PERFORM credential.lock_version(p_deployment,p_version);
    IF EXISTS(SELECT 1 FROM credential.version_retirement WHERE deployment_id=p_deployment AND version_id=p_version) THEN
        RAISE EXCEPTION 'credential version is retired locally';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION credential.guard_version_dependency() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    PERFORM credential.require_available_version(NEW.deployment_id,NEW.version_id);
    RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER validation_receipt_version_available BEFORE INSERT ON credential.validation_receipt
    FOR EACH ROW EXECUTE FUNCTION credential.guard_version_dependency();
CREATE OR REPLACE TRIGGER activation_request_version_available BEFORE INSERT ON credential.activation_request
    FOR EACH ROW EXECUTE FUNCTION credential.guard_version_dependency();

CREATE OR REPLACE FUNCTION credential.guard_version_retirement() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE draft credential.draft_version%ROWTYPE;
BEGIN
    IF TG_OP <> 'INSERT' THEN RAISE EXCEPTION 'credential retirement is immutable'; END IF;
    PERFORM credential.lock_version(NEW.deployment_id,NEW.version_id);
    SELECT * INTO STRICT draft FROM credential.draft_version WHERE deployment_id=NEW.deployment_id AND version_id=NEW.version_id;
    IF EXISTS(SELECT 1 FROM credential.activation_request WHERE deployment_id=NEW.deployment_id AND version_id=NEW.version_id)
       OR EXISTS(SELECT 1 FROM credential.activation_preparation AS p JOIN credential.validation_receipt AS r USING(deployment_id,receipt_id)
                 WHERE r.deployment_id=NEW.deployment_id AND r.version_id=NEW.version_id)
       OR EXISTS(SELECT 1 FROM credential.validation_receipt WHERE deployment_id=NEW.deployment_id AND version_id=NEW.version_id AND expires_at>clock_timestamp()) THEN
        RAISE EXCEPTION 'credential version has retained activation or validation references';
    END IF;
    IF NOT EXISTS(SELECT 1 FROM audit.audit_event
       WHERE audit_id=NEW.audit_id AND actor_id=NEW.retired_by AND source='credential'
         AND ((draft.scope_kind='connection' AND operation='retireCredentialVersion' AND action='credential.version.retired_local' AND resource_kind='connection' AND scope_id=draft.project_id)
           OR (draft.scope_kind='agent' AND operation='retireAgentCredentialVersion' AND action='credential.agent_version.retired_local' AND resource_kind='instance' AND scope_id=draft.deployment_id))
         AND resource_id=draft.resource_id AND outcome='success'
         AND metadata->>'version_id'=NEW.version_id AND metadata->>'state'='retired_local') THEN
        RAISE EXCEPTION 'credential retirement requires exact atomic audit';
    END IF;
    NEW.retired_at=clock_timestamp();
    RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER version_retirement_guard BEFORE INSERT OR UPDATE OR DELETE ON credential.version_retirement
    FOR EACH ROW EXECUTE FUNCTION credential.guard_version_retirement();
REVOKE ALL ON credential.version_retirement FROM PUBLIC;
REVOKE ALL ON FUNCTION credential.lock_version(text,text),credential.require_available_version(text,text),credential.guard_version_dependency(),credential.guard_version_retirement() FROM PUBLIC;
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        GRANT SELECT,INSERT ON credential.version_retirement TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION credential.lock_version(text,text),credential.require_available_version(text,text) TO leapview_control_runtime;
    END IF;
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
        GRANT SELECT ON credential.version_retirement TO leapview_control_backup;
    END IF;
END $$;

-- Application composition connects retained release/configuration authority to
-- the credential-owned version fence. Current pointers alone are insufficient.
CREATE OR REPLACE FUNCTION credential.guard_retained_release_version() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE binding jsonb; version text; deployment text;
BEGIN
    FOR binding IN SELECT value FROM jsonb_array_elements(COALESCE(NEW.provenance#>'{plan,bindings}','[]'::jsonb)) LOOP
        version=binding->>'credentialVersionId';
        IF COALESCE(version,'')<>'' THEN
            SELECT deployment_id INTO STRICT deployment FROM credential.draft_version WHERE version_id=version;
            PERFORM credential.require_available_version(deployment,version);
        END IF;
    END LOOP;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER candidate_credential_version_available BEFORE INSERT ON release.candidate_provenance
    FOR EACH ROW EXECUTE FUNCTION credential.guard_retained_release_version();
CREATE OR REPLACE TRIGGER release_credential_version_available BEFORE INSERT ON release.release_record
    FOR EACH ROW EXECUTE FUNCTION credential.guard_retained_release_version();

CREATE OR REPLACE FUNCTION credential.guard_retained_agent_version() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE deployment text;
BEGIN
    IF NEW.credential_version_id IS NOT NULL THEN
        SELECT deployment_id INTO STRICT deployment FROM credential.draft_version WHERE version_id=NEW.credential_version_id;
        PERFORM credential.require_available_version(deployment,NEW.credential_version_id);
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER agent_credential_version_available BEFORE INSERT ON agent.configuration_revisions
    FOR EACH ROW EXECUTE FUNCTION credential.guard_retained_agent_version();

CREATE OR REPLACE FUNCTION credential.guard_retirement_retained_references() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE needle jsonb;
BEGIN
    PERFORM credential.lock_version(NEW.deployment_id,NEW.version_id);
    needle=jsonb_build_array(jsonb_build_object('credentialVersionId',NEW.version_id));
    IF EXISTS(SELECT 1 FROM release.candidate_provenance WHERE provenance#>'{plan,bindings}' @> needle)
       OR EXISTS(SELECT 1 FROM release.release_record WHERE provenance#>'{plan,bindings}' @> needle)
       OR EXISTS(SELECT 1 FROM agent.configuration_revisions WHERE credential_version_id=NEW.version_id) THEN
        RAISE EXCEPTION 'credential version has retained serving or configuration references';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE TRIGGER version_retirement_retained_references BEFORE INSERT ON credential.version_retirement
    FOR EACH ROW EXECUTE FUNCTION credential.guard_retirement_retained_references();

CREATE INDEX IF NOT EXISTS candidate_provenance_credential_bindings_idx ON release.candidate_provenance USING gin((provenance#>'{plan,bindings}'));
CREATE INDEX IF NOT EXISTS release_record_credential_bindings_idx ON release.release_record USING gin((provenance#>'{plan,bindings}'));
CREATE INDEX IF NOT EXISTS agent_configuration_credential_version_idx ON agent.configuration_revisions(credential_version_id) WHERE credential_version_id IS NOT NULL;
REVOKE ALL ON FUNCTION credential.guard_retained_release_version(),credential.guard_retained_agent_version(),credential.guard_retirement_retained_references() FROM PUBLIC;

-- +goose StatementEnd
RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'credential retirement is forward-only; preserve retired versions and restore a coordinated backup';
END $$;
-- +goose StatementEnd
