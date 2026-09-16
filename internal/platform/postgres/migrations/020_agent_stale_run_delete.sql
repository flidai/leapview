-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- A provider or worker failure can terminalize the durable queue job before
-- its matching agent run is settled. Such an orphan cannot execute again and
-- must not permanently prevent its owner from deleting the conversation.
-- Inline runs (which have no durable job) and genuinely queued/running jobs
-- remain protected.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agent.delete_agent_conversation(
    p_id text,
    p_principal_id text
)
RETURNS SETOF agent.conversations
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, agent
AS $$
DECLARE
    deleted agent.conversations%ROWTYPE;
    has_active_run boolean := false;
BEGIN
    SELECT c.* INTO deleted
    FROM agent.conversations AS c
    WHERE c.id = p_id
      AND c.principal_id = p_principal_id
      AND c.status IN ('active', 'archived')
    FOR UPDATE;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    IF to_regclass('jobs.job_history') IS NULL THEN
        SELECT EXISTS (
            SELECT 1 FROM agent.runs AS r
            WHERE r.conversation_id = deleted.id
              AND r.status IN ('preparing', 'running')
        ) INTO has_active_run;
    ELSE
        EXECUTE $query$
            SELECT EXISTS (
                SELECT 1 FROM agent.runs AS r
                WHERE r.conversation_id = $1
                  AND r.status IN ('preparing', 'running')
                  AND (
                      NOT EXISTS (
                          SELECT 1 FROM jobs.job_history AS j
                          WHERE j.resource_kind = 'agent_run'
                            AND j.resource_id = r.id
                            AND j.kind = 'agent.run'
                      )
                      OR EXISTS (
                          SELECT 1 FROM jobs.job_history AS j
                          WHERE j.resource_kind = 'agent_run'
                            AND j.resource_id = r.id
                            AND j.kind = 'agent.run'
                            AND j.status IN ('queued', 'running')
                      )
                  )
            )
        $query$ INTO has_active_run USING deleted.id;
    END IF;
    IF has_active_run THEN
        RAISE EXCEPTION 'conversation has an active run' USING ERRCODE = '55006';
    END IF;
    PERFORM set_config('agent.chat_delete', 'on', true);
    DELETE FROM agent.conversations WHERE id = deleted.id;
    RETURN NEXT deleted;
    RETURN;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION agent.delete_agent_conversation(text, text) FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT EXECUTE ON FUNCTION agent.delete_agent_conversation(text, text) TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT EXECUTE ON FUNCTION agent.delete_agent_conversation(text, text) TO leapview_control_migrator;
    END IF;
END
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'agent conversation deletion migration is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
