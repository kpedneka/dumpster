-- +goose Up
-- Re-evaluating a past query re-runs it against the current KB state and
-- appends a new assistant message rather than overwriting the original —
-- seeing that an answer changed is often as valuable as the new answer
-- itself. supersedes_message_id links a re-evaluation back to the message
-- it re-answers; NULL for every ordinary turn.
ALTER TABLE inquiry_messages
    ADD COLUMN supersedes_message_id UUID REFERENCES inquiry_messages(id) ON DELETE SET NULL;

CREATE INDEX inquiry_messages_supersedes_idx ON inquiry_messages(supersedes_message_id);

-- +goose Down
DROP INDEX IF EXISTS inquiry_messages_supersedes_idx;
ALTER TABLE inquiry_messages DROP COLUMN IF EXISTS supersedes_message_id;
