package lilith

import "context"

// DB is the database interface.
type DB interface {
	UpsertChat(ctx context.Context, chat Chat) error
	GetChat(ctx context.Context, id int64) (*Chat, error)
	GetChats(ctx context.Context) ([]Chat, error)
	SetLastNotesMsgID(ctx context.Context, chatID int64, msgID int64) (prev int64, err error)
	SaveMessage(ctx context.Context, msg Message) error
	// DeleteMessage removes a single message from a chat's history by its ID.
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
	GetMessage(ctx context.Context, chatID, messageID int64) (*Message, error)
	GetLastMessages(ctx context.Context, chatID int64, n uint64, lastMessageID int64) ([]Message, error)
	GetLastMessage(ctx context.Context, chatID int64) (*Message, error)
	GetLastMessageByAuthorInTopic(ctx context.Context, chatID, authorID int64, messageThreadID *int64, beforeMessageID int64, lookback uint64) (*Message, error)
	CountMessagesSince(ctx context.Context, chatID, sinceMessageID, upToMessageID int64) (int64, error)
	UpsertChatMember(ctx context.Context, m ChatMember) error
	GetChatMember(ctx context.Context, chatID, userID int64) (*ChatMember, error)
	GetChatMembers(ctx context.Context, chatID int64) ([]ChatMember, error)
	AddChatNote(ctx context.Context, chatID int64, text string) (*ChatNote, error)
	// ReplaceChatNotes atomically replaces all of a chat's notes with a single
	// consolidated note. It is the write path for the evolving memory document,
	// which is rewritten in full on each regeneration.
	ReplaceChatNotes(ctx context.Context, chatID int64, text string) (*ChatNote, error)
	GetChatNotes(ctx context.Context, chatID int64) ([]ChatNote, error)
	SetChatModel(ctx context.Context, chatID int64, model string) error
	Lobotomy(ctx context.Context, chatID int64) error
}
