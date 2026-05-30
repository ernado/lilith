ALTER TABLE chat_messages
    ADD COLUMN message_thread_id        BIGINT,
    ADD COLUMN thread_id                BIGINT,
    ADD COLUMN thread_root_message_id   BIGINT,
    ADD COLUMN thread_parent_message_id BIGINT,
    ADD COLUMN thread_source            TEXT;

CREATE INDEX chat_messages_thread_idx
    ON chat_messages (chat_id, thread_id, message_id);

-- Backfill rows that predate threads.
UPDATE chat_messages
SET thread_parent_message_id = reply_to_id,
    thread_root_message_id   = COALESCE(reply_to_id, message_id),
    thread_id                = COALESCE(reply_to_id, message_id),
    thread_source            = CASE
                                   WHEN reply_to_id IS NOT NULL THEN 'legacy_reply'
                                   ELSE 'legacy_single'
                               END;

