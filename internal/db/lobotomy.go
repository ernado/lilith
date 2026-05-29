package db

import (
	"context"

	"github.com/go-faster/errors"
)

// Lobotomy deletes all messages and notes for the given chat and resets
// the last notes message ID to zero.
func (db *DB) Lobotomy(ctx context.Context, chatID int64) error {
	tx, err := db.pgx.Begin(ctx)
	if err != nil {
		return errors.Wrap(err, "begin tx")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	deleteMsgs, args, err := psql.Delete("chat_messages").
		Where("chat_id = ?", chatID).
		ToSql()
	if err != nil {
		return errors.Wrap(err, "build delete messages query")
	}

	if _, err := tx.Exec(ctx, deleteMsgs, args...); err != nil {
		return errors.Wrap(err, "delete messages")
	}

	deleteNotes, args, err := psql.Delete("chat_notes").
		Where("chat_id = ?", chatID).
		ToSql()
	if err != nil {
		return errors.Wrap(err, "build delete notes query")
	}

	if _, err := tx.Exec(ctx, deleteNotes, args...); err != nil {
		return errors.Wrap(err, "delete notes")
	}

	resetNotesMsgID, args, err := psql.Update("chat").
		Set("last_notes_msg_id", 0).
		Where("id = ?", chatID).
		ToSql()
	if err != nil {
		return errors.Wrap(err, "build reset notes msg id query")
	}

	if _, err := tx.Exec(ctx, resetNotesMsgID, args...); err != nil {
		return errors.Wrap(err, "reset last notes msg id")
	}

	if err := tx.Commit(ctx); err != nil {
		return errors.Wrap(err, "commit tx")
	}

	return nil
}
