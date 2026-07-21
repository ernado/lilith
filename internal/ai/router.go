package ai

import (
	"context"

	"google.golang.org/genai"

	"github.com/ernado/lilith"
)

var _ lilith.AI = (*Router)(nil)

// Router implements lilith.AI by dispatching Google ("google/*") models to the
// go-genai backend and all other models to OpenRouter. When the Google backend
// is unavailable (no Gemini API key), every request falls back to OpenRouter.
type Router struct {
	openRouter   *Client
	google       *googleClient
	defaultModel string
}

// NewRouter wraps an OpenRouter client, optionally adding a go-genai backend for
// Google models. When google is nil, the router is a thin pass-through to
// OpenRouter. The Google backend reuses the OpenRouter client's toolset and
// default model.
func NewRouter(openRouter *Client, google *genai.Client) *Router {
	r := &Router{
		openRouter:   openRouter,
		defaultModel: openRouter.model,
	}

	if google != nil {
		r.google = newGoogleClient(google, openRouter.model, openRouter.tools)
	}

	return r
}

// aiBackend is the model-dispatched subset of lilith.AI (DefaultModel is served
// by the Router itself).
type aiBackend interface {
	Respond(ctx context.Context, req lilith.ResponseRequest) (*lilith.ResponseResult, error)
	GenerateNotes(ctx context.Context, model string, existing []lilith.ChatNote, messages []lilith.Message) (string, error)
}

// route returns the backend that should handle the given effective model.
func (r *Router) route(model string) aiBackend {
	if model == "" {
		model = r.defaultModel
	}

	if r.google != nil && isGeminiModel(model) {
		return r.google
	}

	return r.openRouter
}

// Respond dispatches by the request's effective model.
func (r *Router) Respond(ctx context.Context, req lilith.ResponseRequest) (*lilith.ResponseResult, error) {
	return r.route(req.Model).Respond(ctx, req)
}

// GenerateNotes dispatches by the chat's model.
func (r *Router) GenerateNotes(ctx context.Context, model string, existing []lilith.ChatNote, messages []lilith.Message) (string, error) {
	return r.route(model).GenerateNotes(ctx, model, existing, messages)
}

// DefaultModel returns the default model name.
func (r *Router) DefaultModel() string {
	return r.defaultModel
}
