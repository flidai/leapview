-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Managed-data retention roots are the generation-scoped object reachability
-- authority. Keep the query selective as the root table grows, while retaining
-- the conservative treatment of revisions admitted before this migration.
CREATE INDEX IF NOT EXISTS retention_root_revision_state_idx
    ON managed_data.retention_root (revision_id, state);
CREATE INDEX IF NOT EXISTS retention_root_generation_state_idx
    ON managed_data.retention_root ((evidence->>'generation_id'), state)
    WHERE evidence ? 'generation_id';

-- New roots always enter through generation admission as live evidence. Their
-- terminal states are reached only through the fenced lifecycle authority.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION managed_data.guard_retention_root_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
DECLARE revision_project text;
BEGIN
  IF NEW.state <> 'live' THEN
    RAISE EXCEPTION 'retention root must begin in live state';
  END IF;
  IF NEW.revision_id IS NOT NULL THEN
    SELECT c.project_id INTO revision_project
      FROM managed_data.revision r JOIN managed_data.collection c ON c.collection_id=r.collection_id
     WHERE r.revision_id=NEW.revision_id;
    IF revision_project IS NULL OR revision_project <> NEW.project_id THEN
      RAISE EXCEPTION 'retention revision must exist in the declared project';
    END IF;
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS retention_root_insert_guard ON managed_data.retention_root;
CREATE TRIGGER retention_root_insert_guard BEFORE INSERT ON managed_data.retention_root FOR EACH ROW EXECUTE FUNCTION managed_data.guard_retention_root_insert();

-- Root insertion and lifecycle transitions invalidate reachability snapshots in
-- the same database transaction as the durable state change.
DROP TRIGGER IF EXISTS retention_root_reachability_epoch ON managed_data.retention_root;
CREATE TRIGGER retention_root_reachability_epoch
AFTER INSERT OR DELETE OR UPDATE OF state, revision_id
ON managed_data.retention_root FOR EACH ROW
EXECUTE FUNCTION managed_data.bump_reachability_epoch();

-- Runtime admission may create immutable live roots, but cannot transition
-- them. Delivery's SECURITY DEFINER lifecycle functions perform the fenced
-- retiring/expired transition below.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
    REVOKE UPDATE ON managed_data.retention_root FROM leapview_control_runtime;
    GRANT SELECT, INSERT ON managed_data.retention_root TO leapview_control_runtime;
  END IF;
END $$;
-- +goose StatementEnd

-- Delivery's generation root is the authoritative serving lifecycle. This
-- bridge updates only already-admitted serving-generation managed roots and
-- never creates or resurrects one. The absent-table guard keeps deployment's
-- independently testable schema usable before managed-data is installed.
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
    IF to_regclass('managed_data.retention_root') IS NULL THEN
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
           AND state = 'retiring';
    END IF;
END;
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION delivery.sync_managed_data_generation_root(uuid, text) FROM PUBLIC;

-- Replace the lifecycle functions additively so generation-root transitions
-- update managed-data reachability in the same fenced transaction. Candidate,
-- rollback, recovery, and query roots retain their original semantics.
-- +goose StatementBegin
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
BEGIN
    SELECT r.state, r.root_kind, r.target_id, r.candidate_id, r.generation_id, r.snapshot_seal_id, r.expires_at
      INTO root_state, root_kind, root_target_id, root_candidate_id, root_generation_id, root_snapshot_seal_id, root_expires_at
      FROM delivery.delivery_retention_root AS r
     WHERE r.root_id = p_root_id
     FOR UPDATE;
    IF NOT FOUND THEN
        RETURN false;
    END IF;
    IF root_kind NOT IN ('candidate', 'generation', 'rollback', 'recovery', 'query') THEN
        RAISE EXCEPTION 'unsupported retention root kind %', root_kind;
    END IF;
    IF root_kind = 'generation'
       AND EXISTS (
           SELECT 1
             FROM delivery.delivery_active_pointer active
            WHERE active.target_id = root_target_id
              AND active.generation_id = root_generation_id
       ) THEN
        RAISE EXCEPTION 'cannot retire the active generation retention root';
    END IF;
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
    IF root_kind = 'rollback'
       AND NOT EXISTS (
           SELECT 1
             FROM delivery.delivery_publication publication
            WHERE publication.publication_id = p_root_id
              AND publication.target_id = root_target_id
              AND publication.candidate_id = root_candidate_id
              AND publication.generation_id = root_generation_id
              AND publication.snapshot_seal_id = root_snapshot_seal_id
              AND publication.state IN ('committed', 'rejected', 'indeterminate')
       ) THEN
        RAISE EXCEPTION 'rollback retention root lacks terminal publication evidence';
    END IF;
    IF root_state = 'retiring' THEN
        IF root_kind = 'generation' THEN
            PERFORM delivery.sync_managed_data_generation_root(root_generation_id, 'retiring');
        END IF;
        RETURN true;
    ELSIF root_state <> 'live' THEN
        RETURN false;
    END IF;
    UPDATE delivery.delivery_retention_root
       SET state = 'retiring', retired_at = clock_timestamp()
     WHERE root_id = p_root_id AND state = 'live';
    IF root_kind = 'generation' THEN
        PERFORM delivery.sync_managed_data_generation_root(root_generation_id, 'retiring');
    END IF;
    RETURN FOUND;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
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
    SELECT r.state, r.retired_at, r.expires_at, r.target_id, r.candidate_id, r.generation_id, r.snapshot_seal_id, r.root_kind
      INTO root_state, root_retired_at, root_expires_at, root_target_id, root_candidate_id, root_generation_id, root_snapshot_seal_id, root_kind
      FROM delivery.delivery_retention_root AS r
     WHERE r.root_id = p_root_id
     FOR UPDATE;
    IF NOT FOUND THEN
        RETURN false;
    END IF;
    IF root_state = 'expired' THEN
        IF root_kind = 'generation' THEN
            PERFORM delivery.sync_managed_data_generation_root(root_generation_id, 'expired');
        END IF;
        RETURN true;
    END IF;
    IF root_state <> 'retiring' OR root_retired_at IS NULL THEN
        RETURN false;
    END IF;
    db_now := clock_timestamp();
    IF db_now < root_retired_at + p_grace
       OR (root_expires_at IS NOT NULL AND db_now < root_expires_at) THEN
        RETURN false;
    END IF;
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
    -- Reader protection intentionally remains generation-id based for all
    -- delivery root kinds carrying valid generation evidence. Root-kind
    -- filtering applies only to managed-data synchronization below.
    IF root_generation_id IS NOT NULL THEN
        SELECT s.ducklake_snapshot_id
          INTO expected_snapshot
          FROM delivery.delivery_generation g
          JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
         WHERE g.generation_id = root_generation_id
           AND (root_snapshot_seal_id IS NULL OR s.seal_id = root_snapshot_seal_id);
        IF expected_snapshot IS NULL THEN
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
    IF root_kind = 'generation' THEN
        PERFORM delivery.sync_managed_data_generation_root(root_generation_id, 'expired');
    END IF;
    RETURN FOUND;
END;
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- Retention state and lifecycle evidence are durable; removing this capability
-- would make old roots ambiguous and could release objects unsafely.
-- +goose StatementBegin
DO $$ BEGIN
  RAISE EXCEPTION 'managed-data retention lifecycle migration is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
