package bot

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
)

// pngBytes builds a small solid-color PNG.
func pngBytes(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := range 4 {
		for y := range 4 {
			img.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))

	return buf.Bytes()
}

func TestToJPEG_ConvertsPNG(t *testing.T) {
	t.Parallel()

	out, err := toJPEG(pngBytes(t))
	require.NoError(t, err)

	// The output must be decodable as JPEG with the original dimensions.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, "jpeg", format)
	require.Equal(t, 4, cfg.Width)
	require.Equal(t, 4, cfg.Height)

	_, err = jpeg.Decode(bytes.NewReader(out))
	require.NoError(t, err)
}

func TestToJPEG_RejectsNonImage(t *testing.T) {
	t.Parallel()

	_, err := toJPEG([]byte("not an image"))
	require.Error(t, err)
}
