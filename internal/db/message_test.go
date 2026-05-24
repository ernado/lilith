package db

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/ernado/svetik"
)

type MessageTestSuite struct {
	DBTestSuite
}

func (suite *MessageTestSuite) chat() svetik.Chat {
	ctx := suite.T().Context()

	chat := svetik.Chat{
		ID:   1,
		Info: "test chat",
	}

	suite.Require().NoError(suite.db.UpsertChat(ctx, chat))

	return chat
}

func (suite *MessageTestSuite) TestSaveMessage_Insert() {
	ctx := suite.T().Context()

	chat := suite.chat()
	msg := svetik.Message{
		ChatID:    chat.ID,
		MessageID: 100,
		Text:      "hello",
		IsMyself:  false,
	}

	err := suite.db.SaveMessage(ctx, msg)
	suite.Require().NoError(err)

	got, err := suite.db.GetMessage(ctx, msg.ChatID, msg.MessageID)
	suite.Require().NoError(err)
	suite.Equal(msg, *got)
}

func (suite *MessageTestSuite) TestSaveMessage_DoNothingOnConflict() {
	ctx := suite.T().Context()

	chat := suite.chat()
	msg := svetik.Message{
		ChatID:    chat.ID,
		MessageID: 100,
		Text:      "original text",
		IsMyself:  false,
	}

	err := suite.db.SaveMessage(ctx, msg)
	suite.Require().NoError(err)

	msg.Text = "updated text"

	err = suite.db.SaveMessage(ctx, msg)
	suite.Require().NoError(err)

	got, err := suite.db.GetMessage(ctx, msg.ChatID, msg.MessageID)
	suite.Require().NoError(err)
	suite.Equal("original text", got.Text)
}

func (suite *MessageTestSuite) TestSaveMessage_WithReply() {
	ctx := suite.T().Context()

	chat := suite.chat()
	msg := svetik.Message{
		ChatID:      chat.ID,
		MessageID:   200,
		Text:        "reply message",
		IsMyself:    true,
		ReplyToID:   svetik.T(int64(100)),
		ReplyToText: svetik.T("quoted text"),
	}

	err := suite.db.SaveMessage(ctx, msg)
	suite.Require().NoError(err)

	got, err := suite.db.GetMessage(ctx, msg.ChatID, msg.MessageID)
	suite.Require().NoError(err)
	suite.Equal(msg, *got)
}

func TestMessageTestSuite(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(MessageTestSuite))
}
