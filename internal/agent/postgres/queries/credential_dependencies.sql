-- name: CredentialVersionReferences :many
SELECT revision FROM agent.configuration_revisions
WHERE credential_version_id=sqlc.arg(version_id)::text ORDER BY revision LIMIT 101;
