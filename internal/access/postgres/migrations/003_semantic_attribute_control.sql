-- FAI-637: durable semantic-access assignments and trusted claim mappings.
--
-- Revision 002 is intentionally not edited.  This migration owns only the
-- control-plane rows which reference the immutable definition registry.

CREATE TABLE access.semantic_attribute_control_state (
    singleton         boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    profile           text NOT NULL CHECK (profile = 'leapview.semantic-access/v1'),
    control_revision  bigint NOT NULL DEFAULT 0 CHECK (control_revision >= 0),
    control_digest    text NOT NULL CHECK (control_digest ~ '^sha256:[0-9a-f]{64}$'),
    updated_at        timestamptz NOT NULL DEFAULT clock_timestamp()
);

INSERT INTO access.semantic_attribute_control_state
    (singleton, profile, control_revision, control_digest)
VALUES
    (true, 'leapview.semantic-access/v1', 0,
     'sha256:e05005cdeee20cc98d9e8de8f32ed4b8da34a95f82872dc3b65a451ce7de4e37')
ON CONFLICT (singleton) DO NOTHING;

-- A row is one assignment incarnation.  Tombstones remain in place forever;
-- restoring a subject/definition pair creates a new immutable assignment id.
CREATE TABLE access.semantic_attribute_assignment (
    assignment_id       uuid PRIMARY KEY DEFAULT uuidv7(),
    definition_id       uuid NOT NULL REFERENCES access.semantic_attribute_definition(definition_id),
    subject_kind        text NOT NULL CHECK (subject_kind IN ('principal','group')),
    subject_id          uuid NOT NULL,
    definition_version  bigint NOT NULL CHECK (definition_version > 0),
    value_type          text NOT NULL CHECK (value_type IN ('String','Boolean','Integer','Decimal','Date','Timestamp')),
    value_shape         text NOT NULL CHECK (value_shape IN ('scalar','list')),
    canonical_values    text[] NOT NULL,
    value_digest        text NOT NULL CHECK (value_digest ~ '^sha256:[0-9a-f]{64}$'),
    assignment_version  bigint NOT NULL DEFAULT 1 CHECK (assignment_version > 0),
    tombstoned_at       timestamptz,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (array_ndims(canonical_values) = 1),
    CHECK (cardinality(canonical_values) BETWEEN 1 AND 1024),
    CHECK ((value_shape = 'scalar' AND cardinality(canonical_values) = 1) OR value_shape = 'list')
);
CREATE UNIQUE INDEX semantic_attribute_assignment_active_key
    ON access.semantic_attribute_assignment(definition_id, subject_kind, subject_id)
    WHERE tombstoned_at IS NULL;
CREATE INDEX semantic_attribute_assignment_subject_idx
    ON access.semantic_attribute_assignment(subject_kind, subject_id, definition_id, assignment_id);

