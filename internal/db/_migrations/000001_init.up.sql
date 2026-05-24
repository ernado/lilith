CREATE TABLE chat
(
    id   BIGSERIAL PRIMARY KEY,
    info TEXT NOT NULL
);

CREATE TABLE chat_messages
(
    chat_id             BIGINT  NOT NULL,
    message_id          BIGINT  NOT NULL,
    text                TEXT    NOT NULL,
    is_myself           BOOLEAN NOT NULL,
    reply_to_id         BIGINT,
    reply_to_text       TEXT,
    reply_to_myself     BOOLEAN,
    PRIMARY KEY (chat_id, message_id),
    FOREIGN KEY (chat_id) REFERENCES chat (id) ON UPDATE NO ACTION ON DELETE CASCADE
);
