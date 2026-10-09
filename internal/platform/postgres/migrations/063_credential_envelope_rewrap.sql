-- +goose Up
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
-- Envelope maintenance preserves the immutable logical credential version.
-- Runtime cannot invoke the rewrap capability or update envelope columns.
CREATE OR REPLACE FUNCTION credential.guard_envelope_rewrap() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP <> 'UPDATE' OR current_user <> 'leapview_control_owner'
       OR current_setting('credential.rewrap', true) IS DISTINCT FROM jsonb_build_array(OLD.deployment_id, OLD.version_id)::text
       OR NEW.version_id IS DISTINCT FROM OLD.version_id
       OR NEW.deployment_id IS DISTINCT FROM OLD.deployment_id
       OR NEW.format IS DISTINCT FROM OLD.format
       OR NEW.key_id IS NOT DISTINCT FROM OLD.key_id
       OR NEW.ciphertext IS NOT DISTINCT FROM OLD.ciphertext
       OR NEW.envelope_revision <> OLD.envelope_revision + 1 THEN
        RAISE EXCEPTION 'credential envelope changes require bounded maintenance rewrap';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS envelope_immutable ON credential.envelope;
CREATE TRIGGER envelope_immutable BEFORE UPDATE OR DELETE ON credential.envelope
FOR EACH ROW EXECUTE FUNCTION credential.guard_envelope_rewrap();

CREATE OR REPLACE FUNCTION credential.rewrap_envelope(
    p_deployment_id text, p_version_id text, p_previous_revision bigint,
    p_previous_key_id text, p_key_id text, p_format text, p_ciphertext bytea,
    p_audit_id uuid, p_intent_digest text
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE
    changed bigint;
BEGIN
    IF p_previous_revision < 1 OR p_previous_revision >= 9223372036854775807
       OR p_previous_key_id = p_key_id OR p_audit_id IS NULL
       OR p_audit_id = '00000000-0000-0000-0000-000000000000'::uuid
       OR p_intent_digest IS NULL OR p_intent_digest !~ '^sha256:[0-9a-f]{64}$' THEN
        RAISE EXCEPTION 'invalid credential envelope rewrap';
    END IF;
    PERFORM set_config('credential.rewrap', jsonb_build_array(p_deployment_id, p_version_id)::text, true);
    UPDATE credential.envelope
    SET key_id=p_key_id, format=p_format, ciphertext=p_ciphertext,
        envelope_revision=envelope_revision+1
    WHERE deployment_id=p_deployment_id AND version_id=p_version_id
      AND envelope_revision=p_previous_revision AND key_id=p_previous_key_id;
    GET DIAGNOSTICS changed = ROW_COUNT;
    IF changed = 0 THEN
        PERFORM set_config('credential.rewrap', '', true);
        RETURN 0;
    END IF;
    INSERT INTO audit.audit_event (
        audit_id, scope_id, actor_id, source, operation, action, resource_kind,
        resource_id, outcome, aggregate_key, aggregate_sequence, intent_digest, metadata
    ) VALUES (
        p_audit_id, p_deployment_id, 'offline_operator', 'credential',
        'rewrapCredentialEnvelope', 'credential.envelope.rewrapped', 'credential_version',
        p_version_id, 'success', 'credential-envelope:' || p_version_id,
        p_previous_revision+1, p_intent_digest,
        jsonb_build_object('previousKeyId', p_previous_key_id, 'keyId', p_key_id, 'envelopeRevision', p_previous_revision+1)
    );
    PERFORM set_config('credential.rewrap', '', true);
    RETURN p_previous_revision+1;
END;
$$;
REVOKE ALL ON FUNCTION credential.guard_envelope_rewrap() FROM PUBLIC;
REVOKE ALL ON FUNCTION credential.rewrap_envelope(text,text,bigint,text,text,text,bytea,uuid,text) FROM PUBLIC;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN
        ALTER FUNCTION credential.rewrap_envelope(text,text,bigint,text,text,text,bytea,uuid,text) OWNER TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA credential TO leapview_control_maintenance;
        GRANT SELECT ON credential.draft_version, credential.envelope, credential.encryption_budget TO leapview_control_maintenance;
        GRANT INSERT ON credential.encryption_budget TO leapview_control_maintenance;
        GRANT UPDATE (uses) ON credential.encryption_budget TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION credential.rewrap_envelope(text,text,bigint,text,text,text,bytea,uuid,text) TO leapview_control_maintenance;
        IF to_regclass('platform.instance_identity') IS NOT NULL THEN
            GRANT USAGE ON SCHEMA platform TO leapview_control_maintenance;
            GRANT SELECT ON platform.instance_identity, platform.instance_customer_owner TO leapview_control_maintenance;
        END IF;
    END IF;
END $$;

-- +goose StatementEnd
RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'credential envelope rewrap is forward-only; preserve recovery keys and restore a coordinated backup';
END $$;
-- +goose StatementEnd
