-- Native publication approval authority. Static leaves are kept separate from
-- the publication lifecycle queries so approval transitions can be reviewed
-- and generated independently while sharing the canonical delivery schema.

-- name: InsertApprovalRequest :exec
INSERT INTO delivery.delivery_approval_request
 (request_id, publication_id, target_id, candidate_id, generation_id,
  request_digest, expected_target_revision, policy_revision, requested_by,
  request_credential_class, request_credential_id, request_credential_expires_at, expires_at,
  operation_id, event_id, audit_id, evidence)
VALUES (sqlc.arg(request_id)::uuid, sqlc.arg(publication_id)::uuid,
        sqlc.arg(target_id), sqlc.arg(candidate_id)::uuid,
        sqlc.arg(generation_id)::uuid, sqlc.arg(request_digest),
        sqlc.arg(expected_target_revision), sqlc.arg(policy_revision),
        sqlc.arg(requested_by), sqlc.arg(request_credential_class),
        sqlc.arg(request_credential_id), sqlc.arg(request_credential_expires_at), sqlc.arg(expires_at),
        sqlc.arg(operation_id)::uuid,
        sqlc.arg(event_id)::uuid, sqlc.arg(audit_id)::uuid,
        sqlc.arg(evidence)::jsonb)
ON CONFLICT (request_id) DO NOTHING;

-- name: GetApprovalRequest :one
SELECT request_id::text, publication_id::text, target_id, candidate_id::text,
       generation_id::text, request_digest, expected_target_revision,
       policy_revision, requested_by, request_credential_class,
       request_credential_id, request_credential_expires_at, requested_at, expires_at, operation_id::text,
       event_id::text, audit_id::text, evidence
FROM delivery.delivery_approval_request
WHERE request_id = sqlc.arg(request_id)::uuid;

-- name: LockPublicationForApproval :one
SELECT publication_id::text, target_id, generation_id::text,
       candidate_id::text, request_digest, expected_target_revision, state
FROM delivery.delivery_publication
WHERE publication_id = sqlc.arg(publication_id)::uuid
FOR UPDATE;

-- name: GetGenerationApprovalPolicyRevision :one
SELECT plan.approval_policy_revision
FROM delivery.delivery_generation g
JOIN delivery.delivery_plan plan ON plan.plan_id = g.plan_id
WHERE g.generation_id = sqlc.arg(generation_id)::uuid;

-- name: LockApprovalRequest :one
SELECT request_id::text
FROM delivery.delivery_approval_request
WHERE request_id = sqlc.arg(request_id)::uuid
FOR UPDATE;

-- name: LockApprovalRequestForPublication :one
SELECT request_id::text
FROM delivery.delivery_approval_request
WHERE publication_id = sqlc.arg(publication_id)::uuid
  AND target_id = sqlc.arg(target_id)
  AND generation_id = sqlc.arg(generation_id)::uuid
  AND candidate_id = sqlc.arg(candidate_id)::uuid
  AND request_digest = sqlc.arg(request_digest)
  AND expected_target_revision = sqlc.arg(expected_target_revision)
FOR UPDATE;

-- name: NextApprovalDecisionRevision :one
UPDATE delivery.delivery_approval_revision
SET next_revision = next_revision + 1
WHERE request_id = sqlc.arg(request_id)::uuid
RETURNING (next_revision - 1)::bigint AS revision;

-- name: InsertApprovalDecision :exec
INSERT INTO delivery.delivery_approval_decision
 (decision_id, request_id, decision_revision, decision, decided_by,
  decision_credential_class, decision_credential_id,
  decision_credential_expires_at, decided_at, operation_id, event_id,
  audit_id, evidence)
VALUES (sqlc.arg(decision_id)::uuid, sqlc.arg(request_id)::uuid,
        sqlc.arg(decision_revision), sqlc.arg(decision), sqlc.arg(decided_by),
        sqlc.arg(decision_credential_class), sqlc.arg(decision_credential_id),
        sqlc.arg(decision_credential_expires_at), sqlc.arg(decided_at),
        sqlc.arg(operation_id)::uuid, sqlc.arg(event_id)::uuid,
        sqlc.arg(audit_id)::uuid, sqlc.arg(evidence)::jsonb)
ON CONFLICT (decision_id) DO NOTHING;

-- name: GetApprovalDecision :one
SELECT decision_id::text, request_id::text, decision_revision, decision,
       decided_by, decision_credential_class, decision_credential_id,
       decision_credential_expires_at, decided_at, operation_id::text,
       event_id::text, audit_id::text, evidence
FROM delivery.delivery_approval_decision
WHERE decision_id = sqlc.arg(decision_id)::uuid;

