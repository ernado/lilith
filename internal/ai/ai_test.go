package ai_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
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
	return ai.New(completer, "test-model", &mock.WeatherProviderMock{}, &mock.DiscordProviderMock{}, nil, nil)
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

// Each tool result fed back to the model must be preceded by an assistant
// message that carries the matching tool_calls entry, as the tool protocol
// requires. This pins the fix for the missing assistant tool_calls message.
func TestRespond_ToolResultPrecededByAssistantToolCall(t *testing.T) {
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

	_, err := newClient(completer).Respond(context.Background(), basicRequest())
	require.NoError(t, err)

	messages := completer.CreateChatCompletionCalls()[1].Request.Messages

	// Find the tool result and assert an assistant message announcing the same
	// tool call id comes before it.
	toolIndex := -1
	for i, m := range messages {
		if m.Role == openrouter.ChatMessageRoleTool && m.ToolCallID == "call_1" {
			toolIndex = i
			break
		}
	}
	require.NotEqual(t, -1, toolIndex, "tool result message must be present")

	var announced bool
	for _, m := range messages[:toolIndex] {
		if m.Role != openrouter.ChatMessageRoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID == "call_1" {
				announced = true
			}
		}
	}
	require.True(t, announced, "tool result must be preceded by an assistant message carrying its tool_calls entry")
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

	res, err := ai.New(completer, "test-model", weather, &mock.DiscordProviderMock{}, nil, nil).Respond(context.Background(), basicRequest())
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

func TestRespond_DiscordTool(t *testing.T) {
	t.Parallel()

	var calls int
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			calls++
			if calls == 1 {
				return toolCallResponse("call_1", "get_discord_channels", `{}`), nil
			}

			return textResponse("в General сидит ernado"), nil
		},
	}
	discord := &mock.DiscordProviderMock{
		PopulatedChannelsFunc: func(context.Context) ([]lilith.DiscordChannel, error) {
			return []lilith.DiscordChannel{
				{
					ID:    "1",
					Name:  "General",
					Guild: "lilith",
					Members: []lilith.DiscordMember{
						{ID: "42", Username: "ernado", Nickname: "Aleksandr"},
					},
				},
			}, nil
		},
	}

	res, err := ai.New(completer, "test-model", &mock.WeatherProviderMock{}, discord, nil, nil).
		Respond(context.Background(), basicRequest())
	require.NoError(t, err)
	require.Equal(t, "в General сидит ernado", res.Text)

	// The provider was queried once.
	require.Len(t, discord.PopulatedChannelsCalls(), 1)

	// The serialized channels were fed back to the model on the second call.
	var toolText string
	for _, m := range completer.CreateChatCompletionCalls()[1].Request.Messages {
		if m.Role == openrouter.ChatMessageRoleTool {
			toolText = m.Content.Text
		}
	}
	require.Contains(t, toolText, "General")
	require.Contains(t, toolText, "ernado")
}

// The get_discord_channels tool is only offered to the model when a Discord
// provider is configured.
func TestRespond_DiscordToolOmittedWhenNil(t *testing.T) {
	t.Parallel()

	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			return textResponse("привет"), nil
		},
	}

	_, err := ai.New(completer, "test-model", &mock.WeatherProviderMock{}, nil, nil, nil).
		Respond(context.Background(), basicRequest())
	require.NoError(t, err)

	for _, tool := range completer.CreateChatCompletionCalls()[0].Request.Tools {
		if tool.Function != nil {
			require.NotEqual(t, "get_discord_channels", tool.Function.Name)
		}
	}
}

