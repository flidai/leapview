-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Native PostgreSQL authority for durable saved explorations.
--
-- Authored envelopes are stored as bytea deliberately: jsonb would normalize
-- the bytes and make the content hash an identity of a different value. The
-- repository validates the canonical envelope before writing; the database
-- checks that the bytes are valid UTF-8 JSON with the required envelope shape.
CREATE SCHEMA IF NOT EXISTS saved_exploration;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION saved_exploration.is_utc_timestamp(value text)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
SET search_path = pg_catalog
AS $$
BEGIN
    IF value !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]{1,9})?Z$' THEN
        RETURN false;
    END IF;
    PERFORM value::timestamptz;
    RETURN true;
EXCEPTION WHEN others THEN
    RETURN false;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION saved_exploration.timestamp_not_before(candidate text, baseline text)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
SET search_path = pg_catalog
AS $$
DECLARE
    candidateFraction text;
    baselineFraction text;
BEGIN
    candidateFraction := substring(candidate FROM 'T[0-9]{2}:[0-9]{2}:[0-9]{2}\.([0-9]{1,9})Z$');
    baselineFraction := substring(baseline FROM 'T[0-9]{2}:[0-9]{2}:[0-9]{2}\.([0-9]{1,9})Z$');
    RETURN left(candidate, 19) || '.' || rpad(coalesce(candidateFraction, ''), 9, '0')
        >= left(baseline, 19) || '.' || rpad(coalesce(baselineFraction, ''), 9, '0');
