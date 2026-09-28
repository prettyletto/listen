package source

import (
	"context"
	"io"

	"github.com/prettyletto/listen/internal/domain"
)

type Input struct {
	Name string
	Path string
	R    io.Reader
}

// Parser converts a format into the canonical Document IR. Format-specific
// quirks stop here; pipeline stages consume only domain.Document.
type Parser interface {
	Name() string
	Probe(context.Context, Input) (score int, err error)
	Parse(context.Context, Input) (*domain.Document, error)
}
