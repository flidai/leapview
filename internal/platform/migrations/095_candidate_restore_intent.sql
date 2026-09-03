-- +goose Up

-- An explicit restore request is candidate-owned evidence. The intent is
-- immutable after candidate creation; normal candidate transitions update
-- neither column. Empty JSON and reason represent ordinary publication.
ALTER TABLE project_candidates
  ADD COLUMN restore_authored_ids_json TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(restore_authored_ids_json) AND json_type(restore_authored_ids_json) = 'array');

ALTER TABLE project_candidates
  ADD COLUMN restore_reason TEXT NOT NULL DEFAULT ''
    CHECK ((json_array_length(restore_authored_ids_json) = 0 AND restore_reason = '')
      OR (json_array_length(restore_authored_ids_json) > 0 AND length(trim(restore_reason)) > 0));

-- +goose StatementBegin
CREATE TRIGGER project_candidates_restore_intent_immutable
BEFORE UPDATE OF restore_authored_ids_json, restore_reason ON project_candidates
WHEN NEW.restore_authored_ids_json <> OLD.restore_authored_ids_json
  OR NEW.restore_reason <> OLD.restore_reason
BEGIN
  SELECT RAISE(ABORT, 'candidate restore intent is immutable');
END;
-- +goose StatementEnd

-- +goose Down

DROP TRIGGER project_candidates_restore_intent_immutable;
ALTER TABLE project_candidates DROP COLUMN restore_reason;
ALTER TABLE project_candidates DROP COLUMN restore_authored_ids_json;
