package lilith

import "context"

// Model identifies a diffusion model offered by the image generator.
type Model string

// Supported models.
const (
	// ModelDiffusion3 is the NovelAI v3 anime diffusion model.
	ModelDiffusion3 Model = "nai-diffusion-3"
	// ModelDiffusion45Full is the NovelAI v4.5 full diffusion model.
	ModelDiffusion45Full Model = "nai-diffusion-4-5-full"
)

// Models lists the supported models.
var Models = []Model{
	ModelDiffusion3,
	ModelDiffusion45Full,
}

// ImageRequest is the domain payload for generating an image.
type ImageRequest struct {
	// Prompt is the positive text prompt describing the desired image.
	Prompt string
	// NegativePrompt lists concepts to steer away from. Optional.
	NegativePrompt string
	// Model, when non-empty, overrides the generator's default model.
	Model Model
	// Width and Height are the image dimensions in pixels. Zero means the
	// generator default.
	Width  int
	Height int
	// Steps is the number of diffusion steps. Zero means the generator default.
	Steps int
	// Scale is the CFG (prompt guidance) scale. Zero means the generator default.
	Scale float64
	// Sampler selects the diffusion sampler. Empty means the generator default.
	Sampler string
	// Seed fixes the RNG for reproducible output. Zero means a random seed.
	Seed int64
}

// GeneratedImage is a single produced image.
type GeneratedImage struct {
	// Data is the raw encoded image.
	Data []byte
	// Format is the image encoding, e.g. "png" or "webp".
	Format string
}

//go:generate go tool moq -out internal/mock/image_generator.go -pkg mock . ImageGenerator

// ImageGenerator generates images from text prompts. It is used by the AI layer
// as a tool.
type ImageGenerator interface {
	// Generate produces one or more images for the request.
	Generate(ctx context.Context, req ImageRequest) ([]GeneratedImage, error)
}
