// Package image implements the NovelAI image generation API (lilith.ImageGenerator).
// The /ai/generate-image endpoint returns a zip archive of images, which this
// client unpacks into domain GeneratedImage values.
package image

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"path"
	"strings"

	"github.com/go-faster/errors"

	"github.com/ernado/lilith"
)

const (
	defaultBase          = "https://image.novelai.net"
	defaultSampler       = "k_euler_ancestral"
	defaultNoiseSchedule = "karras"
	defaultWidth         = 832
	defaultHeight        = 1216
	defaultSteps         = 23
	defaultScale         = 5.0

	// paramsVersion is the request schema version expected by the v4 models.
	paramsVersion = 3

	// maxSeed bounds the random seed; NovelAI seeds are 32-bit unsigned.
	maxSeed = 1 << 32
)

// defaultModel is used when no model is configured.
const defaultModel = lilith.ModelDiffusion3

var _ lilith.ImageGenerator = (*Client)(nil)

// Client is a NovelAI image generation API client.
type Client struct {
	http  lilith.HTTPClient
	key   string
	base  string
	model lilith.Model
}

// Options configures Client construction.
type Options struct {
	// HTTP overrides the HTTP client. Defaults to http.DefaultClient.
	HTTP lilith.HTTPClient
	// Base overrides the API base URL. Defaults to https://image.novelai.net.
	Base string
	// Model overrides the default model. Defaults to lilith.ModelDiffusion3.
	Model lilith.Model
}

func (o *Options) setDefaults() {
	if o.HTTP == nil {
		o.HTTP = http.DefaultClient
	}
	if o.Base == "" {
		o.Base = defaultBase
	}
	if o.Model == "" {
		o.Model = defaultModel
	}
}

// New returns a Client authenticated with the given persistent API token.
func New(key string, options Options) *Client {
	options.setDefaults()

	return &Client{
		http:  options.HTTP,
		key:   key,
		base:  strings.TrimRight(options.Base, "/"),
		model: options.Model,
	}
}

// generationRequest mirrors image.ImageGenerationRequest.
type generationRequest struct {
	Input      string            `json:"input"`
	Model      string            `json:"model"`
	Action     string            `json:"action"`
	Parameters requestParameters `json:"parameters"`
}

// v4Caption mirrors the caption block of image.V4ConditionInput. char_captions
// is always sent as an empty (non-nil) array; per-character prompts are not
// supported by this client.
type v4Caption struct {
	BaseCaption  string `json:"base_caption"`
	CharCaptions []any  `json:"char_captions"`
}

// v4Prompt mirrors the v4_prompt field (image.V4ConditionInput).
type v4Prompt struct {
	Caption   v4Caption `json:"caption"`
	UseCoords bool      `json:"use_coords"`
	UseOrder  bool      `json:"use_order"`
}

// v4NegativePrompt mirrors the v4_negative_prompt field.
type v4NegativePrompt struct {
	Caption  v4Caption `json:"caption"`
	LegacyUC bool      `json:"legacy_uc"`
}

