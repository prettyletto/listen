package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/prettyletto/listen/internal/cache"
)

const manifestVersion = 1
const maxResumeOffset = 24 * time.Hour

// State is the last durable playback checkpoint for a source. Sensitive
// provider credentials are deliberately not part of the session manifest.
type State struct {
	Version            int           `json:"version"`
	SourcePath         string        `json:"source_path"`
	DocumentID         string        `json:"document_id"`
	SegmentID          string        `json:"segment_id,omitempty"`
	SegmentSeq         uint64        `json:"segment_seq"`
	MajorUnitID        string        `json:"major_unit_id,omitempty"`
	SectionPath        []string      `json:"section_path,omitempty"`
	Offset             time.Duration `json:"offset_ns"`
	PlaybackSpeed      float64       `json:"playback_speed"`
	TargetLanguage     string        `json:"target_language,omitempty"`
	Voice              string        `json:"voice,omitempty"`
	TranslationBackend string        `json:"translation_backend,omitempty"`
	UpdatedAt          time.Time     `json:"updated_at"`
	Complete           bool          `json:"complete,omitempty"`
}

type Store struct {
	storage    *cache.FileStore
	key        string
	sourcePath string
}

func NewStore(cacheRoot, sourcePath string) (*Store, error) {
	if sourcePath == "" {
		return nil, errors.New("session source path is empty")
	}
	absolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("resolve session source path: %w", err)
	}
	storage, err := cache.NewFileStore(cacheRoot)
	if err != nil {
		return nil, err
	}
	return &Store{
		storage: storage, key: cache.Key("session-v1", absolute), sourcePath: absolute,
	}, nil
}

func (s *Store) Load(ctx context.Context) (State, bool, error) {
	if s == nil || s.storage == nil {
		return State{}, false, errors.New("session store is not initialized")
	}
	var state State
	found, err := s.storage.Get(ctx, cache.StageSession, s.key, &state)
	if err != nil || !found {
		return State{}, false, err
	}
	if state.Version != manifestVersion || filepath.Clean(state.SourcePath) != s.sourcePath {
		return State{}, false, nil
	}
	if state.Offset < 0 || state.Offset > maxResumeOffset {
		return State{}, false, nil
	}
	return state, true, nil
}

func (s *Store) Save(ctx context.Context, state State) error {
	if s == nil || s.storage == nil {
		return errors.New("session store is not initialized")
	}
	if state.SourcePath != "" && filepath.Clean(state.SourcePath) != s.sourcePath {
		return errors.New("session state belongs to a different source")
	}
	if state.Offset < 0 || state.Offset > maxResumeOffset {
		return errors.New("session offset is outside the supported range")
	}
	state.Version = manifestVersion
	state.SourcePath = s.sourcePath
	state.UpdatedAt = time.Now().UTC()
	return s.storage.PutAtomic(ctx, cache.StageSession, s.key, state)
}
