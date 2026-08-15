-- +goose Up
-- Global full-text search is a derived projection of the active project
-- generation. It is keyed by project/environment rather than a workspace.

CREATE TABLE active_search_documents (
  id INTEGER PRIMARY KEY,
  project_id TEXT NOT NULL,
  environment TEXT NOT NULL,
  serving_state_id TEXT NOT NULL REFERENCES serving_states(id) ON DELETE CASCADE,
  asset_snapshot_id TEXT NOT NULL,
  asset_id TEXT NOT NULL,
  asset_type TEXT NOT NULL,
  asset_key TEXT NOT NULL,
  parent_asset_id TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  terms TEXT NOT NULL DEFAULT '',
  UNIQUE(project_id, environment, asset_id)
);

CREATE INDEX active_search_documents_scope_idx
  ON active_search_documents(project_id, environment, asset_type, asset_id);

CREATE VIRTUAL TABLE active_search_documents_fts USING fts5(
  identifier,
  title,
  description,
  terms,
  content = 'active_search_documents',
  content_rowid = 'id',
  tokenize = 'unicode61 remove_diacritics 2',
  prefix = '2 3 4'
);

-- +goose StatementBegin
CREATE TRIGGER active_search_documents_ai AFTER INSERT ON active_search_documents BEGIN
  INSERT INTO active_search_documents_fts(rowid, identifier, title, description, terms)
  VALUES (new.id, new.asset_key, new.title, new.description, new.terms);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER active_search_documents_ad AFTER DELETE ON active_search_documents BEGIN
  INSERT INTO active_search_documents_fts(active_search_documents_fts, rowid, identifier, title, description, terms)
  VALUES ('delete', old.id, old.asset_key, old.title, old.description, old.terms);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER active_search_documents_au AFTER UPDATE ON active_search_documents BEGIN
  INSERT INTO active_search_documents_fts(active_search_documents_fts, rowid, identifier, title, description, terms)
  VALUES ('delete', old.id, old.asset_key, old.title, old.description, old.terms);
  INSERT INTO active_search_documents_fts(rowid, identifier, title, description, terms)
  VALUES (new.id, new.asset_key, new.title, new.description, new.terms);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER active_search_pointer_ai AFTER INSERT ON project_active_serving_states BEGIN
  DELETE FROM active_search_documents
  WHERE project_id = new.project_id AND environment = new.environment;

  INSERT INTO active_search_documents (
    project_id, environment, serving_state_id, asset_snapshot_id, asset_id,
    asset_type, asset_key, parent_asset_id, title, description, terms
  )
  SELECT
    state.project_id, new.environment, asset.serving_state_id, asset.snapshot_id,
    asset.logical_asset_id, asset.asset_type, asset.asset_key,
    asset.parent_logical_asset_id, asset.title, asset.description,
    asset.payload_json
  FROM assets asset
  JOIN serving_states state ON state.id = asset.serving_state_id
  WHERE state.project_id = new.project_id
    AND asset.serving_state_id = new.serving_state_id
    AND asset.asset_type NOT IN ('page_item', 'relationship', 'workspace_group', 'workspace_role_binding');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER active_search_pointer_au AFTER UPDATE OF serving_state_id ON project_active_serving_states BEGIN
  DELETE FROM active_search_documents
  WHERE project_id = new.project_id AND environment = new.environment;

  INSERT INTO active_search_documents (
    project_id, environment, serving_state_id, asset_snapshot_id, asset_id,
    asset_type, asset_key, parent_asset_id, title, description, terms
  )
  SELECT
    state.project_id, new.environment, asset.serving_state_id, asset.snapshot_id,
    asset.logical_asset_id, asset.asset_type, asset.asset_key,
    asset.parent_logical_asset_id, asset.title, asset.description,
    asset.payload_json
  FROM assets asset
  JOIN serving_states state ON state.id = asset.serving_state_id
  WHERE state.project_id = new.project_id
    AND asset.serving_state_id = new.serving_state_id
    AND asset.asset_type NOT IN ('page_item', 'relationship', 'workspace_group', 'workspace_role_binding');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER active_search_pointer_ad AFTER DELETE ON project_active_serving_states BEGIN
  DELETE FROM active_search_documents
  WHERE project_id = old.project_id AND environment = old.environment;
