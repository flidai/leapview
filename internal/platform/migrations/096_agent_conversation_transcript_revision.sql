-- +goose Up

-- FAI-881: fence transcript writes from replicas that read an older
-- conversation. Existing conversations begin at revision one; each successful
-- compare-and-swap write advances the revision exactly once.
ALTER TABLE agent_conversations
  ADD COLUMN transcript_revision INTEGER NOT NULL DEFAULT 1
  CHECK (transcript_revision > 0);

-- +goose Down

ALTER TABLE agent_conversations DROP COLUMN transcript_revision;
