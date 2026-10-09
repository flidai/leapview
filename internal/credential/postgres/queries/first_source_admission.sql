-- name: GetFirstSourceAdmission :one
SELECT * FROM credential.first_source_admission WHERE target_id = sqlc.arg(target_id);

-- name: InsertFirstSourceAdmission :one
INSERT INTO credential.first_source_admission(target_id, operation_id, intent_digest, intent_document, binding_digest, policy_revision, policy_digest)
VALUES(sqlc.arg(target_id), sqlc.arg(operation_id), sqlc.arg(intent_digest), sqlc.arg(intent_document), sqlc.arg(binding_digest), sqlc.arg(policy_revision), sqlc.arg(policy_digest))
ON CONFLICT DO NOTHING RETURNING *;
