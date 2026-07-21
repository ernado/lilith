package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"github.com/revrost/go-openrouter"
	"go.uber.org/zap"
	"google.golang.org/genai"

	"github.com/ernado/lilith"
	"github.com/ernado/lilith/internal/reaction"
)

// maxImageBytes bounds how much of a hosted image is read when inlining it for
// the Gemini API, guarding against unexpectedly large responses.
const maxImageBytes = 16 << 20

const (
	// geminiMaxOutputTokens caps a Gemini reply. Unlike OpenRouter, Gemini 3.x
	// spends mandatory "thinking" tokens (~500-750, and not disable-able on
	// flash) out of this same budget, so it is set well above the ~450-token
	// visible-reply target to leave room for both and avoid truncation.
	geminiMaxOutputTokens = 1536

	// geminiNotesMaxOutputTokens caps a notes summary, with the same thinking
	// headroom on top of the ~1024-token notes target.
	geminiNotesMaxOutputTokens = 2048
)

// googleClient is the go-genai (Gemini) backed implementation used for Google
// models. Unlike the OpenRouter backend, it can combine native function calling
// with the built-in Google Search tool by setting
// ToolConfig.IncludeServerSideToolInvocations, which OpenRouter cannot express.
type googleClient struct {
	client *genai.Client
	model  string
	tools  *toolset
	http   *http.Client
}

// newGoogleClient returns a googleClient using the given genai client, default
// model (with or without the "google/" prefix) and shared toolset.
func newGoogleClient(client *genai.Client, model string, tools *toolset) *googleClient {
	return &googleClient{
		client: client,
		model:  model,
		tools:  tools,
		http:   http.DefaultClient,
	}
}

// geminiModelID strips the OpenRouter "google/" vendor prefix, since the native
// Gemini API expects the bare model id (e.g. "gemini-3.6-flash").
func geminiModelID(model string) string {
	return strings.TrimPrefix(model, "google/")
}

// Respond runs the genai completion loop, handling tool calls until the model
// produces a text reply or the iteration limit is hit.
func (c *googleClient) Respond(ctx context.Context, req lilith.ResponseRequest) (*lilith.ResponseResult, error) {
	lg := zctx.From(ctx)

	dialog, err := buildResponseDialog(req)
	if err != nil {
		return nil, err
	}

	model := geminiModelID(c.model)
	if req.Model != "" {
		model = geminiModelID(req.Model)
	}

	system, contents, err := c.translate(ctx, lg, dialog)
	if err != nil {
		return nil, err
	}

	config := &genai.GenerateContentConfig{
		SystemInstruction: system,
		MaxOutputTokens:   geminiMaxOutputTokens,
		// Keep thinking shallow: it is mandatory on Gemini 3.x flash but a chat
		// reply needs little of it, and every thinking token eats the output
		// budget and adds latency and cost.
		ThinkingConfig: &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelLow},
		Tools:          c.genaiTools(),
		// Required so the built-in Google Search tool can be used alongside the
		// function-calling tools; without it Gemini rejects the request.
		ToolConfig: &genai.ToolConfig{
			IncludeServerSideToolInvocations: genai.Ptr(true),
		},
	}

	result := &lilith.ResponseResult{}

	// Keep the typing indicator alive across the (possibly multi-step) call.
	stopTyping := keepAlivePresence(ctx, lg, req.Typing)
	defer stopTyping()

	for i := range maxIterations {
		if i > 0 {
			lg.Info("Retrying", zap.Int("iteration", i))
		}

		start := time.Now()
		resp, err := c.client.Models.GenerateContent(ctx, model, contents, config)
		if err != nil {
			lg.Warn("Failed to create completion", zap.Error(err))
			return nil, errors.Wrap(err, "generate content")
		}

		if u := resp.UsageMetadata; u != nil {
			lg.Info("Token usage",
				zap.Int32("prompt_tokens", u.PromptTokenCount),
				zap.Int32("completion_tokens", u.CandidatesTokenCount),
				zap.Int32("thoughts_tokens", u.ThoughtsTokenCount),
				zap.Int32("total_tokens", u.TotalTokenCount),
				zap.String("model", model),
				zap.Duration("latency", time.Since(start)),
			)
		}

		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
			return nil, errors.New("no candidates found")
		}

		// Echo the model turn (function calls and any text) back into history so
		// the follow-up request carries the tool-call context.
		contents = append(contents, resp.Candidates[0].Content)

		calls := resp.FunctionCalls()
		if len(calls) == 0 {
			result.Text = normalizeText(resp.Text())

			return result, nil
		}

		var responses []*genai.Part
		for _, call := range calls {
			lg.Info("Function call", zap.String("name", call.Name))

			args, err := json.Marshal(call.Args)
			if err != nil {
				return nil, errors.Wrap(err, "marshal function args")
			}

			content, ok, err := c.tools.execute(ctx, lg, req, result, call.Name, args)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}

			responses = append(responses, genai.NewPartFromFunctionResponse(call.Name, map[string]any{
				"result": content,
			}))
		}

		if len(responses) == 0 {
			// Only unknown tools were called; stop with whatever text exists.
			result.Text = normalizeText(resp.Text())

			return result, nil
		}

		contents = append(contents, genai.NewContentFromParts(responses, genai.RoleUser))
	}

	lg.Error("Too many tool-call iterations")

	return result, nil
}

