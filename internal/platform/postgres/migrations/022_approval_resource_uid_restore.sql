-- +goose Up
-- +goose StatementBegin
SET LOCAL ROLE leapview_control_owner;

-- Approval-bound restore evidence records the exact decision that authorized
-- a tombstoned identity. A later approval supersedes pending evidence while
-- preserving the earlier row as immutable audit history.
ALTER TABLE project.resource_uid_restore_authorization
    ADD COLUMN IF NOT EXISTS approval_request_id uuid,
    ADD COLUMN IF NOT EXISTS approval_decision_id uuid,
    ADD COLUMN IF NOT EXISTS approval_decision_revision bigint,
    ADD COLUMN IF NOT EXISTS superseded_at timestamptz;

DO $$
DECLARE constraint_name text;
BEGIN
    FOR constraint_name IN
        SELECT conname
          FROM pg_constraint
         WHERE conrelid = 'project.resource_uid_restore_authorization'::regclass
           AND contype = 'u'
           AND pg_get_constraintdef(oid) = 'UNIQUE (instance_id, project_id, resource_uid, generation_id)'
    LOOP
        EXECUTE format('ALTER TABLE project.resource_uid_restore_authorization DROP CONSTRAINT %I', constraint_name);
    END LOOP;
    FOR constraint_name IN
        SELECT conname
          FROM pg_constraint
         WHERE conrelid = 'project.resource_uid_restore_authorization'::regclass
           AND contype = 'c'
           AND pg_get_constraintdef(oid) LIKE '%status%consumed_at%'
    LOOP
        EXECUTE format('ALTER TABLE project.resource_uid_restore_authorization DROP CONSTRAINT %I', constraint_name);
    END LOOP;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'resource_uid_restore_approval_binding_check'
                   AND conrelid = 'project.resource_uid_restore_authorization'::regclass) THEN
        ALTER TABLE project.resource_uid_restore_authorization
            ADD CONSTRAINT resource_uid_restore_approval_binding_check CHECK (
                (approval_request_id IS NULL AND approval_decision_id IS NULL AND approval_decision_revision IS NULL)
                OR (approval_request_id IS NOT NULL AND approval_decision_id IS NOT NULL AND approval_decision_revision > 0)
            );
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'resource_uid_restore_status_check'
                   AND conrelid = 'project.resource_uid_restore_authorization'::regclass) THEN
        ALTER TABLE project.resource_uid_restore_authorization
            ADD CONSTRAINT resource_uid_restore_status_check CHECK (
                (status = 'pending' AND consumed_at IS NULL AND superseded_at IS NULL)
                OR (status = 'consumed' AND consumed_at IS NOT NULL AND superseded_at IS NULL)
                OR (status = 'superseded' AND consumed_at IS NULL AND superseded_at IS NOT NULL)
            );
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'resource_uid_restore_approval_request_fk'
                   AND conrelid = 'project.resource_uid_restore_authorization'::regclass) THEN
        ALTER TABLE project.resource_uid_restore_authorization
            ADD CONSTRAINT resource_uid_restore_approval_request_fk
            FOREIGN KEY (approval_request_id) REFERENCES delivery.delivery_approval_request(request_id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'resource_uid_restore_approval_decision_fk'
                   AND conrelid = 'project.resource_uid_restore_authorization'::regclass) THEN
        ALTER TABLE project.resource_uid_restore_authorization
            ADD CONSTRAINT resource_uid_restore_approval_decision_fk
            FOREIGN KEY (approval_decision_id) REFERENCES delivery.delivery_approval_decision(decision_id) ON DELETE RESTRICT;
    END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS resource_uid_restore_pending_scope_uidx
    ON project.resource_uid_restore_authorization(instance_id, project_id, resource_uid, generation_id)
    WHERE status = 'pending';
