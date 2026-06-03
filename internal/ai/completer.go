package ai

import (
	"context"

	"github.com/revrost/go-openrouter"
)

//go:generate go tool moq -out ../mock/chat_completer.go -pkg mock . ChatCompleter

// ChatCompleter is the subset of the OpenRouter client used by Client. It is an
// interface so the completion loop (including the tool-call handling) can be
// exercised in tests without calling the live API.
type ChatCompleter interface {
	CreateChatCompletion(ctx context.Context, request openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error)
}
