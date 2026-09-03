-- Forward-only cache capability migration adding durable L3 object and
-- pool-scan fencing. Revision 1 remains immutable; this migration owns every
-- L3-GC table, function, grant, and the fenced manifest-admission contract.

CREATE TABLE IF NOT EXISTS cache.cache_l3_object_fence (
    storage_security_domain text NOT NULL CHECK (storage_security_domain ~ '^sha256:[0-9a-f]{64}$'),
    object_key text NOT NULL CHECK (object_key = btrim(object_key) AND octet_length(object_key) BETWEEN 1 AND 2048),
    lease_id uuid NOT NULL UNIQUE,
    owner_id text NOT NULL CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    fencing_epoch bigint NOT NULL CHECK (fencing_epoch > 0),
    expires_at timestamptz NOT NULL,
    acquired_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (storage_security_domain, object_key),
    CHECK (object_key ~ ('(^|/)sd/' || storage_security_domain || '/sha256:[0-9a-f]{64}/sha256:[0-9a-f]{64}$')),
    CHECK (expires_at > acquired_at)
);

CREATE TABLE IF NOT EXISTS cache.cache_l3_gc_state (
    storage_security_domain text PRIMARY KEY CHECK (storage_security_domain ~ '^sha256:[0-9a-f]{64}$'),
    lease_id uuid NOT NULL UNIQUE,
    owner_id text NOT NULL CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    fencing_epoch bigint NOT NULL CHECK (fencing_epoch > 0),
    cursor_object_key text NOT NULL DEFAULT '' CHECK (cursor_object_key = btrim(cursor_object_key) AND octet_length(cursor_object_key) <= 2048),
    cycle bigint NOT NULL DEFAULT 1 CHECK (cycle > 0),
    expires_at timestamptz NOT NULL,
    acquired_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (cursor_object_key = '' OR cursor_object_key ~ ('(^|/)sd/' || storage_security_domain || '/sha256:[0-9a-f]{64}/sha256:[0-9a-f]{64}$')),
    CHECK (expires_at > acquired_at)
);

-- The baseline manifest table predates this forward migration. Bind every L3
-- reference to the exact security-domain path suffix so even a compromised
-- runtime role cannot admit an object outside the collector's domain scan.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid='cache.cache_manifest'::regclass
          AND conname='cache_manifest_l3_object_key_domain_check'
    ) THEN
        ALTER TABLE cache.cache_manifest
            ADD CONSTRAINT cache_manifest_l3_object_key_domain_check
            CHECK (object_key ~ ('(^|/)sd/' || storage_security_domain || '/sha256:[0-9a-f]{64}/sha256:[0-9a-f]{64}$'));
    END IF;
END;
$$;

-- Refresh takeover timestamps from the database clock instead of reusing the
-- caller's potentially stale values when an expired fill lease is replaced.
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
      ON CONFLICT ON CONSTRAINT cache_fill_lease_cache_key_key DO UPDATE SET lease_id=EXCLUDED.lease_id,manifest_id=NULL,namespace_key=EXCLUDED.namespace_key,namespace_epoch=EXCLUDED.namespace_epoch,owner_id=EXCLUDED.owner_id,fencing_epoch=cache.cache_fill_lease.fencing_epoch+1,expires_at=clock_timestamp()+p_duration,acquired_at=clock_timestamp()
      WHERE cache.cache_fill_lease.expires_at <= clock_timestamp();
    RETURN QUERY SELECT l.lease_id,l.cache_key,l.namespace_epoch,l.owner_id,l.fencing_epoch,l.expires_at,l.acquired_at FROM cache.cache_fill_lease l WHERE l.lease_id=p_lease_id;
END;
$$;

