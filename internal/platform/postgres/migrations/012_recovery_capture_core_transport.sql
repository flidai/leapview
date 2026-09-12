-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- FAI-520 capture-core transport is additive. Existing evidence rows and v3
-- associations are retained byte-for-byte. Nullable capture references and an
-- explicit required marker preserve both historical rows and in-flight older
-- writers without presenting embedded receipt cores as standalone transport.
-- +goose StatementBegin
DO $$
DECLARE
    constraint_name text;
BEGIN
    -- The original migration left these checks unnamed. Identify only the
    -- family/domain checks so their existing canonical-byte checks remain
    -- untouched while the allowlist gains the core family.
    FOR constraint_name IN
        SELECT c.conname
          FROM pg_constraint AS c
         WHERE c.conrelid = 'recovery.successor_evidence_v2'::regclass
           AND c.contype = 'c'
           AND (pg_get_constraintdef(c.oid) LIKE '%payload_family%'
                OR pg_get_constraintdef(c.oid) LIKE '%payload_digest = recovery.successor_domain_sha256%')
    LOOP
        EXECUTE format('ALTER TABLE recovery.successor_evidence_v2 DROP CONSTRAINT %I', constraint_name);
    END LOOP;
    FOR constraint_name IN
        SELECT c.conname
          FROM pg_constraint AS c
         WHERE c.conrelid = 'recovery.successor_evidence_locator_v2'::regclass
           AND c.contype = 'c'
           AND pg_get_constraintdef(c.oid) LIKE '%payload_family%'
    LOOP
        EXECUTE format('ALTER TABLE recovery.successor_evidence_locator_v2 DROP CONSTRAINT %I', constraint_name);
    END LOOP;
END
$$;
-- +goose StatementEnd

ALTER TABLE recovery.successor_evidence_v2
    ADD CONSTRAINT successor_evidence_v2_family_core_ck
    CHECK (payload_family IN ('manifest', 'anchor', 'profile', 'core', 'receipt', 'authority'));
ALTER TABLE recovery.successor_evidence_v2
    ADD CONSTRAINT successor_evidence_v2_domain_core_ck
    CHECK (payload_digest = recovery.successor_domain_sha256(
        CASE payload_family
            WHEN 'manifest' THEN 'leapview/managed-observations/v2'
            WHEN 'anchor' THEN 'leapview/recovery-source-anchor/v2'
            WHEN 'profile' THEN 'leapview/managed-provider-profiles/v2'
            WHEN 'core' THEN 'leapview/managed-capture-core/v2'
            WHEN 'receipt' THEN 'leapview/managed-capture-receipt/v2'
            WHEN 'authority' THEN 'leapview/authority-registry/v2'
        END || chr(10), canonical_bytes));
ALTER TABLE recovery.successor_evidence_locator_v2
    ADD CONSTRAINT successor_evidence_locator_v2_family_core_ck
    CHECK (payload_family IN ('manifest', 'anchor', 'profile', 'core', 'receipt', 'authority'));

ALTER TABLE recovery.successor_manifest_binding
    ADD COLUMN IF NOT EXISTS capture_core_digest text;
ALTER TABLE recovery.successor_manifest_binding
    ADD COLUMN IF NOT EXISTS capture_core_required boolean NOT NULL DEFAULT false;
ALTER TABLE recovery.successor_manifest_binding
    ADD COLUMN IF NOT EXISTS core_family text GENERATED ALWAYS AS ('core') STORED;
ALTER TABLE recovery.successor_manifest_binding
    ADD COLUMN IF NOT EXISTS core_version smallint GENERATED ALWAYS AS (2) STORED;
ALTER TABLE recovery.successor_manifest_binding
    ADD CONSTRAINT successor_manifest_binding_core_digest_ck
    CHECK ((capture_core_required AND capture_core_digest IS NOT NULL AND capture_core_digest ~ '^sha256:[0-9a-f]{64}$') OR
           (NOT capture_core_required AND capture_core_digest IS NULL));
ALTER TABLE recovery.successor_manifest_binding
    ADD CONSTRAINT successor_manifest_binding_core_fk
    FOREIGN KEY (core_family, core_version, capture_core_digest)
    REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest)
    ON DELETE RESTRICT;

ALTER TABLE recovery.recovery_set_v3
    ADD COLUMN IF NOT EXISTS capture_core_digest text;
ALTER TABLE recovery.recovery_set_v3
    ADD COLUMN IF NOT EXISTS capture_core_required boolean NOT NULL DEFAULT false;
ALTER TABLE recovery.recovery_set_v3
    ADD COLUMN IF NOT EXISTS core_family text GENERATED ALWAYS AS ('core') STORED;
ALTER TABLE recovery.recovery_set_v3
    ADD COLUMN IF NOT EXISTS core_version smallint GENERATED ALWAYS AS (2) STORED;
ALTER TABLE recovery.recovery_set_v3
    ADD CONSTRAINT recovery_set_v3_capture_core_ck
    CHECK ((capture_core_required AND capture_core_digest IS NOT NULL AND capture_core_digest = receipt_core_digest) OR
           (NOT capture_core_required AND capture_core_digest IS NULL));