CREATE UNIQUE INDEX IF NOT EXISTS resource_uid_restore_approval_decision_uidx
    ON project.resource_uid_restore_authorization(instance_id, project_id, resource_uid, generation_id, approval_decision_id)
    WHERE approval_decision_id IS NOT NULL;

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
       OR NEW.approval_request_id IS DISTINCT FROM OLD.approval_request_id
       OR NEW.approval_decision_id IS DISTINCT FROM OLD.approval_decision_id
       OR NEW.approval_decision_revision IS DISTINCT FROM OLD.approval_decision_revision
       OR OLD.status <> 'pending'
       OR NEW.consumed_at IS DISTINCT FROM OLD.consumed_at
       OR NEW.superseded_at IS DISTINCT FROM OLD.superseded_at THEN
        RAISE EXCEPTION 'resource UID restore transition is invalid';
    END IF;
    IF NEW.status = 'consumed' THEN
        NEW.consumed_at := clock_timestamp();
    ELSIF NEW.status = 'superseded' THEN
        NEW.superseded_at := clock_timestamp();
    ELSE
        RAISE EXCEPTION 'resource UID restore transition is invalid';
    END IF;
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
    ) ON CONFLICT (instance_id, project_id, resource_uid, generation_id)
        WHERE status = 'pending' DO NOTHING;
    SELECT * INTO result
      FROM project.resource_uid_restore_authorization
     WHERE instance_id = p_instance_id AND project_id = p_project_id
       AND resource_uid = p_resource_uid AND generation_id = p_generation_id
       AND status = 'pending'
     FOR UPDATE;
    IF result.actor_id <> p_actor_id OR result.request_digest <> p_request_digest
       OR result.authored_resource_id <> p_authored_resource_id OR result.resource_kind <> p_resource_kind
       OR result.environment <> p_environment OR result.target_id <> p_target_id
       OR result.approval_decision_id IS NOT NULL THEN
        RAISE EXCEPTION 'resource UID restore authorization conflict';
    END IF;
    RETURN NEXT result;
END;
$$;

-- Approval is the explicit, audited authority for restoring resource identities
-- into a new generation. This capability can only translate one exact latest
-- approval decision; it cannot manufacture a restore for an arbitrary scope.
CREATE OR REPLACE FUNCTION project.authorize_resource_uid_restores_for_approval(
    p_request_id uuid,
    p_decision_id uuid
) RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, project, delivery
AS $$
DECLARE
    scope record;
    entry jsonb;
    existing project.resource_uid_registry%ROWTYPE;
    result project.resource_uid_restore_authorization%ROWTYPE;
    authorized_count integer := 0;
