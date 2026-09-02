-- FAI-617: durable authored-resource identity for one active source bundle.
-- Resource identity is exactly (instance_id, authored_id); no surrogate
-- resource UID is introduced by this schema.

SET ROLE leapview_control_owner;

CREATE TABLE IF NOT EXISTS project.source_bundle (
    instance_id      platform.resource_id NOT NULL,
    bundle_id        platform.resource_id NOT NULL,
    state            text NOT NULL CHECK (state IN ('active', 'superseded')),
    activated_by     platform.resource_id NOT NULL,
    activated_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (instance_id, bundle_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS source_bundle_one_active_idx
    ON project.source_bundle (instance_id) WHERE state = 'active';

CREATE TABLE IF NOT EXISTS project.resource_identity (
    instance_id      platform.resource_id NOT NULL,
    authored_id      platform.resource_id NOT NULL,
    resource_kind    text NOT NULL CHECK (resource_kind IN (
        'connection', 'source', 'model', 'semantic_model', 'pipeline', 'dashboard'
    )),
    lifecycle_state  text NOT NULL CHECK (lifecycle_state IN ('active', 'tombstoned')),
    active_bundle_id platform.resource_id,
    tombstone_reason text NOT NULL DEFAULT '' CHECK (length(tombstone_reason) <= 2048),
    created_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    tombstoned_at    timestamptz,
    restored_at      timestamptz,
    PRIMARY KEY (instance_id, authored_id),
    UNIQUE (instance_id, authored_id, resource_kind),
    CHECK (
        (lifecycle_state = 'active' AND active_bundle_id IS NOT NULL AND tombstoned_at IS NULL)
        OR
        (lifecycle_state = 'tombstoned' AND active_bundle_id IS NULL AND tombstoned_at IS NOT NULL)
    )
);

CREATE OR REPLACE FUNCTION project.reject_resource_identity_kind_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.resource_kind <> OLD.resource_kind THEN
        RAISE EXCEPTION 'resource identity kind is immutable';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS resource_identity_kind_immutable ON project.resource_identity;
CREATE TRIGGER resource_identity_kind_immutable
    BEFORE UPDATE OF resource_kind ON project.resource_identity
    FOR EACH ROW EXECUTE FUNCTION project.reject_resource_identity_kind_change();

CREATE TABLE IF NOT EXISTS project.source_bundle_resource (
    instance_id      platform.resource_id NOT NULL,
    bundle_id        platform.resource_id NOT NULL,
    authored_id      platform.resource_id NOT NULL,
    resource_kind    text NOT NULL,
    PRIMARY KEY (instance_id, bundle_id, authored_id),
    FOREIGN KEY (instance_id, bundle_id)
        REFERENCES project.source_bundle(instance_id, bundle_id) ON DELETE RESTRICT,
    FOREIGN KEY (instance_id, authored_id, resource_kind)
        REFERENCES project.resource_identity(instance_id, authored_id, resource_kind) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS project.resource_identity_history (
    instance_id      platform.resource_id NOT NULL,
    authored_id      platform.resource_id NOT NULL,
    sequence         bigint NOT NULL CHECK (sequence > 0),
    resource_kind    text NOT NULL,
    action           text NOT NULL CHECK (action IN (
        'created', 'activated', 'tombstoned', 'restored',
        'rollback_activated', 'rollback_tombstoned'
    )),
    bundle_id        platform.resource_id NOT NULL,
    actor_id         platform.resource_id NOT NULL,
    reason           text NOT NULL DEFAULT '' CHECK (length(reason) <= 2048),
    occurred_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (instance_id, authored_id, sequence),
    FOREIGN KEY (instance_id, authored_id, resource_kind)
        REFERENCES project.resource_identity(instance_id, authored_id, resource_kind) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS project.durable_resource_reference (
    instance_id        platform.resource_id NOT NULL,
    reference_id       platform.resource_id NOT NULL,
    owner_authored_id  platform.resource_id NOT NULL,
    owner_kind         platform.resource_id NOT NULL,
    target_authored_id platform.resource_id NOT NULL,
    expected_kind      text NOT NULL,
    lifecycle_state    text NOT NULL CHECK (lifecycle_state IN ('active', 'suspended')),
    created_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    suspended_at       timestamptz,
    reactivated_at     timestamptz,
    PRIMARY KEY (instance_id, reference_id),
    FOREIGN KEY (instance_id, target_authored_id, expected_kind)
        REFERENCES project.resource_identity(instance_id, authored_id, resource_kind) ON DELETE RESTRICT,
    CHECK (
        (lifecycle_state = 'active' AND suspended_at IS NULL)
        OR
        (lifecycle_state = 'suspended' AND suspended_at IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS durable_resource_reference_target_idx
    ON project.durable_resource_reference (instance_id, target_authored_id);

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES
    project.source_bundle,
    project.resource_identity,
    project.source_bundle_resource,
    project.resource_identity_history,
    project.durable_resource_reference
    TO leapview_control_runtime;
GRANT SELECT ON TABLES
    project.source_bundle,
    project.resource_identity,
    project.source_bundle_resource,
    project.resource_identity_history,
    project.durable_resource_reference
    TO leapview_control_readonly;

REVOKE ALL ON FUNCTION project.reject_resource_identity_kind_change() FROM PUBLIC;

RESET ROLE;