// GenerateNotes summarizes messages into a fresh notes snapshot using Gemini.
func (c *googleClient) GenerateNotes(ctx context.Context, model string, existing []lilith.ChatNote, messages []lilith.Message) (string, error) {
	lg := zctx.From(ctx)

	if model == "" {
		model = c.model
	}

	dialog, err := buildNotesDialog(existing, messages)
	if err != nil {
		return "", err
	}

	system, contents, err := c.translate(ctx, lg, dialog)
	if err != nil {
		return "", err
	}

	resp, err := c.client.Models.GenerateContent(ctx, geminiModelID(model), contents, &genai.GenerateContentConfig{
		SystemInstruction: system,
		MaxOutputTokens:   geminiNotesMaxOutputTokens,
		ThinkingConfig:    &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelLow},
	})
	if err != nil {
		return "", errors.Wrap(err, "generate notes")
	}

	if u := resp.UsageMetadata; u != nil {
		lg.Info("Token usage (generate notes)",
			zap.Int32("prompt_tokens", u.PromptTokenCount),
			zap.Int32("completion_tokens", u.CandidatesTokenCount),
			zap.Int32("total_tokens", u.TotalTokenCount),
		)
	}

	return strings.TrimSpace(resp.Text()), nil
}

// translate converts an OpenRouter-shaped dialog into a Gemini system
// instruction plus a single user Content carrying every non-system part. All
// prompt turns are user-authored (history is serialized as JSON in user
// messages), so a single user Content is sufficient. Image parts are downloaded
// and inlined as bytes, since the Gemini API cannot fetch arbitrary URLs.
func (c *googleClient) translate(ctx context.Context, lg *zap.Logger, dialog []openrouter.ChatCompletionMessage) (*genai.Content, []*genai.Content, error) {
	var (
		systemParts []string
		userParts   []*genai.Part
	)

	for _, m := range dialog {
		if m.Role == openrouter.ChatMessageRoleSystem {
			if m.Content.Text != "" {
				systemParts = append(systemParts, m.Content.Text)
			}

			continue
		}

		if len(m.Content.Multi) == 0 {
			if m.Content.Text != "" {
				userParts = append(userParts, genai.NewPartFromText(m.Content.Text))
			}

			continue
		}

		for _, p := range m.Content.Multi {
			switch p.Type {
			case openrouter.ChatMessagePartTypeText:
				if p.Text != "" {
					userParts = append(userParts, genai.NewPartFromText(p.Text))
				}

			case openrouter.ChatMessagePartTypeImageURL:
				if p.ImageURL == nil {
					continue
				}

				data, mime, err := c.fetchImage(ctx, p.ImageURL.URL)
				if err != nil {
					// A missing image must not fail the whole reply; drop to a
					// placeholder so the model still gets the surrounding text.
					lg.Warn("Failed to fetch image for Gemini; using placeholder", zap.Error(err))
					userParts = append(userParts, genai.NewPartFromText(imageTooOldText))

					continue
				}

				userParts = append(userParts, genai.NewPartFromBytes(data, mime))
			}
		}
	}

	var system *genai.Content
	if len(systemParts) > 0 {
		system = genai.NewContentFromParts(
			[]*genai.Part{genai.NewPartFromText(strings.Join(systemParts, "\n"))},
			genai.RoleUser,
		)
	}

	contents := []*genai.Content{genai.NewContentFromParts(userParts, genai.RoleUser)}

	return system, contents, nil
}

// fetchImage downloads a hosted image and returns its bytes and MIME type for
// inlining into a Gemini request.
func (c *googleClient) fetchImage(ctx context.Context, url string) ([]byte, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", errors.Wrap(err, "new request")
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, "", errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, "", errors.Errorf("unexpected status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		return nil, "", errors.Wrap(err, "read body")
	}

	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		// The static store serves JPEG; assume it when the header is absent.
		mime = "image/jpeg"
	}

	return data, mime, nil
}

// genaiTools builds the Gemini tool set: the function-calling declarations plus
// the built-in Google Search tool.
func (c *googleClient) genaiTools() []*genai.Tool {
	declarations := []*genai.FunctionDeclaration{
		geminiEmojiDecl(),
		geminiWeatherDecl(),
	}

	if c.tools.discord != nil {
		declarations = append(declarations, geminiDiscordDecl())
	}
	if c.tools.image != nil {
		declarations = append(declarations, geminiImageDecl())
	}

	return []*genai.Tool{
		{FunctionDeclarations: declarations},
		{GoogleSearch: &genai.GoogleSearch{}},
	}
}

func geminiEmojiDecl() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "reply_emoji",
		Description: "Repl to message with emoji. Allowed reactions:" + strings.Join(reaction.Allowed, ""),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"emoji": {Type: genai.TypeString, Description: "Emoji to reply"},
			},
			Required: []string{"emoji"},
		},
	}
}

func geminiWeatherDecl() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "get_weather",
		Description: "Get weather",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"city":         {Type: genai.TypeString, Description: "City name, Moscow"},
				"country_code": {Type: genai.TypeString, Description: "Country code, RU"},
			},
		},
	}
}

func geminiDiscordDecl() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "get_discord_channels",
		Description: "List Discord voice channels that currently have people in them and who is present in each.",
		Parameters: &genai.Schema{
			Type:       genai.TypeObject,
			Properties: map[string]*genai.Schema{},
		},
	}
}

func geminiImageDecl() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "generate_image",
		Description: "Generate an image. Provide a natural-language description and, separately, booru-style positive and negative tags. The tags are used by a fallback generator if the primary one produces nothing.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"prompt":        {Type: genai.TypeString, Description: "A natural-language description of the image to generate, in plain prose"},
				"positive_tags": {Type: genai.TypeString, Description: "Comma-separated booru-style tags describing what the image should contain"},
				"negative_tags": {Type: genai.TypeString, Description: "Comma-separated booru-style tags describing what the image should avoid"},
			},
			Required: []string{"prompt", "positive_tags", "negative_tags"},
		},
	}
}
