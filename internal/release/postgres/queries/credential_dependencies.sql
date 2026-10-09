-- name: CredentialVersionReferences :many
SELECT reference_kind,reference_id FROM (
 SELECT 'candidate_provenance'::text AS reference_kind,candidate_id::text AS reference_id
 FROM release.candidate_provenance
 WHERE provenance#>'{plan,bindings}' @> jsonb_build_array(jsonb_build_object('credentialVersionId',sqlc.arg(version_id)::text))
 UNION ALL
 SELECT 'release'::text,release_id::text FROM release.release_record
 WHERE provenance#>'{plan,bindings}' @> jsonb_build_array(jsonb_build_object('credentialVersionId',sqlc.arg(version_id)::text))
) AS refs ORDER BY reference_kind,reference_id LIMIT 101;
