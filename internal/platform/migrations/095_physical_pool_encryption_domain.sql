-- +goose Up

-- Encryption domain became part of the physical-pool identity after migration
-- 073 was already released. Add it as a forward-only schema change and use the
-- existing isolation boundary as the legacy domain for rows that predate the
-- explicit field. New writes must provide the domain explicitly.
ALTER TABLE physical_pools
  ADD COLUMN encryption_domain TEXT NOT NULL DEFAULT '';

UPDATE physical_pools
SET encryption_domain = isolation_boundary
WHERE encryption_domain = '';

DROP TRIGGER physical_pools_identity_immutable;

-- +goose StatementBegin
CREATE TRIGGER physical_pools_identity_immutable
BEFORE UPDATE OF id, identity_digest, storage_location, storage_namespace,
  storage_implementation, region, tenant, encryption_domain,
  isolation_boundary, encryption_key_ref, credential_reference,
  retention_authority, retention_policy_json, object_naming_contract
ON physical_pools
BEGIN
  SELECT RAISE(ABORT, 'physical pool identity is immutable');
END;
-- +goose StatementEnd

-- SQLite requires a constant default for ALTER TABLE ADD COLUMN. Reject
-- future direct inserts that omit the required domain instead of allowing the
-- temporary empty default used while upgrading legacy rows.
-- +goose StatementBegin
CREATE TRIGGER physical_pools_encryption_domain_required
BEFORE INSERT ON physical_pools
WHEN NEW.encryption_domain = ''
BEGIN
  SELECT RAISE(ABORT, 'physical pool encryption domain is required');
END;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER physical_pools_encryption_domain_required;
DROP TRIGGER physical_pools_identity_immutable;
ALTER TABLE physical_pools DROP COLUMN encryption_domain;

-- +goose StatementBegin
CREATE TRIGGER physical_pools_identity_immutable
BEFORE UPDATE OF id, identity_digest, storage_location, storage_namespace,
  storage_implementation, region, tenant, isolation_boundary,
  encryption_key_ref, credential_reference, retention_authority,
  retention_policy_json, object_naming_contract
ON physical_pools
BEGIN
  SELECT RAISE(ABORT, 'physical pool identity is immutable');
END;
-- +goose StatementEnd
