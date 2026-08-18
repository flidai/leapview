-- +goose Up

-- One append-only explanation row accompanies each monotonic target revision.
-- It is evidence only; publication still compares delivery_target_revisions.
CREATE TABLE delivery_target_revision_components (
  target_id TEXT NOT NULL REFERENCES delivery_target_revisions(target_id) ON DELETE CASCADE,
  target_revision INTEGER NOT NULL CHECK (target_revision > 0),
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  component_kind TEXT NOT NULL CHECK (length(component_kind) > 0 AND component_kind = trim(component_kind)),
  component_id TEXT NOT NULL CHECK (length(component_id) > 0 AND component_id = trim(component_id)),
  component_digest TEXT NOT NULL DEFAULT '',
  operation TEXT NOT NULL CHECK (operation IN ('insert', 'update', 'delete', 'cas')),
  changed_at TEXT NOT NULL,
  PRIMARY KEY (target_id, target_revision),
  FOREIGN KEY (target_id, project_id, environment)
    REFERENCES delivery_target_revisions(target_id, project_id, environment)
);
CREATE INDEX delivery_target_revision_components_scope_idx
  ON delivery_target_revision_components(project_id, environment, target_revision);

-- The narrow target binding semantics exclude credential-reference and health
-- bookkeeping fields. A secret rotation preserving endpoint and declared
-- execution semantics therefore does not stale plans.
-- +goose StatementBegin
CREATE TRIGGER delivery_target_binding_revision_insert
AFTER INSERT ON target_connection_bindings
BEGIN
  UPDATE delivery_target_revisions
     SET target_revision = target_revision + 1, updated_at = NEW.updated_at
   WHERE target_id = NEW.target_id AND project_id = NEW.project_id AND environment = NEW.environment;
  INSERT INTO delivery_target_revision_components
    (target_id,target_revision,project_id,environment,component_kind,component_id,operation,changed_at)
  SELECT target_id,target_revision,project_id,environment,'connection_binding',NEW.id,'insert',NEW.updated_at
    FROM delivery_target_revisions
   WHERE target_id = NEW.target_id AND project_id = NEW.project_id AND environment = NEW.environment;
END;
-- +goose StatementEnd

-- A scope move invalidates both the old and new target scopes.
-- +goose StatementBegin
CREATE TRIGGER delivery_target_binding_revision_update_old_scope
AFTER UPDATE OF target_id,project_id,environment ON target_connection_bindings
WHEN OLD.target_id IS NOT NEW.target_id OR OLD.project_id IS NOT NEW.project_id OR OLD.environment IS NOT NEW.environment
BEGIN
  UPDATE delivery_target_revisions SET target_revision=target_revision+1,updated_at=NEW.updated_at
   WHERE target_id=OLD.target_id AND project_id=OLD.project_id AND environment=OLD.environment;
  INSERT INTO delivery_target_revision_components
    (target_id,target_revision,project_id,environment,component_kind,component_id,operation,changed_at)
  SELECT target_id,target_revision,project_id,environment,'connection_binding',OLD.id,'update',NEW.updated_at
    FROM delivery_target_revisions
   WHERE target_id=OLD.target_id AND project_id=OLD.project_id AND environment=OLD.environment;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_target_binding_revision_update
AFTER UPDATE OF target_id,endpoint_json,connector_kind,authentication_mode,project_id,environment,enabled ON target_connection_bindings
WHEN OLD.target_id IS NOT NEW.target_id
  OR OLD.endpoint_json IS NOT NEW.endpoint_json
  OR OLD.connector_kind IS NOT NEW.connector_kind
  OR OLD.authentication_mode IS NOT NEW.authentication_mode
  OR OLD.project_id IS NOT NEW.project_id
  OR OLD.environment IS NOT NEW.environment
  OR OLD.enabled IS NOT NEW.enabled
BEGIN
  UPDATE delivery_target_revisions
     SET target_revision = target_revision + 1, updated_at = NEW.updated_at
   WHERE target_id = NEW.target_id AND project_id = NEW.project_id AND environment = NEW.environment;
  INSERT INTO delivery_target_revision_components
    (target_id,target_revision,project_id,environment,component_kind,component_id,operation,changed_at)
  SELECT target_id,target_revision,project_id,environment,'connection_binding',NEW.id,'update',NEW.updated_at
    FROM delivery_target_revisions
   WHERE target_id = NEW.target_id AND project_id = NEW.project_id AND environment = NEW.environment;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_target_binding_revision_delete
