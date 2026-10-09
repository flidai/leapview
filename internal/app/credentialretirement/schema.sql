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
