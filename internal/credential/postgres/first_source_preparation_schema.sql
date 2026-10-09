-- First-source source/publisher intent augments the existing exclusive receipt
-- reservation. It never replaces or widens activation_request semantics.
CREATE TABLE IF NOT EXISTS credential.first_source_preparation (
    operation_id text PRIMARY KEY REFERENCES credential.activation_request(operation_id),
    target_id text NOT NULL REFERENCES credential.first_source_admission(target_id),
    original_receipt_id text NOT NULL REFERENCES credential.validation_receipt(receipt_id),
    intent_digest text NOT NULL CHECK (intent_digest ~ '^sha256:[0-9a-f]{64}$'),
    intent_document jsonb NOT NULL CHECK (jsonb_typeof(intent_document)='object' AND octet_length(intent_document::text) BETWEEN 2 AND 32768),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(created_at)),
    CHECK (intent_document->>'preparationId' IS NOT DISTINCT FROM operation_id),
    CHECK (intent_document#>>'{receipt,ReceiptID}' IS NOT DISTINCT FROM original_receipt_id),
    CHECK (intent_document#>>'{receipt,Binding,TargetID}' IS NOT DISTINCT FROM target_id)
);

CREATE TABLE IF NOT EXISTS credential.first_source_plan_link (
    preparation_id text PRIMARY KEY REFERENCES credential.first_source_preparation(operation_id),
    target_id text NOT NULL,
    plan_id text NOT NULL CHECK (plan_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    binding_digest text NOT NULL CHECK (binding_digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(created_at)),
    UNIQUE(target_id,plan_id)
);

CREATE OR REPLACE FUNCTION credential.guard_first_source_preparation() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN RAISE EXCEPTION 'first-source preparation identity is immutable'; END IF;
    IF NOT EXISTS (
        SELECT 1 FROM credential.activation_request AS request
        JOIN credential.first_source_admission AS admission ON admission.target_id=request.deployment_id
        WHERE request.operation_id=NEW.operation_id AND request.deployment_id=NEW.target_id
          AND request.receipt_id=NEW.original_receipt_id AND request.state='preparing'
          AND admission.operation_id=NEW.intent_document->>'admissionOperationId'
          AND admission.intent_digest=NEW.intent_document->>'admissionDigest'
    ) OR NOT EXISTS (
        SELECT 1 FROM audit.audit_event WHERE source='credential' AND action='credential.first_source.prepared'
          AND scope_id=NEW.target_id AND actor_id=NEW.intent_document#>>'{receipt,ActorID}'
          AND metadata->>'preparationId'=NEW.operation_id AND metadata->>'intentDigest'=NEW.intent_digest
    ) THEN RAISE EXCEPTION 'first-source preparation requires exact reservation, admission and atomic audit'; END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION credential.guard_first_source_plan_link() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN RAISE EXCEPTION 'first-source plan selection is immutable'; END IF;
    IF NOT EXISTS (
        SELECT 1 FROM credential.first_source_preparation AS preparation
        JOIN credential.activation_request AS request ON request.operation_id=preparation.operation_id
        WHERE preparation.operation_id=NEW.preparation_id AND preparation.target_id=NEW.target_id
          AND request.state IN ('preparing','prepared','switching')
          AND preparation.intent_document->>'planRequestDigest'=NEW.request_digest
    ) OR NOT EXISTS (
        SELECT 1 FROM audit.audit_event WHERE source='credential' AND action='credential.first_source.plan_selected'
          AND actor_id=(SELECT intent_document->>'publisherId' FROM credential.first_source_preparation WHERE operation_id=NEW.preparation_id)
          AND scope_id=NEW.target_id AND metadata->>'preparationId'=NEW.preparation_id
          AND metadata->>'planId'=NEW.plan_id AND metadata->>'requestDigest'=NEW.request_digest
          AND metadata->>'bindingDigest'=NEW.binding_digest
    ) THEN RAISE EXCEPTION 'first-source plan selection requires exact active preparation and atomic audit'; END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS first_source_preparation_guard ON credential.first_source_preparation;
CREATE TRIGGER first_source_preparation_guard BEFORE INSERT OR UPDATE OR DELETE ON credential.first_source_preparation
FOR EACH ROW EXECUTE FUNCTION credential.guard_first_source_preparation();
DROP TRIGGER IF EXISTS first_source_plan_link_guard ON credential.first_source_plan_link;
CREATE TRIGGER first_source_plan_link_guard BEFORE INSERT OR UPDATE OR DELETE ON credential.first_source_plan_link
FOR EACH ROW EXECUTE FUNCTION credential.guard_first_source_plan_link();
REVOKE ALL ON credential.first_source_preparation,credential.first_source_plan_link FROM PUBLIC;
REVOKE ALL ON FUNCTION credential.guard_first_source_preparation(),credential.guard_first_source_plan_link() FROM PUBLIC;
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        GRANT SELECT,INSERT ON credential.first_source_preparation,credential.first_source_plan_link TO leapview_control_runtime;
    END IF;
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
        GRANT SELECT ON credential.first_source_preparation,credential.first_source_plan_link TO leapview_control_backup;
    END IF;
END $$;
