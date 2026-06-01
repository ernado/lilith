package static

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServer_UploadWritesToDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "img")
	s := New("", "http://example/files", dir)

	url, err := s.Upload(strings.NewReader("image-bytes"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(url, "http://example/files/"))

	id := strings.TrimPrefix(url, "http://example/files/")
	data, err := os.ReadFile(filepath.Join(dir, id))
	require.NoError(t, err)
	require.Equal(t, "image-bytes", string(data))
}

func TestServer_ServeFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "img")
	s := New("", "http://example/files", dir)

	url, err := s.Upload(strings.NewReader("image-bytes"))
	require.NoError(t, err)
	id := strings.TrimPrefix(url, "http://example/files/")

	rec := httptest.NewRecorder()
	s.serveFile(rec, httptest.NewRequest("GET", "/"+id, nil))
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "image-bytes", rec.Body.String())
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
}

func TestServer_ServeFileNotFound(t *testing.T) {
	s := New("", "http://example/files", t.TempDir())

	rec := httptest.NewRecorder()
	s.serveFile(rec, httptest.NewRequest("GET", "/missing", nil))
	require.Equal(t, 404, rec.Code)
}

func TestServer_Delete(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "img")
	s := New("", "http://example/files", dir)

	url, err := s.Upload(strings.NewReader("image-bytes"))
	require.NoError(t, err)
	id := strings.TrimPrefix(url, "http://example/files/")

	require.NoError(t, s.Delete(url))

	_, err = os.Stat(filepath.Join(dir, id))
	require.True(t, os.IsNotExist(err), "file should be removed")
}

func TestServer_DeleteMissingIsNoError(t *testing.T) {
	s := New("", "http://example/files", t.TempDir())
	require.NoError(t, s.Delete("http://example/files/does-not-exist"))
}

// TestServer_ServeFileTraversal ensures path components cannot escape the
// storage directory.
func TestServer_ServeFileTraversal(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(filepath.Dir(dir), "secret")
	require.NoError(t, os.WriteFile(secret, []byte("top-secret"), 0o644))

	s := New("", "http://example/files", dir)

	rec := httptest.NewRecorder()
	s.serveFile(rec, httptest.NewRequest("GET", "/../secret", nil))
	require.Equal(t, 404, rec.Code)
	require.NotContains(t, rec.Body.String(), "top-secret")
}
