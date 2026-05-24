package svetik

// Chat represents a Telegram chat.
type Chat struct {
	ID   int64
	Info string
}

// Message represents a Telegram chat message.
type Message struct {
	ChatID        int64
	MessageID     int64
	Text          string
	IsMyself      bool
	ReplyToID     *int64
	ReplyToText   *string
	ReplyToMyself *bool
}
