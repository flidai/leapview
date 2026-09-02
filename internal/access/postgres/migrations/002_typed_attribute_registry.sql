-- FAI-636: typed semantic-access attribute registry.
--
-- This is a forward-only access-capability migration. It must not be folded
-- into schema.sql because revision 1 is already an immutable, recorded
-- baseline. Principal assignments and trusted claim mappings deliberately
-- remain outside this registry migration.

CREATE TABLE access.semantic_attribute_registry (
    singleton          boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    profile            text NOT NULL CHECK (profile = 'leapview.semantic-access/v1'),
    registry_revision  bigint NOT NULL DEFAULT 0 CHECK (registry_revision >= 0),
    registry_digest    text NOT NULL CHECK (registry_digest ~ '^sha256:[0-9a-f]{64}$'),
    updated_at         timestamptz NOT NULL DEFAULT clock_timestamp()
);

INSERT INTO access.semantic_attribute_registry
    (singleton, profile, registry_revision, registry_digest)
VALUES
    (true, 'leapview.semantic-access/v1', 0,
     'sha256:9362dbdb62923a10f67bc1da04b02e2bbad74dce5b5442aaa3fb5e0cc5851b9d')
ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE access.semantic_attribute_definition (
    definition_id      uuid PRIMARY KEY DEFAULT uuidv7(),
    name               text NOT NULL UNIQUE
                       CHECK (name ~ '^[A-Za-z_][A-Za-z0-9_]*$'),
    value_type         text NOT NULL
                       CHECK (value_type IN ('String','Boolean','Integer','Decimal','Date','Timestamp')),
    value_shape        text NOT NULL CHECK (value_shape IN ('scalar','list')),
    profile            text NOT NULL CHECK (profile = 'leapview.semantic-access/v1'),
    definition_version bigint NOT NULL DEFAULT 1 CHECK (definition_version > 0),
    owner_kind         text NOT NULL DEFAULT 'instance'
                       CHECK (owner_kind IN ('instance','principal','group')),
    owner_id           uuid,
    display_name       text NOT NULL DEFAULT '' CHECK (length(display_name) <= 255),
    description        text NOT NULL DEFAULT '' CHECK (length(description) <= 4096),
    documentation_url  text NOT NULL DEFAULT '' CHECK (length(documentation_url) <= 2048),
    enabled            boolean NOT NULL DEFAULT true,
    disabled_at        timestamptz,
    created_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((owner_kind = 'instance' AND owner_id IS NULL) OR
           (owner_kind <> 'instance' AND owner_id IS NOT NULL)),
    CHECK ((enabled AND disabled_at IS NULL) OR (NOT enabled AND disabled_at IS NOT NULL))
);
CREATE INDEX semantic_attribute_definition_owner_idx
    ON access.semantic_attribute_definition(owner_kind, owner_id, name);

CREATE OR REPLACE FUNCTION access.reject_semantic_attribute_registry_rewrite()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, access
AS $$
BEGIN
    IF OLD.singleton <> NEW.singleton OR OLD.profile <> NEW.profile THEN
        RAISE EXCEPTION 'semantic attribute registry identity is immutable';
    END IF;
    IF NEW.registry_revision <> OLD.registry_revision + 1 OR
       NEW.registry_digest = OLD.registry_digest THEN
        RAISE EXCEPTION 'semantic attribute registry revision must advance with a new digest';
    END IF;
    NEW.updated_at := GREATEST(clock_timestamp(), OLD.updated_at + interval '1 microsecond');
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION access.reject_semantic_attribute_definition_rewrite()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, access
AS $$
BEGIN
    IF OLD.definition_id <> NEW.definition_id OR
       OLD.name <> NEW.name OR
       OLD.value_type <> NEW.value_type OR
       OLD.value_shape <> NEW.value_shape OR
       OLD.profile <> NEW.profile OR
       OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'semantic attribute identity and type are immutable';
    END IF;
    IF NEW.definition_version <> OLD.definition_version + 1 THEN
        RAISE EXCEPTION 'semantic attribute definition version must advance exactly once';
    END IF;
    IF OLD.owner_kind = NEW.owner_kind AND
       OLD.owner_id IS NOT DISTINCT FROM NEW.owner_id AND
       OLD.display_name = NEW.display_name AND
       OLD.description = NEW.description AND
       OLD.documentation_url = NEW.documentation_url AND
       OLD.enabled = NEW.enabled AND
       OLD.disabled_at IS NOT DISTINCT FROM NEW.disabled_at THEN
        RAISE EXCEPTION 'semantic attribute update did not change mutable state';
    END IF;
    IF OLD.enabled = NEW.enabled AND OLD.disabled_at IS DISTINCT FROM NEW.disabled_at THEN
        RAISE EXCEPTION 'semantic attribute disable timestamp is database-owned';
    END IF;
    IF OLD.enabled <> NEW.enabled THEN
        NEW.disabled_at := CASE WHEN NEW.enabled THEN NULL ELSE clock_timestamp() END;
    END IF;
    NEW.updated_at := GREATEST(clock_timestamp(), OLD.updated_at + interval '1 microsecond');
    RETURN NEW;
END;
$$;

CREATE TRIGGER semantic_attribute_registry_no_delete
    BEFORE DELETE ON access.semantic_attribute_registry
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER semantic_attribute_registry_immutable
    BEFORE UPDATE ON access.semantic_attribute_registry
    FOR EACH ROW EXECUTE FUNCTION access.reject_semantic_attribute_registry_rewrite();
CREATE TRIGGER semantic_attribute_definition_no_delete
    BEFORE DELETE ON access.semantic_attribute_definition
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER semantic_attribute_definition_immutable
    BEFORE UPDATE ON access.semantic_attribute_definition
    FOR EACH ROW EXECUTE FUNCTION access.reject_semantic_attribute_definition_rewrite();

REVOKE ALL ON TABLE access.semantic_attribute_registry,
    access.semantic_attribute_definition FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_semantic_attribute_registry_rewrite(),
    access.reject_semantic_attribute_definition_rewrite() FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT SELECT, INSERT, UPDATE ON access.semantic_attribute_registry,
            access.semantic_attribute_definition TO leapview_control_runtime;
        REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.semantic_attribute_registry,
            access.semantic_attribute_definition FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT SELECT ON access.semantic_attribute_registry,
            access.semantic_attribute_definition TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON access.semantic_attribute_registry,
               access.semantic_attribute_definition FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT SELECT ON access.semantic_attribute_registry,
            access.semantic_attribute_definition TO leapview_control_backup;
    END IF;
END
$$;
