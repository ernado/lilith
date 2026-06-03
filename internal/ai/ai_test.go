package ai_test

import (
	"context"
	"strings"
	"testing"

	"github.com/revrost/go-openrouter"
	"github.com/stretchr/testify/require"

	"github.com/ernado/lilith"
	"github.com/ernado/lilith/internal/ai"
	"github.com/ernado/lilith/internal/mock"
)

// textResponse builds a completion that returns a plain text reply.
func textResponse(text string) openrouter.ChatCompletionResponse {
	return openrouter.ChatCompletionResponse{
		Choices: []openrouter.ChatCompletionChoice{
			{Message: openrouter.ChatCompletionMessage{Content: openrouter.Content{Text: text}}},
		},
	}
}

// toolCallResponse builds a completion that calls a single tool with the given
// JSON arguments and no text.
func toolCallResponse(id, name, args string) openrouter.ChatCompletionResponse {
	return openrouter.ChatCompletionResponse{
		Choices: []openrouter.ChatCompletionChoice{
			{Message: openrouter.ChatCompletionMessage{
				ToolCalls: []openrouter.ToolCall{
					{
						ID:       id,
						Type:     openrouter.ToolTypeFunction,
						Function: openrouter.FunctionCall{Name: name, Arguments: args},
					},
				},
			}},
		},
	}
}

func newClient(completer ai.ChatCompleter) *ai.Client {
	return ai.New(completer, "test-model", &mock.WeatherProviderMock{})
}

func basicRequest() lilith.ResponseRequest {
	return lilith.ResponseRequest{
		Current: lilith.Context{Message: &lilith.Message{Text: "привет"}},
	}
}

func TestRespond_PlainText(t *testing.T) {
	t.Parallel()

	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			return textResponse("здравствуй"), nil
		},
	}

	res, err := newClient(completer).Respond(context.Background(), basicRequest())
	require.NoError(t, err)
	require.Equal(t, "здравствуй", res.Text)
	require.Empty(t, res.Reactions)
	require.Len(t, completer.CreateChatCompletionCalls(), 1)
}

func TestRespond_EmojiToolThenText(t *testing.T) {
	t.Parallel()

	var calls int
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			calls++
			if calls == 1 {
				return toolCallResponse("call_1", "reply_emoji", `{"emoji":"👍"}`), nil
			}

			return textResponse("ок"), nil
		},
	}

	res, err := newClient(completer).Respond(context.Background(), basicRequest())
	require.NoError(t, err)
	require.Equal(t, "ок", res.Text)
	require.Equal(t, []string{"👍"}, res.Reactions)
	require.Len(t, completer.CreateChatCompletionCalls(), 2)
}

// The reply_emoji tool result fed back to the model must carry the chosen
// emoji, not an empty value. This pins the fix for the arguments-unmarshaled-
// after-marshaling-the-result ordering bug.
func TestRespond_EmojiToolResultCarriesEmoji(t *testing.T) {
	t.Parallel()

	var calls int
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			calls++
			if calls == 1 {
				return toolCallResponse("call_1", "reply_emoji", `{"emoji":"🔥"}`), nil
			}

			return textResponse("done"), nil
		},
	}

	_, err := newClient(completer).Respond(context.Background(), basicRequest())
	require.NoError(t, err)

	recorded := completer.CreateChatCompletionCalls()
	require.Len(t, recorded, 2)

	// The second request must include the tool result message with the emoji.
	var toolContents []string
	for _, m := range recorded[1].Request.Messages {
		if m.Role == openrouter.ChatMessageRoleTool {
			toolContents = append(toolContents, m.Content.Text)
		}
	}

	require.NotEmpty(t, toolContents, "second call must include a tool result message")
	require.True(t,
		strings.Contains(strings.Join(toolContents, "\n"), "🔥"),
		"tool result fed to the model must carry the chosen emoji, got %q", toolContents,
	)
}
