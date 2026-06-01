// Package ai implements the language-model gateway (lilith.AI) on top of
// OpenRouter. It owns prompt assembly, tool definitions and the tool-call loop;
// all chat state is supplied by the caller via lilith.ResponseRequest.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/revrost/go-openrouter"
	"github.com/revrost/go-openrouter/jsonschema"
	"go.uber.org/zap"

	"github.com/ernado/lilith"
	"github.com/ernado/lilith/internal/prompt"
	"github.com/ernado/lilith/internal/reaction"
)

const (
	// maxTokens controls the length of a chat reply.
	maxTokens = 450

	// maxNotesTokens controls the length of generated notes.
	maxNotesTokens = 1024

	// maxIterations bounds the tool-call loop.
	maxIterations = 4

	// minNoteLen is the minimum length of a single-message note worth keeping.
	minNoteLen = 40

	// maxContextImages is the maximum number of most-recent images attached to
	// the model context. Older images are replaced with imageTooOldText.
	maxContextImages = 3

	// imageTooOldText replaces images that fall outside the most-recent window.
	imageTooOldText = "[image too old]"
)

var _ lilith.AI = (*Client)(nil)

// Client is the OpenRouter-backed implementation of lilith.AI.
type Client struct {
	ai      *openrouter.Client
	model   string
	weather lilith.WeatherProvider
}

// New returns a Client using the given OpenRouter client, model and weather
// provider (used for the weather tool).
func New(ai *openrouter.Client, model string, weather lilith.WeatherProvider) *Client {
	return &Client{
		ai:      ai,
		model:   model,
		weather: weather,
	}
}

// DefaultModel returns the model name used when no per-chat override is set.
func (c *Client) DefaultModel() string {
	return c.model
}

func emojiTool() openrouter.Tool {
	return openrouter.Tool{
		Type: openrouter.ToolTypeFunction,
		Function: &openrouter.FunctionDefinition{
			Name:        "reply_emoji",
			Description: "Repl to message with emoji. Allowed reactions:" + strings.Join(reaction.Allowed, ""),
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"emoji": {
						Type:        jsonschema.String,
						Description: "Emoji to reply",
					},
				},
				Required: []string{"emoji"},
			},
		},
	}
}

func weatherTool() openrouter.Tool {
	return openrouter.Tool{
		Type: openrouter.ToolTypeFunction,
		Function: &openrouter.FunctionDefinition{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"city": {
						Type:        jsonschema.String,
						Description: "City name, Moscow",
					},
					"country_code": {
						Type:        jsonschema.String,
						Description: "Country code, RU",
					},
				},
			},
		},
	}
}

// imagePart builds a high-detail image content part for a hosted image URL.
func imagePart(url string) openrouter.ChatMessagePart {
	return openrouter.ChatMessagePart{
		Type: openrouter.ChatMessagePartTypeImageURL,
		ImageURL: &openrouter.ChatMessageImageURL{
			URL:    url,
			Detail: openrouter.ImageURLDetailHigh,
		},
	}
}

// keptHistoryImageIndices returns the set of req.History indices whose image is
// recent enough to attach to the model context. Only the most recent
// maxContextImages images are kept, counting the current message's image (if
// any) as the newest. History is oldest-first, so newer images have higher
// indices.
func keptHistoryImageIndices(req lilith.ResponseRequest) map[int]bool {
	keep := make(map[int]bool)

	budget := maxContextImages
	if req.ImageURL != "" {
		// The current message's image is the newest and always takes a slot.
		budget--
	}

	for i := len(req.History) - 1; i >= 0 && budget > 0; i-- {
		if msg := req.History[i].Message; msg != nil && msg.ImageURL != "" {
			keep[i] = true
			budget--
		}
	}

	return keep
}

