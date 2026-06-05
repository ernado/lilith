package ai

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/go-faster/errors"
	"github.com/revrost/go-openrouter"

	"github.com/ernado/lilith"
)

// defaultImageModel is an OpenRouter model whose output modalities include
// images.
const defaultImageModel = "google/gemini-3.1-flash-image-preview"

var _ lilith.ImageGenerator = (*ImageClient)(nil)

// ImageClient generates images via an OpenRouter image-capable chat model. It
// reuses the package's ChatCompleter so the same OpenRouter client backs both
// chat replies and image generation.
type ImageClient struct {
	ai    ChatCompleter
	model string
}

// NewImageClient returns an ImageClient using the given completer and model. An
// empty model falls back to defaultImageModel.
func NewImageClient(ai ChatCompleter, model string) *ImageClient {
	if model == "" {
		model = defaultImageModel
	}

	return &ImageClient{ai: ai, model: model}
}

// promptMessage builds the user message, attaching the reference image as an
// image part for image-to-image generation. With no reference it sends a plain
// text message.
func promptMessage(req lilith.ImageRequest) openrouter.ChatCompletionMessage {
	if req.ReferenceImage == "" {
		return openrouter.UserMessage(req.Prompt)
	}

	return openrouter.ChatCompletionMessage{
		Role: openrouter.ChatMessageRoleUser,
		Content: openrouter.Content{
			Multi: []openrouter.ChatMessagePart{
				{Type: openrouter.ChatMessagePartTypeText, Text: req.Prompt},
				imagePart(req.ReferenceImage),
			},
		},
	}
}

// Generate implements lilith.ImageGenerator by requesting image output from the
// chat model and decoding the returned data URLs.
func (c *ImageClient) Generate(ctx context.Context, req lilith.ImageRequest) ([]lilith.GeneratedImage, error) {
	model := c.model
	if req.Model != "" {
		model = string(req.Model)
	}

	resp, err := c.ai.CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
		Model:    model,
		Messages: []openrouter.ChatCompletionMessage{promptMessage(req)},
		Modalities: []openrouter.ChatCompletionModality{
			openrouter.ModalityImage,
		},
	})
	if err != nil {
		return nil, errors.Wrapf(err, "create chat completion (model %q)", model)
	}

	if len(resp.Choices) == 0 {
		return nil, errors.New("no choices found")
	}

	var images []lilith.GeneratedImage
	for _, img := range resp.Choices[0].Message.Images {
		data, format, err := decodeDataURL(img.ImageURL.URL)
		if err != nil {
			return nil, errors.Wrap(err, "decode image")
		}

		images = append(images, lilith.GeneratedImage{Data: data, Format: format})
	}

	if len(images) == 0 {
		return nil, errors.New("no images in response")
	}

	return images, nil
}

// decodeDataURL decodes a base64 image data URL ("data:image/png;base64,...")
// into its bytes and short format name ("png", "webp", ...).
func decodeDataURL(s string) ([]byte, string, error) {
	rest, ok := strings.CutPrefix(s, "data:")
	if !ok {
		return nil, "", errors.New("not a data URL")
	}

	header, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return nil, "", errors.New("malformed data URL")
	}

	if !strings.Contains(header, "base64") {
		return nil, "", errors.New("data URL is not base64")
	}

	mediatype, _, _ := strings.Cut(header, ";")

	format := strings.TrimPrefix(mediatype, "image/")
	if format == "" {
		format = "png"
	}

	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", errors.Wrap(err, "decode base64")
	}

	return data, format, nil
}
