-- Forward-only convergence for both declared revision-002 lineages.
-- Never update historical revision rows or infer schema correctness from them.
SET ROLE leapview_control_owner;

DO $$
DECLARE
    item record;
    relation regclass;
    keys text[];
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_trigger
        WHERE tgrelid = 'platform.schema_revision'::regclass
            AND tgname = 'schema_revision_append_only' AND tgenabled IN ('O','A')
            AND tgfoid = 'platform.reject_schema_revision_mutation()'::regprocedure) THEN
        RAISE EXCEPTION 'migration ledger append-only guard missing or disabled';
    END IF;
    FOR item IN SELECT * FROM (VALUES
        ('source_bundle', ARRAY['instance_id','bundle_id']),
        ('resource_identity', ARRAY['instance_id','authored_id']),
        ('source_bundle_resource', ARRAY['instance_id','bundle_id','authored_id']),
        ('resource_identity_history', ARRAY['instance_id','authored_id','sequence']),
        ('durable_resource_reference', ARRAY['instance_id','reference_id'])
    ) AS required(name, key_columns) LOOP
        relation := to_regclass('project.' || item.name);
        IF relation IS NULL OR NOT EXISTS (
            SELECT 1 FROM pg_class WHERE oid = relation AND relkind = 'r'
                AND relowner = 'leapview_control_owner'::regrole
        ) THEN
            RAISE EXCEPTION 'identity ledger prerequisite missing or wrong owner/kind: %', item.name;
        END IF;
        SELECT array_agg(a.attname::text ORDER BY k.ordinality) INTO keys
        FROM pg_constraint c CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY k(attnum, ordinality)
        JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
        WHERE c.conrelid = relation AND c.contype = 'p';
        IF keys IS DISTINCT FROM item.key_columns THEN
            RAISE EXCEPTION 'identity ledger primary key mismatch: %', item.name;
        END IF;
    END LOOP;
    FOR item IN SELECT * FROM (VALUES
        ('resource_identity', 'resource_identity_kind_immutable', 'reject_resource_identity_kind_change'),
        ('resource_identity', 'resource_identity_no_delete', 'reject_resource_identity_delete'),
        ('resource_identity_history', 'resource_identity_history_append_only', 'reject_resource_identity_history_mutation')
    ) AS required(table_name, trigger_name, function_name) LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
            WHERE tgrelid = to_regclass('project.' || item.table_name)
                AND tgname = item.trigger_name AND tgenabled IN ('O','A')
                AND tgfoid = to_regprocedure('project.' || item.function_name || '()')) THEN
            RAISE EXCEPTION 'identity ledger guard missing or disabled: %', item.trigger_name;
        END IF;
    END LOOP;
END;
$$;

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE
    project.source_bundle, project.resource_identity, project.source_bundle_resource,
    project.resource_identity_history, project.durable_resource_reference
    TO leapview_control_runtime;
GRANT SELECT ON TABLE
    project.source_bundle, project.resource_identity, project.source_bundle_resource,
    project.resource_identity_history, project.durable_resource_reference
    TO leapview_control_readonly;
REVOKE UPDATE, DELETE ON project.resource_identity_history
    FROM leapview_control_runtime, leapview_control_readonly;
REVOKE ALL ON FUNCTION project.reject_resource_identity_kind_change() FROM PUBLIC;
REVOKE ALL ON FUNCTION project.reject_resource_identity_delete() FROM PUBLIC;
REVOKE ALL ON FUNCTION project.reject_resource_identity_history_mutation() FROM PUBLIC;

-- Runtime readiness verifies exact lineage using read-only ledger access.
-- Baseline 001 revoked writes but did not grant runtime this required read.
GRANT SELECT ON platform.schema_revision TO leapview_control_runtime;

-- Prevent TRUNCATE from bypassing the original row-level append-only guard.
CREATE TRIGGER schema_revision_no_truncate
    BEFORE TRUNCATE ON platform.schema_revision
    FOR EACH STATEMENT EXECUTE FUNCTION platform.reject_schema_revision_mutation();

DO $$
DECLARE
    table_name text;
    privilege text;
    expected boolean;
BEGIN
    FOREACH table_name IN ARRAY ARRAY['source_bundle','resource_identity',
        'source_bundle_resource','resource_identity_history','durable_resource_reference'] LOOP
        FOREACH privilege IN ARRAY ARRAY['SELECT','INSERT','UPDATE','DELETE','TRUNCATE','REFERENCES','TRIGGER','MAINTAIN'] LOOP
            expected := privilege IN ('SELECT','INSERT') OR
                (privilege IN ('UPDATE','DELETE') AND table_name <> 'resource_identity_history');
            IF has_table_privilege('leapview_control_runtime', 'project.' || table_name, privilege) IS DISTINCT FROM expected THEN
                RAISE EXCEPTION 'unexpected effective runtime privilege % on %', privilege, table_name;
            END IF;
            IF has_table_privilege('leapview_control_readonly', 'project.' || table_name, privilege) IS DISTINCT FROM (privilege = 'SELECT') THEN
                RAISE EXCEPTION 'unexpected effective readonly privilege % on %', privilege, table_name;
            END IF;
        END LOOP;
    END LOOP;
    FOREACH privilege IN ARRAY ARRAY['SELECT','INSERT','UPDATE','DELETE','TRUNCATE','REFERENCES','TRIGGER','MAINTAIN'] LOOP
        IF has_table_privilege('leapview_control_runtime', 'platform.schema_revision', privilege) IS DISTINCT FROM (privilege = 'SELECT') THEN
            RAISE EXCEPTION 'unexpected effective runtime revision-ledger privilege %', privilege;
        END IF;
    END LOOP;
END;
$$;
RESET ROLE;
