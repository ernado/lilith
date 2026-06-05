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

	// maxContextImages is the maximum number of most-recent images attached to
	// the model context. Older images are replaced with imageTooOldText.
	maxContextImages = 3

	// imageTooOldText replaces images that fall outside the most-recent window.
	imageTooOldText = "[image too old]"
)

var _ lilith.AI = (*Client)(nil)

// Client is the OpenRouter-backed implementation of lilith.AI.
type Client struct {
	ai            ChatCompleter
	model         string
	weather       lilith.WeatherProvider
	discord       lilith.DiscordProvider
	image         lilith.ImageGenerator
	imageFallback lilith.ImageGenerator
}

// New returns a Client using the given chat completer, model, weather provider
// (used for the weather tool), Discord provider (used for the
// get_discord_channels tool), primary image generator and fallback image
// generator (both used for the generate_image tool). The Discord provider and
// primary image generator may be nil, in which case their tools are not offered
// to the model; the fallback may be nil to disable fallback.
func New(ai ChatCompleter, model string, weather lilith.WeatherProvider, discord lilith.DiscordProvider, image, imageFallback lilith.ImageGenerator) *Client {
	return &Client{
		ai:            ai,
		model:         model,
		weather:       weather,
		discord:       discord,
		image:         image,
		imageFallback: imageFallback,
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

func discordTool() openrouter.Tool {
	return openrouter.Tool{
		Type: openrouter.ToolTypeFunction,
		Function: &openrouter.FunctionDefinition{
			Name:        "get_discord_channels",
			Description: "List Discord voice channels that currently have people in them and who is present in each.",
			Parameters: jsonschema.Definition{
				Type:       jsonschema.Object,
				Properties: map[string]jsonschema.Definition{},
			},
		},
	}
}

func imageTool() openrouter.Tool {
	return openrouter.Tool{
		Type: openrouter.ToolTypeFunction,
		Function: &openrouter.FunctionDefinition{
			Name:        "generate_image",
			Description: "Generate an image. Provide a natural-language description and, separately, booru-style positive and negative tags. The tags are used by a fallback generator if the primary one produces nothing.",
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"prompt": {
						Type:        jsonschema.String,
						Description: "A natural-language description of the image to generate, in plain prose",
					},
					"positive_tags": {
						Type:        jsonschema.String,
						Description: "Comma-separated booru-style tags describing what the image should contain",
					},
					"negative_tags": {
						Type:        jsonschema.String,
						Description: "Comma-separated booru-style tags describing what the image should avoid",
					},
				},
				Required: []string{"prompt", "positive_tags", "negative_tags"},
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

// keepAlivePresence sends the presence action immediately and then every second
// until the returned stop function is called, keeping a chat indicator (e.g.
// "sending photo") alive during a long operation. A nil action is a no-op.
func keepAlivePresence(ctx context.Context, lg *zap.Logger, action func(context.Context) error) (stop func()) {
	if action == nil {
		return func() {}
	}

	// Send once synchronously so the indicator appears without a tick of delay.
	if err := action(ctx); err != nil {
		lg.Error("Failed to send presence action", zap.Error(err))
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := action(ctx); err != nil {
					lg.Error("Failed to send presence action", zap.Error(err))
					return
				}
			}
		}
	}()

	return func() { close(done) }
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
	if c.discord != nil {
		tools = append(tools, discordTool())
	}
	if c.image != nil {
		tools = append(tools, imageTool())
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

		start := time.Now()
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

		latency := time.Since(start)
		if u := resp.Usage; u != nil {
			lg.Info("Token usage",
				zap.Int("prompt_tokens", u.PromptTokens),
				zap.Int("completion_tokens", u.CompletionTokens),
				zap.Int("total_tokens", u.TotalTokens),
				zap.String("model", resp.Model),
				zap.String("tier", string(resp.ServiceTier)),
				zap.Duration("latency", latency),
			)
		}

		if len(resp.Choices) == 0 {
			return nil, errors.New("no choices found")
		}

		msg := resp.Choices[0].Message

		// The tool protocol requires the assistant message carrying the
		// tool_calls to precede the tool result messages. Echo it once before
		// processing the individual calls below.
		if len(msg.ToolCalls) > 0 {
			msg.Role = openrouter.ChatMessageRoleAssistant
			dialog = append(dialog, msg)
		}

		for _, tool := range msg.ToolCalls {
			lg.Info("Function call", zap.String("id", tool.ID))
			switch tool.Function.Name {
			case "reply_emoji":
				var args struct {
					Emoji string `json:"emoji"`
				}

				if err := json.Unmarshal([]byte(tool.Function.Arguments), &args); err != nil {
					return nil, errors.Wrap(err, "unmarshal arguments")
				}

				toolContent, err := json.Marshal(struct {
					Emoji string `json:"reply_emoji"`
				}{
					Emoji: args.Emoji,
				})
				if err != nil {
					return nil, errors.Wrap(err, "marshal emoji")
				}

				dialog = append(dialog, openrouter.ChatCompletionMessage{
					Role:       openrouter.ChatMessageRoleTool,
					Content:    openrouter.Content{Text: string(toolContent)},
					ToolCallID: tool.ID,
				})

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

			case "get_discord_channels":
				channels, err := c.discord.PopulatedChannels(ctx)
				if err != nil {
					return nil, errors.Wrap(err, "get discord channels")
				}

				content, err := json.Marshal(channels)
				if err != nil {
					return nil, errors.Wrap(err, "marshal discord channels")
				}

				lg.Info("get_discord_channels result",
					zap.Int("channels", len(channels)),
					zap.Any("discord_channels", channels),
					zap.String("payload", string(content)),
				)

				dialog = append(dialog, openrouter.ChatCompletionMessage{
					Role:       openrouter.ChatMessageRoleTool,
					Content:    openrouter.Content{Text: string(content)},
					ToolCallID: tool.ID,
				})

			case "generate_image":
				var args struct {
					Prompt       string `json:"prompt"`
					PositiveTags string `json:"positive_tags"`
					NegativeTags string `json:"negative_tags"`
				}

				if err := json.Unmarshal([]byte(tool.Function.Arguments), &args); err != nil {
					return nil, errors.Wrap(err, "unmarshal arguments")
				}

				lg.Info("Generate image",
					zap.String("positive", args.PositiveTags),
					zap.String("negative", args.NegativeTags),
					zap.String("prompt", args.Prompt),
					zap.Bool("reference", req.ImageURL != ""),
				)

				// Keep the "sending photo" indicator alive for the duration of
				// generation, which the typing keepalive does not cover.
				stopPresence := keepAlivePresence(ctx, lg, req.UploadingPhoto)

				// Primary: natural-language generation, with the current message's
				// image (if any) as the image-to-image reference.
				images, err := c.image.Generate(ctx, lilith.ImageRequest{
					Prompt:         args.Prompt,
					ReferenceImage: req.ImageURL,
				})
				if err != nil {
					lg.Warn("Primary image generation failed", zap.Error(err))
				}

				// Fallback: when the primary produces nothing, retry with the
				// tag-based generator using the booru-style tags.
				if len(images) == 0 && c.imageFallback != nil {
					lg.Info("Falling back to secondary image generator")

					fallbackPositive := "very aesthetic, masterpiece, no text"
					if args.PositiveTags != "" {
						fallbackPositive = fallbackPositive + ", " + args.PositiveTags
					}

					const fallbackNegative = "lowres, artistic error, film grain, scan artifacts, worst quality, bad quality, jpeg artifacts, very displeasing, chromatic aberration, dithering, halftone, screentone, multiple views, logo, too many watermarks, negative space, blank page"
					fallbackNeg := fallbackNegative
					if args.NegativeTags != "" {
						fallbackNeg = fallbackNeg + ", " + args.NegativeTags
					}

					images, err = c.imageFallback.Generate(ctx, lilith.ImageRequest{
						Prompt:         fallbackPositive,
						NegativePrompt: fallbackNeg,
						ReferenceImage: req.ImageURL,
					})
					if err != nil {
						lg.Warn("Fallback image generation failed", zap.Error(err))
					}
				}

				stopPresence()

				result.Images = append(result.Images, images...)
				if len(images) > 0 {
					// Persist the full tool arguments (prompt + tags) as JSON so
					// the model can recall and reuse them for re-generation.
					if promptJSON, err := json.Marshal(args); err == nil {
						result.ImagePrompt = string(promptJSON)
					} else {
						result.ImagePrompt = args.Prompt
					}
				}

				lg.Info("generate_image result",
					zap.Int("images", len(images)),
				)

				toolContent, err := json.Marshal(struct {
					Generated bool `json:"generated"`
					Count     int  `json:"count"`
				}{
					Generated: len(images) > 0,
					Count:     len(images),
				})
				if err != nil {
					return nil, errors.Wrap(err, "marshal image result")
				}

				dialog = append(dialog, openrouter.ChatCompletionMessage{
					Role:       openrouter.ChatMessageRoleTool,
					Content:    openrouter.Content{Text: string(toolContent)},
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

		result.Text = normalizeText(msg.Content.Text)
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
			"Текущая память о чате:\n"+strings.Join(noteLines, "\n"),
		))
	}

	for _, msg := range messages {
		data, err := json.Marshal(msg)
		if err != nil {
			return "", errors.Wrap(err, "marshal message")
		}
		dialog = append(dialog, openrouter.UserMessage(string(data)))
	}

	dialog = append(dialog, openrouter.UserMessage("Верни полный обновлённый список заметок"))

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