// buildResponseDialog assembles the OpenRouter messages for a reply from the
// domain request.
func buildResponseDialog(req lilith.ResponseRequest) ([]openrouter.ChatCompletionMessage, error) {
	characterParts := []string{prompt.Protocol, prompt.Character}
	if req.CharacterPrompt != "" {
		characterParts = append(characterParts, req.CharacterPrompt)
	}

	dialog := []openrouter.ChatCompletionMessage{
		openrouter.SystemMessage(strings.Join(append(characterParts, req.CurrentTime), "\n")),
	}

	if len(req.Notes) > 0 {
		var noteLines []string
		for _, n := range req.Notes {
			noteLines = append(noteLines, n.Text)
		}
		dialog = append(dialog, openrouter.SystemMessage(
			"Заметки о чате:\n"+strings.Join(noteLines, "\n"),
		))
	}

	if len(req.Members) > 0 {
		membersData, err := json.Marshal(req.Members)
		if err != nil {
			return nil, errors.Wrap(err, "marshal members")
		}
		dialog = append(dialog, openrouter.SystemMessage(
			"Участники чата:\n"+string(membersData),
		))
	}

	{
		selfData, err := json.Marshal(&req.Self)
		if err != nil {
			return nil, errors.Wrap(err, "marshal self")
		}
		dialog = append(dialog,
			openrouter.SystemMessage("Информация о себе:"),
			openrouter.SystemMessage(string(selfData)),
		)
	}

	dialog = append(dialog, openrouter.UserMessage("Предыдущая переписка:"))

	keepImage := keptHistoryImageIndices(req)

	for i := range req.History {
		data, err := json.Marshal(req.History[i])
		if err != nil {
			return nil, errors.Wrap(err, "marshal dialog context")
		}

		if msg := req.History[i].Message; msg != nil && msg.ImageURL != "" {
			// Attach the persisted image only for the most recent messages so the
			// model can still reference them; older images are too costly to keep
			// and are replaced with a placeholder.
			if keepImage[i] {
				dialog = append(dialog, openrouter.ChatCompletionMessage{
					Role: openrouter.ChatMessageRoleUser,
					Content: openrouter.Content{
						Multi: []openrouter.ChatMessagePart{
							{Type: openrouter.ChatMessagePartTypeText, Text: string(data)},
							imagePart(msg.ImageURL),
						},
					},
				})
			} else {
				dialog = append(dialog, openrouter.UserMessage(string(data)+"\n"+imageTooOldText))
			}

			continue
		}

		dialog = append(dialog, openrouter.UserMessage(string(data)))
	}

	if req.Idle {
		dialog = append(dialog, openrouter.UserMessage(prompt.Idle))

		return dialog, nil
	}

	currentData, err := json.Marshal(req.Current)
	if err != nil {
		return nil, errors.Wrap(err, "marshal current context")
	}
	dialog = append(dialog,
		openrouter.UserMessage("Текущее сообщение:"),
		openrouter.UserMessage(string(currentData)),
	)

	if req.ImageURL != "" {
		dialog = append(dialog, openrouter.ChatCompletionMessage{
			Role: openrouter.ChatMessageRoleUser,
			Content: openrouter.Content{
				Multi: []openrouter.ChatMessagePart{imagePart(req.ImageURL)},
			},
		})
	}

	return dialog, nil
}

