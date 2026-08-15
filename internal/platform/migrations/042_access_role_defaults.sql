-- +goose Up
-- Seed project-wide capability bundles. Resource grants are installed only
-- from graph-validated authorization snapshots.

INSERT INTO roles (id, name, capabilities_json)
VALUES
  ('role_owner', 'owner', '["PROJECT_ADMIN","RESOURCE_USE","RESOURCE_READ","RESOURCE_EDIT","RESOURCE_MANAGE","RESOURCE_SHARE","RESOURCE_PUBLISH"]'),
  ('role_admin', 'admin', '["PROJECT_ADMIN","RESOURCE_USE","RESOURCE_READ","RESOURCE_EDIT","RESOURCE_MANAGE","RESOURCE_SHARE","RESOURCE_PUBLISH"]'),
  ('role_deployer', 'deployer', '["RESOURCE_USE","RESOURCE_READ","RESOURCE_PUBLISH"]'),
  ('role_contributor', 'contributor', '["RESOURCE_USE","RESOURCE_READ","RESOURCE_EDIT"]'),
  ('role_editor', 'editor', '["RESOURCE_USE","RESOURCE_READ","RESOURCE_EDIT"]'),
  ('role_member', 'member', '["RESOURCE_USE","RESOURCE_READ","RESOURCE_EDIT","RESOURCE_MANAGE"]'),
  ('role_viewer', 'viewer', '["RESOURCE_USE","RESOURCE_READ"]'),
  ('role_data_deployer', 'data_deployer', '["RESOURCE_USE","RESOURCE_EDIT"]'),
  ('role_platform_admin', 'platform_admin', '["PROJECT_ADMIN","RESOURCE_USE","RESOURCE_READ","RESOURCE_EDIT","RESOURCE_MANAGE","RESOURCE_SHARE","RESOURCE_PUBLISH"]')
ON CONFLICT(name) DO UPDATE SET
  id = excluded.id,
  capabilities_json = excluded.capabilities_json;

DELETE FROM role_grant_templates;
INSERT INTO role_grant_templates (role_name, capability)
SELECT roles.name, CAST(json_each.value AS TEXT)
FROM roles, json_each(roles.capabilities_json);

-- +goose Down
DELETE FROM role_grant_templates;
