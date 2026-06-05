package ai_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/revrost/go-openrouter"
	"github.com/stretchr/testify/require"

	"github.com/ernado/lilith"
	"github.com/ernado/lilith/internal/ai"
	"github.com/ernado/lilith/internal/mock"
)

// imageResponse builds a completion that returns a single base64 image data URL.
func imageResponse(dataURL string) openrouter.ChatCompletionResponse {
	return openrouter.ChatCompletionResponse{
		Choices: []openrouter.ChatCompletionChoice{
			{Message: openrouter.ChatCompletionMessage{
				Images: []openrouter.ChatCompletionImage{
					{ImageURL: openrouter.ChatCompletionImageURL{URL: dataURL}},
				},
			}},
		},
	}
}

func dataURL(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func TestImageClient_DecodesDataURL(t *testing.T) {
	t.Parallel()

	pngBytes := []byte("\x89PNG\r\n\x1a\nfake")

	var captured openrouter.ChatCompletionRequest
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(_ context.Context, req openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			captured = req
			return imageResponse(dataURL("image/png", pngBytes)), nil
		},
	}

	client := ai.NewImageClient(completer, "")
	images, err := client.Generate(context.Background(), lilith.ImageRequest{Prompt: "a cat sitting on a windowsill"})
	require.NoError(t, err)

	require.Len(t, images, 1)
	require.Equal(t, pngBytes, images[0].Data)
	require.Equal(t, "png", images[0].Format)

	// The request asked for image output and used the default model.
	require.Contains(t, captured.Modalities, openrouter.ModalityImage)
	require.Equal(t, "google/gemini-3.1-flash-image-preview", captured.Model)

	// The prompt is sent verbatim as natural language.
	require.Len(t, captured.Messages, 1)
	require.Equal(t, "a cat sitting on a windowsill", captured.Messages[0].Content.Text)
}

func TestImageClient_SendsReferenceImage(t *testing.T) {
	t.Parallel()

	var captured openrouter.ChatCompletionRequest
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(_ context.Context, req openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			captured = req
			return imageResponse(dataURL("image/png", []byte("png"))), nil
		},
	}

	client := ai.NewImageClient(completer, "")
	_, err := client.Generate(context.Background(), lilith.ImageRequest{
		Prompt:         "make it winter",
		ReferenceImage: "https://cdn/a.png",
	})
	require.NoError(t, err)

	require.Len(t, captured.Messages, 1)
	parts := captured.Messages[0].Content.Multi
	require.Len(t, parts, 2) // text + single image

	require.Equal(t, openrouter.ChatMessagePartTypeText, parts[0].Type)
	require.Equal(t, "make it winter", parts[0].Text)

	require.Equal(t, openrouter.ChatMessagePartTypeImageURL, parts[1].Type)
	require.Equal(t, "https://cdn/a.png", parts[1].ImageURL.URL)
}

// With no reference image, a plain text message is sent (no image parts).
func TestImageClient_NoReferenceSendsText(t *testing.T) {
	t.Parallel()

	var captured openrouter.ChatCompletionRequest
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(_ context.Context, req openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			captured = req
			return imageResponse(dataURL("image/png", []byte("png"))), nil
		},
	}

	_, err := ai.NewImageClient(completer, "").Generate(context.Background(), lilith.ImageRequest{Prompt: "a cat"})
	require.NoError(t, err)

	require.Len(t, captured.Messages, 1)
	require.Empty(t, captured.Messages[0].Content.Multi)
	require.Equal(t, "a cat", captured.Messages[0].Content.Text)
}

func TestImageClient_ModelOverride(t *testing.T) {
	t.Parallel()

	var captured openrouter.ChatCompletionRequest
	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(_ context.Context, req openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			captured = req
			return imageResponse(dataURL("image/webp", []byte("webp"))), nil
		},
	}

	client := ai.NewImageClient(completer, "openai/some-image-model")
	images, err := client.Generate(context.Background(), lilith.ImageRequest{Prompt: "x"})
	require.NoError(t, err)

	require.Equal(t, "openai/some-image-model", captured.Model)
	require.Equal(t, "webp", images[0].Format)
}

func TestImageClient_NoImagesIsError(t *testing.T) {
	t.Parallel()

	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			return openrouter.ChatCompletionResponse{
				Choices: []openrouter.ChatCompletionChoice{
					{Message: openrouter.ChatCompletionMessage{}},
				},
			}, nil
		},
	}

	_, err := ai.NewImageClient(completer, "").Generate(context.Background(), lilith.ImageRequest{Prompt: "x"})
	require.Error(t, err)
}

func TestImageClient_RejectsNonDataURL(t *testing.T) {
	t.Parallel()

	completer := &mock.ChatCompleterMock{
		CreateChatCompletionFunc: func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			return imageResponse("https://example.com/image.png"), nil
		},
	}

	_, err := ai.NewImageClient(completer, "").Generate(context.Background(), lilith.ImageRequest{Prompt: "x"})
	require.Error(t, err)
}
