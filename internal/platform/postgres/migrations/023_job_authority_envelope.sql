-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Product jobs must carry an immutable authority envelope. Existing rows are
-- given the empty-object migration sentinel; the jobs module treats that form
-- as invalid and closes it before admission or handler dispatch.
ALTER TABLE jobs.job_history
    ADD COLUMN IF NOT EXISTS authority_envelope jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE jobs.job_history
    DROP CONSTRAINT IF EXISTS job_history_authority_envelope_object;
ALTER TABLE jobs.job_history
    ADD CONSTRAINT job_history_authority_envelope_object
    CHECK (jsonb_typeof(authority_envelope) = 'object');
ALTER TABLE jobs.job_history
    ALTER COLUMN authority_envelope DROP DEFAULT;

-- The baseline lifecycle trigger predates this column. Replace it in this
-- forward migration so authority evidence is immutable with the rest of the
-- product-job identity.
CREATE OR REPLACE FUNCTION jobs.guard_job_history_update()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, jobs
-- +goose StatementBegin
AS $$
BEGIN
    IF ROW(OLD.id, OLD.kind, OLD.workload_class, OLD.principal_id, OLD.group_ids,
           OLD.partition_key, OLD.resource_kind, OLD.resource_id,
           OLD.estimated_memory_bytes, OLD.payload, OLD.request_digest,
           OLD.authority_envelope, OLD.created_at)
       IS DISTINCT FROM
       ROW(NEW.id, NEW.kind, NEW.workload_class, NEW.principal_id, NEW.group_ids,
           NEW.partition_key, NEW.resource_kind, NEW.resource_id,
           NEW.estimated_memory_bytes, NEW.payload, NEW.request_digest,
           NEW.authority_envelope, NEW.created_at) THEN
        RAISE EXCEPTION 'product job identity is immutable';
    END IF;
    IF OLD.river_job_id IS NOT NULL AND NEW.river_job_id IS DISTINCT FROM OLD.river_job_id THEN
        RAISE EXCEPTION 'bound River job identity is immutable';
    END IF;
    IF NEW.attempt_count < OLD.attempt_count THEN
        RAISE EXCEPTION 'product job attempt count cannot decrease';
    END IF;
    IF OLD.status IN ('succeeded', 'failed', 'cancelled') AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal product job history is immutable';
    END IF;
    IF OLD.status = 'queued' AND NEW.status NOT IN ('queued', 'running', 'failed', 'cancelled') THEN
        RAISE EXCEPTION 'invalid queued product job transition';
    END IF;
    IF OLD.status = 'running' AND NEW.status NOT IN ('queued', 'running', 'succeeded', 'failed', 'cancelled') THEN
        RAISE EXCEPTION 'invalid running product job transition';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
ALTER TABLE jobs.job_history DROP CONSTRAINT IF EXISTS job_history_authority_envelope_object;
ALTER TABLE jobs.job_history DROP COLUMN IF EXISTS authority_envelope;
RESET ROLE;
