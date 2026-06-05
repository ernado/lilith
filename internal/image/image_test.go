package image_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ernado/lilith"
	"github.com/ernado/lilith/internal/image"
	"github.com/ernado/lilith/internal/mock"
)

// zipWith builds a zip archive holding the given named files.
func zipWith(t *testing.T, files map[string][]byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	return buf.Bytes()
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Header:     http.Header{},
	}
}

func TestGenerate_UnpacksZip(t *testing.T) {
	t.Parallel()

	pngBytes := []byte("\x89PNG\r\n\x1a\nfake")
	archive := zipWith(t, map[string][]byte{"image_0.png": pngBytes})

	var captured generationBody
	httpClient := &mock.HTTPClientMock{
		DoFunc: func(req *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodPost, req.Method)
			require.Equal(t, "https://image.novelai.net/ai/generate-image", req.URL.String())
			require.Equal(t, "Bearer secret-token", req.Header.Get("Authorization"))
			require.Equal(t, "application/json", req.Header.Get("Content-Type"))

			data, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(data, &captured))

			return &http.Response{
				StatusCode: http.StatusCreated,
				Body:       io.NopCloser(bytes.NewReader(archive)),
				Header:     http.Header{},
			}, nil
		},
	}

	client := image.New("secret-token", image.Options{HTTP: httpClient})
	images, err := client.Generate(context.Background(), lilith.ImageRequest{
		Prompt:         "a cat",
		NegativePrompt: "blurry",
		Seed:           123,
	})
	require.NoError(t, err)

	require.Len(t, images, 1)
	require.Equal(t, pngBytes, images[0].Data)
	require.Equal(t, "png", images[0].Format)

	// The domain request mapped to the wire request with defaults applied.
	require.Equal(t, "a cat", captured.Input)
	require.Equal(t, "generate", captured.Action)
	require.Equal(t, "nai-diffusion-3", captured.Model)
	require.Equal(t, "blurry", captured.Parameters.NegativePrompt)
	require.Equal(t, int64(123), captured.Parameters.Seed)
	require.Equal(t, 832, captured.Parameters.Width)
	require.Equal(t, 1216, captured.Parameters.Height)
	require.Equal(t, 23, captured.Parameters.Steps)
	require.Equal(t, "k_euler_ancestral", captured.Parameters.Sampler)

	// The v4 prompt structure carries the prompts the model requires.
	require.Equal(t, 3, captured.Parameters.ParamsVersion)
	require.Equal(t, "karras", captured.Parameters.NoiseSchedule)
	require.Equal(t, "png", captured.Parameters.ImageFormat)
	require.Equal(t, "a cat", captured.Parameters.V4Prompt.Caption.BaseCaption)
	require.True(t, captured.Parameters.V4Prompt.UseOrder)
	require.Equal(t, "blurry", captured.Parameters.V4NegativePrompt.Caption.BaseCaption)
	// char_captions must serialize as an empty array, never null.
	require.NotNil(t, captured.Parameters.V4Prompt.Caption.CharCaptions)
}

func TestGenerate_RandomSeedWhenZero(t *testing.T) {
	t.Parallel()

	archive := zipWith(t, map[string][]byte{"image_0.png": []byte("data")})

	var captured generationBody
	httpClient := &mock.HTTPClientMock{
		DoFunc: func(req *http.Request) (*http.Response, error) {
			data, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(data, &captured)

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(archive)),
				Header:     http.Header{},
			}, nil
		},
	}

	client := image.New("token", image.Options{HTTP: httpClient})
	_, err := client.Generate(context.Background(), lilith.ImageRequest{Prompt: "x"})
	require.NoError(t, err)

	require.NotZero(t, captured.Parameters.Seed, "a zero seed must be replaced with a random one")
}

func TestGenerate_ModelOverride(t *testing.T) {
	t.Parallel()

	archive := zipWith(t, map[string][]byte{"image_0.webp": []byte("data")})

	var captured generationBody
	httpClient := &mock.HTTPClientMock{
		DoFunc: func(req *http.Request) (*http.Response, error) {
			data, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(data, &captured)

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(archive)),
				Header:     http.Header{},
			}, nil
		},
	}

	client := image.New("token", image.Options{HTTP: httpClient, Model: "nai-diffusion-3"})
	images, err := client.Generate(context.Background(), lilith.ImageRequest{
		Prompt: "x",
		Model:  "nai-diffusion-4-5-full",
	})
	require.NoError(t, err)

	require.Equal(t, "nai-diffusion-4-5-full", captured.Model)
	require.Equal(t, "webp", images[0].Format)
}

func TestGenerate_ErrorStatusSurfacesBody(t *testing.T) {
	t.Parallel()

	httpClient := &mock.HTTPClientMock{
		DoFunc: func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusUnauthorized, `{"message":"invalid token"}`), nil
		},
	}

	client := image.New("token", image.Options{HTTP: httpClient})
	_, err := client.Generate(context.Background(), lilith.ImageRequest{Prompt: "x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid token")
}

func TestGenerate_EmptyZipIsError(t *testing.T) {
	t.Parallel()

	archive := zipWith(t, map[string][]byte{})

	httpClient := &mock.HTTPClientMock{
		DoFunc: func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(archive)),
				Header:     http.Header{},
			}, nil
		},
	}

	client := image.New("token", image.Options{HTTP: httpClient})
	_, err := client.Generate(context.Background(), lilith.ImageRequest{Prompt: "x"})
	require.Error(t, err)
}

// v4Caption mirrors the wire caption block for assertions.
type v4Caption struct {
	BaseCaption  string `json:"base_caption"`
	CharCaptions []any  `json:"char_captions"`
}

// generationBody mirrors the wire request for assertions.
type generationBody struct {
	Input      string `json:"input"`
	Model      string `json:"model"`
	Action     string `json:"action"`
	Parameters struct {
		ParamsVersion  int     `json:"params_version"`
		Width          int     `json:"width"`
		Height         int     `json:"height"`
		Scale          float64 `json:"scale"`
		Sampler        string  `json:"sampler"`
		Steps          int     `json:"steps"`
		Seed           int64   `json:"seed"`
		NSamples       int     `json:"n_samples"`
		NoiseSchedule  string  `json:"noise_schedule"`
		ImageFormat    string  `json:"image_format"`
		NegativePrompt string  `json:"negative_prompt"`
		V4Prompt       struct {
			Caption  v4Caption `json:"caption"`
			UseOrder bool      `json:"use_order"`
		} `json:"v4_prompt"`
		V4NegativePrompt struct {
			Caption v4Caption `json:"caption"`
		} `json:"v4_negative_prompt"`
	} `json:"parameters"`
}
