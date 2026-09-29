// Package catalog reads a local index and keeps its source order.
package catalog

import (
	"context"
	"errors"
	"io"
)

// Entry stores an identifier and a short display name.
type Entry struct {
	ID   int
	Name string
}

// Reader loads entries in stable order.
type Reader interface {
	Read(context.Context) ([]Entry, error)
}

// Stream provides a closeable view of entries.
type Stream struct{}

// Close releases the reader resource.
func (Entry) Close() error {
	return nil
}

// Close releases the stream resource.
func (*Stream) Close() error {
	return nil
}

// Load reads one item and returns an error when the source is empty.
func Load(ctx context.Context, source io.Reader) ([]Entry, error) {
	if ctx.Err() != nil {
		return nil, errors.New("request canceled")
	}

	data, err := io.ReadAll(source)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("empty source")
	}
	return []Entry{{ID: len(data), Name: "loaded"}}, nil
}
