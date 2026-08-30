-- Clean-slate PostgreSQL agent persistence. Agent state is deliberately
-- separate from the platform jobs authority: jobs and workflow events are
-- linked through caller-owned transactions, never through cross-schema FKs.
CREATE SCHEMA IF NOT EXISTS agent;

CREATE TABLE IF NOT EXISTS agent.conversations (
    id              text PRIMARY KEY,
    principal_id    text NOT NULL CHECK (principal_id = btrim(principal_id) AND length(principal_id) BETWEEN 1 AND 256),
    title           text NOT NULL CHECK (title = btrim(title) AND length(title) BETWEEN 1 AND 512),
    status          text NOT NULL CHECK (status IN ('active', 'archived')),
    metadata_json   jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata_json) = 'object' AND octet_length(metadata_json::text) <= 1048576),
    transcript_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(transcript_json) = 'array' AND octet_length(transcript_json::text) <= 1048576),
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    archived_at     timestamptz,
    CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 256),
    CHECK ((status = 'archived') = (archived_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS conversations_principal_updated_idx
    ON agent.conversations(principal_id, updated_at DESC, created_at DESC);

CREATE TABLE IF NOT EXISTS agent.runs (
    id              text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES agent.conversations(id) ON DELETE CASCADE,
    status          text NOT NULL CHECK (status IN ('preparing', 'running', 'completed', 'failed', 'canceled')),
    model           text NOT NULL DEFAULT '',
    stop_reason     text NOT NULL DEFAULT '',
    input_tokens    bigint NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens   bigint NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    total_tokens    bigint NOT NULL DEFAULT 0 CHECK (total_tokens >= 0),
    error           text NOT NULL DEFAULT '',
    metadata_json   jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata_json) = 'object' AND octet_length(metadata_json::text) <= 1048576),
    next_event_sequence bigint NOT NULL DEFAULT 1 CHECK (next_event_sequence > 0),
    started_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at     timestamptz,
    CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 256),
    CHECK ((status IN ('completed', 'failed', 'canceled')) = (finished_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS runs_conversation_started_idx
    ON agent.runs(conversation_id, started_at DESC, id);

CREATE TABLE IF NOT EXISTS agent.messages (
    id              text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES agent.conversations(id) ON DELETE CASCADE,
    run_id          text REFERENCES agent.runs(id) ON DELETE SET NULL,
    sequence        bigint NOT NULL CHECK (sequence > 0),
    role            text NOT NULL CHECK (role IN ('user', 'assistant', 'tool', 'summary')),
    content_text    text NOT NULL DEFAULT '',
    content_json    jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(content_json) = 'object' AND octet_length(content_json::text) <= 1048576),
    tool_call_id    text NOT NULL DEFAULT '',
    tool_name       text NOT NULL DEFAULT '',
    is_error        boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (conversation_id, sequence)
);

CREATE INDEX IF NOT EXISTS messages_conversation_sequence_idx
    ON agent.messages(conversation_id, sequence);

CREATE TABLE IF NOT EXISTS agent.events (
    event_id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id          text NOT NULL REFERENCES agent.runs(id) ON DELETE CASCADE,
    aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
    stream_sequence bigint NOT NULL DEFAULT 0 CHECK (stream_sequence >= 0),
    event_type      text NOT NULL CHECK (event_type = btrim(event_type) AND length(event_type) BETWEEN 1 AND 128),
    severity        text NOT NULL DEFAULT 'info' CHECK (severity = btrim(severity) AND length(severity) BETWEEN 1 AND 32),
    payload_json    jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object' AND octet_length(payload_json::text) <= 1048576),
    event_key       text NOT NULL DEFAULT '' CHECK (length(event_key) <= 256),
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (run_id, aggregate_version)
);

CREATE UNIQUE INDEX IF NOT EXISTS events_run_key_idx
    ON agent.events(run_id, event_key) WHERE event_key <> '';
CREATE INDEX IF NOT EXISTS events_run_sequence_idx ON agent.events(run_id, aggregate_version, event_id);

-- Retention floors are durable evidence of the latest fully drained policy
-- boundary for each agent evidence class.  A floor is a cursor, never an
-- authorization shortcut: runtime writes continue to use the normal
-- repository leaves, while only the bounded maintenance function below can
-- remove history or advance these rows.
CREATE TABLE IF NOT EXISTS agent.retention_floor (
    retention_class text PRIMARY KEY CHECK (retention_class IN ('conversations', 'run_events')),
    floor_at        timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'::timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO agent.retention_floor (retention_class)
VALUES ('conversations'), ('run_events')
ON CONFLICT (retention_class) DO NOTHING;

-- Agent evidence is immutable to request-serving roles.  The retention
-- function sets a transaction-local marker and requires the separately
-- authenticated maintenance session; merely forging the marker cannot bypass
-- this trigger.  Child rows are guarded as well because conversation removal
-- cascades runs, messages and events through their foreign keys.
CREATE OR REPLACE FUNCTION agent.reject_history_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('agent.retention', true) = 'on'
       AND session_user = 'leapview_control_maintenance' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'agent history is immutable';
END;
$$;

DROP TRIGGER IF EXISTS conversations_no_delete ON agent.conversations;
CREATE TRIGGER conversations_no_delete
    BEFORE DELETE ON agent.conversations
    FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();
DROP TRIGGER IF EXISTS runs_no_delete ON agent.runs;
CREATE TRIGGER runs_no_delete
    BEFORE DELETE ON agent.runs
    FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();
DROP TRIGGER IF EXISTS messages_no_delete ON agent.messages;
CREATE TRIGGER messages_no_delete
    BEFORE DELETE ON agent.messages
    FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();
DROP TRIGGER IF EXISTS events_append_only ON agent.events;
CREATE TRIGGER events_append_only
    BEFORE UPDATE OR DELETE ON agent.events
    FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();

-- Remove one bounded batch of run-stream events.  The target is capped by the
-- database clock and cannot move backwards from a previously drained floor.
-- The floor advances only when no eligible row remains: a smaller batch or a
-- row held by another maintenance transaction therefore leaves the floor
-- unchanged, preserving a truthful durable boundary.
CREATE OR REPLACE FUNCTION agent.prune_archived_run_events(
    p_requested_cutoff timestamptz,
    p_batch_limit integer
)
RETURNS TABLE (
    requested_cutoff timestamptz,
    cutoff timestamptz,
    requested_limit integer,
    removed_count bigint,
    retained_floor timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, agent
AS $$
DECLARE
    v_floor timestamptz;
    v_target timestamptz;
    v_removed bigint := 0;
    v_remaining boolean;
BEGIN
    IF session_user <> 'leapview_control_maintenance' THEN
        RAISE EXCEPTION 'agent retention requires the maintenance capability';
    END IF;
    IF p_requested_cutoff IS NULL THEN
        RAISE EXCEPTION 'agent retention cutoff is required';
    END IF;
    IF p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'agent retention batch limit must be between 1 and 1000';
    END IF;
    SELECT f.floor_at INTO STRICT v_floor
      FROM agent.retention_floor f
     WHERE f.retention_class = 'run_events'
     FOR UPDATE;
    requested_cutoff := p_requested_cutoff;
    requested_limit := p_batch_limit;
    v_target := GREATEST(v_floor, LEAST(p_requested_cutoff, clock_timestamp()));
    cutoff := v_target;

    PERFORM set_config('agent.retention', 'on', true);
    WITH candidates AS (
        SELECT e.event_id
          FROM agent.events e
          JOIN agent.runs r ON r.id = e.run_id
          JOIN agent.conversations c ON c.id = r.conversation_id
         WHERE c.status = 'archived'
           AND c.archived_at IS NOT NULL
           AND c.archived_at <= v_target
           AND r.status IN ('completed', 'failed', 'canceled')
         ORDER BY c.archived_at, e.event_id
         FOR UPDATE OF e SKIP LOCKED
         LIMIT p_batch_limit
    ), deleted AS (
        DELETE FROM agent.events e
         USING candidates d
         WHERE e.event_id = d.event_id
         RETURNING e.event_id
    )
    SELECT count(*) INTO v_removed FROM deleted;

    SELECT EXISTS (
        SELECT 1
          FROM agent.events e
          JOIN agent.runs r ON r.id = e.run_id
          JOIN agent.conversations c ON c.id = r.conversation_id
         WHERE c.status = 'archived'
           AND c.archived_at IS NOT NULL
           AND c.archived_at <= v_target
           AND r.status IN ('completed', 'failed', 'canceled')
    ) INTO v_remaining;
    IF NOT v_remaining AND v_target > v_floor THEN
        UPDATE agent.retention_floor
           SET floor_at = v_target, updated_at = clock_timestamp()
         WHERE retention_class = 'run_events';
        v_floor := v_target;
    END IF;
    cutoff := v_target;
    removed_count := v_removed;
    retained_floor := v_floor;
    RETURN NEXT;
END;
$$;

-- Drain archived conversation children in a bounded order: messages, terminal
-- runs with no remaining children, then empty conversations. Foreign-key
-- cascades are therefore no-ops and active/nonterminal evidence is preserved.
CREATE OR REPLACE FUNCTION agent.prune_archived_conversations(
    p_requested_cutoff timestamptz,
    p_batch_limit integer
)
RETURNS TABLE (
    requested_cutoff timestamptz,
    cutoff timestamptz,
    requested_limit integer,
    conversations_removed bigint,
    messages_removed bigint,
    runs_removed bigint,
    retained_floor timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, agent
AS $$
DECLARE
    v_floor timestamptz;
    v_target timestamptz;
    v_conversations bigint := 0;
    v_messages bigint := 0;
    v_runs bigint := 0;
    v_remaining_limit integer;
    v_remaining boolean;
BEGIN
    IF session_user <> 'leapview_control_maintenance' THEN
        RAISE EXCEPTION 'agent retention requires the maintenance capability';
    END IF;
    IF p_requested_cutoff IS NULL THEN
        RAISE EXCEPTION 'agent retention cutoff is required';
    END IF;
    IF p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'agent retention batch limit must be between 1 and 1000';
    END IF;
    SELECT f.floor_at INTO STRICT v_floor
      FROM agent.retention_floor f
     WHERE f.retention_class = 'conversations'
     FOR UPDATE;
    requested_cutoff := p_requested_cutoff;
    requested_limit := p_batch_limit;
    v_target := GREATEST(v_floor, LEAST(p_requested_cutoff, clock_timestamp()));
    cutoff := v_target;

    PERFORM set_config('agent.retention', 'on', true);
    v_remaining_limit := p_batch_limit;

    -- Drain messages first. They are archived evidence and have no child
    -- rows, so deleting them cannot cascade outside this bounded batch.
    WITH candidates AS (
        SELECT m.id
          FROM agent.messages m
          JOIN agent.conversations c ON c.id = m.conversation_id
         WHERE c.status = 'archived'
           AND c.archived_at IS NOT NULL
           AND c.archived_at <= v_target
           AND NOT EXISTS (
               SELECT 1
                 FROM agent.runs r
                WHERE r.conversation_id = c.id
                  AND r.status IN ('preparing', 'running')
           )
         ORDER BY c.archived_at, m.id
         FOR UPDATE OF m SKIP LOCKED
         LIMIT v_remaining_limit
    ), deleted AS (
        DELETE FROM agent.messages m USING candidates d
         WHERE m.id = d.id
         RETURNING m.id
    )
    SELECT count(*) INTO v_messages FROM deleted;
    v_remaining_limit := v_remaining_limit - v_messages::integer;

    -- Runs are removed only once all events and messages for that run are
    -- gone. This makes the FK cascade a no-op and keeps physical deletion
    -- bounded and observable.
    IF v_remaining_limit > 0 THEN
        WITH candidates AS (
            SELECT r.id
              FROM agent.runs r
              JOIN agent.conversations c ON c.id = r.conversation_id
             WHERE c.status = 'archived'
               AND c.archived_at IS NOT NULL
               AND c.archived_at <= v_target
               AND r.status IN ('completed', 'failed', 'canceled')
               AND NOT EXISTS (SELECT 1 FROM agent.events e WHERE e.run_id = r.id)
               AND NOT EXISTS (SELECT 1 FROM agent.messages m WHERE m.run_id = r.id)
             ORDER BY c.archived_at, r.id
             FOR UPDATE OF r SKIP LOCKED
             LIMIT v_remaining_limit
        ), deleted AS (
            DELETE FROM agent.runs r USING candidates d
             WHERE r.id = d.id
             RETURNING r.id
        )
        SELECT count(*) INTO v_runs FROM deleted;
        v_remaining_limit := v_remaining_limit - v_runs::integer;
    END IF;

    -- Finally remove only empty archived conversations. There are no child
    -- rows left, so ON DELETE CASCADE cannot erase uncounted physical rows.
    IF v_remaining_limit > 0 THEN
        WITH candidates AS (
            SELECT c.id
              FROM agent.conversations c
             WHERE c.status = 'archived'
               AND c.archived_at IS NOT NULL
               AND c.archived_at <= v_target
               AND NOT EXISTS (SELECT 1 FROM agent.runs r WHERE r.conversation_id = c.id)
               AND NOT EXISTS (SELECT 1 FROM agent.messages m WHERE m.conversation_id = c.id)
             ORDER BY c.archived_at, c.id
             FOR UPDATE OF c SKIP LOCKED
             LIMIT v_remaining_limit
        ), deleted AS (
            DELETE FROM agent.conversations c USING candidates d
             WHERE c.id = d.id
             RETURNING c.id
        )
        SELECT count(*) INTO v_conversations FROM deleted;
    END IF;

    SELECT EXISTS (
        SELECT 1
          FROM agent.conversations c
         WHERE c.status = 'archived'
           AND c.archived_at IS NOT NULL
           AND c.archived_at <= v_target
    ) INTO v_remaining;
    IF NOT v_remaining AND v_target > v_floor THEN
        UPDATE agent.retention_floor
           SET floor_at = v_target, updated_at = clock_timestamp()
         WHERE retention_class = 'conversations';
        v_floor := v_target;
    END IF;
    cutoff := v_target;
    conversations_removed := v_conversations;
    messages_removed := v_messages;
    runs_removed := v_runs;
    retained_floor := v_floor;
    RETURN NEXT;
END;
$$;

-- One maintenance facade executes both class-specific bounded leaves.  The
-- returned floors let operators prove each policy boundary independently.
CREATE OR REPLACE FUNCTION agent.prune_archived_agent_history(
    p_requested_cutoff timestamptz,
    p_batch_limit integer
)
RETURNS TABLE (
    requested_cutoff timestamptz,
    cutoff timestamptz,
    requested_limit integer,
    conversations_removed bigint,
    messages_removed bigint,
    runs_removed bigint,
    run_events_removed bigint,
    conversations_floor timestamptz,
    run_events_floor timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, agent
AS $$
DECLARE
    v_conversation_removed bigint := 0;
    v_message_removed bigint := 0;
    v_run_removed bigint := 0;
    v_event_removed bigint := 0;
    v_conversation_floor timestamptz;
    v_event_floor timestamptz;
    v_remaining integer;
BEGIN
    IF session_user <> 'leapview_control_maintenance' THEN
        RAISE EXCEPTION 'agent retention requires the maintenance capability';
    END IF;
    IF p_requested_cutoff IS NULL OR p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'agent retention cutoff and batch limit are required (1..1000)';
    END IF;
    -- One global batch budget is shared across classes. Run events drain
    -- first; conversations consume only the remainder, so one invocation can
    -- never delete more than p_batch_limit candidate rows in total.
    SELECT p.removed_count, p.retained_floor
      INTO v_event_removed, v_event_floor
      FROM agent.prune_archived_run_events(p_requested_cutoff, p_batch_limit) p;
    v_remaining := p_batch_limit - v_event_removed::integer;
    IF v_remaining > 0 THEN
        SELECT p.conversations_removed, p.messages_removed, p.runs_removed, p.retained_floor
          INTO v_conversation_removed, v_message_removed, v_run_removed, v_conversation_floor
          FROM agent.prune_archived_conversations(p_requested_cutoff, v_remaining) p;
    ELSE
        SELECT f.floor_at INTO v_conversation_floor
          FROM agent.retention_floor f
         WHERE f.retention_class = 'conversations'
         FOR SHARE;
    END IF;
    requested_cutoff := p_requested_cutoff;
    cutoff := LEAST(p_requested_cutoff, clock_timestamp());
    requested_limit := p_batch_limit;
    conversations_removed := v_conversation_removed;
    messages_removed := v_message_removed;
    runs_removed := v_run_removed;
    run_events_removed := v_event_removed;
    conversations_floor := v_conversation_floor;
    run_events_floor := v_event_floor;
    RETURN NEXT;
END;
$$;

REVOKE ALL ON SCHEMA agent FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA agent FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA agent FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON agent.conversations, agent.runs TO leapview_control_runtime;
        GRANT SELECT, INSERT ON agent.messages, agent.events TO leapview_control_runtime;
        GRANT USAGE ON ALL SEQUENCES IN SCHEMA agent TO leapview_control_runtime;
        REVOKE DELETE ON agent.conversations, agent.runs, agent.messages, agent.events, agent.retention_floor FROM leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION agent.prune_archived_run_events(timestamptz, integer), agent.prune_archived_conversations(timestamptz, integer), agent.prune_archived_agent_history(timestamptz, integer) FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT ALL ON ALL TABLES IN SCHEMA agent TO leapview_control_owner;
        GRANT ALL ON ALL FUNCTIONS IN SCHEMA agent TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT ALL ON ALL TABLES IN SCHEMA agent TO leapview_control_migrator;
        GRANT ALL ON ALL FUNCTIONS IN SCHEMA agent TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION agent.prune_archived_agent_history(timestamptz, integer) TO leapview_control_maintenance;
        REVOKE EXECUTE ON FUNCTION agent.prune_archived_run_events(timestamptz, integer), agent.prune_archived_conversations(timestamptz, integer) FROM leapview_control_maintenance;
        REVOKE ALL ON agent.conversations, agent.runs, agent.messages, agent.events, agent.retention_floor FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_readonly;
        GRANT SELECT ON agent.conversations, agent.runs, agent.messages, agent.events, agent.retention_floor TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON agent.conversations, agent.runs, agent.messages, agent.events, agent.retention_floor FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_backup;
        GRANT SELECT ON agent.conversations, agent.runs, agent.messages, agent.events, agent.retention_floor TO leapview_control_backup;
    END IF;
END
$$;
