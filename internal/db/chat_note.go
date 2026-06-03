package db

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/ernado/lilith"
)

// AddChatNote inserts a new note for the given chat and returns the created note.
func (db *DB) AddChatNote(ctx context.Context, chatID int64, text string) (*lilith.ChatNote, error) {
	q := psql.Insert("chat_notes").
		Columns("chat_id", "text").
		Values(chatID, text).
		Suffix("RETURNING id, chat_id, text")

	sql, args, err := q.ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "build query")
	}

	var note lilith.ChatNote

	if err := db.pgx.QueryRow(ctx, sql, args...).Scan(
		&note.ID,
		&note.ChatID,
		&note.Text,
	); err != nil {
		return nil, errors.Wrap(err, "scan")
	}

	return &note, nil
}

// ReplaceChatNotes atomically replaces all of a chat's notes with a single
// consolidated note and returns the created note. Replacing in a transaction
// keeps the notes as one evolving memory document rather than an unbounded log.
func (db *DB) ReplaceChatNotes(ctx context.Context, chatID int64, text string) (*lilith.ChatNote, error) {
	tx, err := db.pgx.Begin(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "begin tx")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	deleteSQL, deleteArgs, err := psql.Delete("chat_notes").
		Where("chat_id = ?", chatID).
		ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "build delete query")
	}

	if _, err := tx.Exec(ctx, deleteSQL, deleteArgs...); err != nil {
		return nil, errors.Wrap(err, "delete notes")
	}

	insertSQL, insertArgs, err := psql.Insert("chat_notes").
		Columns("chat_id", "text").
		Values(chatID, text).
		Suffix("RETURNING id, chat_id, text").
		ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "build insert query")
	}

	var note lilith.ChatNote

	if err := tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(
		&note.ID,
		&note.ChatID,
		&note.Text,
	); err != nil {
		return nil, errors.Wrap(err, "scan")
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, errors.Wrap(err, "commit tx")
	}

	return &note, nil
}

// GetChatNotes returns all notes for the given chat.
func (db *DB) GetChatNotes(ctx context.Context, chatID int64) ([]lilith.ChatNote, error) {
	q := psql.Select("id", "chat_id", "text").
		From("chat_notes").
		Where("chat_id = ?", chatID).
		OrderBy("id ASC")

	sql, args, err := q.ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "build query")
	}

	rows, err := db.pgx.Query(ctx, sql, args...)
	if err != nil {
		return nil, errors.Wrap(err, "query")
	}
	defer rows.Close()

	var notes []lilith.ChatNote

	for rows.Next() {
		var note lilith.ChatNote

		if err := rows.Scan(&note.ID, &note.ChatID, &note.Text); err != nil {
			return nil, errors.Wrap(err, "scan")
		}

		notes = append(notes, note)
	}

	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "rows")
	}

	return notes, nil
}