END;
$$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS saved_exploration.saved_exploration_revisions (
    project_id              text NOT NULL,
    exploration_id          text NOT NULL,
    revision_id             text NOT NULL,
    revision_number         bigint NOT NULL CHECK (revision_number > 0),
    spec_envelope_version   integer NOT NULL CHECK (spec_envelope_version > 0),
    spec_canonical_json     bytea NOT NULL,
    content_hash            text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    created_by              text NOT NULL,
    created_at              text NOT NULL,
    serving_project_id      text NOT NULL,
    serving_environment     text NOT NULL,
    serving_generation_id   text NOT NULL,
    PRIMARY KEY (project_id, exploration_id, revision_id),
    UNIQUE (project_id, exploration_id, revision_number),
    UNIQUE (project_id, exploration_id, revision_id, revision_number, content_hash),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255
        AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (exploration_id = btrim(exploration_id) AND octet_length(exploration_id) BETWEEN 1 AND 128
        AND exploration_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (revision_id = btrim(revision_id) AND octet_length(revision_id) BETWEEN 1 AND 128
        AND revision_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (serving_project_id = project_id),
    CHECK (serving_environment = btrim(serving_environment) AND octet_length(serving_environment) BETWEEN 1 AND 255
        AND serving_environment ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (serving_generation_id = btrim(serving_generation_id) AND octet_length(serving_generation_id) BETWEEN 1 AND 255
        AND serving_generation_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (created_by = btrim(created_by) AND octet_length(created_by) BETWEEN 1 AND 256
        AND created_by !~ '[\x00\x09\x0a\x0d]'),
    CHECK (saved_exploration.is_utc_timestamp(created_at)),
    CHECK (octet_length(spec_canonical_json) BETWEEN 1 AND 262144),
    CHECK (jsonb_typeof(convert_from(spec_canonical_json, 'UTF8')::jsonb) IS NOT NULL
        AND jsonb_typeof(convert_from(spec_canonical_json, 'UTF8')::jsonb) = 'object'),
    CHECK ((convert_from(spec_canonical_json, 'UTF8')::jsonb ->> 'version') IS NOT NULL
        AND jsonb_typeof(convert_from(spec_canonical_json, 'UTF8')::jsonb -> 'version') = 'number'
        AND (convert_from(spec_canonical_json, 'UTF8')::jsonb ->> 'version') = spec_envelope_version::text),
    CHECK (jsonb_typeof(convert_from(spec_canonical_json, 'UTF8')::jsonb -> 'spec') IS NOT NULL
        AND jsonb_typeof(convert_from(spec_canonical_json, 'UTF8')::jsonb -> 'spec') = 'object'),
    CHECK (btrim(convert_from(spec_canonical_json, 'UTF8')::jsonb #>> '{spec,modelId}') IS NOT NULL
        AND jsonb_typeof(convert_from(spec_canonical_json, 'UTF8')::jsonb #> '{spec,modelId}') = 'string'
        AND btrim(convert_from(spec_canonical_json, 'UTF8')::jsonb #>> '{spec,modelId}') <> '')
);

CREATE TABLE IF NOT EXISTS saved_exploration.saved_explorations (
    project_id              text NOT NULL,
    exploration_id          text NOT NULL,
    owner_principal_id      text NOT NULL,
    title                   text NOT NULL,
    slug                    text NOT NULL,
    visibility              text NOT NULL CHECK (visibility IN ('private', 'restricted', 'organization')),
    status                  text NOT NULL CHECK (status IN ('active', 'archived')),
    semantic_model_id       text NOT NULL,
    created_at              text NOT NULL,
    updated_at              text NOT NULL,
    archived_at             text,
    current_revision_id     text NOT NULL,
    current_revision_number bigint NOT NULL CHECK (current_revision_number > 0),
    current_content_hash    text NOT NULL CHECK (current_content_hash ~ '^sha256:[0-9a-f]{64}$'),
    PRIMARY KEY (project_id, exploration_id),
    UNIQUE (project_id, slug),
    FOREIGN KEY (project_id, exploration_id, current_revision_id, current_revision_number, current_content_hash)
        REFERENCES saved_exploration.saved_exploration_revisions(project_id, exploration_id, revision_id, revision_number, content_hash)
        DEFERRABLE INITIALLY DEFERRED,
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255
        AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (exploration_id = btrim(exploration_id) AND octet_length(exploration_id) BETWEEN 1 AND 128
        AND exploration_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (owner_principal_id = btrim(owner_principal_id) AND octet_length(owner_principal_id) BETWEEN 1 AND 256
        AND owner_principal_id !~ '[\x00\x09\x0a\x0d]'),
    CHECK (title = btrim(title) AND octet_length(title) BETWEEN 1 AND 200
        AND title !~ '[\x00\x09\x0a\x0d]'),
    CHECK (slug = btrim(slug) AND octet_length(slug) BETWEEN 1 AND 128
        AND slug ~ '^[a-z0-9][a-z0-9-]*$'),
    CHECK (semantic_model_id = btrim(semantic_model_id) AND octet_length(semantic_model_id) BETWEEN 1 AND 255
        AND semantic_model_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (saved_exploration.is_utc_timestamp(created_at)),
    CHECK (saved_exploration.is_utc_timestamp(updated_at)),
    CHECK (saved_exploration.timestamp_not_before(updated_at, created_at)),
    CHECK (archived_at IS NULL OR saved_exploration.is_utc_timestamp(archived_at)),
    CHECK (archived_at IS NULL OR saved_exploration.timestamp_not_before(archived_at, updated_at)),
    CHECK ((status = 'archived') = (archived_at IS NOT NULL))
);

ALTER TABLE saved_exploration.saved_exploration_revisions
    DROP CONSTRAINT IF EXISTS saved_exploration_revisions_exploration_fk;
ALTER TABLE saved_exploration.saved_exploration_revisions
    ADD CONSTRAINT saved_exploration_revisions_exploration_fk
    FOREIGN KEY (project_id, exploration_id)
    REFERENCES saved_exploration.saved_explorations(project_id, exploration_id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE IF NOT EXISTS saved_exploration.saved_exploration_operations (
    project_id                    text NOT NULL,
    actor_id                      text NOT NULL,
    operation_kind                text NOT NULL CHECK (operation_kind IN ('create', 'update', 'duplicate', 'archive')),
    idempotency_key               text NOT NULL,
    request_fingerprint           text NOT NULL CHECK (request_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    result_exploration_id         text NOT NULL,
    result_owner_principal_id     text NOT NULL,
    result_title                  text NOT NULL,
    result_slug                   text NOT NULL,
    result_visibility             text NOT NULL CHECK (result_visibility IN ('private', 'restricted', 'organization')),
    result_status                 text NOT NULL CHECK (result_status IN ('active', 'archived')),
    result_semantic_model_id      text NOT NULL,
    result_created_at             text NOT NULL,
    result_updated_at             text NOT NULL,
    result_archived_at            text,
    result_revision_id            text NOT NULL,
    result_revision_number        bigint NOT NULL CHECK (result_revision_number > 0),
    result_content_hash            text NOT NULL CHECK (result_content_hash ~ '^sha256:[0-9a-f]{64}$'),
    result_revision_created_at    text NOT NULL,
    result_revision_created_by    text NOT NULL,
    result_serving_project_id     text NOT NULL,
    result_serving_environment    text NOT NULL,
    result_serving_generation_id  text NOT NULL,
    evidence_version              integer NOT NULL CHECK (evidence_version = 1),
    evidence_request_id           text NOT NULL,
    evidence_correlation_id       text NOT NULL,
    evidence_admin_override       boolean NOT NULL,
    evidence_admin_reason         text NOT NULL DEFAULT '',
    evidence_occurred_at          text NOT NULL,
    created_at                    text NOT NULL,
    PRIMARY KEY (project_id, actor_id, operation_kind, idempotency_key),
    FOREIGN KEY (project_id, result_exploration_id)
        REFERENCES saved_exploration.saved_explorations(project_id, exploration_id),
    FOREIGN KEY (project_id, result_exploration_id, result_revision_id, result_revision_number, result_content_hash)
        REFERENCES saved_exploration.saved_exploration_revisions(project_id, exploration_id, revision_id, revision_number, content_hash),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255
        AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (actor_id = btrim(actor_id) AND octet_length(actor_id) BETWEEN 1 AND 256 AND actor_id !~ '[\x00\x09\x0a\x0d]'),
    CHECK (idempotency_key = btrim(idempotency_key) AND octet_length(idempotency_key) BETWEEN 1 AND 200 AND idempotency_key !~ '[\x00\x09\x0a\x0d]'),
    CHECK (result_exploration_id = btrim(result_exploration_id) AND octet_length(result_exploration_id) BETWEEN 1 AND 128
        AND result_exploration_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (result_owner_principal_id = btrim(result_owner_principal_id) AND octet_length(result_owner_principal_id) BETWEEN 1 AND 256
        AND result_owner_principal_id !~ '[\x00\x09\x0a\x0d]'),
    CHECK (result_title = btrim(result_title) AND octet_length(result_title) BETWEEN 1 AND 200
        AND result_title !~ '[\x00\x09\x0a\x0d]'),
    CHECK (result_slug = btrim(result_slug) AND octet_length(result_slug) BETWEEN 1 AND 128
        AND result_slug ~ '^[a-z0-9][a-z0-9-]*$'),
    CHECK (result_semantic_model_id = btrim(result_semantic_model_id) AND octet_length(result_semantic_model_id) BETWEEN 1 AND 255
        AND result_semantic_model_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (result_serving_project_id = project_id),
    CHECK (result_serving_environment = btrim(result_serving_environment) AND octet_length(result_serving_environment) BETWEEN 1 AND 255
        AND result_serving_environment ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (result_serving_generation_id = btrim(result_serving_generation_id) AND octet_length(result_serving_generation_id) BETWEEN 1 AND 255
        AND result_serving_generation_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (result_revision_created_by = btrim(result_revision_created_by) AND octet_length(result_revision_created_by) BETWEEN 1 AND 256
        AND result_revision_created_by !~ '[\x00\x09\x0a\x0d]'),
    CHECK ((result_status = 'archived') = (result_archived_at IS NOT NULL)),
    CHECK ((operation_kind = 'archive') = (result_status = 'archived')),
    CHECK (operation_kind NOT IN ('create', 'duplicate') OR result_owner_principal_id = actor_id),
    CHECK (operation_kind = 'archive' OR result_revision_created_by = actor_id),
    CHECK (saved_exploration.is_utc_timestamp(result_created_at)),
    CHECK (saved_exploration.is_utc_timestamp(result_updated_at)),
    CHECK (saved_exploration.timestamp_not_before(result_updated_at, result_created_at)),
    CHECK (saved_exploration.is_utc_timestamp(result_revision_created_at)),
    CHECK (saved_exploration.is_utc_timestamp(evidence_occurred_at)),
    CHECK (saved_exploration.is_utc_timestamp(created_at)),
    CHECK (evidence_request_id = btrim(evidence_request_id) AND octet_length(evidence_request_id) BETWEEN 1 AND 256 AND evidence_request_id !~ '[\x00\x09\x0a\x0d]'),
    CHECK (evidence_correlation_id = btrim(evidence_correlation_id) AND octet_length(evidence_correlation_id) BETWEEN 1 AND 256 AND evidence_correlation_id !~ '[\x00\x09\x0a\x0d]'),
    CHECK ((NOT evidence_admin_override AND evidence_admin_reason = '') OR (evidence_admin_override AND octet_length(evidence_admin_reason) BETWEEN 1 AND 500 AND evidence_admin_reason = btrim(evidence_admin_reason))),
    CHECK (result_archived_at IS NULL OR saved_exploration.is_utc_timestamp(result_archived_at)),
    CHECK (result_archived_at IS NULL OR saved_exploration.timestamp_not_before(result_archived_at, result_updated_at))
);

-- The operation ledger is the durable replay snapshot. Its normalized
-- lifecycle and revision references must agree with every copied field, or a
-- direct SQL writer could forge a replay response that never existed.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION saved_exploration.validate_operation_snapshot()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, saved_exploration AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM saved_exploration.saved_explorations AS exploration
          JOIN saved_exploration.saved_exploration_revisions AS revision
            ON revision.project_id = exploration.project_id
           AND revision.exploration_id = exploration.exploration_id
           AND revision.revision_id = NEW.result_revision_id
           AND revision.revision_number = NEW.result_revision_number
           AND revision.content_hash = NEW.result_content_hash
         WHERE exploration.project_id = NEW.project_id
           AND exploration.exploration_id = NEW.result_exploration_id
           AND exploration.owner_principal_id = NEW.result_owner_principal_id
           AND exploration.title = NEW.result_title
           AND exploration.slug = NEW.result_slug
           AND exploration.visibility = NEW.result_visibility
           AND exploration.status = NEW.result_status
           AND exploration.semantic_model_id = NEW.result_semantic_model_id
           AND exploration.created_at = NEW.result_created_at
           AND exploration.updated_at = NEW.result_updated_at
           AND exploration.archived_at IS NOT DISTINCT FROM NEW.result_archived_at
           AND exploration.current_revision_id = NEW.result_revision_id
           AND exploration.current_revision_number = NEW.result_revision_number
           AND exploration.current_content_hash = NEW.result_content_hash
           AND revision.created_at = NEW.result_revision_created_at
           AND revision.created_by = NEW.result_revision_created_by
           AND revision.serving_project_id = NEW.result_serving_project_id
           AND revision.serving_environment = NEW.result_serving_environment
           AND revision.serving_generation_id = NEW.result_serving_generation_id
    ) THEN
        RAISE EXCEPTION 'saved exploration operation snapshot is inconsistent';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS saved_exploration_operation_snapshot_consistent ON saved_exploration.saved_exploration_operations;
CREATE TRIGGER saved_exploration_operation_snapshot_consistent
    AFTER INSERT ON saved_exploration.saved_exploration_operations
    FOR EACH ROW EXECUTE FUNCTION saved_exploration.validate_operation_snapshot();

CREATE INDEX IF NOT EXISTS saved_explorations_project_status_idx
    ON saved_exploration.saved_explorations(project_id, status, exploration_id);
CREATE INDEX IF NOT EXISTS saved_exploration_revisions_lookup_idx
    ON saved_exploration.saved_exploration_revisions(project_id, exploration_id, revision_number DESC);
CREATE INDEX IF NOT EXISTS saved_exploration_operations_result_idx
    ON saved_exploration.saved_exploration_operations(project_id, result_exploration_id, created_at DESC);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION saved_exploration.reject_revision_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, saved_exploration AS $$
BEGIN
    RAISE EXCEPTION 'saved exploration revisions are immutable';
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS saved_exploration_revision_immutable ON saved_exploration.saved_exploration_revisions;
CREATE TRIGGER saved_exploration_revision_immutable
    BEFORE UPDATE OR DELETE ON saved_exploration.saved_exploration_revisions
    FOR EACH ROW EXECUTE FUNCTION saved_exploration.reject_revision_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION saved_exploration.validate_lifecycle_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, saved_exploration AS $$
BEGIN
    IF NEW.project_id <> OLD.project_id OR NEW.exploration_id <> OLD.exploration_id
       OR NEW.owner_principal_id <> OLD.owner_principal_id OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'saved exploration identity is immutable';
    END IF;
    IF OLD.status <> 'active' THEN
        RAISE EXCEPTION 'archived saved exploration is immutable';
    END IF;
    IF NEW.status = 'archived' THEN
        IF NEW.title <> OLD.title OR NEW.slug <> OLD.slug OR NEW.visibility <> OLD.visibility
           OR NEW.current_revision_id <> OLD.current_revision_id
           OR NEW.current_revision_number <> OLD.current_revision_number
           OR NEW.current_content_hash <> OLD.current_content_hash
           OR NEW.semantic_model_id <> OLD.semantic_model_id
           OR NEW.archived_at IS NULL
           OR NOT saved_exploration.timestamp_not_before(NEW.updated_at, OLD.updated_at)
           OR NOT saved_exploration.timestamp_not_before(NEW.archived_at, NEW.updated_at) THEN
            RAISE EXCEPTION 'archive cannot replace saved exploration revision';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.status <> 'active' THEN
        RAISE EXCEPTION 'invalid saved exploration lifecycle transition';
    END IF;
    IF NEW.title <> OLD.title OR NEW.slug <> OLD.slug OR NEW.visibility <> OLD.visibility
       OR NEW.semantic_model_id <> OLD.semantic_model_id OR NEW.updated_at <> OLD.updated_at
       OR NEW.current_revision_id <> OLD.current_revision_id
       OR NEW.current_revision_number <> OLD.current_revision_number
       OR NEW.current_content_hash <> OLD.current_content_hash THEN
        IF NEW.current_revision_id = OLD.current_revision_id
           OR NEW.current_revision_number <> OLD.current_revision_number + 1
           OR NOT saved_exploration.timestamp_not_before(NEW.updated_at, OLD.updated_at) THEN
            RAISE EXCEPTION 'active saved exploration update requires one new revision';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS saved_exploration_lifecycle_guard ON saved_exploration.saved_explorations;
CREATE TRIGGER saved_exploration_lifecycle_guard
    BEFORE UPDATE ON saved_exploration.saved_explorations
    FOR EACH ROW EXECUTE FUNCTION saved_exploration.validate_lifecycle_mutation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION saved_exploration.validate_initial_lifecycle()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, saved_exploration AS $$
BEGIN
    IF NEW.status <> 'active' OR NEW.archived_at IS NOT NULL OR NEW.current_revision_number <> 1 THEN
        RAISE EXCEPTION 'saved exploration must begin active at revision one';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS saved_exploration_initial_guard ON saved_exploration.saved_explorations;
CREATE TRIGGER saved_exploration_initial_guard
    BEFORE INSERT ON saved_exploration.saved_explorations
    FOR EACH ROW EXECUTE FUNCTION saved_exploration.validate_initial_lifecycle();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION saved_exploration.validate_current_revision()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, saved_exploration AS $$
DECLARE
    revision_row record;
BEGIN
    SELECT revision_id, revision_number, content_hash, created_at, serving_project_id,
           convert_from(spec_canonical_json, 'UTF8')::jsonb -> 'spec' ->> 'modelId' AS model_id
      INTO revision_row
      FROM saved_exploration.saved_exploration_revisions
     WHERE project_id = NEW.project_id AND exploration_id = NEW.exploration_id
       AND revision_id = NEW.current_revision_id
       AND revision_number = NEW.current_revision_number
       AND content_hash = NEW.current_content_hash;
    IF NOT FOUND OR revision_row.model_id <> NEW.semantic_model_id
       OR revision_row.serving_project_id <> NEW.project_id
       OR (NEW.status = 'active' AND NEW.current_revision_number = 1 AND revision_row.created_at <> NEW.created_at)
       OR (NEW.status = 'active' AND NEW.current_revision_number > 1 AND revision_row.created_at <> NEW.updated_at) THEN
        RAISE EXCEPTION 'saved exploration current revision is inconsistent';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS saved_exploration_current_revision_guard ON saved_exploration.saved_explorations;
CREATE TRIGGER saved_exploration_current_revision_guard
    BEFORE UPDATE ON saved_exploration.saved_explorations
    FOR EACH ROW EXECUTE FUNCTION saved_exploration.validate_current_revision();

DROP TRIGGER IF EXISTS saved_exploration_current_revision_deferred ON saved_exploration.saved_explorations;
CREATE CONSTRAINT TRIGGER saved_exploration_current_revision_deferred
    AFTER INSERT OR UPDATE ON saved_exploration.saved_explorations
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION saved_exploration.validate_current_revision();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION saved_exploration.validate_revision_insert()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, saved_exploration AS $$
DECLARE
    lifecycle_row record;
BEGIN
    SELECT project_id, status, semantic_model_id, created_at, current_revision_id, current_revision_number,
           current_content_hash INTO lifecycle_row
      FROM saved_exploration.saved_explorations
     WHERE project_id = NEW.project_id AND exploration_id = NEW.exploration_id;
    IF NOT FOUND THEN
        IF NEW.revision_number <> 1 THEN
            RAISE EXCEPTION 'saved exploration revisions must begin at one';
        END IF;
        RETURN NEW;
    END IF;
    IF lifecycle_row.status <> 'active' THEN
        RAISE EXCEPTION 'archived saved exploration cannot receive revisions';
    END IF;
    IF lifecycle_row.current_revision_id = NEW.revision_id
       AND lifecycle_row.current_revision_number = NEW.revision_number
       AND lifecycle_row.current_content_hash = NEW.content_hash THEN
        IF lifecycle_row.semantic_model_id <> (convert_from(NEW.spec_canonical_json, 'UTF8')::jsonb -> 'spec' ->> 'modelId')
           OR lifecycle_row.project_id <> NEW.serving_project_id
           OR (NEW.revision_number = 1 AND lifecycle_row.created_at <> NEW.created_at) THEN
            RAISE EXCEPTION 'saved exploration revision is inconsistent';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.revision_number <> lifecycle_row.current_revision_number + 1 THEN
        RAISE EXCEPTION 'saved exploration revision sequence is not contiguous';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS saved_exploration_revision_guard ON saved_exploration.saved_exploration_revisions;
CREATE TRIGGER saved_exploration_revision_guard
    AFTER INSERT ON saved_exploration.saved_exploration_revisions
    FOR EACH ROW EXECUTE FUNCTION saved_exploration.validate_revision_insert();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION saved_exploration.reject_operation_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, saved_exploration AS $$
BEGIN
    RAISE EXCEPTION 'saved exploration operation ledger is immutable';
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS saved_exploration_operation_immutable ON saved_exploration.saved_exploration_operations;
CREATE TRIGGER saved_exploration_operation_immutable
    BEFORE UPDATE OR DELETE ON saved_exploration.saved_exploration_operations
    FOR EACH ROW EXECUTE FUNCTION saved_exploration.reject_operation_mutation();

-- The repository runs against the runtime pool under the durable runtime
-- role. Read-only and backup roles receive only the projections needed for
-- recovery and verification; no PUBLIC privilege is granted.
GRANT USAGE ON SCHEMA saved_exploration TO
    leapview_control_runtime, leapview_control_readonly,
    leapview_control_backup, leapview_control_maintenance;
GRANT ALL ON TABLE
    saved_exploration.saved_explorations,
    saved_exploration.saved_exploration_revisions,
    saved_exploration.saved_exploration_operations
    TO leapview_control_owner, leapview_control_migrator;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE
    saved_exploration.saved_explorations,
    saved_exploration.saved_exploration_revisions,
    saved_exploration.saved_exploration_operations
    TO leapview_control_runtime;
GRANT SELECT ON TABLE
    saved_exploration.saved_explorations,
    saved_exploration.saved_exploration_revisions,
    saved_exploration.saved_exploration_operations
    TO leapview_control_readonly, leapview_control_backup,
       leapview_control_maintenance;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA saved_exploration
    TO leapview_control_owner, leapview_control_migrator,
       leapview_control_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE leapview_control_owner
    IN SCHEMA saved_exploration
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO leapview_control_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE leapview_control_owner
    IN SCHEMA saved_exploration
    GRANT SELECT ON TABLES TO leapview_control_readonly,
       leapview_control_backup, leapview_control_maintenance;
ALTER DEFAULT PRIVILEGES FOR ROLE leapview_control_owner
    IN SCHEMA saved_exploration
    GRANT EXECUTE ON FUNCTIONS TO leapview_control_runtime;

RESET ROLE;
