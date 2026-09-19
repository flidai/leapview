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
    authorized_count integer := 0;
BEGIN
    IF p_request_id IS NULL OR p_decision_id IS NULL THEN
        RAISE EXCEPTION 'invalid resource UID approval restore identity';
    END IF;

    SELECT r.target_id, r.generation_id, r.request_digest,
           d.decided_by, i.instance_id, i.project_id, i.environment,
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
                PERFORM project.authorize_resource_uid_restore(
                    scope.instance_id, scope.target_id, scope.project_id,
                    scope.environment, scope.generation_id,
                    existing.resource_uid, existing.authored_resource_id,
                    existing.resource_kind, scope.decided_by,
                    scope.request_digest
                );
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