ALTER TABLE recovery.recovery_set_v3
    ADD CONSTRAINT recovery_set_v3_core_fk
    FOREIGN KEY (core_family, core_version, capture_core_digest)
    REFERENCES recovery.successor_evidence_v2(payload_family, payload_version, payload_digest)
    ON DELETE RESTRICT NOT VALID;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION recovery.guard_successor_set_complete()
RETURNS trigger LANGUAGE plpgsql
SET search_path = pg_catalog, recovery
AS $$
BEGIN
    IF (SELECT count(*) FROM recovery.recovery_set_v3_root WHERE set_id = NEW.set_id) <> 2
       OR (SELECT count(*) FROM recovery.recovery_set_v3_root WHERE set_id = NEW.set_id AND root_kind IN ('ducklake', 'serving-artifact')) <> 2 THEN
        RAISE EXCEPTION 'prepared successor recovery set requires exactly two canonical roots';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM recovery.successor_evidence_locator_v2 l JOIN recovery.recovery_set_v3 s ON s.set_id = NEW.set_id
                   WHERE l.payload_family = s.manifest_family AND l.payload_version = s.manifest_version AND l.payload_digest = s.manifest_digest)
       OR NOT EXISTS (SELECT 1 FROM recovery.successor_evidence_locator_v2 l JOIN recovery.recovery_set_v3 s ON s.set_id = NEW.set_id
                   WHERE l.payload_family = s.anchor_family AND l.payload_version = s.anchor_version AND l.payload_digest = s.anchor_digest)
       OR NOT EXISTS (SELECT 1 FROM recovery.successor_evidence_locator_v2 l JOIN recovery.recovery_set_v3 s ON s.set_id = NEW.set_id
                   WHERE l.payload_family = s.profile_family AND l.payload_version = s.profile_version AND l.payload_digest = s.profile_digest)
       OR NOT EXISTS (SELECT 1 FROM recovery.successor_evidence_locator_v2 l JOIN recovery.recovery_set_v3 s ON s.set_id = NEW.set_id
                   WHERE l.payload_family = s.receipt_family AND l.payload_version = s.receipt_version AND l.payload_digest = s.receipt_digest)
       OR (EXISTS (SELECT 1 FROM recovery.recovery_set_v3 s WHERE s.set_id = NEW.set_id AND s.capture_core_required)
           AND NOT EXISTS (SELECT 1 FROM recovery.successor_evidence_locator_v2 l JOIN recovery.recovery_set_v3 s ON s.set_id = NEW.set_id
                   WHERE l.payload_family = 'core' AND l.payload_version = 2 AND l.payload_digest = s.capture_core_digest))
       OR NOT EXISTS (SELECT 1 FROM recovery.successor_evidence_locator_v2 l JOIN recovery.recovery_set_v3 s ON s.set_id = NEW.set_id
                   WHERE l.payload_family = s.authority_family AND l.payload_version = s.authority_version AND l.payload_digest = s.authority_digest) THEN
        RAISE EXCEPTION 'prepared successor recovery set requires exact locators for every selected evidence payload';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON TABLE recovery.successor_evidence_v2,
    recovery.successor_evidence_locator_v2, recovery.successor_manifest_binding,
    recovery.recovery_set_v3, recovery.recovery_set_v3_root FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT SELECT, INSERT ON TABLE recovery.successor_evidence_v2,
            recovery.successor_evidence_locator_v2, recovery.successor_manifest_binding,
            recovery.recovery_set_v3, recovery.recovery_set_v3_root TO leapview_control_maintenance;
        REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE recovery.successor_evidence_v2,
            recovery.successor_evidence_locator_v2, recovery.successor_manifest_binding,
            recovery.recovery_set_v3, recovery.recovery_set_v3_root FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        REVOKE ALL ON TABLE recovery.successor_evidence_v2,
            recovery.successor_evidence_locator_v2, recovery.successor_manifest_binding,
            recovery.recovery_set_v3, recovery.recovery_set_v3_root FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT SELECT ON TABLE recovery.successor_evidence_v2,
            recovery.successor_evidence_locator_v2, recovery.successor_manifest_binding,
            recovery.recovery_set_v3, recovery.recovery_set_v3_root TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE recovery.successor_evidence_v2,
            recovery.successor_evidence_locator_v2, recovery.successor_manifest_binding,
            recovery.recovery_set_v3, recovery.recovery_set_v3_root FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT SELECT ON TABLE recovery.successor_evidence_v2,
            recovery.successor_evidence_locator_v2, recovery.successor_manifest_binding,
            recovery.recovery_set_v3, recovery.recovery_set_v3_root TO leapview_control_backup;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE recovery.successor_evidence_v2,
            recovery.successor_evidence_locator_v2, recovery.successor_manifest_binding,
            recovery.recovery_set_v3, recovery.recovery_set_v3_root FROM leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'capture-core transport migration is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
