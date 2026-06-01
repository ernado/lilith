// Package memory implements the chat-notes layer (lilith.Memory). It owns the
// policy for when notes are generated and persisted, delegating the actual
// summarization to lilith.AI and storage to lilith.DB.
package memory

import (
	"context"
	"strconv"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

	"github.com/ernado/lilith"
)

// contextWindowMessages is the number of messages summarized into a notes
// snapshot, and the threshold that triggers one.
const contextWindowMessages = 150

var _ lilith.Memory = (*Memory)(nil)

// Memory is the DB+AI-backed implementation of lilith.Memory.
type Memory struct {
	db lilith.DB
	ai lilith.AI

	// sfg deduplicates concurrent note-generation calls per chat/message.
	sfg singleflight.Group
}

// New returns a Memory backed by db and ai.
func New(db lilith.DB, ai lilith.AI) *Memory {
	return &Memory{db: db, ai: ai}
}

// Notes returns the current notes for a chat.
func (m *Memory) Notes(ctx context.Context, chatID int64) ([]lilith.ChatNote, error) {
	return m.db.GetChatNotes(ctx, chatID)
}

// Maintain regenerates the notes snapshot when enough messages have accumulated
// since the last one. It is a no-op otherwise.
func (m *Memory) Maintain(ctx context.Context, chatID int64, msg lilith.Message) error {
	needed, err := m.isNotesNeeded(ctx, chatID, msg.MessageID)
	if err != nil {
		return errors.Wrap(err, "is notes needed")
	}

	if needed {
		return m.generateNotes(ctx, chatID, msg.MessageID)
	}

	return nil
}

// isNotesNeeded returns true when at least contextWindowMessages messages have
// been recorded in the chat since the last notes snapshot.
func (m *Memory) isNotesNeeded(ctx context.Context, chatID, currentMsgID int64) (bool, error) {
	chat, err := m.db.GetChat(ctx, chatID)
	if err != nil {
		return false, errors.Wrap(err, "get chat")
	}

	count, err := m.db.CountMessagesSince(ctx, chatID, chat.LastNotesMsgID, currentMsgID)
	if err != nil {
		return false, errors.Wrap(err, "count messages since")
	}

	zctx.From(ctx).Info("isNotesNeeded",
		zap.Int64("chatID", chat.ID),
		zap.Int64("currentMsgID", currentMsgID),
		zap.Int64("count", count),
	)

	return count >= contextWindowMessages, nil
}

// generateNotes generates and persists a notes snapshot for the chat at
// currentMsgID. Concurrent calls for the same chat are coalesced via
// singleflight.
func (m *Memory) generateNotes(ctx context.Context, chatID, currentMsgID int64) error {
	key := strconv.FormatInt(chatID, 10)

	_, err, _ := m.sfg.Do(key, func() (any, error) {
		return nil, m.doGenerateNotes(ctx, chatID, currentMsgID)
	})

	return err
}

func (m *Memory) doGenerateNotes(ctx context.Context, chatID, currentMsgID int64) error {
	lg := zctx.From(ctx).With(zap.Int64("chat_id", chatID))
	lg.Info("Generating notes snapshot")

	lastMessages, err := m.db.GetLastMessages(ctx, chatID, contextWindowMessages, currentMsgID)
	if err != nil {
		return errors.Wrap(err, "get last messages")
	}

	existingNotes, err := m.db.GetChatNotes(ctx, chatID)
	if err != nil {
		return errors.Wrap(err, "get chat notes")
	}

	text, err := m.ai.GenerateNotes(ctx, existingNotes, lastMessages)
	if err != nil {
		return errors.Wrap(err, "generate notes")
	}

	if text == "" {
		lg.Info("No new notes generated")
		return nil
	}

	if _, err := m.db.AddChatNote(ctx, chatID, text); err != nil {
		return errors.Wrap(err, "add chat note")
	}

	if _, err := m.db.SetLastNotesMsgID(ctx, chatID, currentMsgID); err != nil {
		return errors.Wrap(err, "set last notes msg id")
	}

	lg.Info("Notes generated",
		zap.Int64("msg_id", currentMsgID),
		zap.String("text", text),
	)

	return nil
}
