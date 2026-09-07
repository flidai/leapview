-- Agent persistence leaves. All writes remain in the caller's transaction
-- when a workflow or audit side effect is present.

-- name: PruneArchivedAgentHistory :one
SELECT prune.requested_cutoff::timestamptz AS requested_cutoff,
       prune.cutoff::timestamptz AS cutoff,
       prune.requested_limit::integer AS requested_limit,
       prune.conversations_removed::bigint AS conversations_removed,
       prune.messages_removed::bigint AS messages_removed,
       prune.runs_removed::bigint AS runs_removed,
       prune.run_events_removed::bigint AS run_events_removed,
       prune.conversations_floor::timestamptz AS conversations_floor,
       prune.run_events_floor::timestamptz AS run_events_floor
FROM agent.prune_archived_agent_history(sqlc.arg(requested_cutoff)::timestamptz, sqlc.arg(batch_limit)::integer)
    AS prune(requested_cutoff, cutoff, requested_limit, conversations_removed, messages_removed, runs_removed, run_events_removed, conversations_floor, run_events_floor);

-- name: CreateAgentConversation :one
INSERT INTO agent.conversations (id, principal_id, title, status, metadata_json, transcript_json)
VALUES (sqlc.arg(id), sqlc.arg(principal_id), sqlc.arg(title), sqlc.arg(status), sqlc.arg(metadata_json)::jsonb, sqlc.arg(transcript_json)::jsonb)
RETURNING id, principal_id, title, status, metadata_json::text, transcript_json::text,
          created_at, updated_at, archived_at;

-- name: ListAgentConversations :many
SELECT id, principal_id, title, status, metadata_json::text, transcript_json::text,
       created_at, updated_at, archived_at
FROM agent.conversations
WHERE principal_id = sqlc.arg(principal_id) AND status = 'active'
ORDER BY updated_at DESC, created_at DESC, id;

-- name: GetAgentConversation :one
SELECT id, principal_id, title, status, metadata_json::text, transcript_json::text,
       created_at, updated_at, archived_at
FROM agent.conversations
WHERE id = sqlc.arg(id) AND principal_id = sqlc.arg(principal_id);

-- name: ArchiveAgentConversation :one
UPDATE agent.conversations
SET status = 'archived', archived_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND principal_id = sqlc.arg(principal_id)
RETURNING id, principal_id, title, status, metadata_json::text, transcript_json::text,
          created_at, updated_at, archived_at;

-- name: UpdateAgentConversationTranscript :one
UPDATE agent.conversations
SET transcript_json = sqlc.arg(transcript_json)::jsonb, updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND principal_id = sqlc.arg(principal_id) AND status = 'active'
RETURNING id, principal_id, title, status, metadata_json::text, transcript_json::text,
          created_at, updated_at, archived_at;

-- name: UpdateDefaultAgentConversationTitle :one
UPDATE agent.conversations
SET title = sqlc.arg(title), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND principal_id = sqlc.arg(principal_id)
  AND status = 'active' AND title = 'New conversation'
RETURNING id, principal_id, title, status, metadata_json::text, transcript_json::text,
          created_at, updated_at, archived_at;

-- name: AppendAgentMessage :one
INSERT INTO agent.messages (id, conversation_id, run_id, sequence, role, content_text, content_json, tool_call_id, tool_name, is_error)
SELECT sqlc.arg(id), c.id, NULLIF(sqlc.arg(run_id), ''),
       COALESCE((SELECT MAX(sequence) + 1 FROM agent.messages WHERE conversation_id = c.id), 1),
       sqlc.arg(role), sqlc.arg(content_text), sqlc.arg(content_json)::jsonb,
       sqlc.arg(tool_call_id), sqlc.arg(tool_name), sqlc.arg(is_error)
FROM agent.conversations c
WHERE c.id = sqlc.arg(conversation_id) AND c.principal_id = sqlc.arg(principal_id)
  AND c.status = 'active'
RETURNING id, conversation_id, run_id, sequence, role, content_text,
          content_json::text, tool_call_id, tool_name, is_error, created_at;

-- name: ListAgentMessages :many
SELECT m.id, m.conversation_id, m.run_id, m.sequence, m.role,
       m.content_text, m.content_json::text, m.tool_call_id, m.tool_name, m.is_error, m.created_at
FROM agent.messages m
JOIN agent.conversations c ON c.id = m.conversation_id
WHERE c.id = sqlc.arg(conversation_id) AND c.principal_id = sqlc.arg(principal_id)
ORDER BY m.sequence, m.id;

-- name: CreateAgentRun :one
INSERT INTO agent.runs (id, conversation_id, status, model, metadata_json)
SELECT sqlc.arg(id), c.id, sqlc.arg(status), sqlc.arg(model), sqlc.arg(metadata_json)::jsonb
FROM agent.conversations c
WHERE c.id = sqlc.arg(conversation_id) AND c.principal_id = sqlc.arg(principal_id) AND c.status = 'active'
RETURNING id, conversation_id, status, model, stop_reason, input_tokens, output_tokens, total_tokens,
          error, started_at, finished_at, metadata_json::text;

-- name: ActivateAgentRun :execrows
UPDATE agent.runs SET status = 'running'
WHERE agent.runs.id = sqlc.arg(run_id) AND agent.runs.conversation_id = sqlc.arg(conversation_id)
  AND agent.runs.conversation_id IN (SELECT c.id FROM agent.conversations c WHERE c.id = sqlc.arg(conversation_id) AND c.principal_id = sqlc.arg(principal_id))
  AND agent.runs.status = 'preparing';

