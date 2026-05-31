// Package static implements a disk-backed file server. Uploaded files are
// written to a local directory and served over HTTP so they survive restarts
// (in-memory storage would lose images referenced by persisted chat history).
package static

import (
	"context"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ernado/lilith"
	"github.com/go-faster/errors"
	"github.com/google/uuid"
)

var _ lilith.FileStore = (*Server)(nil)

// Server is a disk-backed file server. Files are stored under dir and served
// over HTTP.
type Server struct {
	addr    string
	baseURL string
	dir     string
}

// New creates a new Server that listens on addr, uses baseURL as the URL prefix
// and persists uploaded files under dir.
func New(addr, baseURL, dir string) *Server {
	return &Server{
		addr:    addr,
		baseURL: strings.TrimRight(baseURL, "/"),
		dir:     dir,
	}
}

// Upload reads all data from r, writes it under a new UUID in the storage
// directory, and returns the public URL for the uploaded file.
func (s *Server) Upload(r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", errors.Wrap(err, "read")
	}

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", errors.Wrap(err, "create dir")
	}

	id := uuid.New().String()

	if err := os.WriteFile(filepath.Join(s.dir, id), data, 0o644); err != nil {
		return "", errors.Wrap(err, "write file")
	}

	return s.baseURL + "/" + id, nil
}

// Run starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serveFile)

	srv := &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.Wrap(err, "listen and serve")
	}

	return nil
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	// path.Base strips any directory components, preventing path traversal out
	// of the storage directory.
	id := path.Base(r.URL.Path)
	if id == "." || id == "/" {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(filepath.Join(s.dir, id))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()

	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeContent(w, r, id, time.Time{}, f)
}
