package lilith

import "context"

// ResponseRequest is the domain payload for generating a chat reply. It carries
// only domain types so the AI layer (and its LLM SDK) stays out of the root
// package, mirroring how DB depends on nothing but domain types.
type ResponseRequest struct {
	// Model, when non-empty, overrides the default model for this request.
	Model string
	// CharacterPrompt, when non-empty, is appended to the base character
	// system prompt to customise the bot's persona for this chat.
	CharacterPrompt string
	// CurrentTime is a preformatted, human-readable timestamp injected into the
	// system prompt (e.g. "29 May 26 14:00 +0300, пятница.").
	CurrentTime string
	// Notes are the chat notes to ground the reply.
	Notes []ChatNote
	// Members is the chat member roster.
	Members []ChatMember
	// Self is the bot's own identity in the chat.
	Self Self
	// History is the prior conversation, oldest first, excluding Current.
	History []Context
	// Current is the message being responded to.
	Current Context
	// ImageURL, when non-empty, is attached to the current message as image input.
	ImageURL string
	// Typing, when non-nil, is invoked periodically to keep the chat "typing"
	// indicator alive during long completions. The caller owns the side effect.
	Typing func(context.Context) error
	// UploadingPhoto, when non-nil, is invoked periodically to keep the chat
	// "sending photo" indicator alive while images are being generated. The
	// caller owns the side effect.
	UploadingPhoto func(context.Context) error
	// Idle, when true, signals that no user message triggered this response.
	// The bot is sending proactively after a period of inactivity.
	Idle bool
}

// ResponseResult is the outcome of a Respond call.
type ResponseResult struct {
	// Text is the reply text. It may be empty when the model produced only tool
	// calls (e.g. a reaction) and no message.
	Text string
	// Reactions are canonical emoji the model chose to react with. The caller
	// applies them to the current message.
	Reactions []string
	// Images are images the model generated via the generate_image tool. The
	// caller sends them to the chat.
	Images []GeneratedImage
	// ImagePrompt is the prompt used for the generated images, if any. The
	// caller persists it on the sent message so the model can recall it for
	// re-generation.
	ImagePrompt string
}

//go:generate go tool moq -out internal/mock/ai.go -pkg mock . AI

// AI is the language-model gateway. Implementations are stateless with respect
// to chat storage: all required context is passed in the request, and any
// persistence is the caller's responsibility.
type AI interface {
	// Respond runs the completion loop (including tool calls) and returns the
	// reply text and any reactions chosen by the model.
	Respond(ctx context.Context, req ResponseRequest) (*ResponseResult, error)
	// GenerateNotes summarizes messages into a fresh notes snapshot, given any
	// existing notes. The model is the chat's configured model (empty selects
	// the default). It returns the generated text, which may be empty.
	GenerateNotes(ctx context.Context, model string, existing []ChatNote, messages []Message) (string, error)
	// DefaultModel returns the model name used when no per-chat override is set.
	DefaultModel() string
}
