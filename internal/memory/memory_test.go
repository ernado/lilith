package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ernado/lilith"
)

// fakeDB implements lilith.DB; only the methods exercised by Memory are
// overridden. Any unexpected call panics via the embedded nil interface.
type fakeDB struct {
	lilith.DB

	lastNotesMsgID int64
	notes          []lilith.ChatNote
	nextNoteID     int64
	messageCount   int64

	addCalls     int
	replaceCalls int
}

func (f *fakeDB) GetChat(_ context.Context, id int64) (*lilith.Chat, error) {
	return &lilith.Chat{ID: id, LastNotesMsgID: f.lastNotesMsgID}, nil
}

func (f *fakeDB) CountMessagesSince(_ context.Context, _, _, _ int64) (int64, error) {
	return f.messageCount, nil
}

func (f *fakeDB) GetLastMessages(_ context.Context, _ int64, _ uint64, _ int64) ([]lilith.Message, error) {
	return []lilith.Message{{Text: "hello"}}, nil
}

func (f *fakeDB) GetChatNotes(_ context.Context, chatID int64) ([]lilith.ChatNote, error) {
	return f.notes, nil
}

func (f *fakeDB) AddChatNote(_ context.Context, chatID int64, text string) (*lilith.ChatNote, error) {
	f.addCalls++
	f.nextNoteID++
	note := lilith.ChatNote{ID: f.nextNoteID, ChatID: chatID, Text: text}
	f.notes = append(f.notes, note)

	return &note, nil
}

func (f *fakeDB) ReplaceChatNotes(_ context.Context, chatID int64, text string) (*lilith.ChatNote, error) {
	f.replaceCalls++
	f.nextNoteID++
	note := lilith.ChatNote{ID: f.nextNoteID, ChatID: chatID, Text: text}
	f.notes = []lilith.ChatNote{note}

	return &note, nil
}

func (f *fakeDB) SetLastNotesMsgID(_ context.Context, _ int64, msgID int64) (int64, error) {
	prev := f.lastNotesMsgID
	f.lastNotesMsgID = msgID

	return prev, nil
}

// fakeAI implements lilith.AI; only GenerateNotes is used by Memory.
type fakeAI struct {
	lilith.AI

	text  string
	calls int
}

func (f *fakeAI) GenerateNotes(_ context.Context, _ []lilith.ChatNote, _ []lilith.Message) (string, error) {
	f.calls++
	return f.text, nil
}

func TestMaintain_BelowThresholdDoesNothing(t *testing.T) {
	t.Parallel()

	db := &fakeDB{messageCount: contextWindowMessages - 1}
	ai := &fakeAI{text: "- fact"}
	m := New(db, ai)

	require.NoError(t, m.Maintain(context.Background(), 1, lilith.Message{MessageID: 10}))
	require.Zero(t, ai.calls, "should not generate notes below threshold")
	require.Empty(t, db.notes)
}

func TestMaintain_RewritesMemoryAndAdvances(t *testing.T) {
	t.Parallel()

	db := &fakeDB{messageCount: contextWindowMessages}
	ai := &fakeAI{text: "- fact"}
	m := New(db, ai)

	require.NoError(t, m.Maintain(context.Background(), 1, lilith.Message{MessageID: 200}))

	require.Equal(t, 1, ai.calls)
	require.Equal(t, 1, db.replaceCalls)
	require.Zero(t, db.addCalls, "must not append")
	require.Len(t, db.notes, 1)
	require.Equal(t, "- fact", db.notes[0].Text)
	require.Equal(t, int64(200), db.lastNotesMsgID)
}

func TestMaintain_CollapsesExistingNotesIntoOne(t *testing.T) {
	t.Parallel()

	// A chat that still carries several legacy rows from the append-only era.
	db := &fakeDB{
		messageCount: contextWindowMessages,
		notes: []lilith.ChatNote{
			{ID: 1, ChatID: 1, Text: "- old 1"},
			{ID: 2, ChatID: 1, Text: "- old 2"},
			{ID: 3, ChatID: 1, Text: "- old 3"},
		},
		nextNoteID: 3,
	}
	ai := &fakeAI{text: "- consolidated"}
	m := New(db, ai)

	require.NoError(t, m.Maintain(context.Background(), 1, lilith.Message{MessageID: 300}))

	require.Equal(t, 1, db.replaceCalls)
	require.Len(t, db.notes, 1, "legacy rows collapse into one evolving note")
	require.Equal(t, "- consolidated", db.notes[0].Text)
}

func TestMaintain_EmptyGenerationKeepsNotesButAdvancesWatermark(t *testing.T) {
	t.Parallel()

	db := &fakeDB{
		messageCount: contextWindowMessages,
		notes:        []lilith.ChatNote{{ID: 1, ChatID: 1, Text: "- keep me"}},
		nextNoteID:   1,
	}
	ai := &fakeAI{text: ""}
	m := New(db, ai)

	require.NoError(t, m.Maintain(context.Background(), 1, lilith.Message{MessageID: 200}))

	require.Equal(t, 1, ai.calls)
	require.Zero(t, db.replaceCalls, "empty generation must not overwrite memory")
	require.Len(t, db.notes, 1)
	require.Equal(t, "- keep me", db.notes[0].Text)
	require.Equal(t, int64(200), db.lastNotesMsgID,
		"watermark advances even when empty to avoid re-summarizing the same window")
}
