-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- FAI-881: fence transcript writes from replicas that read an older
-- conversation. Existing conversations begin at revision one; each successful
-- compare-and-swap write advances the revision exactly once.
ALTER TABLE agent.conversations
    ADD COLUMN IF NOT EXISTS transcript_revision bigint NOT NULL DEFAULT 1;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint
         WHERE conrelid = 'agent.conversations'::regclass
           AND conname = 'conversations_transcript_revision_check'
    ) THEN
        ALTER TABLE agent.conversations
            ADD CONSTRAINT conversations_transcript_revision_check
            CHECK (transcript_revision > 0);
    END IF;
END
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
ALTER TABLE agent.conversations DROP CONSTRAINT IF EXISTS conversations_transcript_revision_check;
ALTER TABLE agent.conversations DROP COLUMN IF EXISTS transcript_revision;
RESET ROLE;
