-- name: LockExactRecoveryQualificationOccurrence :one
SELECT occurrence_id FROM refresh.recovery_qualification_occurrence
WHERE occurrence_id=$1 FOR UPDATE;
