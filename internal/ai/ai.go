// Package ai implements the language-model gateway (lilith.AI) on top of
// OpenRouter. It owns prompt assembly, tool definitions and the tool-call loop;
// all chat state is supplied by the caller via lilith.ResponseRequest.
package ai

import (
	"context"
	"encoding/json"
	"strconv"
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
	ai    ChatCompleter
	model string
	tools *toolset
}

// New returns a Client using the given chat completer, model, weather provider
// (used for the weather tool), Discord provider (used for the
// get_discord_channels tool), primary image generator and fallback image
// generator (both used for the generate_image tool). The Discord provider and
// primary image generator may be nil, in which case their tools are not offered
// to the model; the fallback may be nil to disable fallback.
func New(ai ChatCompleter, model string, weather lilith.WeatherProvider, discord lilith.DiscordProvider, image, imageFallback lilith.ImageGenerator) *Client {
	return &Client{
		ai:    ai,
		model: model,
		tools: &toolset{
			weather:       weather,
			discord:       discord,
			image:         image,
			imageFallback: imageFallback,
		},
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

// isNoImageInputError reports whether err is OpenRouter's "no endpoints support
// image input" rejection, meaning the selected model cannot accept images.
func isNoImageInputError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "image input")
}

// isGeminiModel reports whether model is a Google Gemini model. Gemini (via
// Google AI Studio) rejects requests that combine server-side built-in tools
// with function-calling tools, so the web_search tool is withheld from it.
func isGeminiModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "gemini")
}

// APIFailure is a user-renderable summary of an upstream model or provider
// failure extracted from an OpenRouter API error.
type APIFailure struct {
	// StatusCode is the HTTP (or provider) status code, when known (e.g. 429).
	StatusCode int
	// Provider is the upstream provider name (e.g. "Google AI Studio"), when known.
	Provider string
	// Message is the human-readable provider message, when known.
	Message string
	// RateLimited reports whether the failure is an upstream rate limit (429).
	RateLimited bool
}

// AsAPIFailure extracts an APIFailure from err when it carries an OpenRouter API
// or request error. It returns false when err is not provider-related (e.g. a
// context cancellation or a local failure), so the caller can distinguish a
// remote model failure from an internal one.
func AsAPIFailure(err error) (APIFailure, bool) {
	var f APIFailure
	var found bool

	// A RequestError carries the transport-level status code (e.g. 429) and may
	// wrap the APIError below.
	var reqErr *openrouter.RequestError
	if errors.As(err, &reqErr) {
		f.StatusCode = reqErr.HTTPStatusCode
		found = true
	}

	// An APIError carries the structured provider metadata.
	var apiErr *openrouter.APIError
	if errors.As(err, &apiErr) {
		found = true
		if f.StatusCode == 0 {
			f.StatusCode = apiErr.HTTPStatusCode
		}
		f.Message = apiErr.Message
		if m := apiErr.Metadata; m != nil {
			if name, ok := (*m)["provider_name"].(string); ok {
				f.Provider = name
			}
			if raw, ok := (*m)["raw"].(string); ok && strings.TrimSpace(raw) != "" {
				f.Message = raw
			}
			if f.StatusCode == 0 {
				f.StatusCode = metadataStatusCode(*m)
			}
		}
	}

	if !found {
		return APIFailure{}, false
	}

	f.RateLimited = f.StatusCode == 429

	return f, true
}

// metadataStatusCode extracts the provider_error_code from error metadata,
// which OpenRouter may deliver as a number or a string.
func metadataStatusCode(m openrouter.Metadata) int {
	code, ok := m["provider_error_code"]
	if !ok {
		return 0
	}

	switch v := code.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0
		}

		return n
	default:
		return 0
	}
}

// dialogHasImages reports whether any message carries an image part.
func dialogHasImages(dialog []openrouter.ChatCompletionMessage) bool {
	for _, m := range dialog {
		for _, p := range m.Content.Multi {
			if p.Type == openrouter.ChatMessagePartTypeImageURL {
				return true
			}
		}
	}

	return false
}