func TestRespond_ImageTool(t *testing.T) {
	t.Parallel()

	var calls int
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			calls++
			if calls == 1 {
				return toolCallResponse("call_1", "generate_image",
					`{"prompt":"a cat sitting on a windowsill","positive_tags":"1girl, cat","negative_tags":"blurry"}`), nil
			}

			return textResponse("вот кот"), nil
		},
	}
	generator := &mock.ImageGeneratorMock{
		GenerateFunc: func(_ context.Context, req lilith.ImageRequest) ([]lilith.GeneratedImage, error) {
			return []lilith.GeneratedImage{{Data: []byte("png-bytes"), Format: "png"}}, nil
		},
	}
	fallback := &mock.ImageGeneratorMock{
		GenerateFunc: func(context.Context, lilith.ImageRequest) ([]lilith.GeneratedImage, error) {
			return nil, nil
		},
	}

	// The current message carries an image, which must flow through as a
	// generation reference.
	req := basicRequest()
	req.ImageURL = "https://cdn/current.png"

	res, err := ai.New(completer, "test-model", &mock.WeatherProviderMock{}, nil, generator, fallback).
		Respond(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "вот кот", res.Text)

	// The generated image is surfaced on the result for the caller to send.
	require.Len(t, res.Images, 1)
	require.Equal(t, []byte("png-bytes"), res.Images[0].Data)
	require.Equal(t, "png", res.Images[0].Format)

	// The full tool arguments (prompt + tags) are surfaced as JSON so the caller
	// can persist them for re-generation.
	require.JSONEq(t,
		`{"prompt":"a cat sitting on a windowsill","positive_tags":"1girl, cat","negative_tags":"blurry"}`,
		res.ImagePrompt,
	)

	// The primary generator was called with the natural-language prompt and the
	// reference image; the fallback was not needed.
	genCalls := generator.GenerateCalls()
	require.Len(t, genCalls, 1)
	require.Equal(t, "a cat sitting on a windowsill", genCalls[0].Req.Prompt)
	require.Empty(t, genCalls[0].Req.Model)
	require.Equal(t, "https://cdn/current.png", genCalls[0].Req.ReferenceImage)
	require.Empty(t, fallback.GenerateCalls())
}

// When the primary generator returns no images, the tool falls back to the
// secondary generator using the booru-style tags.
func TestRespond_ImageToolFallback(t *testing.T) {
	t.Parallel()

	var calls int
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			calls++
			if calls == 1 {
				return toolCallResponse("call_1", "generate_image",
					`{"prompt":"a cat","positive_tags":"1girl, cat","negative_tags":"blurry, lowres"}`), nil
			}

			return textResponse("держи"), nil
		},
	}
	primary := &mock.ImageGeneratorMock{
		GenerateFunc: func(context.Context, lilith.ImageRequest) ([]lilith.GeneratedImage, error) {
			return nil, nil // produces nothing
		},
	}
	fallback := &mock.ImageGeneratorMock{
		GenerateFunc: func(_ context.Context, req lilith.ImageRequest) ([]lilith.GeneratedImage, error) {
			return []lilith.GeneratedImage{{Data: []byte("nai"), Format: "png"}}, nil
		},
	}

	res, err := ai.New(completer, "test-model", &mock.WeatherProviderMock{}, nil, primary, fallback).
		Respond(context.Background(), basicRequest())
	require.NoError(t, err)

	require.Len(t, res.Images, 1)
	require.Equal(t, []byte("nai"), res.Images[0].Data)

	// The fallback received the fixed prefixes prepended to the booru-style tags.
	fbCalls := fallback.GenerateCalls()
	require.Len(t, fbCalls, 1)
	require.Equal(t, "very aesthetic, masterpiece, no text, 1girl, cat", fbCalls[0].Req.Prompt)
	require.Equal(t, "lowres, artistic error, film grain, scan artifacts, worst quality, bad quality, jpeg artifacts, very displeasing, chromatic aberration, dithering, halftone, screentone, multiple views, logo, too many watermarks, negative space, blank page, blurry, lowres", fbCalls[0].Req.NegativePrompt)
}

