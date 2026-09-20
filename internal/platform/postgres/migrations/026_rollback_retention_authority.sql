-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Retired generations remain fully reachable for their immutable plan's
-- rollback window. These capabilities evaluate aggregate generation roots so
-- a draining historical root cannot discard managed data or snapshots still
-- protected by a live rollback window.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION delivery.sync_managed_data_generation_root(
    p_generation_id uuid,
    p_state text
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, delivery
AS $$
BEGIN
    IF p_generation_id IS NULL OR p_state NOT IN ('retiring', 'expired') THEN
        RAISE EXCEPTION 'managed-data generation-root lifecycle input is invalid';
    END IF;
    -- The delivery capability is also testable as an independent schema. In
    -- the production control baseline managed_data is present; an absent
    -- capability is a no-op rather than a cross-schema bootstrap failure.
    IF to_regclass('managed_data.retention_root') IS NULL THEN
        RETURN;
    END IF;
	-- Managed-data reachability follows the aggregate delivery roots for the
	-- generation, not the lifecycle of any one immutable root. A retired
	-- generation can remain rollback-eligible through a live rollback root,
	-- and reactivation can establish a fresh generation root before an older
	-- root finishes draining.
	IF EXISTS (
		SELECT 1
		  FROM delivery.delivery_retention_root root
		 WHERE root.generation_id = p_generation_id
		   AND root.root_kind IN ('generation', 'rollback')
		   AND ((p_state = 'retiring' AND root.state = 'live')
		     OR (p_state = 'expired' AND root.state IN ('live', 'retiring')))
	) THEN
		RETURN;
	END IF;
    IF p_state = 'retiring' THEN
        UPDATE managed_data.retention_root
           SET state = 'retiring', updated_at = clock_timestamp()
         WHERE evidence->>'generation_id' = p_generation_id::text
           AND evidence->>'kind' = 'serving-generation'
           AND state = 'live';
    ELSE
        UPDATE managed_data.retention_root
           SET state = 'expired', updated_at = clock_timestamp()
         WHERE evidence->>'generation_id' = p_generation_id::text
           AND evidence->>'kind' = 'serving-generation'
		   AND state IN ('live', 'retiring');
    END IF;
END;
$$;
REVOKE ALL ON FUNCTION delivery.sync_managed_data_generation_root(uuid, text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION delivery.retire_retention_root(p_root_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, delivery
AS $$
DECLARE
    root_state text;
    root_kind text;
    root_target_id text;
    root_candidate_id uuid;
    root_generation_id uuid;
    root_snapshot_seal_id uuid;
    root_expires_at timestamptz;
	root_evidence jsonb;
BEGIN
    SELECT r.state, r.root_kind, r.target_id, r.candidate_id, r.generation_id, r.snapshot_seal_id, r.expires_at, r.evidence
      INTO root_state, root_kind, root_target_id, root_candidate_id, root_generation_id, root_snapshot_seal_id, root_expires_at, root_evidence
      FROM delivery.delivery_retention_root AS r
     WHERE r.root_id = p_root_id
     FOR UPDATE;
    IF NOT FOUND THEN
        RETURN false;
    END IF;
    IF root_kind NOT IN ('candidate', 'generation', 'rollback', 'recovery', 'query') THEN
        RAISE EXCEPTION 'unsupported retention root kind %', root_kind;
    END IF;
    -- The runtime capability is intentionally narrow: a generation root that
    -- is still selected by the active pointer cannot be retired by an
    -- arbitrary root-id call. Activation updates the pointer before invoking
    -- this function while retaining the root lock, so predecessor retirement
    -- remains atomic without exposing a live active root to readers.
    IF root_kind = 'generation'
       AND EXISTS (
           SELECT 1
             FROM delivery.delivery_active_pointer active
            WHERE active.target_id = root_target_id
              AND active.generation_id = root_generation_id
       ) THEN
        RAISE EXCEPTION 'cannot retire the active generation retention root';
    END IF;
    -- Candidate roots may be retired only when activation has made their
    -- generation live, or when their explicit DB-owned governance deadline
    -- has elapsed. This prevents a caller that knows only a candidate UUID
    -- from dropping preview reachability early.
    IF root_kind IN ('candidate', 'recovery', 'query')
       AND NOT (
           (root_kind = 'candidate' AND EXISTS (
               SELECT 1
                 FROM delivery.delivery_active_pointer active
                WHERE active.target_id = root_target_id
                  AND active.generation_id = root_generation_id
           ))
           OR (root_expires_at IS NOT NULL AND root_expires_at <= clock_timestamp())
       ) THEN
        RAISE EXCEPTION 'retention root lacks activation or expired deadline evidence';
    END IF;
    -- Pending rollback roots are keyed by publication ID and can retire after
    -- that publication is terminal. Cutover-created rollback-window roots use
    -- a disjoint deterministic ID; they can retire only after their DB-owned
    -- deadline and exact committed cutover evidence proves that this
    -- generation was the displaced predecessor.
    IF root_kind = 'rollback'
	   AND NOT (
		EXISTS (
			SELECT 1
			  FROM delivery.delivery_publication publication
			 WHERE publication.publication_id = p_root_id
			   AND publication.target_id = root_target_id
			   AND publication.candidate_id = root_candidate_id
			   AND publication.generation_id = root_generation_id
			   AND publication.snapshot_seal_id = root_snapshot_seal_id
			   AND publication.state IN ('committed', 'rejected', 'indeterminate')
		)
		OR (
			root_expires_at IS NOT NULL
			AND root_expires_at <= clock_timestamp()
			AND root_evidence->>'purpose' = 'retired generation rollback window'
			AND EXISTS (
				SELECT 1
				  FROM delivery.delivery_publication publication
				 WHERE publication.publication_id::text = root_evidence->>'publication_id'
				   AND publication.target_id = root_target_id
				   AND publication.expected_base_generation_id = root_generation_id
				   AND publication.state = 'committed'
			)
		)
	   ) THEN
        RAISE EXCEPTION 'rollback retention root lacks terminal publication evidence';
    END IF;
    -- Replaying retirement is a successful no-op after the same evidence
    -- checks above. Terminal roots cannot be moved backwards or re-retired.
    IF root_state = 'retiring' THEN
		IF root_kind IN ('generation', 'rollback') THEN
            PERFORM delivery.sync_managed_data_generation_root(root_generation_id, 'retiring');
        END IF;
        RETURN true;
    ELSIF root_state <> 'live' THEN
        RETURN false;
    END IF;
    UPDATE delivery.delivery_retention_root
       SET state = 'retiring', retired_at = clock_timestamp()
     WHERE root_id = p_root_id AND state = 'live';
	IF root_kind IN ('generation', 'rollback') THEN
        PERFORM delivery.sync_managed_data_generation_root(root_generation_id, 'retiring');
    END IF;
    RETURN FOUND;
END;
$$;

CREATE OR REPLACE FUNCTION delivery.expire_retention_root(
    p_root_id uuid,
    p_grace interval DEFAULT interval '0 seconds'
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, delivery
AS $$
DECLARE
    root_state text;
    root_retired_at timestamptz;
    root_expires_at timestamptz;
    root_generation_id uuid;
    root_kind text;
    root_target_id text;
    root_candidate_id uuid;
    root_snapshot_seal_id uuid;
    db_now timestamptz;
    expected_snapshot bigint;
BEGIN
    IF p_grace IS NULL OR p_grace < interval '0 seconds' THEN
        RAISE EXCEPTION 'retention root expiry grace must be non-negative';
    END IF;
    -- Lock the root before inspecting reader leases.  Reader admission takes
    -- a share lock on this row, therefore a concurrent admission either wins
    -- before this lock (and is observed below) or is rejected after retirement.
    SELECT r.state, r.retired_at, r.expires_at, r.target_id, r.candidate_id, r.generation_id, r.snapshot_seal_id, r.root_kind
      INTO root_state, root_retired_at, root_expires_at, root_target_id, root_candidate_id, root_generation_id, root_snapshot_seal_id, root_kind
      FROM delivery.delivery_retention_root AS r
     WHERE r.root_id = p_root_id
     FOR UPDATE;
    IF NOT FOUND THEN
        RETURN false;
    END IF;
    IF root_state = 'expired' THEN
		IF root_kind IN ('generation', 'rollback') THEN
            PERFORM delivery.sync_managed_data_generation_root(root_generation_id, 'expired');
        END IF;
        RETURN true;
    END IF;
    IF root_state <> 'retiring' OR root_retired_at IS NULL THEN
        RETURN false;
    END IF;
    db_now := clock_timestamp();
    -- A root's explicit expiry (for example, a candidate governance deadline)
    -- remains authoritative.  Retirement grace is evaluated from the DB
    -- retirement timestamp, never from an application/node clock.
    IF db_now < root_retired_at + p_grace
       OR (root_expires_at IS NOT NULL AND db_now < root_expires_at) THEN
        RETURN false;
    END IF;
    -- A stale/malformed retiring root must never make the currently active
    -- generation collectible. Reactivation is the exception: its fresh live
    -- generation root remains the canonical admission guard while this older
    -- immutable root is allowed to expire.
    IF root_generation_id IS NOT NULL
       AND EXISTS (
           SELECT 1 FROM delivery.delivery_active_pointer active
            WHERE active.generation_id = root_generation_id
       )
       AND NOT EXISTS (
           SELECT 1
             FROM delivery.delivery_retention_root active_root
            WHERE active_root.root_id <> p_root_id
              AND active_root.target_id = root_target_id
              AND active_root.candidate_id = root_candidate_id
              AND active_root.generation_id = root_generation_id
              AND active_root.snapshot_seal_id = root_snapshot_seal_id
              AND active_root.root_kind = 'generation'
              AND active_root.state = 'live'
              AND (active_root.expires_at IS NULL OR active_root.expires_at > db_now)
       ) THEN
        RETURN false;
    END IF;
    -- Only exact serving-state leases rooted at this generation/snapshot can
    -- delay expiry.  Expired leases no longer represent readers even if the
    -- maintenance marker has not yet been written.
    IF root_generation_id IS NOT NULL THEN
        SELECT s.ducklake_snapshot_id
          INTO expected_snapshot
          FROM delivery.delivery_generation g
          JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
         WHERE g.generation_id = root_generation_id
           AND (root_snapshot_seal_id IS NULL OR s.seal_id = root_snapshot_seal_id);
        IF expected_snapshot IS NULL THEN
            -- Corrupt/missing generation evidence fails closed.
            RETURN false;
        END IF;
        IF EXISTS (
            SELECT 1
              FROM serving_state.reader_lease l
             WHERE l.generation_id = root_generation_id
               AND l.ducklake_snapshot_id = expected_snapshot
               AND l.released_at IS NULL
               AND l.expires_at > db_now
        ) THEN
            RETURN false;
        END IF;
    END IF;
    UPDATE delivery.delivery_retention_root
       SET state = 'expired', expired_at = clock_timestamp()
     WHERE root_id = p_root_id AND state = 'retiring';
	IF root_kind IN ('generation', 'rollback') THEN
        PERFORM delivery.sync_managed_data_generation_root(root_generation_id, 'expired');
    END IF;
    RETURN FOUND;
END;
$$;

-- One bounded maintenance pass first retires candidate/recovery roots whose
-- explicit governance deadline has elapsed, then expires ready retiring roots. The
-- singular expiry function remains the final authority and rechecks reader
-- leases, active-generation protection, grace, and immutable seal identity
-- under the root lock. SKIP LOCKED lets parallel workers make progress
-- without waiting on activation or reader admission transactions.
CREATE OR REPLACE FUNCTION delivery.maintain_retention_roots(
    p_physical_pool_id text,
    p_catalog_id text,
    p_grace interval,
    p_limit integer
)
RETURNS TABLE(retired bigint, expired bigint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, delivery
AS $$
DECLARE
    candidate record;
    db_now timestamptz;
BEGIN
    IF p_physical_pool_id IS NULL OR p_physical_pool_id <> btrim(p_physical_pool_id)
       OR octet_length(p_physical_pool_id) NOT BETWEEN 1 AND 255
       OR p_catalog_id IS NULL OR p_catalog_id <> btrim(p_catalog_id)
       OR octet_length(p_catalog_id) NOT BETWEEN 1 AND 255 THEN
        RAISE EXCEPTION 'retention root maintenance pool/catalog identity is invalid';
    END IF;
    IF p_grace IS NULL OR p_grace < interval '0 seconds' THEN
        RAISE EXCEPTION 'retention root maintenance grace must be non-negative';
    END IF;
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'retention root maintenance limit must be between 1 and 1000';
    END IF;
    retired := 0;
    expired := 0;
    db_now := clock_timestamp();

    FOR candidate IN
        SELECT root.root_id
          FROM delivery.delivery_retention_root root
		 WHERE root.root_kind IN ('candidate', 'rollback', 'recovery')
           AND root.state = 'live'
           AND root.expires_at IS NOT NULL
           AND root.expires_at <= db_now
           AND EXISTS (
               SELECT 1 FROM delivery.delivery_snapshot_seal seal
                WHERE seal.seal_id = root.snapshot_seal_id
                  AND seal.physical_pool_id = p_physical_pool_id
                  AND seal.catalog_id = p_catalog_id
           )
         ORDER BY root.expires_at, root.root_id
         FOR UPDATE SKIP LOCKED
         LIMIT p_limit
    LOOP
        IF delivery.retire_retention_root(candidate.root_id) THEN
            retired := retired + 1;
        END IF;
    END LOOP;

    -- Retirement timestamps use clock_timestamp(), so refresh the DB clock
    -- before evaluating zero-grace roots retired by this same bounded pass.
    db_now := clock_timestamp();
    FOR candidate IN
        SELECT root.root_id
          FROM delivery.delivery_retention_root root
         WHERE root.state = 'retiring'
           AND root.retired_at + p_grace <= db_now
           AND (root.expires_at IS NULL OR root.expires_at <= db_now)
           AND EXISTS (
               SELECT 1 FROM delivery.delivery_snapshot_seal scoped_seal
                WHERE scoped_seal.seal_id = root.snapshot_seal_id
                  AND scoped_seal.physical_pool_id = p_physical_pool_id
                  AND scoped_seal.catalog_id = p_catalog_id
           )
           AND (
               root.generation_id IS NULL
               OR (
                   EXISTS (
                       SELECT 1
                         FROM delivery.delivery_generation g
                         JOIN delivery.delivery_snapshot_seal seal
                           ON seal.seal_id = g.snapshot_seal_id
                        WHERE g.generation_id = root.generation_id
                          AND (root.snapshot_seal_id IS NULL OR root.snapshot_seal_id = seal.seal_id)
                   )
                   AND NOT EXISTS (
                       SELECT 1
                         FROM serving_state.reader_lease lease
                         JOIN delivery.delivery_generation g
                           ON g.generation_id = lease.generation_id
                         JOIN delivery.delivery_snapshot_seal seal
                           ON seal.seal_id = g.snapshot_seal_id
                        WHERE g.generation_id = root.generation_id
                          AND (root.snapshot_seal_id IS NULL OR root.snapshot_seal_id = seal.seal_id)
                          AND lease.ducklake_snapshot_id = seal.ducklake_snapshot_id
                          AND lease.released_at IS NULL
                          AND lease.expires_at > db_now
                   )
                   AND (
                       NOT EXISTS (
                           SELECT 1 FROM delivery.delivery_active_pointer active
                            WHERE active.generation_id = root.generation_id
                       )
                       OR EXISTS (
                           SELECT 1
                             FROM delivery.delivery_retention_root active_root
                            WHERE active_root.root_id <> root.root_id
                              AND active_root.target_id = root.target_id
                              AND active_root.candidate_id = root.candidate_id
                              AND active_root.generation_id = root.generation_id
                              AND active_root.snapshot_seal_id = root.snapshot_seal_id
                              AND active_root.root_kind = 'generation'
                              AND active_root.state = 'live'
                              AND (active_root.expires_at IS NULL OR active_root.expires_at > db_now)
                       )
                   )
               )
           )
         ORDER BY root.retired_at, root.root_id
         FOR UPDATE SKIP LOCKED
         LIMIT p_limit
    LOOP
        IF delivery.expire_retention_root(candidate.root_id, p_grace) THEN
            expired := expired + 1;
        END IF;
    END LOOP;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- Rollback-retention reachability is durable deployment evidence. Restoring
-- the earlier per-root lifecycle would make retained generations unsafe.
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'rollback retention authority is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
