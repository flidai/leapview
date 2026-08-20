-- +goose Up

-- Deployment approvals can belong to either the legacy project-deployment
-- parent or the canonical delivery-publication parent. Keep the polymorphic
-- association in one table while enforcing the complete immutable scope at
-- the durable boundary.
-- +goose StatementBegin
CREATE TRIGGER deployment_approvals_parent_insert
BEFORE INSERT ON deployment_approvals
WHEN NOT EXISTS (
  SELECT 1 FROM project_deployments
  WHERE id = NEW.deployment_id
    AND project_id = NEW.project_id
    AND environment = NEW.environment
    AND request_digest = NEW.request_digest
)
AND NOT EXISTS (
  SELECT 1 FROM delivery_publications
  WHERE id = NEW.deployment_id
    AND project_id = NEW.project_id
    AND environment = NEW.environment
    AND request_digest = NEW.request_digest
)
BEGIN
  SELECT RAISE(ABORT, 'deployment approval parent scope is missing');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER deployment_approvals_parent_update
BEFORE UPDATE OF deployment_id, project_id, environment, request_digest ON deployment_approvals
WHEN NOT EXISTS (
  SELECT 1 FROM project_deployments
  WHERE id = NEW.deployment_id
    AND project_id = NEW.project_id
    AND environment = NEW.environment
    AND request_digest = NEW.request_digest
)
AND NOT EXISTS (
  SELECT 1 FROM delivery_publications
  WHERE id = NEW.deployment_id
    AND project_id = NEW.project_id
    AND environment = NEW.environment
    AND request_digest = NEW.request_digest
)
BEGIN
  SELECT RAISE(ABORT, 'deployment approval parent scope is missing');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER project_deployments_approval_delete
AFTER DELETE ON project_deployments
BEGIN
  DELETE FROM deployment_approvals
  WHERE deployment_id = OLD.id
    AND project_id = OLD.project_id
    AND environment = OLD.environment
    AND request_digest = OLD.request_digest;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER delivery_publications_approval_delete
AFTER DELETE ON delivery_publications
BEGIN
  DELETE FROM deployment_approvals
  WHERE deployment_id = OLD.id
    AND project_id = OLD.project_id
    AND environment = OLD.environment
    AND request_digest = OLD.request_digest;
END;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER delivery_publications_approval_delete;
DROP TRIGGER project_deployments_approval_delete;
DROP TRIGGER deployment_approvals_parent_update;
DROP TRIGGER deployment_approvals_parent_insert;
