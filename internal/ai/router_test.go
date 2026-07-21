package ai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeminiModelID(t *testing.T) {
	require.Equal(t, "gemini-3.6-flash", geminiModelID("google/gemini-3.6-flash"))
	require.Equal(t, "gemini-3.6-flash", geminiModelID("gemini-3.6-flash"))
	require.Equal(t, "deepseek/deepseek-v4-flash", geminiModelID("deepseek/deepseek-v4-flash"))
}

func TestIsGeminiModel(t *testing.T) {
	require.True(t, isGeminiModel("google/gemini-3.6-flash"))
	require.True(t, isGeminiModel("gemini-2.0-flash"))
	require.False(t, isGeminiModel("deepseek/deepseek-v4-flash"))
	require.False(t, isGeminiModel("openai/gpt-5"))
}

func TestRouterRoute(t *testing.T) {
	or := &Client{model: "deepseek/deepseek-v4-flash", tools: &toolset{}}
	goog := &googleClient{}

	t.Run("WithGoogleBackend", func(t *testing.T) {
		r := &Router{openRouter: or, google: goog, defaultModel: or.model}

		require.Same(t, goog, r.route("google/gemini-3.6-flash"), "gemini routes to genai")
		require.Same(t, or, r.route("deepseek/deepseek-v4-flash"), "non-gemini routes to openrouter")
		require.Same(t, or, r.route(""), "empty falls back to default model (deepseek)")
	})

	t.Run("DefaultModelGemini", func(t *testing.T) {
		r := &Router{openRouter: or, google: goog, defaultModel: "google/gemini-3.6-flash"}

		require.Same(t, goog, r.route(""), "empty uses gemini default → genai")
	})

	t.Run("NoGoogleBackend", func(t *testing.T) {
		r := &Router{openRouter: or, defaultModel: or.model}

		require.Same(t, or, r.route("google/gemini-3.6-flash"), "without a key, gemini falls back to openrouter")
	})
}
