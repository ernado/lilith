package db

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/ernado/svetik"
)

// SaveMessage inserts a chat message record, doing nothing on conflict.
func (db *DB) SaveMessage(ctx context.Context, msg svetik.Message) error {
	q := psql.Insert("chat_messages").
		Columns(
			"chat_id",
			"message_id",
			"text",
			"is_myself",
			"reply_to_id",
			"reply_to_text",
			"reply_to_myself",
		).
		Values(
			msg.ChatID,
			msg.MessageID,
			msg.Text,
			msg.IsMyself,
			msg.ReplyToID,
			msg.ReplyToText,
			msg.ReplyToMyself,
		).
		Suffix("ON CONFLICT (chat_id, message_id) DO NOTHING")

	sql, args, err := q.ToSql()
	if err != nil {
		return errors.Wrap(err, "build query")
	}

	if _, err := db.pgx.Exec(ctx, sql, args...); err != nil {
		return errors.Wrap(err, "exec")
	}

	return nil
}

// GetMessage returns a message by chat ID and message ID.
func (db *DB) GetMessage(ctx context.Context, chatID, messageID int64) (*svetik.Message, error) {
	q := psql.Select(
		"chat_id",
		"message_id",
		"text",
		"is_myself",
		"reply_to_id",
		"reply_to_text",
		"reply_to_myself",
	).
		From("chat_messages").
		Where("chat_id = ? AND message_id = ?", chatID, messageID)

	sql, args, err := q.ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "build query")
	}

	var msg svetik.Message

	err = db.pgx.QueryRow(ctx, sql, args...).Scan(
		&msg.ChatID,
		&msg.MessageID,
		&msg.Text,
		&msg.IsMyself,
		&msg.ReplyToID,
		&msg.ReplyToText,
		&msg.ReplyToMyself,
	)
	if err != nil {
		return nil, errors.Wrap(err, "scan")
	}

	return &msg, nil
}
