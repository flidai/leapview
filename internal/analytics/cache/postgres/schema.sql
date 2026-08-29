-- Standalone cache capability schema.  The control-plane baseline owns the
-- same tables; this copy is useful for isolated repository conformance tests.
CREATE SCHEMA IF NOT EXISTS cache;

CREATE TABLE IF NOT EXISTS cache.cache_manifest (
    manifest_id uuid PRIMARY KEY,
    partition_kind text NOT NULL CHECK (partition_kind IN ('production','candidate')),
    project_id text NOT NULL CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    environment text NOT NULL CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 255),
    candidate_id text CHECK (candidate_id IS NULL OR (candidate_id = btrim(candidate_id) AND octet_length(candidate_id) BETWEEN 1 AND 255)),
    partition_format_version bigint NOT NULL CHECK (partition_format_version = 1),
    dependency_digest text NOT NULL CHECK (dependency_digest ~ '^sha256:[0-9a-f]{64}$'),
    policy_fingerprint text NOT NULL CHECK (policy_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    canonical_query_digest text NOT NULL CHECK (canonical_query_digest ~ '^sha256:[0-9a-f]{64}$'),
    key_format_version bigint NOT NULL CHECK (key_format_version = 1),
    storage_security_domain text NOT NULL CHECK (storage_security_domain ~ '^sha256:[0-9a-f]{64}$'),
    object_digest text NOT NULL CHECK (object_digest ~ '^sha256:[0-9a-f]{64}$'),
    object_key text NOT NULL CHECK (object_key = btrim(object_key) AND octet_length(object_key) BETWEEN 1 AND 2048),
    byte_size bigint NOT NULL CHECK (byte_size > 0),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object' AND octet_length(metadata::text) <= 16384),
    state text NOT NULL CHECK (state IN ('admitted','retiring','expired')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz,
    retired_at timestamptz,
    expired_at timestamptz,
    retire_evidence jsonb,
    expire_evidence jsonb,
    CHECK ((partition_kind = 'production' AND candidate_id IS NULL) OR (partition_kind = 'candidate' AND candidate_id IS NOT NULL)),
    CHECK (expires_at IS NULL OR (expires_at > created_at AND expires_at <= created_at + interval '365 days')),
    CHECK ((state = 'admitted' AND retired_at IS NULL AND expired_at IS NULL AND retire_evidence IS NULL AND expire_evidence IS NULL)
        OR (state = 'retiring' AND retired_at IS NOT NULL AND expired_at IS NULL AND retire_evidence IS NOT NULL AND expire_evidence IS NULL)
        OR (state = 'expired' AND retired_at IS NOT NULL AND expired_at IS NOT NULL AND retire_evidence IS NOT NULL AND expire_evidence IS NOT NULL)),
    CHECK (retire_evidence IS NULL OR (jsonb_typeof(retire_evidence) = 'object' AND retire_evidence <> '{}'::jsonb AND retire_evidence->>'version' = '1' AND octet_length(retire_evidence::text) <= 4096)),
    CHECK (expire_evidence IS NULL OR (jsonb_typeof(expire_evidence) = 'object' AND expire_evidence <> '{}'::jsonb AND expire_evidence->>'version' = '1' AND octet_length(expire_evidence::text) <= 4096)),
    CHECK ((retired_at IS NULL OR retired_at >= created_at)
        AND (expired_at IS NULL OR expired_at >= COALESCE(retired_at, created_at)))
);
CREATE INDEX IF NOT EXISTS cache_manifest_lookup_idx ON cache.cache_manifest (partition_kind, project_id, environment, candidate_id, dependency_digest, policy_fingerprint, canonical_query_digest);
-- A cache key may have many historical manifests, but only one currently
-- admitted object.  Retiring and expired rows are immutable evidence and must
-- never block a later publication for the same key.
CREATE UNIQUE INDEX IF NOT EXISTS cache_manifest_admitted_key_idx
    ON cache.cache_manifest (partition_kind, project_id, environment, COALESCE(candidate_id, ''), partition_format_version, dependency_digest, policy_fingerprint, canonical_query_digest, key_format_version)
    WHERE state = 'admitted';

-- Migrate the pre-history schema's one full-key UNIQUE constraint when this
-- file is applied to an existing control database.  The column-vector match
-- is intentionally exact so future unrelated constraints are untouched.
DO $$
DECLARE c record; key_columns smallint[];
BEGIN
    SELECT ARRAY_AGG(a.attnum ORDER BY x.ord) INTO key_columns
      FROM unnest(ARRAY['partition_kind','project_id','environment','candidate_id','partition_format_version','dependency_digest','policy_fingerprint','canonical_query_digest','key_format_version']) WITH ORDINALITY AS x(name,ord)
      JOIN pg_attribute a ON a.attrelid='cache.cache_manifest'::regclass AND a.attname=x.name;
    FOR c IN SELECT conname FROM pg_constraint WHERE conrelid='cache.cache_manifest'::regclass AND contype='u' AND conkey=key_columns LOOP
        EXECUTE format('ALTER TABLE cache.cache_manifest DROP CONSTRAINT %I', c.conname);
    END LOOP;
END;
$$;

-- Stable namespace epochs fence fills across nodes. Epochs are scoped by the
-- exact production or candidate partition; no global counter can accidentally
-- invalidate another project.
CREATE TABLE IF NOT EXISTS cache.cache_namespace_epoch (
    namespace_key text PRIMARY KEY CHECK (namespace_key = btrim(namespace_key) AND octet_length(namespace_key) BETWEEN 1 AND 2048),
    partition_kind text NOT NULL CHECK (partition_kind IN ('production','candidate')),
    project_id text NOT NULL CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    environment text NOT NULL CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 255),
    candidate_id text,
    epoch bigint NOT NULL DEFAULT 1 CHECK (epoch > 0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE NULLS NOT DISTINCT (partition_kind, project_id, environment, candidate_id),
    CHECK ((partition_kind = 'production' AND candidate_id IS NULL) OR (partition_kind = 'candidate' AND candidate_id IS NOT NULL))
);

CREATE OR REPLACE FUNCTION cache.namespace_key(p_kind text, p_project text, p_environment text, p_candidate text)
RETURNS text
LANGUAGE sql
IMMUTABLE
SET search_path = pg_catalog, cache
AS $$
    SELECT 'ns1_' || replace(replace(replace(rtrim(encode(convert_to(
        CASE WHEN p_candidate IS NULL THEN
            '{"v":1,"k":' || to_json(p_kind)::text || ',"p":' || to_json(p_project)::text || ',"e":' || to_json(p_environment)::text || '}'
        ELSE
            '{"v":1,"k":' || to_json(p_kind)::text || ',"p":' || to_json(p_project)::text || ',"e":' || to_json(p_environment)::text || ',"c":' || to_json(p_candidate)::text || '}'
        END, 'UTF8'), 'base64'), '='), E'\n', ''), '+', '-'), '/', '_')
$$;

CREATE OR REPLACE FUNCTION cache.enforce_namespace_epoch()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, cache
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF current_setting('cache.capability', true) IS DISTINCT FROM 'namespace_epoch' THEN
            RAISE EXCEPTION 'namespace creation requires cache capability';
        END IF;
        IF NEW.namespace_key IS DISTINCT FROM cache.namespace_key(NEW.partition_kind, NEW.project_id, NEW.environment, NEW.candidate_id)
           OR NEW.epoch <> 1 THEN
            RAISE EXCEPTION 'namespace identity must be canonical and begin at epoch one';
        END IF;
        NEW.updated_at := clock_timestamp();
        RETURN NEW;
    END IF;
    IF NEW.namespace_key IS DISTINCT FROM OLD.namespace_key
       OR NEW.partition_kind IS DISTINCT FROM OLD.partition_kind
       OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.environment IS DISTINCT FROM OLD.environment
       OR NEW.candidate_id IS DISTINCT FROM OLD.candidate_id THEN
        RAISE EXCEPTION 'namespace identity is immutable';
    END IF;
    IF current_setting('cache.capability', true) IS DISTINCT FROM 'namespace_epoch' THEN
        RAISE EXCEPTION 'namespace epoch mutation requires cache capability';
    END IF;
    IF NEW.epoch <> OLD.epoch + 1 THEN
        RAISE EXCEPTION 'namespace epoch must advance exactly one step';
    END IF;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS cache_namespace_epoch_guard ON cache.cache_namespace_epoch;
CREATE TRIGGER cache_namespace_epoch_guard
    BEFORE INSERT OR UPDATE ON cache.cache_namespace_epoch
    FOR EACH ROW EXECUTE FUNCTION cache.enforce_namespace_epoch();

CREATE TABLE IF NOT EXISTS cache.cache_dependency_revision (
    namespace_key text NOT NULL REFERENCES cache.cache_namespace_epoch(namespace_key),
    dependency_kind text NOT NULL CHECK (dependency_kind IN ('source','project','semantic_model','deployment','custom')),
    dependency_id text NOT NULL CHECK (dependency_id = btrim(dependency_id) AND octet_length(dependency_id) BETWEEN 1 AND 255),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    revision_digest text NOT NULL CHECK (revision_digest ~ '^sha256:[0-9a-f]{64}$'),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (namespace_key, dependency_kind, dependency_id)
);
CREATE INDEX IF NOT EXISTS cache_dependency_revision_digest_idx ON cache.cache_dependency_revision(namespace_key, revision_digest);

CREATE OR REPLACE FUNCTION cache.enforce_dependency_revision()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, cache
AS $$
BEGIN
    IF current_setting('cache.capability', true) IS DISTINCT FROM 'dependency_revision' THEN
        RAISE EXCEPTION 'dependency revision mutation requires cache capability';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.revision <> 1 THEN
            RAISE EXCEPTION 'dependency revision must begin at one';
        END IF;
        NEW.updated_at := clock_timestamp();
        RETURN NEW;
    END IF;
    IF NEW.namespace_key IS DISTINCT FROM OLD.namespace_key
       OR NEW.dependency_kind IS DISTINCT FROM OLD.dependency_kind
       OR NEW.dependency_id IS DISTINCT FROM OLD.dependency_id
       OR NEW.revision <> OLD.revision + 1 THEN
        RAISE EXCEPTION 'dependency revision identity or sequence is immutable';
    END IF;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS cache_dependency_revision_guard ON cache.cache_dependency_revision;
CREATE TRIGGER cache_dependency_revision_guard
    BEFORE INSERT OR UPDATE ON cache.cache_dependency_revision
    FOR EACH ROW EXECUTE FUNCTION cache.enforce_dependency_revision();

-- Durable invalidation records are the reconciliation authority.  NOTIFY is
-- emitted only as a bounded wake hint and never carries evidence or payloads.
CREATE TABLE IF NOT EXISTS cache.cache_invalidation (
    event_id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    invalidation_id uuid NOT NULL UNIQUE,
    namespace_key text NOT NULL REFERENCES cache.cache_namespace_epoch(namespace_key),
    dependency_kind text NOT NULL CHECK (dependency_kind IN ('source','project','semantic_model','deployment','custom')),
    dependency_id text NOT NULL CHECK (dependency_id = btrim(dependency_id) AND octet_length(dependency_id) BETWEEN 1 AND 255),
    dependency_digest text CHECK (dependency_digest IS NULL OR dependency_digest ~ '^sha256:[0-9a-f]{64}$'),
    namespace_epoch bigint NOT NULL CHECK (namespace_epoch > 0),
    retired_manifests bigint NOT NULL DEFAULT 0 CHECK (retired_manifests >= 0),
    idempotency_key text NOT NULL CHECK (idempotency_key = btrim(idempotency_key) AND octet_length(idempotency_key) BETWEEN 1 AND 255),
    reason text NOT NULL CHECK (reason = btrim(reason) AND octet_length(reason) BETWEEN 1 AND 255),
    evidence jsonb NOT NULL CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb AND octet_length(evidence::text) <= 4096),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (namespace_key, dependency_kind, dependency_id, namespace_epoch, idempotency_key)
);
CREATE INDEX IF NOT EXISTS cache_invalidation_reconcile_idx ON cache.cache_invalidation(event_id);
CREATE INDEX IF NOT EXISTS cache_invalidation_namespace_idx ON cache.cache_invalidation(namespace_key, event_id);
CREATE UNIQUE INDEX IF NOT EXISTS cache_invalidation_idempotency_idx ON cache.cache_invalidation(namespace_key,idempotency_key);