-- name: ListApprovalDecisions :many
SELECT decision_id::text, request_id::text, decision_revision, decision,
       decided_by, decision_credential_class, decision_credential_id,
       decision_credential_expires_at, decided_at, operation_id::text,
       event_id::text, audit_id::text, evidence
FROM delivery.delivery_approval_decision
WHERE request_id = sqlc.arg(request_id)::uuid
ORDER BY decision_revision, decision_id;

-- name: GetLatestApprovalDecision :one
SELECT decision_id::text, request_id::text, decision_revision, decision,
       decided_by, decision_credential_class, decision_credential_id,
       decision_credential_expires_at, decided_at, operation_id::text,
       event_id::text, audit_id::text, evidence
FROM delivery.delivery_approval_decision
WHERE request_id = sqlc.arg(request_id)::uuid
ORDER BY decision_revision DESC, decision_id DESC
LIMIT 1;

-- name: GetEffectiveApproval :one
-- The effective decision is valid only while the exact publication remains
-- pending and every immutable request identity still matches that publication.
SELECT r.request_id::text AS request_id, r.publication_id::text AS publication_id, r.target_id,
       r.candidate_id::text AS candidate_id, r.generation_id::text AS generation_id, r.request_digest,
       r.expected_target_revision, r.policy_revision, r.requested_by,
       r.request_credential_class, r.request_credential_id, r.request_credential_expires_at,
       r.requested_at, r.expires_at, r.operation_id::text AS request_operation_id,
       r.event_id::text AS request_event_id, r.audit_id::text AS request_audit_id,
       r.evidence AS request_evidence, d.decision_id::text AS decision_id, d.decision_revision, d.decision,
       d.decided_by, d.decision_credential_class, d.decision_credential_id,
       d.decision_credential_expires_at, d.decided_at, d.operation_id::text AS decision_operation_id,
       d.event_id::text AS decision_event_id, d.audit_id::text AS decision_audit_id, d.evidence AS decision_evidence
FROM delivery.delivery_approval_request r
JOIN delivery.delivery_publication p
  ON p.publication_id = r.publication_id
 AND p.target_id = r.target_id
 AND p.generation_id = r.generation_id
 AND p.candidate_id = r.candidate_id
 AND p.request_digest = r.request_digest
 AND p.expected_target_revision = r.expected_target_revision
LEFT JOIN LATERAL (
    SELECT *
    FROM delivery.delivery_approval_decision d0
    WHERE d0.request_id = r.request_id
    ORDER BY d0.decision_revision DESC, d0.decision_id DESC
    LIMIT 1
) d ON TRUE
WHERE r.request_id = sqlc.arg(request_id)::uuid
  AND p.state = 'pending'
  AND r.expires_at > clock_timestamp()
  AND d.decision_credential_expires_at > clock_timestamp()
  AND d.decision = 'approved';

-- name: EffectiveApprovalForPublication :one
-- Activation authorizes only the immutable publication scope supplied by its
-- caller; it cannot select an arbitrary approval request ID or candidate.
SELECT EXISTS (
    SELECT 1
    FROM delivery.delivery_approval_request r
    JOIN delivery.delivery_publication p
      ON p.publication_id = r.publication_id
     AND p.target_id = r.target_id
     AND p.generation_id = r.generation_id
     AND p.candidate_id = r.candidate_id
     AND p.request_digest = r.request_digest
     AND p.expected_target_revision = r.expected_target_revision
    JOIN LATERAL (
        SELECT d0.decision, d0.decision_credential_expires_at
        FROM delivery.delivery_approval_decision d0
        WHERE d0.request_id = r.request_id
        ORDER BY d0.decision_revision DESC, d0.decision_id DESC
        LIMIT 1
    ) d ON TRUE
    JOIN delivery.delivery_generation g ON g.generation_id = p.generation_id
    JOIN delivery.delivery_plan plan ON plan.plan_id = g.plan_id
    WHERE p.publication_id = sqlc.arg(publication_id)::uuid
      AND p.target_id = sqlc.arg(target_id)
      AND p.generation_id = sqlc.arg(generation_id)::uuid
      AND p.candidate_id = sqlc.arg(candidate_id)::uuid
      AND p.request_digest = sqlc.arg(request_digest)
      AND p.expected_target_revision = sqlc.arg(expected_target_revision)
      AND p.state = 'pending'
      AND r.expires_at > clock_timestamp()
      AND r.policy_revision = plan.approval_policy_revision
      AND d.decision_credential_expires_at > clock_timestamp()
      AND d.decision = 'approved'
)::boolean AS approved;
