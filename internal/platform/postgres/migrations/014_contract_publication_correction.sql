-- Forward correction for the 003/008 CHECK-name collision. Historical checks
-- are retained, whether they contain the original shape checks or stronger
-- definitions. Names are not evidence of byte-to-envelope validation.
SET ROLE leapview_control_owner;

-- Prevent writers between preflight and constraint installation. The caller's
-- migration transaction makes the lock, DDL and revision record atomic.
LOCK TABLE project.contract_publication IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE
    existing record;
    corrupt_count bigint;
BEGIN
    -- Inspect definitions without dropping, renaming or trusting historical
    -- constraints. Preserve additional deployment-local restrictions as well.
    FOR existing IN
        SELECT conname, pg_get_constraintdef(oid) AS definition, convalidated
        FROM pg_constraint
        WHERE conrelid = 'project.contract_publication'::regclass
        ORDER BY conname
    LOOP
        RAISE NOTICE 'publication constraint % (validated=%): %',
            existing.conname, existing.convalidated, existing.definition;
    END LOOP;

    BEGIN
        SELECT count(*) INTO corrupt_count
        FROM project.contract_publication AS publication
        CROSS JOIN LATERAL (
            SELECT convert_from(publication.canonical_bytes, 'UTF8')::jsonb AS envelope
        ) AS canonical
        WHERE publication.canonical_digest IS DISTINCT FROM
                  'sha256:' || encode(sha256(publication.canonical_bytes), 'hex')
           OR canonical.envelope ->> 'apiVersion' IS DISTINCT FROM 'leapview.dev/v1'
           OR publication.projection_profile IS DISTINCT FROM canonical.envelope ->> 'profile'
           OR publication.authored_id IS DISTINCT FROM canonical.envelope -> 'metadata' ->> 'id'
           OR publication.resource_kind IS DISTINCT FROM
                  CASE canonical.envelope ->> 'kind'
                      WHEN 'Source' THEN 'source'
                      WHEN 'Model' THEN 'model'
                      WHEN 'SemanticModel' THEN 'semantic_model'
                      ELSE NULL
                  END
           OR publication.version IS DISTINCT FROM canonical.envelope -> 'metadata' -> 'contract' ->> 'version'
           OR publication.version_baseline IS DISTINCT FROM split_part(publication.version, '+', 1);
    EXCEPTION WHEN data_exception THEN
        -- Do not include publication contents in the error (privacy boundary).
        RAISE EXCEPTION 'contract publication correction found corrupt canonical JSON/UTF8'
            USING ERRCODE = '23514';
    END;
    IF corrupt_count > 0 THEN
        RAISE EXCEPTION 'contract publication correction found % corrupt existing row(s)', corrupt_count
            USING ERRCODE = '23514';
    END IF;
END
$$;

-- A new, unconditional CHECK fails closed on an unexpected name collision.
-- IS NOT DISTINCT FROM rejects missing envelope fields rather than accepting
-- SQL NULL. This is the intended 008 semantics, not a canonicalization change.
ALTER TABLE project.contract_publication
    ADD CONSTRAINT contract_publication_evidence_binding_check
    CHECK (
        canonical_digest IS NOT DISTINCT FROM 'sha256:' || encode(sha256(canonical_bytes), 'hex')
        AND (convert_from(canonical_bytes, 'UTF8')::jsonb ->> 'apiVersion')
            IS NOT DISTINCT FROM 'leapview.dev/v1'
        AND projection_profile IS NOT DISTINCT FROM
            (convert_from(canonical_bytes, 'UTF8')::jsonb ->> 'profile')
        AND authored_id IS NOT DISTINCT FROM
            (convert_from(canonical_bytes, 'UTF8')::jsonb -> 'metadata' ->> 'id')
        AND resource_kind IS NOT DISTINCT FROM
            CASE convert_from(canonical_bytes, 'UTF8')::jsonb ->> 'kind'
                WHEN 'Source' THEN 'source'
                WHEN 'Model' THEN 'model'
                WHEN 'SemanticModel' THEN 'semantic_model'
                ELSE NULL
            END
        AND version IS NOT DISTINCT FROM
            (convert_from(canonical_bytes, 'UTF8')::jsonb -> 'metadata' -> 'contract' ->> 'version')
        AND version_baseline IS NOT DISTINCT FROM split_part(version, '+', 1)
    );

RESET ROLE;
