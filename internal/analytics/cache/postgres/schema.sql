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
    UNIQUE NULLS NOT DISTINCT (partition_kind, project_id, environment, candidate_id, partition_format_version, dependency_digest, policy_fingerprint, canonical_query_digest, key_format_version),
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

CREATE OR REPLACE FUNCTION cache.enforce_manifest_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'cache manifest deletion requires the retention reconciler';
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
    BEFORE UPDATE OR DELETE ON cache.cache_manifest
    FOR EACH ROW EXECUTE FUNCTION cache.enforce_manifest_lifecycle();

CREATE TABLE IF NOT EXISTS cache.cache_fill_lease (
    lease_id uuid PRIMARY KEY,
    manifest_id uuid REFERENCES cache.cache_manifest(manifest_id),
    cache_key text NOT NULL CHECK (cache_key ~ '^sha256:[0-9a-f]{64}$'),
    owner_id text NOT NULL CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    fencing_epoch bigint NOT NULL CHECK (fencing_epoch > 0),
    expires_at timestamptz NOT NULL,
    acquired_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (cache_key),
    CHECK (expires_at > acquired_at)
);

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
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'cache retention root deletion requires the retention reconciler';
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
    BEFORE UPDATE OR DELETE ON cache.cache_retention_root
    FOR EACH ROW EXECUTE FUNCTION cache.enforce_retention_root_lifecycle();