CREATE OR REPLACE FUNCTION cache.enforce_invalidation_append_only()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, cache
AS $$
BEGIN
    IF TG_OP <> 'INSERT' AND current_setting('cache.capability', true) IS DISTINCT FROM 'maintenance' THEN
        RAISE EXCEPTION 'invalidation history is append-only';
    END IF;
    IF TG_OP = 'INSERT' AND current_setting('cache.capability', true) IS DISTINCT FROM 'invalidation' THEN
        RAISE EXCEPTION 'invalidation insertion requires cache capability';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS cache_invalidation_append_only ON cache.cache_invalidation;
CREATE TRIGGER cache_invalidation_append_only
    BEFORE INSERT OR UPDATE OR DELETE ON cache.cache_invalidation
    FOR EACH ROW EXECUTE FUNCTION cache.enforce_invalidation_append_only();

CREATE OR REPLACE FUNCTION cache.notify_invalidation_hint()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, cache
AS $$
BEGIN
    PERFORM pg_notify('leapview_cache_invalidation',
        left(json_build_object('event_id', NEW.event_id, 'namespace', NEW.namespace_key)::text, 7900));
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS cache_invalidation_notify ON cache.cache_invalidation;
CREATE TRIGGER cache_invalidation_notify
    AFTER INSERT ON cache.cache_invalidation
    FOR EACH ROW EXECUTE FUNCTION cache.notify_invalidation_hint();

