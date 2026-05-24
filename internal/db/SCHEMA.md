# Schema

## chat

| Column | Type      | Constraints |
|--------|-----------|-------------|
| id     | BIGSERIAL | PRIMARY KEY |
| info   | TEXT      | NOT NULL    |

## chat_messages

| Column          | Type    | Constraints                                   |
|-----------------|---------|-----------------------------------------------|
| chat_id         | BIGINT  | NOT NULL, PK, FK → chat(id) ON DELETE CASCADE |
| message_id      | BIGINT  | NOT NULL, PK                                  |
| text            | TEXT    | NOT NULL                                      |
| is_myself       | BOOLEAN | NOT NULL                                      |
| reply_to_id     | BIGINT  |                                               |
| reply_to_text   | TEXT    |                                               |
| reply_to_myself | BOOLEAN |                                               |
