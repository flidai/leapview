-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Bound background Undo recovery to rows with a pending operation.
CREATE INDEX agent_conversations_pending_until_idx
ON agent.conversations ((metadata_json #>> '{_leapview_chat,pendingUntil}'))
WHERE COALESCE(metadata_json #>> '{_leapview_chat,pendingAction}', '') IN ('archive', 'delete');

-- Chat deletion is an explicit runtime operation. The agent tables remain
-- append-only for retention and maintenance, while this narrow path removes a
-- principal-scoped conversation and its FK-cascaded content after rejecting
-- preparing/running turns. Audit rows are recorded by the caller's audit
-- authority in the same transaction.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION agent.reject_history_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND ((current_setting('agent.retention', true) = 'on'
             AND session_user = 'leapview_control_maintenance')
            OR current_setting('agent.chat_delete', true) = 'on') THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'agent history is immutable';
END;
$$;
-- +goose StatementEnd

-- Runtime callers have no table DELETE privilege. SECURITY DEFINER keeps that
-- least-privilege contract while the owner-scoped arguments and row lock make
-- the operation safe for the service path.
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
    IF EXISTS (
        SELECT 1 FROM agent.runs
        WHERE conversation_id = deleted.id
          AND status IN ('preparing', 'running')
    ) THEN
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