CREATE OR REPLACE FUNCTION cache.prune_coordination(p_before timestamptz, p_limit integer)
RETURNS TABLE(invalidations bigint, expired_leases bigint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, cache
AS $$
DECLARE
    cutoff timestamptz := LEAST(p_before, clock_timestamp());
    remaining integer;
BEGIN
    IF p_before IS NULL OR p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'invalid cache prune bounds';
    END IF;
    PERFORM set_config('cache.capability', 'maintenance', true);
    WITH doomed AS (
        SELECT event_id FROM cache.cache_invalidation
        WHERE created_at < cutoff ORDER BY event_id LIMIT p_limit FOR UPDATE SKIP LOCKED
    )
    DELETE FROM cache.cache_invalidation i USING doomed d WHERE i.event_id=d.event_id;
    GET DIAGNOSTICS invalidations = ROW_COUNT;
    remaining := p_limit - invalidations::integer;
    IF remaining <= 0 THEN
        expired_leases := 0;
        RETURN NEXT;
        RETURN;
    END IF;
    WITH doomed AS (
        SELECT lease_id FROM cache.cache_fill_lease
        WHERE expires_at < clock_timestamp() ORDER BY expires_at, lease_id LIMIT remaining FOR UPDATE SKIP LOCKED
    )
    DELETE FROM cache.cache_fill_lease l USING doomed d WHERE l.lease_id=d.lease_id;
    GET DIAGNOSTICS expired_leases = ROW_COUNT;
    RETURN NEXT;
END;
$$;

CREATE OR REPLACE FUNCTION cache.enforce_manifest_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.state <> 'admitted' OR current_setting('cache.capability', true) IS DISTINCT FROM 'publish_manifest' THEN
            RAISE EXCEPTION 'manifest admission requires cache publication capability';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'cache manifest deletion requires the retention reconciler';
    END IF;
    IF session_user <> 'postgres' AND current_setting('cache.capability', true) IS DISTINCT FROM 'manifest_lifecycle' THEN
        RAISE EXCEPTION 'manifest lifecycle mutation requires cache capability';
    END IF;
    IF NEW.manifest_id IS DISTINCT FROM OLD.manifest_id
       OR NEW.partition_kind IS DISTINCT FROM OLD.partition_kind
       OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.environment IS DISTINCT FROM OLD.environment
       OR NEW.candidate_id IS DISTINCT FROM OLD.candidate_id
       OR NEW.partition_format_version IS DISTINCT FROM OLD.partition_format_version
       OR NEW.dependency_digest IS DISTINCT FROM OLD.dependency_digest
       OR NEW.policy_fingerprint IS DISTINCT FROM OLD.policy_fingerprint
       OR NEW.canonical_query_digest IS DISTINCT FROM OLD.canonical_query_digest
       OR NEW.key_format_version IS DISTINCT FROM OLD.key_format_version
       OR NEW.storage_security_domain IS DISTINCT FROM OLD.storage_security_domain
       OR NEW.object_digest IS DISTINCT FROM OLD.object_digest
       OR NEW.object_key IS DISTINCT FROM OLD.object_key
       OR NEW.byte_size IS DISTINCT FROM OLD.byte_size
       OR NEW.metadata IS DISTINCT FROM OLD.metadata
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
        RAISE EXCEPTION 'cache manifest identity, object, metadata, and expiry fields are immutable';
    END IF;
    IF NEW.state = OLD.state THEN
        IF NEW.retired_at IS DISTINCT FROM OLD.retired_at OR NEW.expired_at IS DISTINCT FROM OLD.expired_at
           OR NEW.retire_evidence IS DISTINCT FROM OLD.retire_evidence OR NEW.expire_evidence IS DISTINCT FROM OLD.expire_evidence THEN
            RAISE EXCEPTION 'cache manifest lifecycle timestamps are immutable';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.state = 'admitted' AND NEW.state = 'retiring' THEN
        IF NEW.retire_evidence IS NULL THEN
            RAISE EXCEPTION 'manifest retirement evidence is required';
        END IF;
        NEW.retired_at := COALESCE(NEW.retired_at, clock_timestamp());
        NEW.expired_at := NULL;
        NEW.expire_evidence := NULL;
        RETURN NEW;
    END IF;
    IF OLD.state = 'retiring' AND NEW.state = 'expired' THEN
        IF NEW.expire_evidence IS NULL THEN
            RAISE EXCEPTION 'manifest expiry evidence is required';
        END IF;
        NEW.retired_at := OLD.retired_at;
        NEW.expired_at := COALESCE(NEW.expired_at, clock_timestamp());
        NEW.retire_evidence := OLD.retire_evidence;
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'invalid cache manifest lifecycle transition % -> %', OLD.state, NEW.state;
END;
$$;
DROP TRIGGER IF EXISTS cache_manifest_lifecycle ON cache.cache_manifest;
CREATE TRIGGER cache_manifest_lifecycle
    BEFORE INSERT OR UPDATE OR DELETE ON cache.cache_manifest
    FOR EACH ROW EXECUTE FUNCTION cache.enforce_manifest_lifecycle();

CREATE TABLE IF NOT EXISTS cache.cache_fill_lease (
    lease_id uuid PRIMARY KEY,
    manifest_id uuid REFERENCES cache.cache_manifest(manifest_id),
    cache_key text NOT NULL CHECK (cache_key ~ '^sha256:[0-9a-f]{64}$'),
    -- namespace_key and namespace_epoch are the invalidation fence.
    namespace_key text NOT NULL REFERENCES cache.cache_namespace_epoch(namespace_key) CHECK (namespace_key = btrim(namespace_key) AND octet_length(namespace_key) BETWEEN 1 AND 2048),
    namespace_epoch bigint NOT NULL CHECK (namespace_epoch > 0),
    owner_id text NOT NULL CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    fencing_epoch bigint NOT NULL CHECK (fencing_epoch > 0),
    expires_at timestamptz NOT NULL,
    acquired_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (cache_key),
    CHECK (expires_at > acquired_at)
);
CREATE INDEX IF NOT EXISTS cache_fill_lease_namespace_idx ON cache.cache_fill_lease(namespace_key, namespace_epoch);

CREATE OR REPLACE FUNCTION cache.enforce_fill_lease()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, cache
AS $$
DECLARE current_epoch bigint;
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF current_setting('cache.capability', true) IS DISTINCT FROM 'acquire_fill' THEN
            RAISE EXCEPTION 'fill lease acquisition requires cache capability';
        END IF;
        SELECT epoch INTO current_epoch FROM cache.cache_namespace_epoch WHERE namespace_key = NEW.namespace_key;
        IF current_epoch IS NULL OR NEW.namespace_epoch <> current_epoch OR NEW.fencing_epoch <> 1 THEN
            RAISE EXCEPTION 'fill lease namespace epoch or fence is invalid';
        END IF;
        RETURN NEW;
    END IF;
    IF session_user = 'postgres' THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF current_setting('cache.capability', true) IS DISTINCT FROM 'maintenance' THEN
            RAISE EXCEPTION 'fill lease deletion requires maintenance capability';
        END IF;
        RETURN OLD;
    END IF;
    IF current_setting('cache.capability', true) NOT IN ('acquire_fill','renew_fill','release_fill','publish_manifest') THEN
        RAISE EXCEPTION 'fill lease mutation requires cache capability';
    END IF;
    IF NEW.lease_id IS DISTINCT FROM OLD.lease_id OR NEW.cache_key IS DISTINCT FROM OLD.cache_key THEN
        IF current_setting('cache.capability', true) IS DISTINCT FROM 'acquire_fill' OR OLD.expires_at > clock_timestamp() OR NEW.fencing_epoch <> OLD.fencing_epoch + 1 THEN
            RAISE EXCEPTION 'fill lease fence takeover is invalid';
        END IF;
    ELSE
        IF NEW.namespace_key IS DISTINCT FROM OLD.namespace_key
           OR NEW.namespace_epoch IS DISTINCT FROM OLD.namespace_epoch
           OR NEW.owner_id IS DISTINCT FROM OLD.owner_id
           OR NEW.fencing_epoch IS DISTINCT FROM OLD.fencing_epoch
           OR NEW.acquired_at IS DISTINCT FROM OLD.acquired_at THEN
            RAISE EXCEPTION 'fill lease identity is immutable';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS cache_fill_lease_guard ON cache.cache_fill_lease;
CREATE TRIGGER cache_fill_lease_guard
    BEFORE INSERT OR UPDATE OR DELETE ON cache.cache_fill_lease
    FOR EACH ROW EXECUTE FUNCTION cache.enforce_fill_lease();

CREATE TABLE IF NOT EXISTS cache.cache_retention_root (
    root_id uuid PRIMARY KEY,
    manifest_id uuid NOT NULL REFERENCES cache.cache_manifest(manifest_id),
    state text NOT NULL CHECK (state IN ('live','retiring','expired')),
    reason text NOT NULL CHECK (reason = btrim(reason) AND octet_length(reason) BETWEEN 1 AND 255),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    retired_at timestamptz,
    expired_at timestamptz,
    retire_evidence jsonb,
    expire_evidence jsonb,
    CHECK ((state = 'live' AND retired_at IS NULL AND expired_at IS NULL AND retire_evidence IS NULL AND expire_evidence IS NULL)
        OR (state = 'retiring' AND retired_at IS NOT NULL AND expired_at IS NULL AND retire_evidence IS NOT NULL AND expire_evidence IS NULL)
        OR (state = 'expired' AND retired_at IS NOT NULL AND expired_at IS NOT NULL AND retire_evidence IS NOT NULL AND expire_evidence IS NOT NULL)),
    CHECK (retire_evidence IS NULL OR (jsonb_typeof(retire_evidence) = 'object' AND retire_evidence <> '{}'::jsonb AND retire_evidence->>'version' = '1' AND octet_length(retire_evidence::text) <= 4096)),
    CHECK (expire_evidence IS NULL OR (jsonb_typeof(expire_evidence) = 'object' AND expire_evidence <> '{}'::jsonb AND expire_evidence->>'version' = '1' AND octet_length(expire_evidence::text) <= 4096)),
    CHECK ((retired_at IS NULL OR retired_at >= created_at)
        AND (expired_at IS NULL OR expired_at >= COALESCE(retired_at, created_at)))
);

CREATE OR REPLACE FUNCTION cache.enforce_retention_root_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF current_setting('cache.capability', true) IS DISTINCT FROM 'retention_root' THEN
            RAISE EXCEPTION 'retention root insertion requires cache capability';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'cache retention root deletion requires the retention reconciler';
    END IF;
    IF session_user <> 'postgres' AND current_setting('cache.capability', true) IS DISTINCT FROM 'retention_root' THEN
        RAISE EXCEPTION 'retention root lifecycle mutation requires cache capability';
    END IF;
    IF NEW.root_id IS DISTINCT FROM OLD.root_id
       OR NEW.manifest_id IS DISTINCT FROM OLD.manifest_id
       OR NEW.reason IS DISTINCT FROM OLD.reason
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'cache retention root identity is immutable';
    END IF;
    IF NEW.state = OLD.state THEN
        IF NEW.retired_at IS DISTINCT FROM OLD.retired_at OR NEW.expired_at IS DISTINCT FROM OLD.expired_at
           OR NEW.retire_evidence IS DISTINCT FROM OLD.retire_evidence OR NEW.expire_evidence IS DISTINCT FROM OLD.expire_evidence THEN
            RAISE EXCEPTION 'cache retention root lifecycle timestamps are immutable';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.state = 'live' AND NEW.state = 'retiring' THEN
        IF NEW.retire_evidence IS NULL THEN
            RAISE EXCEPTION 'retention root retirement evidence is required';
        END IF;
        NEW.expired_at := NULL;
        NEW.retired_at := COALESCE(NEW.retired_at, clock_timestamp());
        NEW.expire_evidence := NULL;
        RETURN NEW;
    END IF;
    IF OLD.state = 'retiring' AND NEW.state = 'expired' THEN
        IF NEW.expire_evidence IS NULL THEN
            RAISE EXCEPTION 'retention root expiry evidence is required';
        END IF;
        NEW.retired_at := OLD.retired_at;
        NEW.expired_at := COALESCE(NEW.expired_at, clock_timestamp());
        NEW.retire_evidence := OLD.retire_evidence;
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'invalid cache retention root lifecycle transition % -> %', OLD.state, NEW.state;
END;
$$;
DROP TRIGGER IF EXISTS cache_retention_root_lifecycle ON cache.cache_retention_root;
CREATE TRIGGER cache_retention_root_lifecycle
    BEFORE INSERT OR UPDATE OR DELETE ON cache.cache_retention_root
    FOR EACH ROW EXECUTE FUNCTION cache.enforce_retention_root_lifecycle();

-- Capability entry points.  Runtime roles receive EXECUTE on these functions,
-- but no table DML privileges.  Each function sets a transaction-local marker
-- consumed by the immutable/lifecycle triggers above.
CREATE OR REPLACE FUNCTION cache.ensure_namespace(p_namespace_key text, p_kind text, p_project text, p_environment text, p_candidate text)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, cache
AS $$
DECLARE out_epoch bigint;
BEGIN
    PERFORM set_config('cache.capability', 'namespace_epoch', true);
    INSERT INTO cache.cache_namespace_epoch(namespace_key,partition_kind,project_id,environment,candidate_id,epoch)
    VALUES (p_namespace_key,p_kind,p_project,p_environment,p_candidate,1)
    ON CONFLICT (namespace_key) DO NOTHING;
    SELECT epoch INTO out_epoch FROM cache.cache_namespace_epoch WHERE namespace_key=p_namespace_key FOR UPDATE;
    IF out_epoch IS NULL OR p_namespace_key IS DISTINCT FROM cache.namespace_key(p_kind,p_project,p_environment,p_candidate) THEN
        RAISE EXCEPTION 'namespace identity is invalid';
    END IF;
    RETURN out_epoch;
END;
$$;

CREATE OR REPLACE FUNCTION cache.advance_dependency_revision(p_namespace_key text, p_kind text, p_dependency_id text, p_digest text, p_expected bigint, p_evidence jsonb, p_idempotency_key text)
RETURNS TABLE(revision bigint, revision_digest text, updated_at timestamptz, changed boolean, old_digest text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, cache
AS $$
DECLARE current_revision bigint;
BEGIN
    PERFORM set_config('cache.capability', 'dependency_revision', true);
    SELECT r.revision,r.revision_digest,r.updated_at INTO current_revision,revision_digest,updated_at
      FROM cache.cache_dependency_revision r
      WHERE r.namespace_key=p_namespace_key AND r.dependency_kind=p_kind AND r.dependency_id=p_dependency_id FOR UPDATE;
    IF NOT FOUND THEN
        IF p_expected > 0 THEN RAISE EXCEPTION 'dependency revision conflict'; END IF;
        revision := 1; changed := false; old_digest := NULL;
        INSERT INTO cache.cache_dependency_revision(namespace_key,dependency_kind,dependency_id,revision,revision_digest)
        VALUES (p_namespace_key,p_kind,p_dependency_id,1,p_digest)
        RETURNING cache_dependency_revision.updated_at INTO updated_at;
        revision_digest := p_digest;
        RETURN NEXT; RETURN;
    END IF;
    IF revision_digest = p_digest THEN
        IF (current_revision = 1 AND p_expected IN (0,1)) OR p_expected = current_revision OR (current_revision > 1 AND p_expected = current_revision - 1 AND EXISTS (SELECT 1 FROM cache.cache_invalidation i WHERE i.namespace_key=p_namespace_key AND i.dependency_kind=p_kind AND i.dependency_id=p_dependency_id AND i.idempotency_key=p_idempotency_key AND i.evidence IS NOT DISTINCT FROM p_evidence)) THEN
            revision := current_revision; changed := false; old_digest := revision_digest; RETURN NEXT; RETURN;
        END IF;
        RAISE EXCEPTION 'dependency revision conflict';
    END IF;
    IF p_expected <= 0 THEN RAISE EXCEPTION 'dependency revision expectation is required'; END IF;
    IF current_revision <> p_expected THEN RAISE EXCEPTION 'dependency revision conflict'; END IF;
    IF p_evidence IS NULL OR jsonb_typeof(p_evidence) <> 'object' OR p_evidence = '{}'::jsonb OR p_evidence->>'version' <> '1' OR NULLIF(btrim(p_evidence->>'reason'),'') IS NULL THEN
        RAISE EXCEPTION 'revision change evidence is required';
    END IF;
    old_digest := revision_digest;
    revision := current_revision + 1;
    changed := true;
    UPDATE cache.cache_dependency_revision AS d SET revision=current_revision+1,revision_digest=p_digest WHERE d.namespace_key=p_namespace_key AND d.dependency_kind=p_kind AND d.dependency_id=p_dependency_id
    RETURNING d.updated_at INTO updated_at;
    revision_digest := p_digest;
    RETURN NEXT;
END;
$$;

CREATE OR REPLACE FUNCTION cache.record_dependency_revision(p_namespace_key text, p_kind text, p_dependency_id text, p_digest text, p_expected bigint, p_invalidation_id uuid, p_idempotency_key text, p_reason text, p_evidence jsonb)
RETURNS TABLE(revision bigint, revision_digest text, updated_at timestamptz, changed boolean, old_digest text, invalidation_id uuid, event_id bigint, namespace_epoch bigint, retired_manifests bigint, created_at timestamptz)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, cache
AS $$
BEGIN
    SELECT a.revision,a.revision_digest,a.updated_at,a.changed,a.old_digest INTO revision,revision_digest,updated_at,changed,old_digest
      FROM cache.advance_dependency_revision(p_namespace_key,p_kind,p_dependency_id,p_digest,p_expected,p_evidence,p_idempotency_key) a;
    IF changed THEN
        SELECT i.invalidation_id,i.event_id,i.namespace_epoch,i.retired_manifests,i.created_at INTO invalidation_id,event_id,namespace_epoch,retired_manifests,created_at
          FROM cache.invalidate_namespace(p_invalidation_id,p_namespace_key,p_kind,p_dependency_id,old_digest,0,p_idempotency_key,p_reason,p_evidence) i;
    END IF;
    RETURN NEXT;
END;
$$;

CREATE OR REPLACE FUNCTION cache.invalidate_namespace(p_invalidation_id uuid, p_namespace_key text, p_kind text, p_dependency_id text, p_dependency_digest text, p_expected_epoch bigint, p_idempotency_key text, p_reason text, p_evidence jsonb)
RETURNS TABLE(invalidation_id uuid, event_id bigint, namespace_epoch bigint, retired_manifests bigint, created_at timestamptz)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, cache
AS $$
DECLARE previous_epoch bigint; existing cache.cache_invalidation%ROWTYPE; retired bigint; now_epoch bigint;
BEGIN
    PERFORM set_config('cache.capability', 'namespace_epoch', true);
    SELECT epoch INTO previous_epoch FROM cache.cache_namespace_epoch WHERE namespace_key=p_namespace_key FOR UPDATE;
    IF previous_epoch IS NULL THEN RAISE EXCEPTION 'namespace not found'; END IF;
    SELECT * INTO existing FROM cache.cache_invalidation WHERE namespace_key=p_namespace_key AND idempotency_key=p_idempotency_key;
    IF FOUND THEN
        IF existing.dependency_kind IS DISTINCT FROM p_kind OR existing.dependency_id IS DISTINCT FROM p_dependency_id OR COALESCE(existing.dependency_digest,'') IS DISTINCT FROM COALESCE(p_dependency_digest,'') OR existing.reason IS DISTINCT FROM p_reason OR existing.evidence IS DISTINCT FROM p_evidence THEN
            RAISE EXCEPTION 'cache invalidation conflict';
        END IF;
        invalidation_id := existing.invalidation_id; event_id := existing.event_id; namespace_epoch := existing.namespace_epoch; retired_manifests := existing.retired_manifests; created_at := existing.created_at; RETURN NEXT; RETURN;
    END IF;
    IF p_expected_epoch > 0 AND previous_epoch <> p_expected_epoch THEN RAISE EXCEPTION 'namespace epoch conflict'; END IF;
    UPDATE cache.cache_namespace_epoch SET epoch=epoch+1 WHERE namespace_key=p_namespace_key RETURNING epoch INTO now_epoch;
    PERFORM set_config('cache.capability', 'manifest_lifecycle', true);
    UPDATE cache.cache_manifest SET state='retiring', retired_at=clock_timestamp(), retire_evidence=p_evidence
      WHERE partition_kind=(SELECT partition_kind FROM cache.cache_namespace_epoch WHERE namespace_key=p_namespace_key)
        AND project_id=(SELECT project_id FROM cache.cache_namespace_epoch WHERE namespace_key=p_namespace_key)
        AND environment=(SELECT environment FROM cache.cache_namespace_epoch WHERE namespace_key=p_namespace_key)
        AND candidate_id IS NOT DISTINCT FROM (SELECT candidate_id FROM cache.cache_namespace_epoch WHERE namespace_key=p_namespace_key)
        AND partition_format_version=1 AND state='admitted'
        AND (NULLIF(p_dependency_digest,'') IS NULL OR dependency_digest=p_dependency_digest);
    GET DIAGNOSTICS retired = ROW_COUNT;
    PERFORM set_config('cache.capability', 'invalidation', true);
    INSERT INTO cache.cache_invalidation(invalidation_id,namespace_key,dependency_kind,dependency_id,dependency_digest,namespace_epoch,retired_manifests,idempotency_key,reason,evidence)
      VALUES (p_invalidation_id,p_namespace_key,p_kind,p_dependency_id,NULLIF(p_dependency_digest,''),now_epoch,retired,p_idempotency_key,p_reason,p_evidence)
      RETURNING cache_invalidation.event_id,cache_invalidation.created_at INTO event_id,created_at;
    invalidation_id := p_invalidation_id; namespace_epoch := now_epoch; retired_manifests := retired;
    RETURN NEXT;
END;
$$;

CREATE OR REPLACE FUNCTION cache.acquire_fill(p_lease_id uuid, p_cache_key text, p_namespace_key text, p_namespace_epoch bigint, p_owner_id text, p_duration interval)
RETURNS TABLE(lease_id uuid, cache_key text, namespace_epoch bigint, owner_id text, fencing_epoch bigint, expires_at timestamptz, acquired_at timestamptz)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, cache
AS $$
DECLARE current_epoch bigint;
BEGIN
    PERFORM set_config('cache.capability', 'acquire_fill', true);
    IF p_duration <= interval '0' OR p_duration > interval '24 hours' THEN RAISE EXCEPTION 'fill lease duration is out of bounds'; END IF;
    SELECT epoch INTO current_epoch FROM cache.cache_namespace_epoch WHERE namespace_key=p_namespace_key FOR UPDATE;
    IF current_epoch IS NULL OR current_epoch <> p_namespace_epoch THEN RAISE EXCEPTION 'stale namespace epoch'; END IF;
    INSERT INTO cache.cache_fill_lease(lease_id,cache_key,namespace_key,namespace_epoch,owner_id,fencing_epoch,expires_at,acquired_at)
      VALUES (p_lease_id,p_cache_key,p_namespace_key,p_namespace_epoch,p_owner_id,1,clock_timestamp()+p_duration,clock_timestamp())
      ON CONFLICT ON CONSTRAINT cache_fill_lease_cache_key_key DO UPDATE SET lease_id=EXCLUDED.lease_id,manifest_id=NULL,namespace_key=EXCLUDED.namespace_key,namespace_epoch=EXCLUDED.namespace_epoch,owner_id=EXCLUDED.owner_id,fencing_epoch=cache.cache_fill_lease.fencing_epoch+1,expires_at=EXCLUDED.expires_at,acquired_at=EXCLUDED.acquired_at
      WHERE cache.cache_fill_lease.expires_at <= clock_timestamp();
    RETURN QUERY SELECT l.lease_id,l.cache_key,l.namespace_epoch,l.owner_id,l.fencing_epoch,l.expires_at,l.acquired_at FROM cache.cache_fill_lease l WHERE l.lease_id=p_lease_id;
END;
$$;

CREATE OR REPLACE FUNCTION cache.renew_fill(p_lease_id uuid, p_cache_key text, p_owner_id text, p_fencing_epoch bigint, p_duration interval)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
BEGIN
    PERFORM set_config('cache.capability', 'renew_fill', true);
    IF p_duration <= interval '0' OR p_duration > interval '24 hours' THEN RAISE EXCEPTION 'fill lease duration is out of bounds'; END IF;
    UPDATE cache.cache_fill_lease SET expires_at=GREATEST(expires_at,clock_timestamp()+p_duration)
      WHERE lease_id=p_lease_id AND cache_key=p_cache_key AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch AND expires_at>clock_timestamp();
    RETURN FOUND;
END;
$$;

CREATE OR REPLACE FUNCTION cache.release_fill(p_lease_id uuid, p_cache_key text, p_owner_id text, p_fencing_epoch bigint)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
BEGIN
    PERFORM set_config('cache.capability', 'release_fill', true);
    UPDATE cache.cache_fill_lease SET expires_at=clock_timestamp()
      WHERE lease_id=p_lease_id AND cache_key=p_cache_key AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch;
    RETURN FOUND;
END;
$$;

CREATE OR REPLACE FUNCTION cache.admit_manifest(p_manifest_id uuid, p_lease_id uuid, p_cache_key text, p_owner_id text, p_fencing_epoch bigint, p_namespace_key text, p_namespace_epoch bigint, p_partition_kind text, p_project_id text, p_environment text, p_candidate_id text, p_partition_format_version bigint, p_dependency_digest text, p_policy_fingerprint text, p_query_digest text, p_key_format_version bigint, p_storage_domain text, p_object_digest text, p_object_key text, p_byte_size bigint, p_metadata jsonb, p_expires_at timestamptz)
RETURNS uuid
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
DECLARE actual_id uuid; existing cache.cache_manifest%ROWTYPE; current_epoch bigint;
BEGIN
    PERFORM set_config('cache.capability', 'publish_manifest', true);
    SELECT n.epoch INTO current_epoch FROM cache.cache_namespace_epoch n WHERE n.namespace_key=p_namespace_key FOR UPDATE;
    IF current_epoch IS NULL OR current_epoch <> p_namespace_epoch THEN
        RAISE EXCEPTION 'cache stale fill fence';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM cache.cache_fill_lease l WHERE l.lease_id=p_lease_id AND l.cache_key=p_cache_key AND l.owner_id=p_owner_id AND l.fencing_epoch=p_fencing_epoch AND l.namespace_key=p_namespace_key AND l.namespace_epoch=p_namespace_epoch AND l.expires_at>clock_timestamp() AND l.manifest_id IS NULL) THEN
        RAISE EXCEPTION 'cache stale fill fence';
    END IF;
    INSERT INTO cache.cache_manifest(manifest_id,partition_kind,project_id,environment,candidate_id,partition_format_version,dependency_digest,policy_fingerprint,canonical_query_digest,key_format_version,storage_security_domain,object_digest,object_key,byte_size,metadata,state,expires_at)
      VALUES (p_manifest_id,p_partition_kind,p_project_id,p_environment,p_candidate_id,p_partition_format_version,p_dependency_digest,p_policy_fingerprint,p_query_digest,p_key_format_version,p_storage_domain,p_object_digest,p_object_key,p_byte_size,p_metadata,'admitted',p_expires_at)
      ON CONFLICT DO NOTHING;
    IF NOT EXISTS (SELECT 1 FROM cache.cache_namespace_epoch n WHERE n.namespace_key=p_namespace_key AND n.epoch=p_namespace_epoch AND n.partition_kind=p_partition_kind AND n.project_id=p_project_id AND n.environment=p_environment AND n.candidate_id IS NOT DISTINCT FROM p_candidate_id) THEN
        RAISE EXCEPTION 'cache stale fill fence';
    END IF;
    SELECT * INTO existing FROM cache.cache_manifest
      WHERE partition_kind=p_partition_kind AND project_id=p_project_id AND environment=p_environment
        AND candidate_id IS NOT DISTINCT FROM p_candidate_id AND partition_format_version=p_partition_format_version
        AND dependency_digest=p_dependency_digest AND policy_fingerprint=p_policy_fingerprint
        AND canonical_query_digest=p_query_digest AND key_format_version=p_key_format_version
        AND state='admitted' FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'cache manifest conflict'; END IF;
    IF existing.storage_security_domain IS DISTINCT FROM p_storage_domain OR existing.object_digest IS DISTINCT FROM p_object_digest OR existing.object_key IS DISTINCT FROM p_object_key OR existing.byte_size IS DISTINCT FROM p_byte_size OR existing.metadata IS DISTINCT FROM p_metadata OR existing.expires_at IS DISTINCT FROM p_expires_at THEN
        RAISE EXCEPTION 'cache manifest conflict';
    END IF;
    actual_id := existing.manifest_id;
    PERFORM set_config('cache.capability', 'publish_manifest', true);
    UPDATE cache.cache_fill_lease SET manifest_id=actual_id,expires_at=clock_timestamp()
      WHERE lease_id=p_lease_id AND cache_key=p_cache_key AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch AND manifest_id IS NULL;
    IF NOT FOUND THEN RAISE EXCEPTION 'cache stale fill fence'; END IF;
    RETURN actual_id;
END;
$$;

CREATE OR REPLACE FUNCTION cache.add_retention_root(p_root_id uuid, p_manifest_id uuid, p_reason text)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
DECLARE manifest_state text; manifest_expires_at timestamptz;
BEGIN
    PERFORM set_config('cache.capability', 'retention_root', true);
    SELECT state,expires_at INTO manifest_state,manifest_expires_at FROM cache.cache_manifest WHERE manifest_id=p_manifest_id FOR UPDATE;
    IF NOT FOUND OR manifest_state <> 'admitted' OR (manifest_expires_at IS NOT NULL AND manifest_expires_at<=clock_timestamp()) THEN RAISE EXCEPTION 'manifest not found'; END IF;
    INSERT INTO cache.cache_retention_root(root_id,manifest_id,state,reason) VALUES (p_root_id,p_manifest_id,'live',p_reason) ON CONFLICT DO NOTHING;
    RETURN FOUND;
END;
$$;

CREATE OR REPLACE FUNCTION cache.retire_retention_root(p_root_id uuid, p_evidence jsonb)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
BEGIN
    PERFORM set_config('cache.capability', 'retention_root', true);
    UPDATE cache.cache_retention_root SET state='retiring',retired_at=clock_timestamp(),retire_evidence=p_evidence WHERE root_id=p_root_id AND state='live';
    RETURN FOUND;
END;
$$;

CREATE OR REPLACE FUNCTION cache.expire_retention_root(p_root_id uuid, p_evidence jsonb)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
BEGIN
    PERFORM set_config('cache.capability', 'retention_root', true);
    UPDATE cache.cache_retention_root SET state='expired',expired_at=clock_timestamp(),expire_evidence=p_evidence WHERE root_id=p_root_id AND state='retiring';
    RETURN FOUND;
END;
$$;

CREATE OR REPLACE FUNCTION cache.expire_manifest(p_manifest_id uuid, p_evidence jsonb)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
DECLARE manifest_state text;
BEGIN
    PERFORM set_config('cache.capability', 'manifest_lifecycle', true);
    SELECT state INTO manifest_state FROM cache.cache_manifest WHERE manifest_id=p_manifest_id FOR UPDATE;
    IF NOT FOUND OR manifest_state <> 'retiring' THEN RETURN false; END IF;
    IF EXISTS (SELECT 1 FROM cache.cache_retention_root WHERE manifest_id=p_manifest_id AND state IN ('live','retiring')) THEN RAISE EXCEPTION 'cache manifest has live retention roots'; END IF;
    UPDATE cache.cache_manifest SET state='expired',expired_at=clock_timestamp(),expire_evidence=p_evidence WHERE manifest_id=p_manifest_id AND state='retiring';
    RETURN FOUND;
END;
$$;

-- Cache metadata is not public.  Runtime may coordinate and publish
-- manifests, readonly/backup roles can inspect state, and only the owner or
-- migrator can alter the capability DDL.  Role existence is conditional so
-- isolated repository conformance databases remain self-contained.
REVOKE ALL ON SCHEMA cache FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA cache FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA cache FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA cache FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA cache TO leapview_control_runtime;
        REVOKE ALL ON cache.cache_manifest, cache.cache_fill_lease,
            cache.cache_namespace_epoch, cache.cache_dependency_revision,
            cache.cache_invalidation, cache.cache_retention_root FROM leapview_control_runtime;
        REVOKE ALL ON FUNCTION cache.advance_dependency_revision(text,text,text,text,bigint,jsonb,text) FROM leapview_control_runtime;
        GRANT SELECT ON cache.cache_manifest, cache.cache_fill_lease,
            cache.cache_namespace_epoch, cache.cache_dependency_revision,
            cache.cache_invalidation, cache.cache_retention_root TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION cache.ensure_namespace(text,text,text,text,text),
            cache.record_dependency_revision(text,text,text,text,bigint,uuid,text,text,jsonb),
            cache.invalidate_namespace(uuid,text,text,text,text,bigint,text,text,jsonb),
            cache.acquire_fill(uuid,text,text,bigint,text,interval), cache.renew_fill(uuid,text,text,bigint,interval),
            cache.release_fill(uuid,text,text,bigint),
            cache.admit_manifest(uuid,uuid,text,text,bigint,text,bigint,text,text,text,text,bigint,text,text,text,bigint,text,text,text,bigint,jsonb,timestamptz),
            cache.add_retention_root(uuid,uuid,text), cache.retire_retention_root(uuid,jsonb), cache.expire_retention_root(uuid,jsonb),
            cache.expire_manifest(uuid,jsonb) TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA cache TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA cache TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA cache TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA cache TO leapview_control_backup;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA cache TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION cache.prune_coordination(timestamptz,integer) TO leapview_control_maintenance;
    END IF;
END
$$;