// stripImages returns the dialog with image parts removed: multi-part messages
// keep only their text, and image-only messages are dropped.
func stripImages(dialog []openrouter.ChatCompletionMessage) []openrouter.ChatCompletionMessage {
	out := make([]openrouter.ChatCompletionMessage, 0, len(dialog))
	for _, m := range dialog {
		if len(m.Content.Multi) == 0 {
			out = append(out, m)
			continue
		}

		var texts []string
		for _, p := range m.Content.Multi {
			if p.Type == openrouter.ChatMessagePartTypeText {
				texts = append(texts, p.Text)
			}
		}

		if len(texts) == 0 {
			// Image-only message; nothing left once the image is removed.
			continue
		}

		m.Content = openrouter.Content{Text: strings.Join(texts, "\n")}
		out = append(out, m)
	}

	return out
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
					// Cancellation is expected once the request completes; do
					// not surface it as an error.
					if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
						lg.Error("Failed to send presence action", zap.Error(err))
					}
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
	characterParts := []string{prompt.Protocol, prompt.Markdown, prompt.Character}
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

// buildNotesDialog assembles the OpenRouter messages for a notes-summary
// request. It is provider-agnostic prompt assembly, translated per backend.
func buildNotesDialog(existing []lilith.ChatNote, messages []lilith.Message) ([]openrouter.ChatCompletionMessage, error) {
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
			return nil, errors.Wrap(err, "marshal message")
		}
		dialog = append(dialog, openrouter.UserMessage(string(data)))
	}

	dialog = append(dialog, openrouter.UserMessage("Верни полный обновлённый список заметок"))

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

	model := c.model
	if req.Model != "" {
		model = req.Model
	}

	tools := []openrouter.Tool{
		emojiTool(),
		weatherTool(),
	}

	// Gemini (via Google AI Studio) rejects any request that combines a
	// server-side built-in tool with function-calling tools, and the required
	// opt-in flag (tool_config.include_server_side_tool_invocations) is not
	// plumbable through OpenRouter. So the web_search tool is only offered to
	// non-Gemini models; on Gemini it would make every completion fail with 400.
	if !isGeminiModel(model) {
		tools = append(tools, openrouter.Tool{Type: "openrouter:web_search"})
	}

	if c.tools.discord != nil {
		tools = append(tools, discordTool())
	}
	if c.tools.image != nil {
		tools = append(tools, imageTool())
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
							// The request context is canceled once the completion
							// returns, so a cancellation here is expected shutdown
							// noise, not a real failure.
							if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
								lg.Error("Failed to send typing action", zap.Error(err))
							}
							return
						}
					}
				}
			}()
		}

		start := time.Now()
		chatReq := openrouter.ChatCompletionRequest{
			Model:       model,
			Messages:    dialog,
			MaxTokens:   maxTokens,
			Tools:       tools,
			ServiceTier: serviceTier,
		}

		resp, err := c.ai.CreateChatCompletion(ctx, chatReq)
		close(done)

		// Some models/providers reject image input. When that happens, drop the
		// images from the dialog and retry once without them.
		if err != nil && isNoImageInputError(err) && dialogHasImages(dialog) {
			lg.Warn("Model does not support image input; retrying without images", zap.Error(err))
			dialog = stripImages(dialog)
			chatReq.Messages = dialog
			resp, err = c.ai.CreateChatCompletion(ctx, chatReq)
		}

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

			content, ok, err := c.tools.execute(ctx, lg, req, result, tool.Function.Name, json.RawMessage(tool.Function.Arguments))
			if err != nil {
				return nil, err
			}
			if !ok {
				// Unknown tool: nothing to feed back to the model.
				continue
			}

			dialog = append(dialog, openrouter.ChatCompletionMessage{
				Role:       openrouter.ChatMessageRoleTool,
				Content:    openrouter.Content{Text: content},
				ToolCallID: tool.ID,
			})
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

// GenerateNotes summarizes messages into a fresh notes snapshot. The model, when
// non-empty, overrides the default model for this request.
func (c *Client) GenerateNotes(ctx context.Context, model string, existing []lilith.ChatNote, messages []lilith.Message) (string, error) {
	if model == "" {
		model = c.model
	}

	dialog, err := buildNotesDialog(existing, messages)
	if err != nil {
		return "", err
	}

	resp, err := c.ai.CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
		Model:       model,
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
