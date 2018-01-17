package backend

import (
	"context"
	"io"
	"io/ioutil"

	"github.com/restic/restic/internal/restic"
)

// LoadAll reads all data stored in the backend for the handle.
func LoadAll(ctx context.Context, be restic.Backend, h restic.Handle) (buf []byte, err error) {
	err = be.Load(ctx, h, 0, 0, func(rd io.Reader) error {
		buf, err = ioutil.ReadAll(rd)
		return err
	})
	return buf, err
}

// LimitedReadCloser wraps io.LimitedReader and exposes the Close() method.
type LimitedReadCloser struct {
	io.ReadCloser
	io.Reader
}

// Read reads data from the limited reader.
func (l *LimitedReadCloser) Read(p []byte) (int, error) {
	return l.Reader.Read(p)
}

// LimitReadCloser returns a new reader wraps r in an io.LimitReader, but also
// exposes the Close() method.
func LimitReadCloser(r io.ReadCloser, n int64) *LimitedReadCloser {
	return &LimitedReadCloser{ReadCloser: r, Reader: io.LimitReader(r, n)}
}