-- A mapping has no value payload: it names the trusted provider claim which
-- will be canonicalized at authentication/evaluation time.
CREATE TABLE access.semantic_attribute_claim_mapping (
    mapping_id          uuid PRIMARY KEY DEFAULT uuidv7(),
    source_kind        text NOT NULL CHECK (source_kind IN ('saml','oidc','embed','service_token')),
    provider            text NOT NULL CHECK (provider = btrim(provider) AND octet_length(provider) BETWEEN 1 AND 128 AND provider !~ '[[:cntrl:]]'),
    issuer              text NOT NULL CHECK (issuer = btrim(issuer) AND octet_length(issuer) BETWEEN 1 AND 1024 AND issuer !~ '[[:cntrl:]]'),
    audience            text NOT NULL CHECK (audience = btrim(audience) AND octet_length(audience) BETWEEN 1 AND 512 AND audience !~ '[[:cntrl:]]'),
    claim               text NOT NULL CHECK (claim = btrim(claim) AND octet_length(claim) BETWEEN 1 AND 1024 AND claim !~ '[[:cntrl:]]'),
    definition_id       uuid NOT NULL REFERENCES access.semantic_attribute_definition(definition_id),
    definition_version  bigint NOT NULL CHECK (definition_version > 0),
    value_type          text NOT NULL CHECK (value_type IN ('String','Boolean','Integer','Decimal','Date','Timestamp')),
    value_shape         text NOT NULL CHECK (value_shape IN ('scalar','list')),
    mapping_version     bigint NOT NULL DEFAULT 1 CHECK (mapping_version > 0),
    tombstoned_at       timestamptz,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE UNIQUE INDEX semantic_attribute_claim_mapping_active_key
    ON access.semantic_attribute_claim_mapping(source_kind, provider, issuer, audience, claim, definition_id)
    WHERE tombstoned_at IS NULL;
CREATE INDEX semantic_attribute_claim_mapping_lookup_idx
    ON access.semantic_attribute_claim_mapping(source_kind, provider, issuer, audience, claim, mapping_id);

CREATE OR REPLACE FUNCTION access.validate_semantic_attribute_owner_exists()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, access
AS $$
BEGIN
    -- An already-owned definition may still be edited after its owner is
    -- revoked. Requiring the owner to remain active on every metadata update
    -- would strand the definition and prevent its lifecycle from completing.
    IF TG_OP = 'UPDATE' AND OLD.owner_kind = NEW.owner_kind AND OLD.owner_id IS NOT DISTINCT FROM NEW.owner_id THEN
        RETURN NEW;
    END IF;
    IF NEW.owner_kind = 'principal' AND NOT EXISTS (
        SELECT 1 FROM access.principal WHERE id = NEW.owner_id AND revoked_at IS NULL
    ) THEN
        RAISE EXCEPTION 'semantic attribute owner principal does not exist';
    ELSIF NEW.owner_kind = 'group' AND NOT EXISTS (
        SELECT 1 FROM access.access_group WHERE id = NEW.owner_id AND revoked_at IS NULL
    ) THEN
        RAISE EXCEPTION 'semantic attribute owner group does not exist';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER semantic_attribute_definition_owner_exists
    BEFORE INSERT OR UPDATE ON access.semantic_attribute_definition
    FOR EACH ROW EXECUTE FUNCTION access.validate_semantic_attribute_owner_exists();

CREATE OR REPLACE FUNCTION access.validate_semantic_attribute_assignment()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, access
AS $$
DECLARE definition_type text; definition_shape text; definition_enabled boolean; tombstone_transition boolean := false;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        tombstone_transition := OLD.tombstoned_at IS NULL AND NEW.tombstoned_at IS NOT NULL;
    END IF;
    SELECT value_type, value_shape, enabled
      INTO definition_type, definition_shape, definition_enabled
      FROM access.semantic_attribute_definition
     WHERE definition_id = NEW.definition_id;
    IF NOT FOUND OR (NOT definition_enabled AND NOT tombstone_transition) THEN
        RAISE EXCEPTION 'semantic attribute definition is missing or disabled';
    END IF;
    IF definition_type <> NEW.value_type OR definition_shape <> NEW.value_shape THEN
        RAISE EXCEPTION 'semantic attribute assignment type is not the definition type';
    END IF;
    IF TG_OP = 'INSERT' OR NOT tombstone_transition THEN
        IF NEW.subject_kind = 'principal' AND NOT EXISTS (
            SELECT 1 FROM access.principal WHERE id = NEW.subject_id AND revoked_at IS NULL
        ) THEN
            RAISE EXCEPTION 'semantic attribute assignment principal does not exist';
        ELSIF NEW.subject_kind = 'group' AND NOT EXISTS (
            SELECT 1 FROM access.access_group WHERE id = NEW.subject_id AND revoked_at IS NULL
        ) THEN
            RAISE EXCEPTION 'semantic attribute assignment group does not exist';
        END IF;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF OLD.assignment_id <> NEW.assignment_id OR OLD.definition_id <> NEW.definition_id OR
           OLD.subject_kind <> NEW.subject_kind OR OLD.subject_id <> NEW.subject_id OR
           OLD.definition_version <> NEW.definition_version OR OLD.value_type <> NEW.value_type OR
           OLD.value_shape <> NEW.value_shape OR OLD.created_at <> NEW.created_at THEN
            RAISE EXCEPTION 'semantic attribute assignment identity and type are immutable';
        END IF;
        IF OLD.tombstoned_at IS NOT NULL AND NEW.tombstoned_at IS NULL THEN
            RAISE EXCEPTION 'semantic attribute assignment tombstone is immutable';
        END IF;
        IF OLD.tombstoned_at IS NOT NULL THEN
            RAISE EXCEPTION 'semantic attribute assignment tombstone is immutable';
        END IF;
        IF OLD.tombstoned_at IS NULL AND NEW.tombstoned_at IS NOT NULL AND
           (OLD.canonical_values IS DISTINCT FROM NEW.canonical_values OR OLD.value_digest <> NEW.value_digest) THEN
            RAISE EXCEPTION 'semantic attribute assignment tombstone cannot rewrite its value';
        END IF;
        IF NEW.assignment_version <> OLD.assignment_version + 1 THEN
            RAISE EXCEPTION 'semantic attribute assignment version must advance exactly once';
        END IF;
        IF OLD.canonical_values = NEW.canonical_values AND OLD.value_digest = NEW.value_digest AND
           OLD.tombstoned_at IS NOT DISTINCT FROM NEW.tombstoned_at THEN
            RAISE EXCEPTION 'semantic attribute assignment update did not change mutable state';
        END IF;
        IF OLD.tombstoned_at IS DISTINCT FROM NEW.tombstoned_at AND NEW.tombstoned_at IS NULL THEN
            RAISE EXCEPTION 'semantic attribute assignment tombstone is database-owned';
        END IF;
    END IF;
    NEW.updated_at := CASE WHEN TG_OP = 'UPDATE'
        THEN GREATEST(clock_timestamp(), OLD.updated_at + interval '1 microsecond')
        ELSE clock_timestamp() END;
    IF TG_OP = 'UPDATE' AND OLD.tombstoned_at IS NULL AND NEW.tombstoned_at IS NOT NULL THEN
        NEW.tombstoned_at := clock_timestamp();
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION access.validate_semantic_attribute_claim_mapping()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, access
AS $$
DECLARE definition_type text; definition_shape text; definition_enabled boolean; tombstone_transition boolean := false;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        tombstone_transition := OLD.tombstoned_at IS NULL AND NEW.tombstoned_at IS NOT NULL;
    END IF;
    SELECT value_type, value_shape, enabled
      INTO definition_type, definition_shape, definition_enabled
      FROM access.semantic_attribute_definition
     WHERE definition_id = NEW.definition_id;
    IF NOT FOUND OR (NOT definition_enabled AND NOT tombstone_transition) THEN
        RAISE EXCEPTION 'semantic attribute mapping definition is missing or disabled';
    END IF;
    IF definition_type <> NEW.value_type OR definition_shape <> NEW.value_shape THEN
        RAISE EXCEPTION 'semantic attribute mapping type is not the definition type';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF OLD.mapping_id <> NEW.mapping_id OR OLD.source_kind <> NEW.source_kind OR
           OLD.provider <> NEW.provider OR OLD.issuer <> NEW.issuer OR OLD.audience <> NEW.audience OR
           OLD.claim <> NEW.claim OR OLD.definition_id <> NEW.definition_id OR
           OLD.definition_version <> NEW.definition_version OR
           OLD.value_type <> NEW.value_type OR OLD.value_shape <> NEW.value_shape OR OLD.created_at <> NEW.created_at THEN
            RAISE EXCEPTION 'semantic attribute mapping identity and type are immutable';
        END IF;
        IF OLD.tombstoned_at IS NOT NULL AND NEW.tombstoned_at IS NULL THEN
            RAISE EXCEPTION 'semantic attribute mapping tombstone is immutable';
        END IF;
        IF OLD.tombstoned_at IS NOT NULL THEN
            RAISE EXCEPTION 'semantic attribute mapping tombstone is immutable';
        END IF;
        IF NEW.mapping_version <> OLD.mapping_version + 1 THEN
            RAISE EXCEPTION 'semantic attribute mapping version must advance exactly once';
        END IF;
        IF OLD.tombstoned_at IS NOT DISTINCT FROM NEW.tombstoned_at THEN
            RAISE EXCEPTION 'semantic attribute mapping update did not change mutable state';
        END IF;
    END IF;
    NEW.updated_at := CASE WHEN TG_OP = 'UPDATE'
        THEN GREATEST(clock_timestamp(), OLD.updated_at + interval '1 microsecond')
        ELSE clock_timestamp() END;
    IF TG_OP = 'UPDATE' AND OLD.tombstoned_at IS NULL AND NEW.tombstoned_at IS NOT NULL THEN
        NEW.tombstoned_at := clock_timestamp();
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION access.reject_semantic_attribute_control_state_rewrite()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, access
AS $$
BEGIN
    IF OLD.singleton <> NEW.singleton OR OLD.profile <> NEW.profile OR
       NEW.control_revision <> OLD.control_revision + 1 OR NEW.control_digest = OLD.control_digest THEN
        RAISE EXCEPTION 'semantic attribute control revision must advance with a new digest';
    END IF;
    NEW.updated_at := GREATEST(clock_timestamp(), OLD.updated_at + interval '1 microsecond');
    RETURN NEW;
END;
$$;

CREATE TRIGGER semantic_attribute_control_state_no_delete
    BEFORE DELETE ON access.semantic_attribute_control_state
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER semantic_attribute_control_state_immutable
    BEFORE UPDATE ON access.semantic_attribute_control_state
    FOR EACH ROW EXECUTE FUNCTION access.reject_semantic_attribute_control_state_rewrite();
CREATE TRIGGER semantic_attribute_assignment_no_delete
    BEFORE DELETE ON access.semantic_attribute_assignment
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER semantic_attribute_assignment_immutable
    BEFORE INSERT OR UPDATE ON access.semantic_attribute_assignment
    FOR EACH ROW EXECUTE FUNCTION access.validate_semantic_attribute_assignment();
CREATE TRIGGER semantic_attribute_claim_mapping_no_delete
    BEFORE DELETE ON access.semantic_attribute_claim_mapping
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER semantic_attribute_claim_mapping_immutable
    BEFORE INSERT OR UPDATE ON access.semantic_attribute_claim_mapping
    FOR EACH ROW EXECUTE FUNCTION access.validate_semantic_attribute_claim_mapping();

REVOKE ALL ON TABLE access.semantic_attribute_control_state,
    access.semantic_attribute_assignment,
    access.semantic_attribute_claim_mapping FROM PUBLIC;
REVOKE ALL ON FUNCTION access.validate_semantic_attribute_owner_exists(),
    access.validate_semantic_attribute_assignment(),
    access.validate_semantic_attribute_claim_mapping(),
    access.reject_semantic_attribute_control_state_rewrite() FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT SELECT, INSERT, UPDATE ON access.semantic_attribute_control_state,
            access.semantic_attribute_assignment,
            access.semantic_attribute_claim_mapping TO leapview_control_runtime;
        REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.semantic_attribute_control_state,
            access.semantic_attribute_assignment,
            access.semantic_attribute_claim_mapping FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT SELECT ON access.semantic_attribute_control_state,
            access.semantic_attribute_assignment,
            access.semantic_attribute_claim_mapping TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON access.semantic_attribute_control_state,
               access.semantic_attribute_assignment,
               access.semantic_attribute_claim_mapping FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT SELECT ON access.semantic_attribute_control_state,
            access.semantic_attribute_assignment,
            access.semantic_attribute_claim_mapping TO leapview_control_backup;
    END IF;
END
$$;
