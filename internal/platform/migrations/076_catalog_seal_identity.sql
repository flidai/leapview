-- +goose Up

-- LEA-412: retain the complete catalog-seal identity before object upload.
-- The legacy closure_digest and qualification_digest columns are verification
-- evidence and intentionally remain nullable until a seal is verified. These
-- identity columns bind the evidence and candidate selected by the build
-- before any remote bytes are written, so a restart cannot substitute either.
ALTER TABLE delivery_catalog_seals
  ADD COLUMN identity_candidate_id TEXT
    CHECK (identity_candidate_id IS NULL OR (
      length(identity_candidate_id) BETWEEN 1 AND 128
      AND identity_candidate_id = trim(identity_candidate_id)
      AND identity_candidate_id NOT GLOB '*[^A-Za-z0-9._:/-]*'));

ALTER TABLE delivery_catalog_seals
  ADD COLUMN identity_closure_digest TEXT
    CHECK (identity_closure_digest IS NULL OR (
      length(identity_closure_digest) = 71
      AND substr(identity_closure_digest, 1, 7) = 'sha256:'
      AND substr(identity_closure_digest, 8) NOT GLOB '*[^0-9a-f]*'));

ALTER TABLE delivery_catalog_seals
  ADD COLUMN identity_qualification_digest TEXT
    CHECK (identity_qualification_digest IS NULL OR (
      length(identity_qualification_digest) = 71
      AND substr(identity_qualification_digest, 1, 7) = 'sha256:'
      AND substr(identity_qualification_digest, 8) NOT GLOB '*[^0-9a-f]*'));

-- +goose Down

ALTER TABLE delivery_catalog_seals DROP COLUMN identity_qualification_digest;
ALTER TABLE delivery_catalog_seals DROP COLUMN identity_closure_digest;
ALTER TABLE delivery_catalog_seals DROP COLUMN identity_candidate_id;