// requestParameters mirrors the subset of image.RequestParameters this client
// sets, matching what the v4.x models require.
type requestParameters struct {
	ParamsVersion int     `json:"params_version"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	Scale         float64 `json:"scale"`
	Sampler       string  `json:"sampler"`
	Steps         int     `json:"steps"`
	Seed          int64   `json:"seed"`
	NSamples      int     `json:"n_samples"`
	UCPreset      int     `json:"ucPreset"`
	QualityToggle bool    `json:"qualityToggle"`

	DynamicThresholding bool    `json:"dynamic_thresholding"`
	ControlnetStrength  float64 `json:"controlnet_strength"`
	Legacy              bool    `json:"legacy"`
	AddOriginalImage    bool    `json:"add_original_image"`
	CFGRescale          float64 `json:"cfg_rescale"`
	NoiseSchedule       string  `json:"noise_schedule"`
	LegacyV3Extend      bool    `json:"legacy_v3_extend"`

	// SkipCFGAboveSigma is sent as JSON null when unset.
	SkipCFGAboveSigma *float64 `json:"skip_cfg_above_sigma"`

	UseCoords               bool `json:"use_coords"`
	LegacyUC                bool `json:"legacy_uc"`
	NormalizeRefStrengthMul bool `json:"normalize_reference_strength_multiple"`
	DeliberateEulerAncBug   bool `json:"deliberate_euler_ancestral_bug"`
	PreferBrownian          bool `json:"prefer_brownian"`

	V4Prompt         v4Prompt         `json:"v4_prompt"`
	V4NegativePrompt v4NegativePrompt `json:"v4_negative_prompt"`
	NegativePrompt   string           `json:"negative_prompt"`

	ImageFormat string `json:"image_format"`
}

// randomSeed returns a non-zero 32-bit seed.
func randomSeed() int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(maxSeed-1))
	if err != nil {
		// rand.Reader failures are not expected; fall back to a fixed seed
		// rather than failing the whole request.
		return 1
	}

	return n.Int64() + 1
}

// buildRequest maps a domain request to the NovelAI wire request, applying
// defaults.
func (c *Client) buildRequest(req lilith.ImageRequest) generationRequest {
	model := c.model
	if req.Model != "" {
		model = req.Model
	}

	width := req.Width
	if width == 0 {
		width = defaultWidth
	}

	height := req.Height
	if height == 0 {
		height = defaultHeight
	}

	scale := req.Scale
	if scale == 0 {
		scale = defaultScale
	}

	sampler := req.Sampler
	if sampler == "" {
		sampler = defaultSampler
	}

	steps := req.Steps
	if steps == 0 {
		steps = defaultSteps
	}

	seed := req.Seed
	if seed == 0 {
		seed = randomSeed()
	}

	params := requestParameters{
		ParamsVersion:           paramsVersion,
		Width:                   width,
		Height:                  height,
		Scale:                   scale,
		Sampler:                 sampler,
		Steps:                   steps,
		Seed:                    seed,
		NSamples:                1,
		UCPreset:                0,
		QualityToggle:           true,
		ControlnetStrength:      1,
		AddOriginalImage:        true,
		NoiseSchedule:           defaultNoiseSchedule,
		PreferBrownian:          true,
		NormalizeRefStrengthMul: true,
		V4Prompt: v4Prompt{
			Caption:   v4Caption{BaseCaption: req.Prompt, CharCaptions: []any{}},
			UseCoords: false,
			UseOrder:  true,
		},
		V4NegativePrompt: v4NegativePrompt{
			Caption:  v4Caption{BaseCaption: req.NegativePrompt, CharCaptions: []any{}},
			LegacyUC: false,
		},
		NegativePrompt: req.NegativePrompt,
		ImageFormat:    "png",
	}

	return generationRequest{
		Input:      req.Prompt,
		Model:      string(model),
		Action:     "generate",
		Parameters: params,
	}
}

// Generate implements lilith.ImageGenerator. It posts the request and unpacks
// the returned zip archive into images.
func (c *Client) Generate(ctx context.Context, req lilith.ImageRequest) ([]lilith.GeneratedImage, error) {
	body, err := json.Marshal(c.buildRequest(req))
	if err != nil {
		return nil, errors.Wrap(err, "marshal request")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/ai/generate-image", bytes.NewReader(body))
	if err != nil {
		return nil, errors.Wrap(err, "build request")
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/zip")
	httpReq.Header.Set("Authorization", "Bearer "+c.key)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, errors.Wrap(err, "do request")
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "read body")
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		// Error responses are JSON, not zip; surface the body for diagnostics.
		return nil, errors.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	images, err := extractImages(data)
	if err != nil {
		return nil, errors.Wrap(err, "extract images")
	}

	if len(images) == 0 {
		return nil, errors.New("no images in response")
	}

	return images, nil
}

// extractImages unpacks every file in the zip archive into a GeneratedImage,
// deriving the format from the file extension.
func extractImages(data []byte) ([]lilith.GeneratedImage, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.Wrap(err, "open zip")
	}

	var images []lilith.GeneratedImage
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, errors.Wrap(err, "open zip entry")
		}

		content, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, errors.Wrap(err, "read zip entry")
		}

		format := strings.ToLower(strings.TrimPrefix(path.Ext(f.Name), "."))
		if format == "" {
			format = "png"
		}

		images = append(images, lilith.GeneratedImage{
			Data:   content,
			Format: format,
		})
	}

	return images, nil
}
