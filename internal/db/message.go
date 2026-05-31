package db

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/ernado/lilith"
)

// messageColumns is the canonical column list used by message read queries.
var messageColumns = []string{
	"chat_id",
	"message_id",
	"user_id",
	"date",
	"text",
	"is_myself",
	"image_url",
	"reply_to_id",
	"reply_to_text",
	"reply_to_myself",
	"message_thread_id",
	"thread_id",
	"thread_root_message_id",
	"thread_parent_message_id",
	"thread_source",
}

// scanDest returns the scan destinations matching messageColumns for msg.
func scanDest(msg *lilith.Message) []any {
	return []any{
		&msg.ChatID,
		&msg.MessageID,
		&msg.UserID,
		&msg.Date,
		&msg.Text,
		&msg.IsMyself,
		&msg.ImageURL,
		&msg.ReplyToID,
		&msg.ReplyToText,
		&msg.ReplyToMyself,
		&msg.MessageThreadID,
		&msg.ThreadID,
		&msg.ThreadRootMessageID,
		&msg.ThreadParentMessageID,
		&msg.ThreadSource,
	}
}

// SaveMessage inserts a chat message record, doing nothing on conflict.
func (db *DB) SaveMessage(ctx context.Context, msg lilith.Message) error {
	q := psql.Insert("chat_messages").
		Columns(
			"chat_id",
			"message_id",
			"user_id",
			"date",
			"text",
			"is_myself",
			"image_url",
			"reply_to_id",
			"reply_to_text",
			"reply_to_myself",
			"message_thread_id",
			"thread_id",
			"thread_root_message_id",
			"thread_parent_message_id",
			"thread_source",
		).
		Values(
			msg.ChatID,
			msg.MessageID,
			msg.UserID,
			msg.Date,
			msg.Text,
			msg.IsMyself,
			msg.ImageURL,
			msg.ReplyToID,
			msg.ReplyToText,
			msg.ReplyToMyself,
			msg.MessageThreadID,
			msg.ThreadID,
			msg.ThreadRootMessageID,
			msg.ThreadParentMessageID,
			msg.ThreadSource,
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

// GetLastMessages returns the last n messages for a given chat ID up to and including
// lastMessageID, ordered by message_id ascending.
func (db *DB) GetLastMessages(ctx context.Context, chatID int64, n uint64, lastMessageID int64) ([]lilith.Message, error) {
	q := psql.Select(messageColumns...).
		From("chat_messages").
		Where("chat_id = ? AND message_id <= ?", chatID, lastMessageID).
		OrderBy("message_id DESC").
		Limit(n)

	sql, args, err := q.ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "build query")
	}

	rows, err := db.pgx.Query(ctx, sql, args...)
	if err != nil {
		return nil, errors.Wrap(err, "query")
	}
	defer rows.Close()

	var msgs []lilith.Message

	for rows.Next() {
		var msg lilith.Message

		if err := rows.Scan(scanDest(&msg)...); err != nil {
			return nil, errors.Wrap(err, "scan")
		}

		msgs = append(msgs, msg)
	}

	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "rows")
	}

	// Reverse to return messages in ascending order.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	return msgs, nil
}

// CountMessagesSince returns the number of messages for a given chat with
// message_id at most upToMessageID. When sinceMessageID is 0 (no prior snapshot),
// all messages up to upToMessageID are counted. Otherwise only messages with
// message_id strictly greater than sinceMessageID are counted.
func (db *DB) CountMessagesSince(ctx context.Context, chatID, sinceMessageID, upToMessageID int64) (int64, error) {
	q := psql.Select("COUNT(*)").
		From("chat_messages").
		Where("chat_id = ? AND message_id <= ?", chatID, upToMessageID)

	if sinceMessageID != 0 {
		q = q.Where("message_id > ?", sinceMessageID)
	}

	sql, args, err := q.ToSql()
	if err != nil {
		return 0, errors.Wrap(err, "build query")
	}

	var count int64

	if err := db.pgx.QueryRow(ctx, sql, args...).Scan(&count); err != nil {
		return 0, errors.Wrap(err, "scan")
	}

	return count, nil
}

// GetMessage returns a message by chat ID and message ID.
func (db *DB) GetMessage(ctx context.Context, chatID, messageID int64) (*lilith.Message, error) {
	q := psql.Select(messageColumns...).
		From("chat_messages").
		Where("chat_id = ? AND message_id = ?", chatID, messageID)

	sql, args, err := q.ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "build query")
	}

	var msg lilith.Message

	if err := db.pgx.QueryRow(ctx, sql, args...).Scan(scanDest(&msg)...); err != nil {
		return nil, errors.Wrap(err, "scan")
	}

	return &msg, nil
}

// GetLastMessageByAuthorInTopic returns the most recent message authored by
// authorID within the same Telegram topic (messageThreadID) and strictly before
// beforeMessageID. It scans at most lookback recent messages newest-first and
// returns the first match, or (nil, nil) when none qualifies.
func (db *DB) GetLastMessageByAuthorInTopic(ctx context.Context, chatID, authorID int64, messageThreadID *int64, beforeMessageID int64, lookback uint64) (*lilith.Message, error) {
	q := psql.Select(messageColumns...).
		From("chat_messages").
		Where("chat_id = ? AND message_id < ?", chatID, beforeMessageID).
		OrderBy("message_id DESC").
		Limit(lookback)

	sql, args, err := q.ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "build query")
	}

	rows, err := db.pgx.Query(ctx, sql, args...)
	if err != nil {
		return nil, errors.Wrap(err, "query")
	}
	defer rows.Close()

	for rows.Next() {
		var msg lilith.Message

		if err := rows.Scan(scanDest(&msg)...); err != nil {
			return nil, errors.Wrap(err, "scan")
		}

		if msg.UserID != authorID {
			continue
		}

		if !sameTopic(messageThreadID, msg.MessageThreadID) {
			continue
		}

		if err := rows.Err(); err != nil {
			return nil, errors.Wrap(err, "rows")
		}

		return &msg, nil
	}

	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "rows")
	}

	return nil, nil
}

// GetLastMessage returns the most recent message for a given chat, or nil when
// the chat has no messages yet.
func (db *DB) GetLastMessage(ctx context.Context, chatID int64) (*lilith.Message, error) {
	q := psql.Select(messageColumns...).
		From("chat_messages").
		Where("chat_id = ?", chatID).
		OrderBy("message_id DESC").
		Limit(1)

	sql, args, err := q.ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "build query")
	}

	rows, err := db.pgx.Query(ctx, sql, args...)
	if err != nil {
		return nil, errors.Wrap(err, "query")
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, rows.Err()
	}

	var msg lilith.Message

	if err := rows.Scan(scanDest(&msg)...); err != nil {
		return nil, errors.Wrap(err, "scan")
	}

	return &msg, rows.Err()
}

// sameTopic reports whether two Telegram topic ids refer to the same topic,
// treating two nil values as the same (non-forum) topic.
func sameTopic(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}

	if a == nil || b == nil {
		return false
	}

	return *a == *b
}
