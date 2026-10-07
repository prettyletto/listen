package playback

import (
	"context"
	"errors"
	"time"
)

var ErrSeekNotBuffered = errors.New("seek target audio is not ready yet")

type Item struct {
	Seq      uint64
	Path     string
	Duration time.Duration
	Title    string
}

type Player interface {
	Start(context.Context) error
	Append(context.Context, Item) error
	Position(context.Context) (seq uint64, offset time.Duration, err error)
	Wait(context.Context) error
	Close() error
}

// Seeker resumes playback within the current queue item.
type Seeker interface {
	Seek(context.Context, time.Duration) error
}

type Position struct {
	Seq      uint64
	Offset   time.Duration
	Duration time.Duration
}

// BufferedSeeker moves along the audio files currently queued for playback.
// ErrSeekNotBuffered means synthesis has not queued the requested target yet.
type BufferedSeeker interface {
	SeekBy(context.Context, time.Duration) (Position, error)
}

// OffsetAppender queues the first item at an offset as it is loaded. This
// avoids racing an asynchronous player load with a separate seek command.
type OffsetAppender interface {
	AppendAt(context.Context, Item, time.Duration) error
}

// Pausable controls playback without exposing the provider's command protocol.
type Pausable interface {
	SetPaused(context.Context, bool) error
}

type DurationReader interface {
	CurrentDuration(context.Context) (time.Duration, error)
}

// RateSetter changes playback speed without restarting the current audio item.
type RateSetter interface {
	SetPlaybackRate(context.Context, float64) error
}