// Respond runs the completion loop, handling tool calls until the model
// produces a text reply or the iteration limit is hit.
func (c *Client) Respond(ctx context.Context, req lilith.ResponseRequest) (*lilith.ResponseResult, error) {
	lg := zctx.From(ctx)

	dialog, err := buildResponseDialog(req)
	if err != nil {
		return nil, err
	}

	tools := []openrouter.Tool{
		emojiTool(),
		weatherTool(),
		{Type: "openrouter:web_search"},
	}

	model := c.model
	if req.Model != "" {
		model = req.Model
	}

	result := &lilith.ResponseResult{}
	serviceTier := openrouter.ServiceTierDefault

	for i := range maxIterations {
		if i > 0 {
			lg.Info("Retrying", zap.Int("iteration", i))
		}

		done := make(chan struct{})
		if req.Typing != nil {
			go func() {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-done:
						return
					case <-ticker.C:
						if err := req.Typing(ctx); err != nil {
							lg.Error("Failed to send typing action", zap.Error(err))
							return
						}
					}
				}
			}()
		}

		resp, err := c.ai.CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
			Model:       model,
			Messages:    dialog,
			MaxTokens:   maxTokens,
			Tools:       tools,
			ServiceTier: serviceTier,
		})
		close(done)

		if err != nil {
			lg.Warn("Failed to create completion", zap.Error(err))
			return nil, errors.Wrap(err, "generate content")
		}

		if u := resp.Usage; u != nil {
			lg.Info("Token usage",
				zap.Int("prompt_tokens", u.PromptTokens),
				zap.Int("completion_tokens", u.CompletionTokens),
				zap.Int("total_tokens", u.TotalTokens),
				zap.String("model", resp.Model),
				zap.String("tier", string(resp.ServiceTier)),
			)
		}

		if len(resp.Choices) == 0 {
			return nil, errors.New("no choices found")
		}

		msg := resp.Choices[0].Message

		for _, tool := range msg.ToolCalls {
			lg.Info("Function call", zap.String("id", tool.ID))
			switch tool.Function.Name {
			case "reply_emoji":
				var args struct {
					Emoji string `json:"emoji"`
				}

				toolContent, err := json.Marshal(struct {
					Emoji string `json:"reply_emoji"`
				}{
					Emoji: args.Emoji,
				})
				if err != nil {
					return nil, errors.Wrap(err, "marshal emoji")
				}
				assistantContent, err := json.Marshal(tool)
				if err != nil {
					return nil, errors.Wrap(err, "marshal tool")
				}

				dialog = append(dialog,
					openrouter.ChatCompletionMessage{
						Role:    openrouter.ChatMessageRoleAssistant,
						Content: openrouter.Content{Text: string(assistantContent)},
					},
					openrouter.ChatCompletionMessage{
						Role:       openrouter.ChatMessageRoleTool,
						Content:    openrouter.Content{Text: string(toolContent)},
						ToolCallID: tool.ID,
					},
				)

				if err := json.Unmarshal([]byte(tool.Function.Arguments), &args); err != nil {
					return nil, errors.Wrap(err, "unmarshal arguments")
				}
				if text, ok := reaction.Canonicalize(args.Emoji); ok {
					result.Reactions = append(result.Reactions, text)
				}

			case "get_weather":
				var args struct {
					City        string `json:"city"`
					CountryCode string `json:"country_code"`
				}

				if err := json.Unmarshal([]byte(tool.Function.Arguments), &args); err != nil {
					return nil, errors.Wrap(err, "unmarshal arguments")
				}

				info, err := c.weather.Current(ctx, args.City, args.CountryCode)
				if err != nil {
					return nil, errors.Wrap(err, "get weather")
				}

				desc := args.City
				if info.Description != "" {
					desc = info.Description
				}

				weatherInfo := fmt.Sprintf(
					"Погода в %s (%s): %s, %d °C, ощущается как %d °C, влажность %d%%, ветер %d м/с %s",
					info.LocationName,
					info.Country,
					desc,
					info.Temperature,
					info.FeelsLike,
					info.Humidity,
					info.WindSpeed,
					info.WindDir,
				)

				lg.Info("Adding weather info to dialog", zap.String("weather_info", weatherInfo))

				dialog = append(dialog, openrouter.ChatCompletionMessage{
					Role:       openrouter.ChatMessageRoleTool,
					Content:    openrouter.Content{Text: weatherInfo},
					ToolCallID: tool.ID,
				})
			default:
				lg.Warn("Unknown function call", zap.String("name", tool.Function.Name))
			}
		}

		// Only loop again when the model called a tool but produced no text yet.
		if len(msg.ToolCalls) > 0 {
			continue
		}

		result.Text = trimEmoji(msg.Content.Text)
		return result, nil
	}

	lg.Error("Too many tool-call iterations")

	return result, nil
}

