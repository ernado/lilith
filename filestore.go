package lilith

import "io"

//go:generate go tool moq -out internal/mock/filestore.go -pkg mock . FileStore

// FileStore stores a blob and returns a public URL for it. Used to host media
// (e.g. photos) so it can be passed to the model as an image URL.
type FileStore interface {
	Upload(r io.Reader) (string, error)
	// Delete removes the blob previously returned as url. A missing blob is not
	// an error.
	Delete(url string) error
}