END;
-- +goose StatementEnd

-- Assets are normally persisted before activation. This trigger also keeps the
-- projection correct for bootstrap/import flows that insert into an active state.
-- +goose StatementBegin
CREATE TRIGGER active_search_asset_ai AFTER INSERT ON assets
WHEN new.asset_type NOT IN ('page_item', 'relationship', 'workspace_group', 'workspace_role_binding')
 AND EXISTS (
   SELECT 1 FROM project_active_serving_states active
   WHERE active.project_id = (SELECT project_id FROM serving_states WHERE id = new.serving_state_id)
     AND active.serving_state_id = new.serving_state_id
 )
BEGIN
  INSERT OR REPLACE INTO active_search_documents (
    project_id, environment, serving_state_id, asset_snapshot_id, asset_id,
    asset_type, asset_key, parent_asset_id, title, description, terms
  )
  SELECT
    active.project_id, active.environment, new.serving_state_id, new.snapshot_id,
    new.logical_asset_id, new.asset_type, new.asset_key, new.parent_logical_asset_id,
    new.title, new.description, new.payload_json
  FROM project_active_serving_states active
  WHERE active.project_id = (SELECT project_id FROM serving_states WHERE id = new.serving_state_id)
    AND active.serving_state_id = new.serving_state_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER active_search_asset_au AFTER UPDATE ON assets BEGIN
  DELETE FROM active_search_documents
  WHERE serving_state_id = old.serving_state_id AND asset_id = old.logical_asset_id;

  INSERT OR REPLACE INTO active_search_documents (
    project_id, environment, serving_state_id, asset_snapshot_id, asset_id,
    asset_type, asset_key, parent_asset_id, title, description, terms
  )
  SELECT
    active.project_id, active.environment, new.serving_state_id, new.snapshot_id,
    new.logical_asset_id, new.asset_type, new.asset_key, new.parent_logical_asset_id,
    new.title, new.description, new.payload_json
  FROM project_active_serving_states active
  WHERE active.project_id = (SELECT project_id FROM serving_states WHERE id = new.serving_state_id)
    AND active.serving_state_id = new.serving_state_id
    AND new.asset_type NOT IN ('page_item', 'relationship', 'workspace_group', 'workspace_role_binding');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER active_search_asset_ad AFTER DELETE ON assets BEGIN
  DELETE FROM active_search_documents
  WHERE serving_state_id = old.serving_state_id AND asset_id = old.logical_asset_id;
END;
-- +goose StatementEnd

INSERT INTO active_search_documents (
  project_id, environment, serving_state_id, asset_snapshot_id, asset_id,
  asset_type, asset_key, parent_asset_id, title, description, terms
)
SELECT
  active.project_id, active.environment, asset.serving_state_id, asset.snapshot_id,
  asset.logical_asset_id, asset.asset_type, asset.asset_key,
  asset.parent_logical_asset_id, asset.title, asset.description, asset.payload_json
FROM project_active_serving_states active
JOIN assets asset ON asset.serving_state_id = active.serving_state_id
JOIN serving_states state ON state.id = asset.serving_state_id
WHERE state.project_id = active.project_id
  AND asset.asset_type NOT IN ('page_item', 'relationship', 'workspace_group', 'workspace_role_binding');

INSERT INTO active_search_documents_fts(active_search_documents_fts) VALUES ('optimize');

-- +goose Down
DROP TRIGGER IF EXISTS active_search_asset_ad;
DROP TRIGGER IF EXISTS active_search_asset_au;
DROP TRIGGER IF EXISTS active_search_asset_ai;
DROP TRIGGER IF EXISTS active_search_pointer_ad;
DROP TRIGGER IF EXISTS active_search_pointer_au;
DROP TRIGGER IF EXISTS active_search_pointer_ai;
DROP TRIGGER IF EXISTS active_search_documents_au;
DROP TRIGGER IF EXISTS active_search_documents_ad;
DROP TRIGGER IF EXISTS active_search_documents_ai;
DROP TABLE IF EXISTS active_search_documents_fts;
DROP TABLE IF EXISTS active_search_documents;