// GenerateNotes summarizes messages into a fresh notes snapshot.
func (c *Client) GenerateNotes(ctx context.Context, existing []lilith.ChatNote, messages []lilith.Message) (string, error) {
	dialog := []openrouter.ChatCompletionMessage{
		openrouter.SystemMessage(strings.Join([]string{
			prompt.Character,
			prompt.Notes,
		}, "\n")),
	}

	if len(existing) > 0 {
		var noteLines []string
		for _, n := range existing {
			noteLines = append(noteLines, n.Text)
		}
		dialog = append(dialog, openrouter.UserMessage(
			"Существующие заметки:\n"+strings.Join(noteLines, "\n"),
		))
	}

	for _, msg := range messages {
		data, err := json.Marshal(msg)
		if err != nil {
			return "", errors.Wrap(err, "marshal message")
		}
		dialog = append(dialog, openrouter.UserMessage(string(data)))
	}

	dialog = append(dialog, openrouter.UserMessage("Сгенерируй заметки"))

	resp, err := c.ai.CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
		Model:       c.model,
		Messages:    dialog,
		MaxTokens:   maxNotesTokens,
		ServiceTier: openrouter.ServiceTierFlex,
	})
	if err != nil {
		return "", errors.Wrap(err, "generate notes")
	}

	if u := resp.Usage; u != nil {
		zctx.From(ctx).Info("Token usage (generate notes)",
			zap.Int("prompt_tokens", u.PromptTokens),
			zap.Int("completion_tokens", u.CompletionTokens),
			zap.Int("total_tokens", u.TotalTokens),
			zap.String("tier", string(resp.ServiceTier)),
		)
	}

	if len(resp.Choices) == 0 {
		return "", errors.New("no choices found")
	}

	return strings.TrimSpace(resp.Choices[0].Message.Content.Text), nil
}

// GenerateNote decides whether a single message is worth noting and returns the
// note text. An empty string means no note is needed.
func (c *Client) GenerateNote(ctx context.Context, existing []lilith.ChatNote, msg lilith.Message) (string, error) {
	dialog := []openrouter.ChatCompletionMessage{
		openrouter.SystemMessage(prompt.Character),
		openrouter.SystemMessage(prompt.NoteSingle),
	}

	if len(existing) > 0 {
		var noteLines []string
		for _, n := range existing {
			noteLines = append(noteLines, n.Text)
		}
		dialog = append(dialog, openrouter.SystemMessage(
			"Существующие заметки:\n"+strings.Join(noteLines, "\n"),
		))
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return "", errors.Wrap(err, "marshal message")
	}
	dialog = append(dialog, openrouter.UserMessage(string(data)))

	resp, err := c.ai.CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
		Model:       c.model,
		Messages:    dialog,
		MaxTokens:   maxNotesTokens,
		ServiceTier: openrouter.ServiceTierFlex,
	})
	if err != nil {
		return "", errors.Wrap(err, "generate note for message")
	}

	if u := resp.Usage; u != nil {
		zctx.From(ctx).Info("Token usage (generate note)",
			zap.Int("prompt_tokens", u.PromptTokens),
			zap.Int("completion_tokens", u.CompletionTokens),
			zap.Int("total_tokens", u.TotalTokens),
		)
	}

	if len(resp.Choices) == 0 {
		return "", errors.New("no choices found")
	}

	text := strings.TrimSpace(resp.Choices[0].Message.Content.Text)
	if text == "" || text == "Empty line." || len(text) < minNoteLen {
		return "", nil
	}
	if strings.Contains(text, "empty response") {
		return "", nil
	}

	return text, nil
}