-- name: ListAgentRuns :many
SELECT r.id, r.conversation_id, r.status, r.model, r.stop_reason, r.input_tokens, r.output_tokens,
       r.total_tokens, r.error, r.started_at, r.finished_at, r.metadata_json::text
FROM agent.runs r JOIN agent.conversations c ON c.id = r.conversation_id
WHERE c.id = sqlc.arg(conversation_id) AND c.principal_id = sqlc.arg(principal_id)
ORDER BY r.started_at DESC, r.id DESC;

-- name: FinishAgentRun :one
UPDATE agent.runs
SET status = sqlc.arg(status), stop_reason = sqlc.arg(stop_reason), input_tokens = sqlc.arg(input_tokens),
    output_tokens = sqlc.arg(output_tokens), total_tokens = sqlc.arg(total_tokens), error = sqlc.arg(error),
    finished_at = clock_timestamp(), metadata_json = sqlc.arg(metadata_json)::jsonb
WHERE agent.runs.id = sqlc.arg(id) AND agent.runs.conversation_id = sqlc.arg(conversation_id)
  AND agent.runs.conversation_id IN (SELECT c.id FROM agent.conversations c WHERE c.id = sqlc.arg(conversation_id) AND c.principal_id = sqlc.arg(principal_id))
  AND agent.runs.status IN ('running', 'preparing')
RETURNING id, conversation_id, status, model, stop_reason, input_tokens, output_tokens, total_tokens,
          error, started_at, finished_at, metadata_json::text;

-- name: UpdateAgentConversationTitle :one
UPDATE agent.conversations SET title = sqlc.arg(title), updated_at = clock_timestamp()
WHERE id = sqlc.arg(conversation_id) AND principal_id = sqlc.arg(principal_id) AND status = 'active'
RETURNING id, principal_id, title, status, metadata_json::text, transcript_json::text,
          created_at, updated_at, archived_at;

-- name: AcquireAgentConversationMutationLock :exec
SELECT id FROM agent.conversations
WHERE id = sqlc.arg(conversation_id) AND principal_id = sqlc.arg(principal_id)
FOR UPDATE;

-- name: GetAgentRunInConversation :one
SELECT r.id, r.conversation_id, r.status, r.model, r.stop_reason, r.input_tokens, r.output_tokens,
       r.total_tokens, r.error, r.started_at, r.finished_at, r.metadata_json::text
FROM agent.runs r JOIN agent.conversations c ON c.id = r.conversation_id
WHERE r.id = sqlc.arg(run_id) AND c.id = sqlc.arg(conversation_id) AND c.principal_id = sqlc.arg(principal_id);

-- name: GetAgentRunForPrincipal :one
SELECT r.id, r.conversation_id, r.status, r.model, r.stop_reason, r.input_tokens, r.output_tokens,
       r.total_tokens, r.error, r.started_at, r.finished_at, r.metadata_json::text
FROM agent.runs r JOIN agent.conversations c ON c.id = r.conversation_id
WHERE r.id = sqlc.arg(run_id) AND c.principal_id = sqlc.arg(principal_id);

-- name: AgentRunExistsForPrincipal :one
SELECT EXISTS (SELECT 1 FROM agent.runs r JOIN agent.conversations c ON c.id = r.conversation_id
               WHERE r.id = sqlc.arg(run_id) AND c.principal_id = sqlc.arg(principal_id));

-- name: GetAgentRunStatus :one
SELECT status FROM agent.runs WHERE id = sqlc.arg(run_id) AND conversation_id = sqlc.arg(conversation_id);

-- name: AcquireAgentRunMutationLock :exec
SELECT id FROM agent.runs
WHERE id = sqlc.arg(run_id) AND conversation_id = sqlc.arg(conversation_id)
FOR UPDATE;

-- name: AcquireAgentRunByIDMutationLock :exec
SELECT id FROM agent.runs WHERE id = sqlc.arg(run_id) FOR UPDATE;

-- name: AllocateAgentEventSequence :one
UPDATE agent.runs
SET next_event_sequence = next_event_sequence + 1
WHERE id = sqlc.arg(run_id)
RETURNING next_event_sequence - 1 AS sequence;

-- name: InsertAgentEvent :one
INSERT INTO agent.events (run_id, aggregate_version, stream_sequence, event_type, severity, payload_json, event_key)
VALUES (sqlc.arg(run_id), sqlc.arg(aggregate_version), sqlc.arg(stream_sequence), sqlc.arg(event_type), sqlc.arg(severity), sqlc.arg(payload_json)::jsonb, sqlc.arg(event_key))
ON CONFLICT (run_id, aggregate_version) DO NOTHING
RETURNING event_id, run_id, aggregate_version, stream_sequence, event_type, severity, payload_json::text, event_key, created_at;

-- name: GetAgentEventBySequence :one
SELECT event_id, run_id, aggregate_version, stream_sequence, event_type, severity, payload_json::text, event_key, created_at
FROM agent.events WHERE run_id = sqlc.arg(run_id) AND aggregate_version = sqlc.arg(aggregate_version);

-- name: GetAgentEventByKey :one
SELECT event_id, run_id, aggregate_version, stream_sequence, event_type, severity, payload_json::text, event_key, created_at
FROM agent.events WHERE run_id = sqlc.arg(run_id) AND event_key = sqlc.arg(event_key);

-- name: ListAgentEvents :many
SELECT event_id, run_id, aggregate_version, stream_sequence, event_type, severity, payload_json::text, event_key, created_at
FROM agent.events WHERE run_id = sqlc.arg(run_id) AND event_id > sqlc.arg(after_id)
ORDER BY event_id LIMIT sqlc.arg(page_limit);
