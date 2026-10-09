-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Unresolved multipart intent retains its object independently of parent status.
CREATE INDEX IF NOT EXISTS multipart_upload_reachability_idx
    ON managed_data.multipart_upload (multipart_id)
    WHERE status IN ('creating', 'open', 'completing', 'aborting', 'failed');

DROP TRIGGER IF EXISTS multipart_reachability_epoch ON managed_data.multipart_upload;
CREATE TRIGGER multipart_reachability_epoch
AFTER INSERT OR DELETE OR UPDATE OF status, logical_path, sha256, size_bytes
ON managed_data.multipart_upload FOR EACH ROW
EXECUTE FUNCTION managed_data.bump_reachability_epoch();

-- Fixed table identities and lock mode expose only read-only GC fencing.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION managed_data.lock_stable_reachability() RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog
AS $$
BEGIN
  LOCK TABLE managed_data.multipart_upload, managed_data.upload_session,
             managed_data.revision, managed_data.retention_root IN SHARE MODE NOWAIT;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION managed_data.lock_stable_reachability() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION managed_data.lock_stable_reachability() TO leapview_control_runtime;

-- Invalidate cached snapshots assembled before multipart intent was a source.
-- +goose StatementBegin
DO $$ BEGIN
  UPDATE managed_data.reachability_epoch SET epoch = epoch + 1 WHERE singleton = true;
  IF NOT FOUND THEN RAISE EXCEPTION 'managed-data reachability epoch is missing'; END IF;
END $$;
-- +goose StatementEnd
RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'managed multipart GC safety is durable; destructive down is forbidden'; END $$;
-- +goose StatementEnd
