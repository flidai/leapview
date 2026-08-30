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

REVOKE ALL ON SCHEMA agent FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA agent FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON agent.conversations, agent.runs TO leapview_control_runtime;
        GRANT SELECT, INSERT ON agent.messages, agent.events TO leapview_control_runtime;
        GRANT USAGE ON ALL SEQUENCES IN SCHEMA agent TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_readonly;
        GRANT SELECT ON agent.conversations, agent.runs, agent.messages, agent.events TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_backup;
        GRANT SELECT ON agent.conversations, agent.runs, agent.messages, agent.events TO leapview_control_backup;
    END IF;
END
$$;
