package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ernado/lilith"
	"github.com/ernado/lilith/internal/mock"
)

// dbState is the mutable state shared by the DB mock's method closures, so the
// mock behaves like a tiny in-memory store across calls within a test.
type dbState struct {
	lastNotesMsgID int64
	notes          []lilith.ChatNote
	nextNoteID     int64
	messageCount   int64
}

// newDBMock returns a DB mock backed by s. Methods not exercised by Memory are
// left unset and panic if called.
func newDBMock(s *dbState) *mock.DBMock {
	return &mock.DBMock{
		GetChatFunc: func(_ context.Context, id int64) (*lilith.Chat, error) {
			return &lilith.Chat{ID: id, LastNotesMsgID: s.lastNotesMsgID}, nil
		},
		CountMessagesSinceFunc: func(_ context.Context, _, _, _ int64) (int64, error) {
			return s.messageCount, nil
		},
		GetLastMessagesFunc: func(_ context.Context, _ int64, _ uint64, _ int64) ([]lilith.Message, error) {
			return []lilith.Message{{Text: "hello"}}, nil
		},
		GetChatNotesFunc: func(_ context.Context, _ int64) ([]lilith.ChatNote, error) {
			return s.notes, nil
		},
		ReplaceChatNotesFunc: func(_ context.Context, chatID int64, text string) (*lilith.ChatNote, error) {
			s.nextNoteID++
			note := lilith.ChatNote{ID: s.nextNoteID, ChatID: chatID, Text: text}
			s.notes = []lilith.ChatNote{note}

			return &note, nil
		},
		SetLastNotesMsgIDFunc: func(_ context.Context, _ int64, msgID int64) (int64, error) {
			prev := s.lastNotesMsgID
			s.lastNotesMsgID = msgID

			return prev, nil
		},
	}
}

// newAIMock returns an AI mock whose GenerateNotes always yields text.
func newAIMock(text string) *mock.AIMock {
	return &mock.AIMock{
		GenerateNotesFunc: func(_ context.Context, _ string, _ []lilith.ChatNote, _ []lilith.Message) (string, error) {
			return text, nil
		},
	}
}

func TestMaintain_BelowThresholdDoesNothing(t *testing.T) {
	t.Parallel()

	s := &dbState{messageCount: contextWindowMessages - 1}
	db := newDBMock(s)
	ai := newAIMock("- fact")
	m := New(db, ai)

	require.NoError(t, m.Maintain(context.Background(), 1, lilith.Message{MessageID: 10}))
	require.Empty(t, ai.GenerateNotesCalls(), "should not generate notes below threshold")
	require.Empty(t, s.notes)
}

func TestMaintain_RewritesMemoryAndAdvances(t *testing.T) {
	t.Parallel()

	s := &dbState{messageCount: contextWindowMessages}
	db := newDBMock(s)
	ai := newAIMock("- fact")
	m := New(db, ai)

	require.NoError(t, m.Maintain(context.Background(), 1, lilith.Message{MessageID: 200}))

	require.Len(t, ai.GenerateNotesCalls(), 1)
	require.Len(t, db.ReplaceChatNotesCalls(), 1)
	require.Empty(t, db.AddChatNoteCalls(), "must not append")
	require.Len(t, s.notes, 1)
	require.Equal(t, "- fact", s.notes[0].Text)
	require.Equal(t, int64(200), s.lastNotesMsgID)
}

func TestMaintain_CollapsesExistingNotesIntoOne(t *testing.T) {
	t.Parallel()

	// A chat that still carries several legacy rows from the append-only era.
	s := &dbState{
		messageCount: contextWindowMessages,
		notes: []lilith.ChatNote{
			{ID: 1, ChatID: 1, Text: "- old 1"},
			{ID: 2, ChatID: 1, Text: "- old 2"},
			{ID: 3, ChatID: 1, Text: "- old 3"},
		},
		nextNoteID: 3,
	}
	db := newDBMock(s)
	ai := newAIMock("- consolidated")
	m := New(db, ai)

	require.NoError(t, m.Maintain(context.Background(), 1, lilith.Message{MessageID: 300}))

	require.Len(t, db.ReplaceChatNotesCalls(), 1)
	require.Len(t, s.notes, 1, "legacy rows collapse into one evolving note")
	require.Equal(t, "- consolidated", s.notes[0].Text)
}

func TestMaintain_EmptyGenerationKeepsNotesButAdvancesWatermark(t *testing.T) {
	t.Parallel()

	s := &dbState{
		messageCount: contextWindowMessages,
		notes:        []lilith.ChatNote{{ID: 1, ChatID: 1, Text: "- keep me"}},
		nextNoteID:   1,
	}
	db := newDBMock(s)
	ai := newAIMock("")
	m := New(db, ai)

	require.NoError(t, m.Maintain(context.Background(), 1, lilith.Message{MessageID: 200}))

	require.Len(t, ai.GenerateNotesCalls(), 1)
	require.Empty(t, db.ReplaceChatNotesCalls(), "empty generation must not overwrite memory")
	require.Len(t, s.notes, 1)
	require.Equal(t, "- keep me", s.notes[0].Text)
	require.Equal(t, int64(200), s.lastNotesMsgID,
		"watermark advances even when empty to avoid re-summarizing the same window")
}
