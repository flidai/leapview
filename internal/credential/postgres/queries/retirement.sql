-- name: LockCredentialVersion :exec
SELECT credential.lock_version(sqlc.arg(deployment_id)::text,sqlc.arg(version_id)::text);

-- name: GetCredentialRetirement :one
SELECT * FROM credential.version_retirement WHERE deployment_id=sqlc.arg(deployment_id) AND version_id=sqlc.arg(version_id);

-- name: InsertCredentialRetirement :one
INSERT INTO credential.version_retirement(deployment_id,version_id,retired_by,audit_id)
VALUES(sqlc.arg(deployment_id),sqlc.arg(version_id),sqlc.arg(retired_by),sqlc.arg(audit_id))
RETURNING *;

-- name: CredentialVersionDependencies :many
SELECT dependency_kind,dependency_id FROM (
  SELECT 'activation_request'::text AS dependency_kind,operation_id::text AS dependency_id
  FROM credential.activation_request AS request WHERE request.deployment_id=sqlc.arg(deployment_id) AND request.version_id=sqlc.arg(version_id)
  UNION ALL
  SELECT 'activation_preparation'::text,p.operation_id::text
  FROM credential.activation_preparation AS p JOIN credential.validation_receipt AS r USING(deployment_id,receipt_id)
  WHERE r.deployment_id=sqlc.arg(deployment_id) AND r.version_id=sqlc.arg(version_id)
  UNION ALL
  SELECT 'live_validation_receipt'::text,receipt_id::text FROM credential.validation_receipt AS receipt
  WHERE receipt.deployment_id=sqlc.arg(deployment_id) AND receipt.version_id=sqlc.arg(version_id) AND receipt.expires_at>clock_timestamp()
) AS dependencies ORDER BY dependency_kind,dependency_id LIMIT 101;
