-- name: GetFirstSourcePreparation :one
SELECT * FROM credential.first_source_preparation WHERE target_id=sqlc.arg(target_id) AND operation_id=sqlc.arg(operation_id);

-- name: InsertFirstSourcePreparation :one
INSERT INTO credential.first_source_preparation(operation_id,target_id,original_receipt_id,intent_digest,intent_document)
VALUES(sqlc.arg(operation_id),sqlc.arg(target_id),sqlc.arg(original_receipt_id),sqlc.arg(intent_digest),sqlc.arg(intent_document)) RETURNING *;

-- name: GetFirstSourcePlanLink :one
SELECT * FROM credential.first_source_plan_link WHERE target_id=sqlc.arg(target_id) AND plan_id=sqlc.arg(plan_id);

-- name: GetFirstSourcePlanLinkForPreparation :one
SELECT * FROM credential.first_source_plan_link WHERE target_id=sqlc.arg(target_id) AND preparation_id=sqlc.arg(preparation_id);

-- name: InsertFirstSourcePlanLink :one
INSERT INTO credential.first_source_plan_link(preparation_id,target_id,plan_id,request_digest,binding_digest)
VALUES(sqlc.arg(preparation_id),sqlc.arg(target_id),sqlc.arg(plan_id),sqlc.arg(request_digest),sqlc.arg(binding_digest)) RETURNING *;
