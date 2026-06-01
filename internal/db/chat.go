package db

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/ernado/lilith"
)

// UpsertChat inserts or updates a chat record.
// access_hash and type are only overwritten when the incoming values are
// non-zero / non-empty, so a call that lacks those fields never clobbers
// previously stored values.
func (db *DB) UpsertChat(ctx context.Context, chat lilith.Chat) error {
	q := psql.Insert("chat").
		Columns("id", "info", "access_hash", "type").
		Values(chat.ID, chat.Info, chat.AccessHash, chat.Type).
		Suffix(`ON CONFLICT (id) DO UPDATE
			SET info        = EXCLUDED.info,
			    access_hash = CASE
			                      WHEN EXCLUDED.access_hash != 0 THEN EXCLUDED.access_hash
			                      ELSE chat.access_hash
			                  END,
			    type        = CASE
			                      WHEN EXCLUDED.type != '' THEN EXCLUDED.type
			                      ELSE chat.type
			                  END`)

	sql, args, err := q.ToSql()
	if err != nil {
		return errors.Wrap(err, "build query")
	}

	if _, err := db.pgx.Exec(ctx, sql, args...); err != nil {
		return errors.Wrap(err, "exec")
	}

	return nil
}

// GetChat returns a chat by ID.
func (db *DB) GetChat(ctx context.Context, id int64) (*lilith.Chat, error) {
	q := psql.Select("id", "info", "last_notes_msg_id", "model", "character_prompt", "idle_enabled", "access_hash", "type").
		From("chat").
		Where("id = ?", id)

	sql, args, err := q.ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "build query")
	}

	var chat lilith.Chat

	err = db.pgx.QueryRow(ctx, sql, args...).Scan(
		&chat.ID, &chat.Info, &chat.LastNotesMsgID, &chat.Model, &chat.CharacterPrompt, &chat.IdleEnabled, &chat.AccessHash, &chat.Type,
	)
	if err != nil {
		return nil, errors.Wrap(err, "scan")
	}

	return &chat, nil
}

// SetChatModel sets the model override for a chat.
func (db *DB) SetChatModel(ctx context.Context, chatID int64, model string) error {
	q := psql.Update("chat").
		Set("model", model).
		Where("id = ?", chatID)

	sql, args, err := q.ToSql()
	if err != nil {
		return errors.Wrap(err, "build query")
	}

	if _, err := db.pgx.Exec(ctx, sql, args...); err != nil {
		return errors.Wrap(err, "exec")
	}

	return nil
}

// GetChats returns all chats.
func (db *DB) GetChats(ctx context.Context) ([]lilith.Chat, error) {
	q := psql.Select("id", "info", "last_notes_msg_id", "model", "character_prompt", "idle_enabled", "access_hash", "type").From("chat")

	sql, args, err := q.ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "build query")
	}

	rows, err := db.pgx.Query(ctx, sql, args...)
	if err != nil {
		return nil, errors.Wrap(err, "query")
	}
	defer rows.Close()

	var chats []lilith.Chat

	for rows.Next() {
		var chat lilith.Chat

		if err := rows.Scan(&chat.ID, &chat.Info, &chat.LastNotesMsgID, &chat.Model, &chat.CharacterPrompt, &chat.IdleEnabled, &chat.AccessHash, &chat.Type); err != nil {
			return nil, errors.Wrap(err, "scan")
		}

		chats = append(chats, chat)
	}

	return chats, rows.Err()
}

// SetLastNotesMsgID updates the last_notes_msg_id for a chat atomically,
// only if the new value is greater than the stored one.
// It returns the value that was stored before the update.
func (db *DB) SetLastNotesMsgID(ctx context.Context, chatID int64, msgID int64) (prev int64, err error) {
	sql := `UPDATE chat
	        SET last_notes_msg_id = $1
	        WHERE id = $2 AND last_notes_msg_id < $1
	        RETURNING (SELECT last_notes_msg_id FROM chat WHERE id = $2)`

	// Use a raw query: fetch prev value first, then conditionally update.
	// Simpler approach: SELECT then UPDATE in one round-trip using a CTE.
	const q = `
WITH prev AS (SELECT last_notes_msg_id FROM chat WHERE id = $2),
     upd  AS (
         UPDATE chat
         SET last_notes_msg_id = $1
         WHERE id = $2 AND last_notes_msg_id < $1
     )
SELECT last_notes_msg_id FROM prev`
	_ = sql

	row := db.pgx.QueryRow(ctx, q, msgID, chatID)
	if err = row.Scan(&prev); err != nil {
		return 0, errors.Wrap(err, "scan")
	}

	return prev, nil
}
