-- Instance-local authored-resource identity registry (FAI-670). Resource UIDs
-- are allocated only by the activation-owned SECURITY DEFINER function below.
-- The registry is qualified by instance and Project; no global lookup is part
-- of this capability.
CREATE TABLE IF NOT EXISTS project.resource_uid_registry (
    resource_uid             uuid PRIMARY KEY DEFAULT uuidv4(),
    instance_id              text NOT NULL,
    project_id               text NOT NULL,
    authored_resource_id     text NOT NULL,
    resource_kind            text NOT NULL,
    state                    text NOT NULL DEFAULT 'active',
    first_generation_id      uuid NOT NULL,
    latest_generation_id     uuid NOT NULL,
    current_generation_id    uuid,
    removed_in_generation_id uuid,
    created_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (instance_id, project_id, authored_resource_id),
    UNIQUE (instance_id, project_id, resource_uid),
    CHECK (instance_id = btrim(instance_id) AND instance_id ~ '^(lvinst_[A-Za-z0-9_-]{32}|instance_[0-9a-f]{32})$'),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255 AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (authored_resource_id = btrim(authored_resource_id) AND octet_length(authored_resource_id) >= 1 AND authored_resource_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (resource_kind IN ('connection','source','model','semantic_model','pipeline','dashboard')),
    CHECK (state IN ('active','tombstoned')),
    CHECK (first_generation_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    CHECK (latest_generation_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    CHECK ((state = 'active' AND current_generation_id IS NOT NULL AND removed_in_generation_id IS NULL AND current_generation_id = latest_generation_id)
        OR (state = 'tombstoned' AND current_generation_id IS NULL AND removed_in_generation_id IS NOT NULL AND removed_in_generation_id = latest_generation_id)),
    CHECK (updated_at >= created_at)
);

CREATE TABLE IF NOT EXISTS project.resource_uid_generation (
    resource_uid          uuid NOT NULL,
    instance_id           text NOT NULL,
    project_id            text NOT NULL,
    environment           text NOT NULL,
    target_id             text NOT NULL,
    generation_id         uuid NOT NULL,
    authored_resource_id  text NOT NULL,
    resource_kind         text NOT NULL,
    contract_status       text NOT NULL,
    contract_profile      text,
    contract_version      text,
    contract_digest       text,
    contract_bytes        bytea,
    bound_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (instance_id, project_id, generation_id, authored_resource_id),
    UNIQUE (instance_id, project_id, generation_id, resource_uid),
    FOREIGN KEY (instance_id, project_id, resource_uid)
        REFERENCES project.resource_uid_registry(instance_id, project_id, resource_uid) ON DELETE RESTRICT,
    CHECK (instance_id = btrim(instance_id) AND instance_id ~ '^(lvinst_[A-Za-z0-9_-]{32}|instance_[0-9a-f]{32})$'),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255 AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 255 AND environment ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (target_id = instance_id),
    CHECK (generation_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    CHECK (authored_resource_id = btrim(authored_resource_id) AND octet_length(authored_resource_id) >= 1 AND authored_resource_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (resource_kind IN ('connection','source','model','semantic_model','pipeline','dashboard')),
    CHECK (contract_status IN ('canonical','unversioned','not_contract_bearing')),
    CHECK ((contract_profile IS NULL AND contract_version IS NULL AND contract_digest IS NULL AND contract_bytes IS NULL)
        OR (contract_profile IS NOT NULL AND contract_profile = 'leapview.contract/v1'
            AND contract_version IS NOT NULL AND contract_digest IS NOT NULL
            AND contract_digest ~ '^sha256:[0-9a-f]{64}$'
            AND contract_bytes IS NOT NULL AND octet_length(contract_bytes) > 0)),
    CHECK ((contract_status = 'canonical' AND contract_profile IS NOT NULL AND contract_digest IS NOT NULL AND contract_bytes IS NOT NULL)
        OR (contract_status = 'unversioned' AND resource_kind IN ('source','model','semantic_model') AND contract_profile IS NULL AND contract_version IS NULL AND contract_digest IS NULL AND contract_bytes IS NULL)
        OR (contract_status = 'not_contract_bearing' AND resource_kind IN ('connection','pipeline','dashboard') AND contract_profile IS NULL AND contract_version IS NULL AND contract_digest IS NULL AND contract_bytes IS NULL)),
    CHECK (contract_profile IS NULL OR (contract_profile = btrim(contract_profile) AND octet_length(contract_profile) BETWEEN 1 AND 255)),
    CHECK (contract_version IS NULL OR (contract_version = btrim(contract_version) AND octet_length(contract_version) > 0))
);

-- Admission seals one complete, UID-free inventory per generation. This row
-- is immutable and is the only registry data written before activation.
CREATE TABLE IF NOT EXISTS project.resource_uid_inventory (
    generation_id  uuid PRIMARY KEY,
    instance_id    text NOT NULL,
    project_id     text NOT NULL,
    environment    text NOT NULL,
    target_id      text NOT NULL,
    graph_digest   text NOT NULL,
    bundle_digest  text NOT NULL,
    graph_bytes    bytea NOT NULL,
    inventory_json jsonb NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (instance_id, project_id, generation_id),
    CHECK (generation_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    CHECK (instance_id = btrim(instance_id) AND instance_id ~ '^(lvinst_[A-Za-z0-9_-]{32}|instance_[0-9a-f]{32})$'),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255 AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 255 AND environment ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (target_id = instance_id),
    CHECK (graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (bundle_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (octet_length(graph_bytes) > 0),
    CHECK (graph_digest = 'sha256:' || pg_catalog.encode(pg_catalog.sha256(graph_bytes), 'hex')),
    CHECK (jsonb_typeof(inventory_json) = 'array')
);

CREATE OR REPLACE FUNCTION project.reject_resource_uid_inventory_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    RAISE EXCEPTION 'resource UID inventory evidence is immutable';
END;
$$;

DROP TRIGGER IF EXISTS resource_uid_inventory_immutable ON project.resource_uid_inventory;
CREATE TRIGGER resource_uid_inventory_immutable
BEFORE UPDATE OR DELETE ON project.resource_uid_inventory
FOR EACH ROW EXECUTE FUNCTION project.reject_resource_uid_inventory_mutation();

-- Tombstones are append-only generation evidence. They make removal auditable
-- without relying on mutable current-state fields alone.
CREATE TABLE IF NOT EXISTS project.resource_uid_tombstone (
    instance_id              text NOT NULL,
    project_id               text NOT NULL,
    resource_uid             uuid NOT NULL,
    authored_resource_id     text NOT NULL,
    resource_kind            text NOT NULL,
    removed_in_generation_id uuid NOT NULL,
    tombstoned_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (instance_id, project_id, authored_resource_id, removed_in_generation_id),
    UNIQUE (instance_id, project_id, resource_uid, removed_in_generation_id),
    FOREIGN KEY (instance_id, project_id, resource_uid)
        REFERENCES project.resource_uid_registry(instance_id, project_id, resource_uid) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS project.resource_uid_restore_authorization (
    restore_id              uuid PRIMARY KEY DEFAULT uuidv4(),
    resource_uid            uuid NOT NULL,
    instance_id             text NOT NULL,
    project_id              text NOT NULL,
    environment             text NOT NULL,
    target_id               text NOT NULL,
    generation_id           uuid NOT NULL,
    authored_resource_id    text NOT NULL,
    resource_kind           text NOT NULL,
    actor_id                text NOT NULL,
    request_digest          text NOT NULL,
    status                  text NOT NULL DEFAULT 'pending',
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    consumed_at             timestamptz,
    UNIQUE (instance_id, project_id, resource_uid, generation_id),
    FOREIGN KEY (instance_id, project_id, resource_uid)
        REFERENCES project.resource_uid_registry(instance_id, project_id, resource_uid) ON DELETE RESTRICT,
    CHECK (instance_id = btrim(instance_id) AND instance_id ~ '^(lvinst_[A-Za-z0-9_-]{32}|instance_[0-9a-f]{32})$'),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255 AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 255 AND environment ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (target_id = instance_id),
    CHECK (generation_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    CHECK (authored_resource_id = btrim(authored_resource_id) AND octet_length(authored_resource_id) >= 1 AND authored_resource_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (resource_kind IN ('connection','source','model','semantic_model','pipeline','dashboard')),
    CHECK (actor_id = btrim(actor_id) AND octet_length(actor_id) BETWEEN 1 AND 255 AND actor_id !~ '[[:cntrl:]]'),
    CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK ((status = 'pending' AND consumed_at IS NULL) OR (status = 'consumed' AND consumed_at IS NOT NULL))
);

CREATE OR REPLACE FUNCTION project.guard_resource_uid_registry_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.resource_uid IS DISTINCT FROM OLD.resource_uid
       OR NEW.instance_id IS DISTINCT FROM OLD.instance_id OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.authored_resource_id IS DISTINCT FROM OLD.authored_resource_id
       OR NEW.resource_kind IS DISTINCT FROM OLD.resource_kind OR NEW.first_generation_id IS DISTINCT FROM OLD.first_generation_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'resource UID identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION project.reject_resource_uid_history_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    RAISE EXCEPTION 'resource UID generation and tombstone evidence is immutable';
END;
$$;

DROP TRIGGER IF EXISTS resource_uid_registry_identity ON project.resource_uid_registry;
CREATE TRIGGER resource_uid_registry_identity
BEFORE UPDATE OR DELETE ON project.resource_uid_registry
FOR EACH ROW EXECUTE FUNCTION project.guard_resource_uid_registry_mutation();
DROP TRIGGER IF EXISTS resource_uid_generation_immutable ON project.resource_uid_generation;
CREATE TRIGGER resource_uid_generation_immutable
BEFORE UPDATE OR DELETE ON project.resource_uid_generation
FOR EACH ROW EXECUTE FUNCTION project.reject_resource_uid_history_mutation();
DROP TRIGGER IF EXISTS resource_uid_tombstone_immutable ON project.resource_uid_tombstone;
CREATE TRIGGER resource_uid_tombstone_immutable
BEFORE UPDATE OR DELETE ON project.resource_uid_tombstone
FOR EACH ROW EXECUTE FUNCTION project.reject_resource_uid_history_mutation();

CREATE OR REPLACE FUNCTION project.guard_resource_uid_restore_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'resource UID restore evidence is immutable';
    END IF;
    IF NEW.restore_id IS DISTINCT FROM OLD.restore_id OR NEW.resource_uid IS DISTINCT FROM OLD.resource_uid
       OR NEW.instance_id IS DISTINCT FROM OLD.instance_id OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.target_id IS DISTINCT FROM OLD.target_id
       OR NEW.generation_id IS DISTINCT FROM OLD.generation_id OR NEW.authored_resource_id IS DISTINCT FROM OLD.authored_resource_id
       OR NEW.resource_kind IS DISTINCT FROM OLD.resource_kind OR NEW.actor_id IS DISTINCT FROM OLD.actor_id
       OR NEW.request_digest IS DISTINCT FROM OLD.request_digest OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR OLD.status <> 'pending' OR NEW.status <> 'consumed'
       OR NEW.consumed_at IS DISTINCT FROM OLD.consumed_at THEN
        RAISE EXCEPTION 'resource UID restore transition is invalid';
    END IF;
    NEW.consumed_at := clock_timestamp();
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS resource_uid_restore_immutable ON project.resource_uid_restore_authorization;
CREATE TRIGGER resource_uid_restore_immutable
BEFORE UPDATE OR DELETE ON project.resource_uid_restore_authorization
FOR EACH ROW EXECUTE FUNCTION project.guard_resource_uid_restore_mutation();

-- The restore operation is separate from activation and creates durable,
-- actor/request evidence. It never allocates a new UID.
CREATE OR REPLACE FUNCTION project.authorize_resource_uid_restore(
    p_instance_id text,
    p_target_id text,
    p_project_id text,
    p_environment text,
    p_generation_id uuid,
    p_resource_uid uuid,
    p_authored_resource_id text,
    p_resource_kind text,
    p_actor_id text,
    p_request_digest text
) RETURNS SETOF project.resource_uid_restore_authorization
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, project
AS $$
DECLARE
    existing project.resource_uid_registry%ROWTYPE;
    result project.resource_uid_restore_authorization%ROWTYPE;
BEGIN
    IF p_instance_id IS NULL OR p_instance_id <> btrim(p_instance_id) OR p_instance_id !~ '^(lvinst_[A-Za-z0-9_-]{32}|instance_[0-9a-f]{32})$'
       OR p_target_id IS DISTINCT FROM p_instance_id OR p_project_id IS NULL OR p_project_id !~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'
       OR p_environment IS NULL OR p_environment <> btrim(p_environment) OR p_environment !~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'
       OR p_generation_id IS NULL OR p_resource_uid IS NULL OR p_authored_resource_id IS NULL
       OR p_resource_kind IS NULL OR p_resource_kind NOT IN ('connection','source','model','semantic_model','pipeline','dashboard')
       OR p_actor_id IS NULL OR p_actor_id <> btrim(p_actor_id) OR p_actor_id ~ '[[:cntrl:]]'
       OR p_request_digest IS NULL OR p_request_digest !~ '^sha256:[0-9a-f]{64}$' THEN
        RAISE EXCEPTION 'invalid resource UID restore identity';
    END IF;
    -- Authorize an already-admitted, not-yet-bound generation in this exact
    -- claimed scope. Take the target lock before the registry lock, matching
    -- activation's lock order.
    PERFORM 1
      FROM delivery.delivery_target t
      JOIN delivery.delivery_generation g ON g.target_id = t.target_id
      JOIN project.resource_uid_inventory i ON i.generation_id = g.generation_id
       AND i.instance_id = t.target_id AND i.project_id = t.project_id
       AND i.environment = t.environment AND i.target_id = t.target_id
     WHERE t.target_id = p_target_id AND t.project_id = p_project_id
       AND t.environment = p_environment AND g.generation_id = p_generation_id
       AND EXISTS (SELECT 1 FROM platform.instance_identity ii
                    WHERE ii.singleton_id = 1 AND ii.instance_id = p_instance_id)
       AND EXISTS (SELECT 1 FROM platform.instance_project_claim pc
                    WHERE pc.singleton_id = 1 AND pc.project_id = p_project_id
                      AND pc.environment = p_environment)
     FOR UPDATE OF t;
    IF NOT FOUND OR EXISTS (
        SELECT 1 FROM project.resource_uid_generation b
         WHERE b.instance_id = p_instance_id AND b.project_id = p_project_id
           AND b.generation_id = p_generation_id
    ) THEN
        RAISE EXCEPTION 'resource UID restore requires an admitted unbound generation in the claimed scope';
    END IF;
    SELECT * INTO existing
      FROM project.resource_uid_registry
     WHERE instance_id = p_instance_id AND project_id = p_project_id
       AND resource_uid = p_resource_uid AND authored_resource_id = p_authored_resource_id
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'resource UID not found';
    END IF;
    IF existing.resource_kind <> p_resource_kind THEN
        RAISE EXCEPTION 'resource UID kind conflict';
    END IF;
    IF existing.state <> 'tombstoned' THEN
        RAISE EXCEPTION 'resource UID is not tombstoned';
    END IF;
    INSERT INTO project.resource_uid_restore_authorization(
        resource_uid, instance_id, project_id, environment, target_id,
        generation_id, authored_resource_id, resource_kind, actor_id, request_digest
    ) VALUES (
        p_resource_uid, p_instance_id, p_project_id, p_environment, p_target_id,
        p_generation_id, p_authored_resource_id, p_resource_kind, p_actor_id, p_request_digest
    ) ON CONFLICT (instance_id, project_id, resource_uid, generation_id) DO NOTHING;
    SELECT * INTO result
      FROM project.resource_uid_restore_authorization
     WHERE instance_id = p_instance_id AND project_id = p_project_id
       AND resource_uid = p_resource_uid AND generation_id = p_generation_id
     FOR UPDATE;
    IF result.actor_id <> p_actor_id OR result.request_digest <> p_request_digest
       OR result.authored_resource_id <> p_authored_resource_id OR result.resource_kind <> p_resource_kind
       OR result.environment <> p_environment OR result.target_id <> p_target_id THEN
        RAISE EXCEPTION 'resource UID restore authorization conflict';
    END IF;
    RETURN NEXT result;
END;
$$;

-- Admission is a proof-carrying write. It accepts only a graph and inventory
-- already attached to the exact delivery generation and serving bundle. The
-- runtime role may execute this narrow definer, but has no registry table
-- INSERT privilege and cannot retarget a row or alter the bundle evidence.
CREATE OR REPLACE FUNCTION project.admit_resource_uid_inventory(
    p_target_id text,
    p_project_id text,
    p_environment text,
    p_generation_id uuid,
    p_graph_digest text,
    p_bundle_digest text,
    p_graph_bytes bytea,
    p_inventory_json jsonb
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, project
AS $$
DECLARE
    target_row record;
    existing project.resource_uid_inventory%ROWTYPE;
BEGIN
    IF p_target_id IS NULL OR p_target_id <> btrim(p_target_id)
       OR p_target_id !~ '^(lvinst_[A-Za-z0-9_-]{32}|instance_[0-9a-f]{32})$'
       OR p_project_id IS NULL OR p_project_id <> btrim(p_project_id)
       OR p_project_id !~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'
       OR p_environment IS NULL OR p_environment <> btrim(p_environment)
       OR p_environment !~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'
       OR p_generation_id IS NULL
       OR p_graph_digest IS NULL OR p_graph_digest !~ '^sha256:[0-9a-f]{64}$'
       OR p_bundle_digest IS NULL OR p_bundle_digest !~ '^sha256:[0-9a-f]{64}$'
       OR p_graph_bytes IS NULL OR octet_length(p_graph_bytes) = 0
       OR p_inventory_json IS NULL OR jsonb_typeof(p_inventory_json) <> 'array' THEN
        RAISE EXCEPTION 'invalid resource UID inventory proof';
    END IF;
    IF p_graph_digest IS DISTINCT FROM ('sha256:' || pg_catalog.encode(pg_catalog.sha256(p_graph_bytes), 'hex')) THEN
        RAISE EXCEPTION 'resource UID graph bytes digest differs';
    END IF;

    -- This join is the admission boundary: a generation without an admitted
    -- serving bundle, or a bundle from another target/scope, cannot acquire
    -- registry evidence. Locking the target serializes empty inventories too.
    SELECT t.target_id, t.project_id, t.environment,
           g.compiled_graph_digest AS generation_graph_digest,
           b.compiled_graph_digest AS bundle_graph_digest,
           b.project_digest AS serving_project_digest
      INTO target_row
      FROM delivery.delivery_generation g
      JOIN delivery.delivery_target t ON t.target_id = g.target_id
      JOIN serving_state.bundle b ON b.generation_id = g.generation_id
       AND b.project_id = t.project_id AND b.environment = t.environment
     WHERE g.generation_id = p_generation_id
       AND t.target_id = p_target_id AND t.project_id = p_project_id
       AND t.environment = p_environment
     FOR UPDATE OF t;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'resource UID generation serving bundle proof is missing';
    END IF;
    IF target_row.generation_graph_digest IS DISTINCT FROM p_graph_digest
       OR target_row.bundle_graph_digest IS DISTINCT FROM p_graph_digest
       OR target_row.serving_project_digest IS DISTINCT FROM p_bundle_digest THEN
        RAISE EXCEPTION 'resource UID inventory lineage proof differs';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM platform.instance_identity ii
         WHERE ii.singleton_id = 1 AND ii.instance_id = p_target_id
    ) OR NOT EXISTS (
        SELECT 1 FROM platform.instance_project_claim pc
         WHERE pc.singleton_id = 1 AND pc.project_id = p_project_id
           AND pc.environment = p_environment
    ) THEN
        RAISE EXCEPTION 'resource UID inventory target claim is missing';
    END IF;
    IF EXISTS (
        SELECT 1 FROM jsonb_array_elements(p_inventory_json) AS item(value)
         WHERE jsonb_typeof(item.value) <> 'object'
    ) OR EXISTS (
        SELECT 1
          FROM jsonb_array_elements(p_inventory_json) AS item(value)
         GROUP BY item.value->>'authored_id'
        HAVING item.value->>'authored_id' IS NULL OR count(*) <> 1
    ) THEN
        RAISE EXCEPTION 'resource UID inventory is not a complete unique object set';
    END IF;
    IF jsonb_typeof((convert_from(p_graph_bytes, 'UTF8'))::jsonb) <> 'object'
       OR ((convert_from(p_graph_bytes, 'UTF8'))::jsonb)->>'version' IS DISTINCT FROM '2'
       OR jsonb_typeof(((convert_from(p_graph_bytes, 'UTF8'))::jsonb)->'resources') <> 'array' THEN
        RAISE EXCEPTION 'resource UID graph proof is invalid';
    END IF;
    IF (SELECT count(*) FROM jsonb_array_elements(((convert_from(p_graph_bytes, 'UTF8'))::jsonb)->'resources'))
       <> jsonb_array_length(p_inventory_json) THEN
        RAISE EXCEPTION 'resource UID inventory is not graph-complete';
    END IF;
    IF EXISTS (
        SELECT 1
          FROM jsonb_array_elements(((convert_from(p_graph_bytes, 'UTF8'))::jsonb)->'resources') AS graph_item(value)
         FULL OUTER JOIN jsonb_array_elements(p_inventory_json) AS inventory_item(value)
           ON graph_item.value->>'id' = inventory_item.value->>'authored_id'
          AND graph_item.value->>'kind' = inventory_item.value->>'kind'
         WHERE graph_item.value IS NULL OR inventory_item.value IS NULL
    ) THEN
        RAISE EXCEPTION 'resource UID inventory identity set differs from graph';
    END IF;

    INSERT INTO project.resource_uid_inventory(
        generation_id, instance_id, project_id, environment, target_id,
        graph_digest, bundle_digest, graph_bytes, inventory_json
    ) VALUES (
        p_generation_id, p_target_id, p_project_id, p_environment, p_target_id,
        p_graph_digest, p_bundle_digest, p_graph_bytes, p_inventory_json
    ) ON CONFLICT (generation_id) DO NOTHING;
    SELECT * INTO existing FROM project.resource_uid_inventory
     WHERE generation_id = p_generation_id FOR UPDATE;
    IF existing.instance_id IS DISTINCT FROM p_target_id
       OR existing.project_id IS DISTINCT FROM p_project_id
       OR existing.environment IS DISTINCT FROM p_environment
       OR existing.target_id IS DISTINCT FROM p_target_id
       OR existing.graph_digest IS DISTINCT FROM p_graph_digest
       OR existing.bundle_digest IS DISTINCT FROM p_bundle_digest
       OR existing.graph_bytes IS DISTINCT FROM p_graph_bytes
       OR existing.inventory_json IS DISTINCT FROM p_inventory_json THEN
        RAISE EXCEPTION 'resource UID inventory replay conflict';
    END IF;
    RETURN true;
END;
$$;

-- Allocation is private to the delivery activation capability. The trigger
-- invokes this function while delivery.commit_activation_transition is a
-- SECURITY DEFINER owner transaction; no caller can supply a foreign scope
-- or an arbitrary inventory to allocate identities.
CREATE OR REPLACE FUNCTION project.bind_resource_uid_generation(p_generation_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, project
AS $$
<<binding>>
DECLARE
    scope record;
    entry jsonb;
    existing project.resource_uid_registry%ROWTYPE;
    prior_binding project.resource_uid_generation%ROWTYPE;
    restore_row project.resource_uid_restore_authorization%ROWTYPE;
    authored_id text;
    resource_kind text;
    contract_status text;
    contract_profile text;
    contract_version text;
    contract_digest text;
    contract_bytes bytea;
    contract_json jsonb;
    expected_contract_kind text;
    has_contract boolean;
    prior_binding_found boolean;
BEGIN
    IF p_generation_id IS NULL OR p_generation_id = '00000000-0000-0000-0000-000000000000'::uuid THEN
        RAISE EXCEPTION 'invalid resource UID generation';
    END IF;
    SELECT t.target_id, t.project_id, t.environment, g.compiled_graph_digest,
           i.inventory_json, i.graph_digest, i.bundle_digest,
           b.compiled_graph_digest AS serving_graph_digest,
           b.project_digest AS serving_project_digest
      INTO scope
      FROM delivery.delivery_generation g
      JOIN delivery.delivery_target t ON t.target_id = g.target_id
      JOIN delivery.delivery_active_pointer ap
        ON ap.target_id = t.target_id AND ap.generation_id = g.generation_id
      JOIN delivery.delivery_publication pub
        ON pub.publication_id = ap.publication_id
       AND pub.generation_id = g.generation_id
       AND pub.target_id = t.target_id
       AND pub.state = 'committed'
      JOIN serving_state.bundle b ON b.generation_id = g.generation_id
       AND b.project_id = t.project_id AND b.environment = t.environment
      JOIN project.resource_uid_inventory i
        ON i.generation_id = g.generation_id
       AND i.instance_id = t.target_id
       AND i.project_id = t.project_id
       AND i.environment = t.environment
       AND i.target_id = t.target_id
     WHERE g.generation_id = p_generation_id
     FOR UPDATE OF t;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'resource UID generation or inventory proof is missing';
    END IF;
    IF scope.target_id !~ '^(lvinst_[A-Za-z0-9_-]{32}|instance_[0-9a-f]{32})$'
       OR scope.graph_digest IS DISTINCT FROM scope.compiled_graph_digest
       OR scope.serving_graph_digest IS DISTINCT FROM scope.compiled_graph_digest
       OR scope.bundle_digest IS DISTINCT FROM scope.serving_project_digest THEN
        RAISE EXCEPTION 'resource UID generation scope or graph proof differs';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM platform.instance_identity ii
        WHERE ii.singleton_id = 1 AND ii.instance_id = scope.target_id
    ) OR NOT EXISTS (
        SELECT 1 FROM platform.instance_project_claim pc
        WHERE pc.singleton_id = 1 AND pc.project_id = scope.project_id AND pc.environment = scope.environment
    ) THEN
        RAISE EXCEPTION 'resource UID generation target claim is missing';
    END IF;
    -- Explicitly lock the target even for an empty inventory so concurrent
    -- removal/reconciliation passes serialize on the same scope fence.
    PERFORM 1 FROM delivery.delivery_target t
     WHERE t.target_id = scope.target_id AND t.project_id = scope.project_id AND t.environment = scope.environment
     FOR UPDATE;

    IF EXISTS (
        SELECT 1
          FROM jsonb_array_elements(scope.inventory_json) AS item(value)
         GROUP BY item.value->>'authored_id'
        HAVING item.value->>'authored_id' IS NULL OR count(*) <> 1
    ) THEN
        RAISE EXCEPTION 'resource UID inventory contains duplicate or missing authored IDs';
    END IF;

    FOR entry IN SELECT value FROM jsonb_array_elements(scope.inventory_json) LOOP
        restore_row := NULL;
        IF jsonb_typeof(entry) <> 'object' THEN
            RAISE EXCEPTION 'resource UID inventory entry is not an object';
        END IF;
        IF EXISTS (
            SELECT 1
              FROM jsonb_object_keys(entry) AS key(name)
             WHERE key.name NOT IN ('authored_id', 'kind', 'contract_status',
                                    'contract_profile', 'contract_version',
                                    'canonical_contract', 'contract_digest')
        ) THEN
            RAISE EXCEPTION 'resource UID inventory entry contains unsupported fields';
        END IF;
        authored_id := entry->>'authored_id';
        resource_kind := entry->>'kind';
        contract_status := entry->>'contract_status';
        contract_profile := NULLIF(entry->>'contract_profile', '');
        contract_digest := NULLIF(entry->>'contract_digest', '');
        contract_version := NULLIF(entry->>'contract_version', '');
        contract_bytes := NULL;
        contract_json := NULL;
        IF entry ? 'canonical_contract' AND entry->'canonical_contract' <> 'null'::jsonb THEN
            IF jsonb_typeof(entry->'canonical_contract') <> 'string' THEN
                RAISE EXCEPTION 'resource UID canonical contract must retain exact JSON bytes as a string';
            END IF;
            contract_bytes := convert_to(entry->>'canonical_contract', 'UTF8');
            contract_json := (entry->>'canonical_contract')::jsonb;
        END IF;
        has_contract := contract_profile IS NOT NULL OR contract_digest IS NOT NULL OR contract_version IS NOT NULL OR contract_bytes IS NOT NULL;
        IF authored_id IS NULL OR authored_id <> btrim(authored_id) OR authored_id !~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'
           OR resource_kind IS NULL OR resource_kind NOT IN ('connection','source','model','semantic_model','pipeline','dashboard')
           OR contract_status IS NULL OR contract_status NOT IN ('canonical','unversioned','not_contract_bearing') THEN
            RAISE EXCEPTION 'invalid resource UID inventory entry';
        END IF;
        IF contract_status = 'canonical' THEN
            expected_contract_kind := CASE resource_kind
                WHEN 'source' THEN 'Source'
                WHEN 'model' THEN 'Model'
                WHEN 'semantic_model' THEN 'SemanticModel'
                ELSE NULL
            END;
            IF expected_contract_kind IS NULL OR contract_profile IS DISTINCT FROM 'leapview.contract/v1'
               OR contract_version IS NULL OR contract_digest IS NULL OR contract_bytes IS NULL
               OR contract_digest !~ '^sha256:[0-9a-f]{64}$'
               OR jsonb_typeof(contract_json) <> 'object'
               OR contract_json->>'profile' IS DISTINCT FROM contract_profile
               OR contract_json->>'apiVersion' IS DISTINCT FROM 'leapview.dev/v1'
               OR contract_json->>'kind' IS DISTINCT FROM expected_contract_kind
               OR contract_json->'metadata'->>'id' IS DISTINCT FROM authored_id
               OR contract_json->'metadata'->'contract'->>'version' IS DISTINCT FROM contract_version
               OR contract_digest IS DISTINCT FROM ('sha256:' || pg_catalog.encode(pg_catalog.sha256(contract_bytes), 'hex')) THEN
                RAISE EXCEPTION 'resource UID canonical contract evidence is incomplete';
            END IF;
        ELSIF contract_status = 'unversioned' THEN
            IF resource_kind NOT IN ('source','model','semantic_model') OR has_contract THEN
                RAISE EXCEPTION 'resource UID unversioned status is invalid';
            END IF;
        ELSIF contract_status = 'not_contract_bearing' THEN
            IF resource_kind NOT IN ('connection','pipeline','dashboard') OR has_contract THEN
                RAISE EXCEPTION 'resource UID non-contract-bearing status is invalid';
            END IF;
        END IF;
        SELECT * INTO existing
          FROM project.resource_uid_registry r
         WHERE r.instance_id = scope.target_id AND r.project_id = scope.project_id
           AND r.authored_resource_id = authored_id
         FOR UPDATE;
        IF NOT FOUND THEN
            INSERT INTO project.resource_uid_registry(
                instance_id, project_id, authored_resource_id, resource_kind,
                first_generation_id, latest_generation_id, current_generation_id
            ) VALUES (scope.target_id, scope.project_id, authored_id, resource_kind,
                      p_generation_id, p_generation_id, p_generation_id)
            RETURNING * INTO existing;
        ELSIF existing.resource_kind <> resource_kind THEN
            RAISE EXCEPTION 'resource UID kind conflict for %', authored_id;
        END IF;

        SELECT * INTO prior_binding
          FROM project.resource_uid_generation b
         WHERE b.instance_id = scope.target_id AND b.project_id = scope.project_id
           AND b.generation_id = p_generation_id AND b.authored_resource_id = authored_id
         FOR UPDATE;
        prior_binding_found := FOUND;
        IF prior_binding_found THEN
            IF prior_binding.resource_uid <> existing.resource_uid OR prior_binding.resource_kind <> resource_kind
               OR prior_binding.environment <> scope.environment OR prior_binding.target_id <> scope.target_id
               OR prior_binding.contract_status IS DISTINCT FROM contract_status
               OR prior_binding.contract_profile IS DISTINCT FROM contract_profile
               OR prior_binding.contract_version IS DISTINCT FROM contract_version
               OR prior_binding.contract_digest IS DISTINCT FROM contract_digest
               OR prior_binding.contract_bytes IS DISTINCT FROM contract_bytes THEN
                RAISE EXCEPTION 'resource UID generation binding conflict for %', authored_id;
            END IF;
        END IF;

        IF existing.state = 'tombstoned' THEN
            -- Re-activating an exact historical generation is rollback-safe:
            -- its immutable binding is already the authority and does not
            -- need a new restore request. A new generation requires explicit
            -- audited authorization for this exact UID and generation.
            IF NOT prior_binding_found THEN
                SELECT ra.* INTO restore_row
                  FROM project.resource_uid_restore_authorization ra
                 WHERE ra.instance_id = scope.target_id AND ra.project_id = scope.project_id
                   AND ra.target_id = scope.target_id AND ra.environment = scope.environment
                   AND ra.generation_id = p_generation_id AND ra.resource_uid = existing.resource_uid
                   AND ra.authored_resource_id = authored_id AND ra.resource_kind = binding.resource_kind
                   AND ra.status = 'pending'
                 FOR UPDATE;
                IF NOT FOUND THEN
                    RAISE EXCEPTION 'resource UID restore authorization required for %', authored_id;
                END IF;
            END IF;
            UPDATE project.resource_uid_registry
               SET state = 'active', latest_generation_id = p_generation_id,
                   current_generation_id = p_generation_id, removed_in_generation_id = NULL,
                   updated_at = clock_timestamp()
             WHERE resource_uid = existing.resource_uid;
            IF restore_row.restore_id IS NOT NULL THEN
                UPDATE project.resource_uid_restore_authorization
                   SET status = 'consumed'
                 WHERE restore_id = restore_row.restore_id;
            END IF;
        ELSIF existing.latest_generation_id IS DISTINCT FROM p_generation_id
           OR existing.current_generation_id IS DISTINCT FROM p_generation_id THEN
            UPDATE project.resource_uid_registry
               SET latest_generation_id = p_generation_id,
                   current_generation_id = p_generation_id,
                   updated_at = clock_timestamp()
             WHERE resource_uid = existing.resource_uid;
        END IF;

        IF NOT prior_binding_found THEN
            INSERT INTO project.resource_uid_generation(
                resource_uid, instance_id, project_id, environment, target_id,
                generation_id, authored_resource_id, resource_kind, contract_status,
                contract_profile, contract_version, contract_digest, contract_bytes
            ) VALUES (
                existing.resource_uid, scope.target_id, scope.project_id, scope.environment, scope.target_id,
                p_generation_id, authored_id, resource_kind, contract_status,
                contract_profile, contract_version, contract_digest, contract_bytes
            );
        END IF;
    END LOOP;

    FOR existing IN
        SELECT * FROM project.resource_uid_registry r
         WHERE r.instance_id = scope.target_id AND r.project_id = scope.project_id AND r.state = 'active'
         FOR UPDATE
    LOOP
        IF existing.current_generation_id IS DISTINCT FROM p_generation_id
           AND NOT EXISTS (
               SELECT 1 FROM jsonb_array_elements(scope.inventory_json) AS item(value)
               WHERE item.value->>'authored_id' = existing.authored_resource_id
           ) THEN
            UPDATE project.resource_uid_registry
               SET state = 'tombstoned', latest_generation_id = p_generation_id,
                   current_generation_id = NULL, removed_in_generation_id = p_generation_id,
                   updated_at = clock_timestamp()
             WHERE resource_uid = existing.resource_uid;
            INSERT INTO project.resource_uid_tombstone(
                instance_id, project_id, resource_uid, authored_resource_id, resource_kind, removed_in_generation_id
            ) VALUES (scope.target_id, scope.project_id, existing.resource_uid, existing.authored_resource_id, existing.resource_kind, p_generation_id)
            ON CONFLICT DO NOTHING;
        END IF;
    END LOOP;
    RETURN true;
END;
$$;

-- The only allocation trigger is attached to the authoritative publication
-- commit. delivery.commit_activation_transition is SECURITY DEFINER and
-- performs this update in the same caller-owned transaction as the pointer
-- CAS; a failed bind therefore rolls back the activation and leaks no UID.
CREATE OR REPLACE FUNCTION project.bind_resource_uid_on_publication_commit()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, project
AS $$
BEGIN
    IF NEW.state = 'committed' AND OLD.state IS DISTINCT FROM NEW.state THEN
        PERFORM project.bind_resource_uid_generation(NEW.generation_id);
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS resource_uid_bind_on_publication_commit ON delivery.delivery_publication;
CREATE TRIGGER resource_uid_bind_on_publication_commit
AFTER UPDATE OF state ON delivery.delivery_publication
FOR EACH ROW
WHEN (NEW.state = 'committed' AND OLD.state IS DISTINCT FROM NEW.state)
EXECUTE FUNCTION project.bind_resource_uid_on_publication_commit();

REVOKE ALL ON TABLE project.resource_uid_registry, project.resource_uid_generation,
    project.resource_uid_inventory, project.resource_uid_tombstone,
    project.resource_uid_restore_authorization FROM PUBLIC;
REVOKE ALL ON FUNCTION project.admit_resource_uid_inventory(text,text,text,uuid,text,text,bytea,jsonb),
    project.bind_resource_uid_generation(uuid),
    project.bind_resource_uid_on_publication_commit(),
    project.authorize_resource_uid_restore(text,text,text,text,uuid,uuid,text,text,text,text),
    project.guard_resource_uid_restore_mutation(),
    project.reject_resource_uid_inventory_mutation() FROM PUBLIC;
DO $$
DECLARE role_name text;
BEGIN
    FOREACH role_name IN ARRAY ARRAY['leapview_control_owner','leapview_control_migrator','leapview_control_runtime','leapview_control_readonly','leapview_control_backup'] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
            EXECUTE format('GRANT USAGE ON SCHEMA project TO %I', role_name);
            IF role_name IN ('leapview_control_owner','leapview_control_migrator') THEN
                EXECUTE format('GRANT ALL ON project.resource_uid_registry, project.resource_uid_generation, project.resource_uid_inventory, project.resource_uid_tombstone, project.resource_uid_restore_authorization TO %I', role_name);
                -- The binder is reached only by the delivery activation
                -- trigger. Do not grant it as a callable repository API.
                EXECUTE format('GRANT EXECUTE ON FUNCTION project.admit_resource_uid_inventory(text,text,text,uuid,text,text,bytea,jsonb), project.authorize_resource_uid_restore(text,text,text,text,uuid,uuid,text,text,text,text) TO %I', role_name);
            ELSIF role_name = 'leapview_control_runtime' THEN
                EXECUTE 'GRANT EXECUTE ON FUNCTION project.admit_resource_uid_inventory(text,text,text,uuid,text,text,bytea,jsonb) TO leapview_control_runtime';
                EXECUTE 'GRANT SELECT ON project.resource_uid_registry, project.resource_uid_generation, project.resource_uid_tombstone, project.resource_uid_restore_authorization TO leapview_control_runtime';
            ELSE
                EXECUTE format('GRANT SELECT ON project.resource_uid_registry, project.resource_uid_generation, project.resource_uid_inventory, project.resource_uid_tombstone, project.resource_uid_restore_authorization TO %I', role_name);
            END IF;
        END IF;
    END LOOP;
END
$$;
