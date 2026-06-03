package ai_test

import (
	"context"
	"errors"
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

func TestRespond_WeatherTool(t *testing.T) {
	t.Parallel()

	var calls int
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			calls++
			if calls == 1 {
				return toolCallResponse("call_1", "get_weather", `{"city":"Moscow","country_code":"RU"}`), nil
			}

			return textResponse("за окном тепло"), nil
		},
	}
	weather := &mock.WeatherProviderMock{
		CurrentFunc: func(context.Context, string, string) (*lilith.WeatherReport, error) {
			return &lilith.WeatherReport{
				LocationName: "Москва",
				Country:      "RU",
				Description:  "ясно",
				Temperature:  21,
			}, nil
		},
	}

	res, err := ai.New(completer, "test-model", weather).Respond(context.Background(), basicRequest())
	require.NoError(t, err)
	require.Equal(t, "за окном тепло", res.Text)

	// The provider was queried with the arguments the model supplied.
	weatherCalls := weather.CurrentCalls()
	require.Len(t, weatherCalls, 1)
	require.Equal(t, "Moscow", weatherCalls[0].City)
	require.Equal(t, "RU", weatherCalls[0].CountryCode)

	// The formatted report was fed back to the model on the second call.
	var toolText string
	for _, m := range completer.CreateChatCompletionCalls()[1].Request.Messages {
		if m.Role == openrouter.ChatMessageRoleTool {
			toolText = m.Content.Text
		}
	}
	require.Contains(t, toolText, "Москва")
	require.Contains(t, toolText, "21")
}

func TestRespond_UnknownToolIsIgnored(t *testing.T) {
	t.Parallel()

	var calls int
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			calls++
			if calls == 1 {
				return toolCallResponse("call_1", "do_magic", `{}`), nil
			}

			return textResponse("готово"), nil
		},
	}

	res, err := newClient(completer).Respond(context.Background(), basicRequest())
	require.NoError(t, err)
	require.Equal(t, "готово", res.Text)
	require.Empty(t, res.Reactions)
	require.Len(t, completer.CreateChatCompletionCalls(), 2)
}

func TestRespond_StopsAfterMaxIterations(t *testing.T) {
	t.Parallel()

	// The model never stops calling a tool. Respond must terminate with no
	// error and no text, accumulating one reaction per iteration.
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			return toolCallResponse("call_n", "reply_emoji", `{"emoji":"👍"}`), nil
		},
	}

	res, err := newClient(completer).Respond(context.Background(), basicRequest())
	require.NoError(t, err)
	require.Empty(t, res.Text, "no text is produced when the loop is exhausted")

	calls := completer.CreateChatCompletionCalls()
	require.GreaterOrEqual(t, len(calls), 2, "the tool-call loop must iterate")
	require.Len(t, res.Reactions, len(calls), "one reaction accumulated per iteration")
}

func TestRespond_NoChoicesIsError(t *testing.T) {
	t.Parallel()

	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			return openrouter.ChatCompletionResponse{}, nil
		},
	}

	res, err := newClient(completer).Respond(context.Background(), basicRequest())
	require.Error(t, err)
	require.Nil(t, res)
}

func TestRespond_CompletionErrorIsPropagated(t *testing.T) {
	t.Parallel()

	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			return openrouter.ChatCompletionResponse{}, errors.New("upstream down")
		},
	}

	res, err := newClient(completer).Respond(context.Background(), basicRequest())
	require.Error(t, err)
	require.Nil(t, res)
}

func TestRespond_TrimsLeadingTrailingEmoji(t *testing.T) {
	t.Parallel()

	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			return textResponse("🔥привет🔥"), nil
		},
	}

	res, err := newClient(completer).Respond(context.Background(), basicRequest())
	require.NoError(t, err)
	require.Equal(t, "привет", res.Text)
}
