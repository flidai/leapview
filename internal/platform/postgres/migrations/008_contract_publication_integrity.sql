-- FAI-662: bind immutable contract publication evidence to its canonical bytes.
-- Revision 003 intentionally retains its original compatibility contract; this
-- forward migration hardens rows already written by that revision.

SET ROLE leapview_control_owner;

-- Check existing rows before installing the constraints.  IS DISTINCT FROM is
-- deliberate: a missing envelope field must fail closed rather than turn a
-- CHECK expression into SQL NULL (which PostgreSQL treats as passing).
DO $$
DECLARE
    corrupt_count bigint;
BEGIN
    SELECT count(*)
    INTO corrupt_count
    FROM project.contract_publication AS publication
    CROSS JOIN LATERAL (
        SELECT convert_from(publication.canonical_bytes, 'UTF8')::jsonb AS envelope
    ) AS canonical
    WHERE publication.canonical_digest IS DISTINCT FROM
              'sha256:' || encode(sha256(publication.canonical_bytes), 'hex')
       OR canonical.envelope ->> 'apiVersion' IS DISTINCT FROM
              'leapview.dev/v1'
       OR publication.projection_profile IS DISTINCT FROM
              canonical.envelope ->> 'profile'
       OR publication.authored_id IS DISTINCT FROM
              canonical.envelope -> 'metadata' ->> 'id'
       OR publication.resource_kind IS DISTINCT FROM
              CASE canonical.envelope ->> 'kind'
                  WHEN 'Source' THEN 'source'
                  WHEN 'Model' THEN 'model'
                  WHEN 'SemanticModel' THEN 'semantic_model'
                  ELSE NULL
              END
       OR publication.version IS DISTINCT FROM
              canonical.envelope -> 'metadata' -> 'contract' ->> 'version'
       OR publication.version_baseline IS DISTINCT FROM
              split_part(publication.version, '+', 1);

    IF corrupt_count > 0 THEN
        RAISE EXCEPTION
            'contract publication integrity migration found % corrupt existing row(s)',
            corrupt_count;
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'project.contract_publication'::regclass
          AND conname = 'contract_publication_canonical_digest_check'
    ) THEN
        ALTER TABLE project.contract_publication
            ADD CONSTRAINT contract_publication_canonical_digest_check
            CHECK (
                canonical_digest = 'sha256:' || encode(sha256(canonical_bytes), 'hex')
            );
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'project.contract_publication'::regclass
          AND conname = 'contract_publication_api_version_check'
    ) THEN
        ALTER TABLE project.contract_publication
            ADD CONSTRAINT contract_publication_api_version_check
            CHECK (
                (convert_from(canonical_bytes, 'UTF8')::jsonb ->> 'apiVersion')
                    IS NOT DISTINCT FROM 'leapview.dev/v1'
            );
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'project.contract_publication'::regclass
          AND conname = 'contract_publication_projection_profile_check'
    ) THEN
        ALTER TABLE project.contract_publication
            ADD CONSTRAINT contract_publication_projection_profile_check
            CHECK (
                projection_profile IS NOT DISTINCT FROM
                    (convert_from(canonical_bytes, 'UTF8')::jsonb ->> 'profile')
            );
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'project.contract_publication'::regclass
          AND conname = 'contract_publication_authored_id_check'
    ) THEN
        ALTER TABLE project.contract_publication
            ADD CONSTRAINT contract_publication_authored_id_check
            CHECK (
                authored_id IS NOT DISTINCT FROM
                    (convert_from(canonical_bytes, 'UTF8')::jsonb -> 'metadata' ->> 'id')
            );
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'project.contract_publication'::regclass
          AND conname = 'contract_publication_resource_kind_check'
    ) THEN
        ALTER TABLE project.contract_publication
            ADD CONSTRAINT contract_publication_resource_kind_check
            CHECK (
                resource_kind IS NOT DISTINCT FROM
                    CASE convert_from(canonical_bytes, 'UTF8')::jsonb ->> 'kind'
                        WHEN 'Source' THEN 'source'
                        WHEN 'Model' THEN 'model'
                        WHEN 'SemanticModel' THEN 'semantic_model'
                        ELSE NULL
                    END
            );
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'project.contract_publication'::regclass
          AND conname = 'contract_publication_version_baseline_check'
    ) THEN
        ALTER TABLE project.contract_publication
            ADD CONSTRAINT contract_publication_version_baseline_check
            CHECK (
                version_baseline IS NOT DISTINCT FROM split_part(version, '+', 1)
            );
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'project.contract_publication'::regclass
          AND conname = 'contract_publication_version_check'
    ) THEN
        ALTER TABLE project.contract_publication
            ADD CONSTRAINT contract_publication_version_check
            CHECK (
                version IS NOT DISTINCT FROM
                    (convert_from(canonical_bytes, 'UTF8')::jsonb -> 'metadata' -> 'contract' ->> 'version')
            );
    END IF;
END
$$;

RESET ROLE;
