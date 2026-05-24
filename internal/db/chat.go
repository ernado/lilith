package db

import (
	"context"

	"github.com/go-faster/errors"

	"github.com/ernado/svetik"
)

// UpsertChat inserts or updates a chat record.
func (db *DB) UpsertChat(ctx context.Context, chat svetik.Chat) error {
	q := psql.Insert("chat").
		Columns("id", "info").
		Values(chat.ID, chat.Info).
		Suffix("ON CONFLICT (id) DO UPDATE SET info = EXCLUDED.info")

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
func (db *DB) GetChat(ctx context.Context, id int64) (*svetik.Chat, error) {
	q := psql.Select("id", "info").
		From("chat").
		Where("id = ?", id)

	sql, args, err := q.ToSql()
	if err != nil {
		return nil, errors.Wrap(err, "build query")
	}

	var chat svetik.Chat

	err = db.pgx.QueryRow(ctx, sql, args...).Scan(&chat.ID, &chat.Info)
	if err != nil {
		return nil, errors.Wrap(err, "scan")
	}

	return &chat, nil
}
