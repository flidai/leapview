-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- The source transaction owns this append. A PITR-restored copy is never the
-- authority for post-frontier actions; reconciliation reads the surviving
-- source database through a separate connection.
CREATE TABLE IF NOT EXISTS access.lifecycle_authority (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    authority_id uuid NOT NULL
);
INSERT INTO access.lifecycle_authority(singleton, authority_id)
VALUES (true, uuidv7()) ON CONFLICT (singleton) DO NOTHING;
CREATE TABLE IF NOT EXISTS access.lifecycle_action (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurrence_id uuid NOT NULL UNIQUE,
    customer_id text NOT NULL CHECK (customer_id = btrim(customer_id) AND length(customer_id) BETWEEN 1 AND 255),
    deployment_id text NOT NULL CHECK (deployment_id = btrim(deployment_id) AND length(deployment_id) BETWEEN 1 AND 255),
    resource_id uuid NOT NULL,
    store text NOT NULL CHECK (store = btrim(store) AND length(store) BETWEEN 1 AND 128),
    action text NOT NULL CHECK (action IN ('delete','restrict','revoke','disable','supersede')),
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    source_revision text NOT NULL DEFAULT '' CHECK (source_revision = '' OR source_revision ~ '^sha256:[0-9a-f]{64}$'),
    completed boolean NOT NULL,
    CHECK (sequence > 0)
);
CREATE INDEX IF NOT EXISTS lifecycle_action_deployment_sequence_idx
    ON access.lifecycle_action(deployment_id, sequence);
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.append_lifecycle_action(
    p_occurrence_id uuid, p_customer_id text, p_deployment_id text,
    p_resource_id uuid, p_store text, p_action text,
    p_source_revision text, p_completed boolean
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, access AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(77246215304761);
    INSERT INTO access.lifecycle_action(
        occurrence_id, customer_id, deployment_id, resource_id,
        store, action, source_revision, completed
    ) VALUES (
        p_occurrence_id, p_customer_id, p_deployment_id, p_resource_id,
        p_store, p_action, p_source_revision, p_completed
    ) ON CONFLICT (occurrence_id) DO NOTHING;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER lifecycle_action_immutable BEFORE UPDATE ON access.lifecycle_action
FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_policy_history_mutation();
CREATE TRIGGER lifecycle_action_no_delete BEFORE DELETE ON access.lifecycle_action
FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
REVOKE ALL ON access.lifecycle_action FROM PUBLIC;
REVOKE ALL ON access.lifecycle_authority FROM PUBLIC;
REVOKE ALL ON FUNCTION access.append_lifecycle_action(uuid,text,text,uuid,text,text,text,boolean) FROM PUBLIC;
GRANT SELECT ON access.lifecycle_authority TO leapview_control_runtime, leapview_control_readonly, leapview_control_backup;
GRANT SELECT ON access.lifecycle_action TO leapview_control_runtime;
GRANT EXECUTE ON FUNCTION access.append_lifecycle_action(uuid,text,text,uuid,text,text,text,boolean) TO leapview_control_runtime;
GRANT SELECT ON access.lifecycle_action TO leapview_control_readonly, leapview_control_backup;
RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'lifecycle actions are immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
