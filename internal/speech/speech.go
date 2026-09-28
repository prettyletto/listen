package speech

import (
	"context"
	"time"

	"github.com/prettyletto/listen/internal/segment"
)

type Request struct {
	Segment segment.Segment
	Voice   string
	Speed   float64
	Output  string
}

type Result struct {
	SegmentID string
	Seq       uint64
	Path      string
	Duration  time.Duration
}

type Synthesizer interface {
	ID() string
	Synthesize(context.Context, Request) (Result, error)
	Close() error
}