// While images are generated, the "sending photo" presence callback must fire.
func TestRespond_ImageToolSendsUploadingPhoto(t *testing.T) {
	t.Parallel()

	var calls int
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			calls++
			if calls == 1 {
				return toolCallResponse("call_1", "generate_image",
					`{"prompt":"a cat","positive_tags":"cat","negative_tags":"blurry"}`), nil
			}

			return textResponse("вот"), nil
		},
	}
	generator := &mock.ImageGeneratorMock{
		GenerateFunc: func(context.Context, lilith.ImageRequest) ([]lilith.GeneratedImage, error) {
			return []lilith.GeneratedImage{{Data: []byte("x"), Format: "png"}}, nil
		},
	}

	var uploads atomic.Int64
	req := basicRequest()
	req.UploadingPhoto = func(context.Context) error {
		uploads.Add(1)
		return nil
	}

	_, err := ai.New(completer, "test-model", &mock.WeatherProviderMock{}, nil, generator, nil).
		Respond(context.Background(), req)
	require.NoError(t, err)

	require.GreaterOrEqual(t, uploads.Load(), int64(1), "the uploading-photo presence must fire during generation")
}

// The generate_image tool is only offered to the model when an image generator
// is configured.
func TestRespond_ImageToolOmittedWhenNil(t *testing.T) {
	t.Parallel()

	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			return textResponse("привет"), nil
		},
	}

	_, err := newClient(completer).Respond(context.Background(), basicRequest())
	require.NoError(t, err)

	for _, tool := range completer.CreateChatCompletionCalls()[0].Request.Tools {
		if tool.Function != nil {
			require.NotEqual(t, "generate_image", tool.Function.Name)
		}
	}
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

// hasImagePart reports whether any message carries an image part.
func hasImagePart(messages []openrouter.ChatCompletionMessage) bool {
	for _, m := range messages {
		for _, p := range m.Content.Multi {
			if p.Type == openrouter.ChatMessagePartTypeImageURL {
				return true
			}
		}
	}

	return false
}

// When the model rejects image input, Respond retries once without the images.
func TestRespond_RetriesWithoutImagesOnNoImageEndpoint(t *testing.T) {
	t.Parallel()

	var calls int
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(_ context.Context, _ openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			calls++
			if calls == 1 {
				return openrouter.ChatCompletionResponse{},
					errors.New("No endpoints found that support image input")
			}

			return textResponse("ок"), nil
		},
	}

	req := basicRequest()
	req.ImageURL = "https://cdn/x.png"

	res, err := newClient(completer).Respond(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "ок", res.Text)

	recorded := completer.CreateChatCompletionCalls()
	require.Len(t, recorded, 2)

	// The first attempt carried the image; the retry must not.
	require.True(t, hasImagePart(recorded[0].Request.Messages), "first attempt should carry the image")
	require.False(t, hasImagePart(recorded[1].Request.Messages), "retry must drop the image")
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

func TestAsAPIFailure(t *testing.T) {
	t.Run("NotProviderError", func(t *testing.T) {
		_, ok := ai.AsAPIFailure(errors.New("boom"))
		require.False(t, ok)
	})

	t.Run("RequestWrappingAPIError", func(t *testing.T) {
		// Mirrors the real OpenRouter shape: a RequestError carrying the 429
		// status wraps an APIError holding the provider metadata.
		apiErr := &openrouter.APIError{
			Message: "Provider returned error",
			Metadata: &openrouter.Metadata{
				"provider_name":       "Google AI Studio",
				"provider_error_code": float64(429),
				"raw":                 "google/gemini-3.6-flash is temporarily rate-limited upstream.",
			},
		}
		reqErr := &openrouter.RequestError{HTTPStatusCode: 429, Err: apiErr}

		f, ok := ai.AsAPIFailure(errors.New("wrap: " + reqErr.Error()))
		require.False(t, ok, "plain string must not match")

		f, ok = ai.AsAPIFailure(reqErr)
		require.True(t, ok)
		require.Equal(t, 429, f.StatusCode)
		require.True(t, f.RateLimited)
		require.Equal(t, "Google AI Studio", f.Provider)
		require.Equal(t, "google/gemini-3.6-flash is temporarily rate-limited upstream.", f.Message)
	})

	t.Run("StatusFromMetadataCode", func(t *testing.T) {
		apiErr := &openrouter.APIError{
			Message:  "Provider returned error",
			Metadata: &openrouter.Metadata{"provider_error_code": "503"},
		}

		f, ok := ai.AsAPIFailure(apiErr)
		require.True(t, ok)
		require.Equal(t, 503, f.StatusCode)
		require.False(t, f.RateLimited)
	})
}
