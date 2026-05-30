DROP INDEX IF EXISTS chat_messages_thread_idx;

ALTER TABLE chat_messages
    DROP COLUMN message_thread_id,
    DROP COLUMN thread_id,
    DROP COLUMN thread_root_message_id,
    DROP COLUMN thread_parent_message_id,
    DROP COLUMN thread_source;

