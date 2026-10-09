-- name: GetValidationReceipt :one
SELECT * FROM credential.validation_receipt WHERE deployment_id=sqlc.arg(deployment_id) AND receipt_id=sqlc.arg(receipt_id);

-- name: GetActivationRequest :one
SELECT * FROM credential.activation_request WHERE deployment_id=sqlc.arg(deployment_id) AND operation_id=sqlc.arg(operation_id);

-- name: GetPendingActivationRequest :one
SELECT * FROM credential.activation_request WHERE deployment_id=sqlc.arg(deployment_id) AND state NOT IN ('completed','aborted');

-- name: InsertActivationRequest :one
INSERT INTO credential.activation_request (
    operation_id,deployment_id,receipt_id,version_id,expected_binding_revision,state,revision,created_at,updated_at
) VALUES (
    sqlc.arg(operation_id),sqlc.arg(deployment_id),sqlc.arg(receipt_id),sqlc.arg(version_id),sqlc.arg(expected_binding_revision),
    'preparing',1,clock_timestamp(),clock_timestamp()
) RETURNING *;

-- name: TransitionActivationRequest :one
UPDATE credential.activation_request SET
    state=sqlc.arg(state), revision=revision+1, updated_at=GREATEST(clock_timestamp(),updated_at),
    plan_id=sqlc.arg(plan_id),candidate_id=sqlc.arg(candidate_id),generation_id=sqlc.arg(generation_id),
    publication_id=sqlc.arg(publication_id),configuration_revision=sqlc.arg(configuration_revision)
WHERE deployment_id=sqlc.arg(deployment_id) AND operation_id=sqlc.arg(operation_id)
  AND revision=sqlc.arg(expected_revision)
RETURNING *;

-- name: AppendActivationRequestReceipt :execrows
INSERT INTO credential.activation_request_receipt(operation_id,deployment_id,receipt_id)
VALUES(sqlc.arg(operation_id),sqlc.arg(deployment_id),sqlc.arg(receipt_id))
ON CONFLICT(receipt_id) DO NOTHING;

-- name: GetActivationReceiptReservation :one
SELECT operation_id FROM credential.activation_request_receipt
WHERE deployment_id=sqlc.arg(deployment_id) AND receipt_id=sqlc.arg(receipt_id);

-- name: CheckActivationReceiptFresh :one
SELECT COALESCE((
    SELECT receipt.validated_at <= clock_timestamp() AND receipt.expires_at > clock_timestamp()
    FROM credential.activation_request_receipt AS attempt
    JOIN credential.validation_receipt AS receipt USING(deployment_id,receipt_id)
    JOIN credential.activation_request AS request ON request.operation_id=attempt.operation_id
    WHERE attempt.deployment_id=sqlc.arg(deployment_id) AND attempt.operation_id=sqlc.arg(operation_id)
      AND request.state IN ('preparing','prepared','switching')
    ORDER BY attempt.observed_at DESC,attempt.receipt_id DESC LIMIT 1
),false)::boolean AS fresh;