BEGIN
    IF p_request_id IS NULL OR p_decision_id IS NULL THEN
        RAISE EXCEPTION 'invalid resource UID approval restore identity';
    END IF;

    SELECT r.target_id, r.generation_id, r.request_digest,
           d.decided_by, d.decision_revision, i.instance_id, i.project_id, i.environment,
           i.inventory_json
      INTO scope
      FROM delivery.delivery_approval_request r
      JOIN delivery.delivery_approval_decision d
        ON d.request_id = r.request_id AND d.decision_id = p_decision_id
      JOIN delivery.delivery_publication p
        ON p.publication_id = r.publication_id
       AND p.target_id = r.target_id
       AND p.generation_id = r.generation_id
       AND p.candidate_id = r.candidate_id
       AND p.request_digest = r.request_digest
       AND p.expected_target_revision = r.expected_target_revision
      JOIN delivery.delivery_target t ON t.target_id = r.target_id
      JOIN project.resource_uid_inventory i
        ON i.generation_id = r.generation_id
       AND i.instance_id = t.target_id
       AND i.target_id = t.target_id
       AND i.project_id = t.project_id
       AND i.environment = t.environment
     WHERE r.request_id = p_request_id
       AND d.decision = 'approved'
       AND p.state = 'pending'
       AND NOT EXISTS (
           SELECT 1
             FROM delivery.delivery_approval_decision later
            WHERE later.request_id = r.request_id
              AND later.decision_revision > d.decision_revision
       )
     FOR UPDATE OF t;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'resource UID restore requires an effective approved publication';
    END IF;

    FOR entry IN SELECT value FROM jsonb_array_elements(scope.inventory_json)
    LOOP
        SELECT * INTO existing
          FROM project.resource_uid_registry registry
         WHERE registry.instance_id = scope.instance_id
           AND registry.project_id = scope.project_id
           AND registry.authored_resource_id = entry->>'authored_id'
         FOR UPDATE;
        IF FOUND AND existing.state = 'tombstoned' THEN
            IF existing.resource_kind IS DISTINCT FROM entry->>'kind' THEN
                RAISE EXCEPTION 'resource UID kind conflict for %', entry->>'authored_id';
            END IF;
            IF NOT EXISTS (
                SELECT 1
                  FROM project.resource_uid_generation binding
                 WHERE binding.instance_id = scope.instance_id
                   AND binding.project_id = scope.project_id
                   AND binding.generation_id = scope.generation_id
                   AND binding.resource_uid = existing.resource_uid
            ) THEN
                UPDATE project.resource_uid_restore_authorization ra
                   SET status = 'superseded'
                 WHERE ra.instance_id = scope.instance_id
                   AND ra.project_id = scope.project_id
                   AND ra.resource_uid = existing.resource_uid
                   AND ra.generation_id = scope.generation_id
                   AND ra.status = 'pending'
                   AND ra.approval_decision_id IS DISTINCT FROM p_decision_id;

                INSERT INTO project.resource_uid_restore_authorization(
                    resource_uid, instance_id, project_id, environment, target_id,
                    generation_id, authored_resource_id, resource_kind, actor_id, request_digest,
                    approval_request_id, approval_decision_id, approval_decision_revision
                ) VALUES (
                    existing.resource_uid, scope.instance_id, scope.project_id, scope.environment, scope.target_id,
                    scope.generation_id, existing.authored_resource_id, existing.resource_kind, scope.decided_by,
                    scope.request_digest, p_request_id, p_decision_id, scope.decision_revision
                ) ON CONFLICT (instance_id, project_id, resource_uid, generation_id)
                    WHERE status = 'pending' DO NOTHING;

                SELECT * INTO result
                  FROM project.resource_uid_restore_authorization ra
                 WHERE ra.instance_id = scope.instance_id
                   AND ra.project_id = scope.project_id
                   AND ra.resource_uid = existing.resource_uid
                   AND ra.generation_id = scope.generation_id
                   AND ra.approval_decision_id = p_decision_id
                   AND ra.status = 'pending'
                 FOR UPDATE;
                IF NOT FOUND OR result.approval_request_id IS DISTINCT FROM p_request_id
                   OR result.approval_decision_revision IS DISTINCT FROM scope.decision_revision
                   OR result.actor_id IS DISTINCT FROM scope.decided_by
                   OR result.request_digest IS DISTINCT FROM scope.request_digest
                   OR result.authored_resource_id IS DISTINCT FROM existing.authored_resource_id
                   OR result.resource_kind IS DISTINCT FROM existing.resource_kind
                   OR result.environment IS DISTINCT FROM scope.environment
                   OR result.target_id IS DISTINCT FROM scope.target_id THEN
                    RAISE EXCEPTION 'resource UID approval restore authorization conflict';
                END IF;
                authorized_count := authorized_count + 1;
            END IF;
        END IF;
    END LOOP;
    RETURN authorized_count;
END;
$$;

REVOKE ALL ON FUNCTION project.authorize_resource_uid_restores_for_approval(uuid,uuid) FROM PUBLIC;
DO $$
DECLARE role_name text;
BEGIN
    FOREACH role_name IN ARRAY ARRAY['leapview_control_owner','leapview_control_migrator','leapview_control_runtime'] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
            EXECUTE format(
                'GRANT EXECUTE ON FUNCTION project.authorize_resource_uid_restores_for_approval(uuid,uuid) TO %I',
                role_name
            );
        END IF;
    END LOOP;
END
$$;

RESET ROLE;
-- +goose StatementEnd
