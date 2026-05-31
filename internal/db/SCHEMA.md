# Schema

## chat

| Column             | Type      | Constraints |
|--------------------|-----------|-------------|
| id                 | BIGSERIAL | PRIMARY KEY |
| info               | TEXT      | NOT NULL    |
| last_notes_msg_id  | BIGINT    | NOT NULL DEFAULT 0 |
| model              | TEXT      | NOT NULL DEFAULT '' |
| access_hash        | BIGINT    | NOT NULL DEFAULT 0 |
| type               | TEXT      | NOT NULL DEFAULT 'channel' |

## chat_messages

| Column          | Type        | Constraints                                   |
|-----------------|-------------|-----------------------------------------------|
| chat_id         | BIGINT      | NOT NULL, PK, FK → chat(id) ON DELETE CASCADE |
| message_id      | BIGINT      | NOT NULL, PK                                  |
| user_id         | BIGINT      | NOT NULL                                      |
| date            | TIMESTAMPTZ | NOT NULL                                      |
| text            | TEXT        | NOT NULL                                      |
| is_myself       | BOOLEAN     | NOT NULL                                      |
| image_url       | TEXT        | NOT NULL DEFAULT ''                           |
| reply_to_id     | BIGINT      |                                               |
| reply_to_text   | TEXT        |                                               |
| reply_to_myself | BOOLEAN     |                                               |
| message_thread_id        | BIGINT |                                          |
| thread_id                | BIGINT |                                          |
| thread_root_message_id   | BIGINT |                                          |
| thread_parent_message_id | BIGINT |                                          |
| thread_source            | TEXT   |                                          |

Index `chat_messages_thread_idx` on `(chat_id, thread_id, message_id)`.

`message_thread_id` is Telegram's native forum topic id (a scoping filter).
`thread_id` is the bot's logical conversation id (the thread's root message id).
See THREADS.md.

## chat_members

| Column     | Type    | Constraints                                   |
|------------|---------|-----------------------------------------------|
| chat_id    | BIGINT  | NOT NULL, PK, FK → chat(id) ON DELETE CASCADE |
| user_id    | BIGINT  | NOT NULL, PK                                  |
| username   | TEXT    | NOT NULL                                      |
| first_name | TEXT    | NOT NULL                                      |
| last_name  | TEXT    | NOT NULL                                      |
| is_admin   | BOOLEAN | NOT NULL                                      |
| is_creator | BOOLEAN | NOT NULL                                      |
| rank       | TEXT    | NOT NULL                                      |

## chat_notes

| Column  | Type      | Constraints                                   |
|---------|-----------|-----------------------------------------------|
| id      | BIGSERIAL | PRIMARY KEY                                   |
| chat_id | BIGINT    | NOT NULL, FK → chat(id) ON DELETE CASCADE     |
| text    | TEXT      | NOT NULL                                      |