-- Keep coordination pruning bounded while collecting expired object fences.
CREATE OR REPLACE FUNCTION cache.prune_coordination(p_before timestamptz, p_limit integer)
RETURNS TABLE(invalidations bigint, expired_leases bigint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, cache
AS $$
DECLARE
    cutoff timestamptz := LEAST(p_before, clock_timestamp());
    remaining integer;
    expired_fill integer;
    expired_objects integer;
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
    GET DIAGNOSTICS expired_fill = ROW_COUNT;
    expired_leases := expired_fill;
    remaining := remaining - expired_fill;
    IF remaining > 0 THEN
        WITH doomed AS (
            SELECT lease_id FROM cache.cache_l3_object_fence
            WHERE expires_at < clock_timestamp() ORDER BY expires_at, lease_id LIMIT remaining FOR UPDATE SKIP LOCKED
        )
        DELETE FROM cache.cache_l3_object_fence f USING doomed d WHERE f.lease_id=d.lease_id;
        GET DIAGNOSTICS expired_objects = ROW_COUNT;
        expired_leases := expired_leases + expired_objects;
    END IF;
    RETURN NEXT;
END;
$$;

CREATE OR REPLACE FUNCTION cache.acquire_l3_object_fence(p_lease_id uuid, p_storage_domain text, p_object_key text, p_owner_id text, p_duration interval)
RETURNS TABLE(lease_id uuid, storage_security_domain text, object_key text, owner_id text, fencing_epoch bigint, expires_at timestamptz, acquired_at timestamptz)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
BEGIN
    IF p_duration <= interval '0' OR p_duration > interval '24 hours' THEN RAISE EXCEPTION 'L3 object-fence duration is out of bounds'; END IF;
    INSERT INTO cache.cache_l3_object_fence(storage_security_domain,object_key,lease_id,owner_id,fencing_epoch,expires_at,acquired_at)
      VALUES (p_storage_domain,p_object_key,p_lease_id,p_owner_id,1,clock_timestamp()+p_duration,clock_timestamp())
      ON CONFLICT ON CONSTRAINT cache_l3_object_fence_pkey DO UPDATE
      SET lease_id=EXCLUDED.lease_id,owner_id=EXCLUDED.owner_id,
          fencing_epoch=cache.cache_l3_object_fence.fencing_epoch+1,
          expires_at=clock_timestamp()+p_duration,acquired_at=clock_timestamp()
      WHERE cache.cache_l3_object_fence.expires_at <= clock_timestamp();
    RETURN QUERY SELECT f.lease_id,f.storage_security_domain,f.object_key,f.owner_id,f.fencing_epoch,f.expires_at,f.acquired_at
      FROM cache.cache_l3_object_fence f WHERE f.lease_id=p_lease_id;
END;
$$;

CREATE OR REPLACE FUNCTION cache.renew_l3_object_fence(p_lease_id uuid, p_storage_domain text, p_object_key text, p_owner_id text, p_fencing_epoch bigint, p_duration interval)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
BEGIN
    IF p_duration <= interval '0' OR p_duration > interval '24 hours' THEN RAISE EXCEPTION 'L3 object-fence duration is out of bounds'; END IF;
    UPDATE cache.cache_l3_object_fence SET expires_at=GREATEST(expires_at,clock_timestamp()+p_duration)
      WHERE lease_id=p_lease_id AND storage_security_domain=p_storage_domain AND object_key=p_object_key
        AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch AND expires_at>clock_timestamp();
    RETURN FOUND;
END;
$$;

CREATE OR REPLACE FUNCTION cache.release_l3_object_fence(p_lease_id uuid, p_storage_domain text, p_object_key text, p_owner_id text, p_fencing_epoch bigint)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
BEGIN
    UPDATE cache.cache_l3_object_fence SET expires_at=clock_timestamp()
      WHERE lease_id=p_lease_id AND storage_security_domain=p_storage_domain AND object_key=p_object_key
        AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch;
    RETURN FOUND;
END;
$$;

CREATE OR REPLACE FUNCTION cache.acquire_l3_gc_lease(p_lease_id uuid, p_storage_domain text, p_owner_id text, p_duration interval)
RETURNS TABLE(lease_id uuid, storage_security_domain text, owner_id text, fencing_epoch bigint, cursor_object_key text, cycle bigint, expires_at timestamptz, acquired_at timestamptz)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
BEGIN
    IF p_duration <= interval '0' OR p_duration > interval '24 hours' THEN RAISE EXCEPTION 'L3 GC lease duration is out of bounds'; END IF;
    INSERT INTO cache.cache_l3_gc_state(storage_security_domain,lease_id,owner_id,fencing_epoch,expires_at,acquired_at,updated_at)
      VALUES (p_storage_domain,p_lease_id,p_owner_id,1,clock_timestamp()+p_duration,clock_timestamp(),clock_timestamp())
      ON CONFLICT ON CONSTRAINT cache_l3_gc_state_pkey DO UPDATE
      SET lease_id=EXCLUDED.lease_id,owner_id=EXCLUDED.owner_id,
          fencing_epoch=cache.cache_l3_gc_state.fencing_epoch+1,
          expires_at=clock_timestamp()+p_duration,acquired_at=clock_timestamp(),updated_at=clock_timestamp()
      WHERE cache.cache_l3_gc_state.expires_at <= clock_timestamp();
    RETURN QUERY SELECT s.lease_id,s.storage_security_domain,s.owner_id,s.fencing_epoch,s.cursor_object_key,s.cycle,s.expires_at,s.acquired_at
      FROM cache.cache_l3_gc_state s WHERE s.lease_id=p_lease_id;
END;
$$;

CREATE OR REPLACE FUNCTION cache.renew_l3_gc_lease(p_lease_id uuid, p_storage_domain text, p_owner_id text, p_fencing_epoch bigint, p_duration interval)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
BEGIN
    IF p_duration <= interval '0' OR p_duration > interval '24 hours' THEN RAISE EXCEPTION 'L3 GC lease duration is out of bounds'; END IF;
    UPDATE cache.cache_l3_gc_state SET expires_at=GREATEST(expires_at,clock_timestamp()+p_duration),updated_at=clock_timestamp()
      WHERE lease_id=p_lease_id AND storage_security_domain=p_storage_domain AND owner_id=p_owner_id
        AND fencing_epoch=p_fencing_epoch AND expires_at>clock_timestamp();
    RETURN FOUND;
END;
$$;

CREATE OR REPLACE FUNCTION cache.release_l3_gc_lease(p_lease_id uuid, p_storage_domain text, p_owner_id text, p_fencing_epoch bigint)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
BEGIN
    UPDATE cache.cache_l3_gc_state SET expires_at=clock_timestamp(),updated_at=clock_timestamp()
      WHERE lease_id=p_lease_id AND storage_security_domain=p_storage_domain AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch;
    RETURN FOUND;
END;
$$;

CREATE OR REPLACE FUNCTION cache.advance_l3_gc_cursor(p_lease_id uuid, p_storage_domain text, p_owner_id text, p_fencing_epoch bigint, p_next_object_key text, p_complete boolean)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
BEGIN
    IF p_complete THEN
        IF p_next_object_key <> '' THEN RAISE EXCEPTION 'completed L3 GC cycle cannot retain a cursor'; END IF;
        UPDATE cache.cache_l3_gc_state SET cursor_object_key='',cycle=cycle+1,updated_at=clock_timestamp()
          WHERE lease_id=p_lease_id AND storage_security_domain=p_storage_domain AND owner_id=p_owner_id
            AND fencing_epoch=p_fencing_epoch AND expires_at>clock_timestamp();
    ELSE
        IF p_next_object_key = '' THEN RAISE EXCEPTION 'L3 GC cursor is required'; END IF;
        UPDATE cache.cache_l3_gc_state SET cursor_object_key=p_next_object_key,updated_at=clock_timestamp()
          WHERE lease_id=p_lease_id AND storage_security_domain=p_storage_domain AND owner_id=p_owner_id
            AND fencing_epoch=p_fencing_epoch AND expires_at>clock_timestamp()
            AND (cursor_object_key='' OR p_next_object_key COLLATE "C" >= cursor_object_key COLLATE "C");
    END IF;
    RETURN FOUND;
END;
$$;

-- Revision 1 contains an unfenced admission signature; remove it before
-- installing the replacement so stale development databases cannot retain a
-- bypass capability or grant.
DROP FUNCTION IF EXISTS cache.admit_manifest(uuid,uuid,text,text,bigint,text,bigint,text,text,text,text,text,bigint,text,text,text,bigint,text,text,text,bigint,jsonb,uuid,timestamptz);

CREATE OR REPLACE FUNCTION cache.admit_manifest(p_manifest_id uuid, p_lease_id uuid, p_cache_key text, p_owner_id text, p_fencing_epoch bigint, p_namespace_key text, p_namespace_epoch bigint, p_partition_kind text, p_target_id text, p_project_id text, p_environment text, p_candidate_id text, p_partition_format_version bigint, p_dependency_digest text, p_policy_fingerprint text, p_query_digest text, p_key_format_version bigint, p_storage_domain text, p_object_digest text, p_object_key text, p_object_lease_id uuid, p_object_owner_id text, p_object_fencing_epoch bigint, p_byte_size bigint, p_metadata jsonb, p_origin_snapshot_seal_id uuid, p_expires_at timestamptz)
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
    PERFORM 1 FROM cache.cache_l3_object_fence f
      WHERE f.lease_id=p_object_lease_id AND f.storage_security_domain=p_storage_domain
        AND f.object_key=p_object_key AND f.owner_id=p_object_owner_id
        AND f.fencing_epoch=p_object_fencing_epoch AND f.expires_at>clock_timestamp()
      FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'cache stale object fence'; END IF;
    INSERT INTO cache.cache_manifest(manifest_id,partition_kind,target_id,project_id,environment,candidate_id,partition_format_version,dependency_digest,policy_fingerprint,canonical_query_digest,key_format_version,storage_security_domain,object_digest,object_key,byte_size,metadata,state,origin_snapshot_seal_id,expires_at)
      VALUES (p_manifest_id,p_partition_kind,p_target_id,p_project_id,p_environment,p_candidate_id,p_partition_format_version,p_dependency_digest,p_policy_fingerprint,p_query_digest,p_key_format_version,p_storage_domain,p_object_digest,p_object_key,p_byte_size,p_metadata,'admitted',p_origin_snapshot_seal_id,p_expires_at)
      ON CONFLICT DO NOTHING;
    IF NOT EXISTS (SELECT 1 FROM cache.cache_namespace_epoch n WHERE n.namespace_key=p_namespace_key AND n.epoch=p_namespace_epoch AND n.partition_kind=p_partition_kind AND n.target_id=p_target_id AND n.project_id=p_project_id AND n.environment=p_environment AND n.candidate_id IS NOT DISTINCT FROM p_candidate_id) THEN
        RAISE EXCEPTION 'cache stale fill fence';
    END IF;
    SELECT * INTO existing FROM cache.cache_manifest
      WHERE partition_kind=p_partition_kind AND target_id=p_target_id AND project_id=p_project_id AND environment=p_environment
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

-- Tombstone terminal manifests before asynchronous object deletion.
CREATE OR REPLACE FUNCTION cache.prepare_l3_object_gc(p_lease_id uuid, p_storage_domain text, p_object_key text, p_owner_id text, p_fencing_epoch bigint)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, cache
AS $$
DECLARE evidence jsonb := jsonb_build_object('version',1,'reason','l3-orphan-gc');
BEGIN
    PERFORM 1 FROM cache.cache_l3_object_fence f
      WHERE f.lease_id=p_lease_id AND f.storage_security_domain=p_storage_domain
        AND f.object_key=p_object_key AND f.owner_id=p_owner_id
        AND f.fencing_epoch=p_fencing_epoch AND f.expires_at>clock_timestamp()
      FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'cache stale object fence'; END IF;
    PERFORM set_config('cache.capability', 'manifest_lifecycle', true);
    PERFORM 1 FROM cache.cache_manifest m
      WHERE m.storage_security_domain=p_storage_domain AND m.object_key=p_object_key
      FOR UPDATE;
    UPDATE cache.cache_manifest m
      SET state='retiring',retired_at=clock_timestamp(),retire_evidence=evidence
      WHERE m.storage_security_domain=p_storage_domain AND m.object_key=p_object_key
        AND m.state='admitted' AND m.expires_at IS NOT NULL AND m.expires_at<=clock_timestamp();
    IF EXISTS (
        SELECT 1 FROM cache.cache_manifest m
        WHERE m.storage_security_domain=p_storage_domain AND m.object_key=p_object_key
          AND ((m.state='admitted' AND (m.expires_at IS NULL OR m.expires_at>clock_timestamp())) OR EXISTS (
              SELECT 1 FROM cache.cache_retention_root rr
              WHERE rr.manifest_id=m.manifest_id AND rr.state IN ('live','retiring')
          ))
    ) THEN
        RETURN false;
    END IF;
    UPDATE cache.cache_manifest m
      SET state='expired',expired_at=clock_timestamp(),expire_evidence=evidence
      WHERE m.storage_security_domain=p_storage_domain AND m.object_key=p_object_key
        AND m.state='retiring' AND NOT EXISTS (
            SELECT 1 FROM cache.cache_retention_root rr
            WHERE rr.manifest_id=m.manifest_id AND rr.state IN ('live','retiring')
        );
    RETURN true;
END;
$$;

-- New functions are not public; named roles receive only their intended
-- capability surface.
REVOKE ALL ON cache.cache_l3_object_fence, cache.cache_l3_gc_state FROM PUBLIC;
REVOKE ALL ON FUNCTION cache.prune_coordination(timestamptz,integer),
    cache.acquire_l3_object_fence(uuid,text,text,text,interval),
    cache.renew_l3_object_fence(uuid,text,text,text,bigint,interval),
    cache.release_l3_object_fence(uuid,text,text,text,bigint),
    cache.acquire_l3_gc_lease(uuid,text,text,interval),
    cache.renew_l3_gc_lease(uuid,text,text,bigint,interval),
    cache.release_l3_gc_lease(uuid,text,text,bigint),
    cache.advance_l3_gc_cursor(uuid,text,text,bigint,text,boolean),
    cache.prepare_l3_object_gc(uuid,text,text,text,bigint),
    cache.admit_manifest(uuid,uuid,text,text,bigint,text,bigint,text,text,text,text,text,bigint,text,text,text,bigint,text,text,text,uuid,text,bigint,bigint,jsonb,uuid,timestamptz) FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA cache TO leapview_control_runtime;
        REVOKE ALL ON cache.cache_l3_object_fence, cache.cache_l3_gc_state FROM leapview_control_runtime;
        REVOKE ALL ON FUNCTION cache.prune_coordination(timestamptz,integer),
            cache.acquire_l3_gc_lease(uuid,text,text,interval),
            cache.renew_l3_gc_lease(uuid,text,text,bigint,interval),
            cache.release_l3_gc_lease(uuid,text,text,bigint),
            cache.advance_l3_gc_cursor(uuid,text,text,bigint,text,boolean),
            cache.prepare_l3_object_gc(uuid,text,text,text,bigint)
            FROM leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION cache.acquire_l3_object_fence(uuid,text,text,text,interval),
            cache.renew_l3_object_fence(uuid,text,text,text,bigint,interval),
            cache.release_l3_object_fence(uuid,text,text,text,bigint),
            cache.admit_manifest(uuid,uuid,text,text,bigint,text,bigint,text,text,text,text,text,bigint,text,text,text,bigint,text,text,text,uuid,text,bigint,bigint,jsonb,uuid,timestamptz)
            TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA cache TO leapview_control_maintenance;
        REVOKE ALL ON cache.cache_l3_object_fence, cache.cache_l3_gc_state FROM leapview_control_maintenance;
        REVOKE ALL ON FUNCTION cache.admit_manifest(uuid,uuid,text,text,bigint,text,bigint,text,text,text,text,text,bigint,text,text,text,bigint,text,text,text,uuid,text,bigint,bigint,jsonb,uuid,timestamptz)
            FROM leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION cache.prune_coordination(timestamptz,integer),
            cache.acquire_l3_object_fence(uuid,text,text,text,interval),
            cache.renew_l3_object_fence(uuid,text,text,text,bigint,interval),
            cache.release_l3_object_fence(uuid,text,text,text,bigint),
            cache.acquire_l3_gc_lease(uuid,text,text,interval),
            cache.renew_l3_gc_lease(uuid,text,text,bigint,interval),
            cache.release_l3_gc_lease(uuid,text,text,bigint),
            cache.advance_l3_gc_cursor(uuid,text,text,bigint,text,boolean),
            cache.prepare_l3_object_gc(uuid,text,text,text,bigint)
            TO leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA cache TO leapview_control_readonly;
        GRANT SELECT ON cache.cache_l3_object_fence, cache.cache_l3_gc_state TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA cache TO leapview_control_backup;
        GRANT SELECT ON cache.cache_l3_object_fence, cache.cache_l3_gc_state TO leapview_control_backup;
    END IF;
END;
$$;
