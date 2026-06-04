package db

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/ernado/lilith"
)

type ChatMemberTestSuite struct {
	DBTestSuite
}

func (suite *ChatMemberTestSuite) chat() lilith.Chat {
	ctx := suite.T().Context()

	chat := lilith.Chat{
		ID:   1,
		Info: "test chat",
	}

	suite.Require().NoError(suite.db.UpsertChat(ctx, chat))

	return chat
}

func (suite *ChatMemberTestSuite) TestUpsertChatMember_Insert() {
	ctx := suite.T().Context()

	chat := suite.chat()
	member := lilith.ChatMember{
		ChatID:    chat.ID,
		UserID:    42,
		Username:  "johndoe",
		FirstName: "John",
		LastName:  "Doe",
		IsAdmin:   false,
		IsCreator: false,
		Rank:      "",
	}

	err := suite.db.UpsertChatMember(ctx, member)
	suite.Require().NoError(err)

	got, err := suite.db.GetChatMember(ctx, member.ChatID, member.UserID)
	suite.Require().NoError(err)
	suite.Equal(member, *got)
}

func (suite *ChatMemberTestSuite) TestUpsertChatMember_Update() {
	ctx := suite.T().Context()

	chat := suite.chat()
	member := lilith.ChatMember{
		ChatID:    chat.ID,
		UserID:    42,
		Username:  "johndoe",
		FirstName: "John",
		LastName:  "Doe",
		IsAdmin:   false,
		IsCreator: false,
		Rank:      "",
	}

	err := suite.db.UpsertChatMember(ctx, member)
	suite.Require().NoError(err)

	member.Username = "janedoe"
	member.FirstName = "Jane"
	member.IsAdmin = true
	member.Rank = "moderator"

	err = suite.db.UpsertChatMember(ctx, member)
	suite.Require().NoError(err)

	got, err := suite.db.GetChatMember(ctx, member.ChatID, member.UserID)
	suite.Require().NoError(err)
	suite.Equal(member, *got)
}

// A later upsert carrying empty first_name, last_name or rank must not clobber
// values already stored: these often arrive empty from sparse updates.
func (suite *ChatMemberTestSuite) TestUpsertChatMember_PreservesNamesAndRankOnEmpty() {
	ctx := suite.T().Context()

	chat := suite.chat()
	member := lilith.ChatMember{
		ChatID:    chat.ID,
		UserID:    42,
		Username:  "johndoe",
		FirstName: "John",
		LastName:  "Doe",
		IsAdmin:   true,
		IsCreator: false,
		Rank:      "moderator",
	}

	suite.Require().NoError(suite.db.UpsertChatMember(ctx, member))

	// A sparse update: names and rank empty, admin status flips.
	sparse := lilith.ChatMember{
		ChatID:    chat.ID,
		UserID:    42,
		Username:  "johndoe",
		FirstName: "",
		LastName:  "",
		IsAdmin:   false,
		IsCreator: false,
		Rank:      "",
	}

	suite.Require().NoError(suite.db.UpsertChatMember(ctx, sparse))

	got, err := suite.db.GetChatMember(ctx, member.ChatID, member.UserID)
	suite.Require().NoError(err)

	// Names and rank are preserved; the authoritative boolean still updates.
	suite.Equal("John", got.FirstName)
	suite.Equal("Doe", got.LastName)
	suite.Equal("moderator", got.Rank)
	suite.False(got.IsAdmin)
}

func TestChatMemberTestSuite(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(ChatMemberTestSuite))
}
