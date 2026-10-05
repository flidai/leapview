-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- FAI-966 execution state only. Intake, legal decisions and retention remain
-- outside this capability. The table carries opaque identifiers, not content.
CREATE TABLE access.privacy_action_run (
    run_id uuid PRIMARY KEY,
    customer_id text NOT NULL CHECK (customer_id = btrim(customer_id) AND length(customer_id) BETWEEN 1 AND 255),
    deployment_id text NOT NULL CHECK (deployment_id = btrim(deployment_id) AND length(deployment_id) BETWEEN 1 AND 255),
    case_id text NOT NULL CHECK (case_id = btrim(case_id) AND length(case_id) BETWEEN 1 AND 256),
    correlation_id text NOT NULL CHECK (correlation_id = btrim(correlation_id) AND length(correlation_id) BETWEEN 1 AND 256),
    actor_id uuid NOT NULL,
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    principal_type text NOT NULL CHECK (principal_type IN ('user','service')),
    action text NOT NULL CHECK (action = 'restrict_access'),
    graph_digest text NOT NULL CHECK (graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    manifest_digest text NOT NULL CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','completed')),
    cursor bigint NOT NULL DEFAULT 0 CHECK (cursor >= 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    UNIQUE (customer_id, deployment_id, case_id, action),
    CHECK ((status = 'pending' AND completed_at IS NULL) OR (status = 'completed' AND completed_at IS NOT NULL))
);
CREATE TABLE access.privacy_action_item (
    run_id uuid NOT NULL REFERENCES access.privacy_action_run(run_id),
    ordinal bigint NOT NULL CHECK (ordinal > 0),
    store text NOT NULL CHECK (length(store) BETWEEN 1 AND 128),
    record_id text NOT NULL CHECK (length(record_id) BETWEEN 1 AND 255),
    actionable boolean NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','completed','excluded')),
    outcome text NOT NULL DEFAULT '' CHECK (length(outcome) <= 64),
    completed_at timestamptz,
    PRIMARY KEY (run_id, ordinal),
    UNIQUE (run_id, store, record_id),
    CHECK ((status = 'completed') = (completed_at IS NOT NULL))
);
CREATE INDEX privacy_action_item_pending_idx ON access.privacy_action_item(run_id, ordinal) WHERE status = 'pending';

REVOKE ALL ON access.privacy_action_run, access.privacy_action_item FROM PUBLIC;
GRANT SELECT, INSERT ON access.privacy_action_run, access.privacy_action_item TO leapview_control_runtime;
GRANT UPDATE (status, cursor, updated_at, completed_at) ON access.privacy_action_run TO leapview_control_runtime;
GRANT UPDATE (status, outcome, completed_at) ON access.privacy_action_item TO leapview_control_runtime;
GRANT SELECT ON access.privacy_action_run, access.privacy_action_item TO leapview_control_backup;

RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'privacy action execution evidence is durable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
