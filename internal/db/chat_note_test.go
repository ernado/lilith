package db

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/ernado/lilith"
)

type ChatNoteTestSuite struct {
	DBTestSuite
}

func (suite *ChatNoteTestSuite) seedChat(id int64) {
	ctx := suite.T().Context()
	err := suite.db.UpsertChat(ctx, lilith.Chat{ID: id, Info: "test chat"})
	suite.Require().NoError(err)
}

func (suite *ChatNoteTestSuite) TestReplaceChatNotes_Insert() {
	ctx := suite.T().Context()
	suite.seedChat(1)

	note, err := suite.db.ReplaceChatNotes(ctx, 1, "- first")
	suite.Require().NoError(err)
	suite.Equal(int64(1), note.ChatID)
	suite.Equal("- first", note.Text)

	notes, err := suite.db.GetChatNotes(ctx, 1)
	suite.Require().NoError(err)
	suite.Require().Len(notes, 1)
	suite.Equal("- first", notes[0].Text)
}

func (suite *ChatNoteTestSuite) TestReplaceChatNotes_CollapsesExisting() {
	ctx := suite.T().Context()
	suite.seedChat(1)

	// Simulate the legacy append-only state with several accumulated rows.
	for _, text := range []string{"- old 1", "- old 2", "- old 3"} {
		_, err := suite.db.AddChatNote(ctx, 1, text)
		suite.Require().NoError(err)
	}

	notes, err := suite.db.GetChatNotes(ctx, 1)
	suite.Require().NoError(err)
	suite.Require().Len(notes, 3)

	replaced, err := suite.db.ReplaceChatNotes(ctx, 1, "- consolidated")
	suite.Require().NoError(err)

	notes, err = suite.db.GetChatNotes(ctx, 1)
	suite.Require().NoError(err)
	suite.Require().Len(notes, 1, "all prior rows must collapse into one")
	suite.Equal("- consolidated", notes[0].Text)
	suite.Equal(replaced.ID, notes[0].ID)
}

func (suite *ChatNoteTestSuite) TestReplaceChatNotes_ScopedToChat() {
	ctx := suite.T().Context()
	suite.seedChat(1)
	suite.seedChat(2)

	_, err := suite.db.AddChatNote(ctx, 2, "- other chat")
	suite.Require().NoError(err)

	_, err = suite.db.ReplaceChatNotes(ctx, 1, "- mine")
	suite.Require().NoError(err)

	other, err := suite.db.GetChatNotes(ctx, 2)
	suite.Require().NoError(err)
	suite.Require().Len(other, 1, "replacing one chat's notes must not affect another")
	suite.Equal("- other chat", other[0].Text)
}

func TestChatNoteTestSuite(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(ChatNoteTestSuite))
}