AFTER DELETE ON target_connection_bindings
BEGIN
  UPDATE delivery_target_revisions
     SET target_revision = target_revision + 1, updated_at = OLD.updated_at
   WHERE target_id = OLD.target_id AND project_id = OLD.project_id AND environment = OLD.environment;
  INSERT INTO delivery_target_revision_components
    (target_id,target_revision,project_id,environment,component_kind,component_id,operation,changed_at)
  SELECT target_id,target_revision,project_id,environment,'connection_binding',OLD.id,'delete',OLD.updated_at
    FROM delivery_target_revisions
   WHERE target_id = OLD.target_id AND project_id = OLD.project_id AND environment = OLD.environment;
END;
-- +goose StatementEnd

-- Managed-data environment pointers are result bindings. Upload sessions and
-- revisions without an environment are intentionally excluded until a pointer
-- is installed. Mutable capability/policy inputs have no generic table in the
-- current schema; their adapters must use the deployment repository's
-- BumpTargetRevision port with component evidence rather than mutating the
-- compiled authorization snapshots.
-- +goose StatementBegin
CREATE TRIGGER delivery_managed_pointer_revision_insert AFTER INSERT ON managed_data_environment_pointers BEGIN
  UPDATE delivery_target_revisions SET target_revision=target_revision+1,updated_at=NEW.updated_at WHERE project_id=(SELECT project_id FROM managed_data_collections WHERE id=NEW.collection_id) AND environment=NEW.environment;
  INSERT INTO delivery_target_revision_components SELECT target_id,target_revision,project_id,environment,'managed_data_pointer',NEW.collection_id,'','insert',NEW.updated_at FROM delivery_target_revisions WHERE project_id=(SELECT project_id FROM managed_data_collections WHERE id=NEW.collection_id) AND environment=NEW.environment;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_managed_pointer_revision_update AFTER UPDATE ON managed_data_environment_pointers BEGIN
  UPDATE delivery_target_revisions SET target_revision=target_revision+1,updated_at=NEW.updated_at WHERE project_id=(SELECT project_id FROM managed_data_collections WHERE id=NEW.collection_id) AND environment=NEW.environment;
  INSERT INTO delivery_target_revision_components SELECT target_id,target_revision,project_id,environment,'managed_data_pointer',NEW.collection_id,'','update',NEW.updated_at FROM delivery_target_revisions WHERE project_id=(SELECT project_id FROM managed_data_collections WHERE id=NEW.collection_id) AND environment=NEW.environment;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_managed_pointer_revision_update_old_scope
AFTER UPDATE OF collection_id,environment ON managed_data_environment_pointers
WHEN OLD.collection_id IS NOT NEW.collection_id OR OLD.environment IS NOT NEW.environment
BEGIN
  UPDATE delivery_target_revisions SET target_revision=target_revision+1,updated_at=NEW.updated_at WHERE project_id=(SELECT project_id FROM managed_data_collections WHERE id=OLD.collection_id) AND environment=OLD.environment;
  INSERT INTO delivery_target_revision_components SELECT target_id,target_revision,project_id,environment,'managed_data_pointer',OLD.collection_id,'','update',NEW.updated_at FROM delivery_target_revisions WHERE project_id=(SELECT project_id FROM managed_data_collections WHERE id=OLD.collection_id) AND environment=OLD.environment;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER delivery_managed_pointer_revision_delete AFTER DELETE ON managed_data_environment_pointers BEGIN
  UPDATE delivery_target_revisions SET target_revision=target_revision+1,updated_at=CURRENT_TIMESTAMP WHERE project_id=(SELECT project_id FROM managed_data_collections WHERE id=OLD.collection_id) AND environment=OLD.environment;
  INSERT INTO delivery_target_revision_components SELECT target_id,target_revision,project_id,environment,'managed_data_pointer',OLD.collection_id,'','delete',CURRENT_TIMESTAMP FROM delivery_target_revisions WHERE project_id=(SELECT project_id FROM managed_data_collections WHERE id=OLD.collection_id) AND environment=OLD.environment;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER delivery_managed_pointer_revision_delete;
DROP TRIGGER delivery_managed_pointer_revision_update;
DROP TRIGGER delivery_managed_pointer_revision_update_old_scope;
DROP TRIGGER delivery_managed_pointer_revision_insert;
DROP TRIGGER delivery_target_binding_revision_delete;
DROP TRIGGER delivery_target_binding_revision_update;
DROP TRIGGER delivery_target_binding_revision_insert;
DROP TRIGGER delivery_target_binding_revision_update_old_scope;
DROP INDEX delivery_target_revision_components_scope_idx;
DROP TABLE delivery_target_revision_components;
