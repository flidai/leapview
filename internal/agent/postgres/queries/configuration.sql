-- name: CurrentAgentConfiguration :one
SELECT revision, enabled, config_json, credential, credential_version_id, actor_id, created_at
FROM agent.configuration_revisions ORDER BY revision DESC LIMIT 1;

-- name: AgentConfigurationByRevision :one
SELECT revision, enabled, config_json, credential, credential_version_id, actor_id, created_at
FROM agent.configuration_revisions WHERE revision = $1;

-- name: LockAgentConfiguration :exec
SELECT pg_advisory_xact_lock(741930281);

-- name: InsertAgentConfiguration :one
INSERT INTO agent.configuration_revisions (revision, enabled, config_json, credential, credential_version_id, actor_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING revision, enabled, config_json, credential, credential_version_id, actor_id, created_at;

-- name: InsertAgentConfigurationCandidate :one
INSERT INTO agent.configuration_candidates (candidate_id, expected_revision, enabled, config_json, actor_id)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: AgentConfigurationCandidate :one
SELECT * FROM agent.configuration_candidates WHERE candidate_id = $1;
