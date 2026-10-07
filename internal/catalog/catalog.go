// Package catalog stores the user's local listening library.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/prettyletto/listen/internal/atomicfile"
)

const (
	// FileName is the catalog file stored inside listen's XDG data directory.
	FileName       = "library.json"
	catalogVersion = 1
)

// Entry is one source in the user's listening home. Path is also the stable
// entry ID. It is made absolute and cleaned without resolving symlinks, matching
// the source identity used by session storage.
type Entry struct {
	Path           string    `json:"path"`
	Title          string    `json:"title,omitempty"`
	TargetLanguage string    `json:"target_language,omitempty"`
	CodeMode       bool      `json:"code_mode,omitempty"`
	ResumeLabel    string    `json:"resume_label,omitempty"`
	LastOpenedAt   time.Time `json:"last_opened_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Complete       bool      `json:"complete,omitempty"`
}

// ID returns the canonical path identity used by the catalog and suitable for
// stable UI list keys. Entries returned from List or Upsert always have a
// cleaned absolute Path.
func (entry Entry) ID() string { return entry.Path }

// Catalog stores entries in dataDir/library.json. Mutations are serialized
// across processes and atomically replace the catalog file.
type Catalog struct {
	path    string
	initErr error
}

// New creates a catalog handle without touching the filesystem. dataDir should
// be the listen-specific XDG data directory returned by paths.FromEnv.
func New(dataDir string) *Catalog {
	if dataDir == "" {
		return &Catalog{initErr: errors.New("catalog data directory is empty")}
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return &Catalog{initErr: fmt.Errorf("resolve catalog data directory: %w", err)}
	}
	return &Catalog{path: filepath.Join(filepath.Clean(absolute), FileName)}
}

type diskCatalog struct {
	Version int     `json:"catalog_version"`
	Entries []Entry `json:"entries"`
}

// List returns entries in most-recently-opened order. Reading an absent
// catalog yields an empty slice and does not create its data directory.
func (catalog *Catalog) List(ctx context.Context) ([]Entry, error) {
	if err := catalog.ready(ctx); err != nil {
		return nil, err
	}
	document, err := catalog.read()
	if err != nil {
		return nil, err
	}
	entries := append([]Entry(nil), document.Entries...)
	sortEntries(entries)
	return entries, nil
}

// Upsert inserts or replaces the entry with the same canonical path. The
// caller supplies all fields that should be retained. A zero LastOpenedAt is
// set to the current time; UpdatedAt is always assigned by the catalog.
func (catalog *Catalog) Upsert(ctx context.Context, entry Entry) (Entry, error) {
	if err := catalog.ready(ctx); err != nil {
		return Entry{}, err
	}
	path, err := canonicalPath(entry.Path)
	if err != nil {
		return Entry{}, err
	}
	entry.Path = path

	lock, err := acquireLock(ctx, catalog.path+".lock")
	if err != nil {
		return Entry{}, fmt.Errorf("lock catalog: %w", err)
	}
	defer lock.close()

	document, err := catalog.read()
	if err != nil {
		return Entry{}, err
	}
	now := time.Now().UTC()
	if entry.LastOpenedAt.IsZero() {
		entry.LastOpenedAt = now
	} else {
		entry.LastOpenedAt = entry.LastOpenedAt.UTC()
	}
	entry.UpdatedAt = now

	updated := false
	for index := range document.Entries {
		if document.Entries[index].Path == path {
			document.Entries[index] = entry
			updated = true
			break
		}
	}
	if !updated {
		document.Entries = append(document.Entries, entry)
	}
	sortEntries(document.Entries)
	if err := atomicfile.WriteJSON(ctx, catalog.path, document, 0o600); err != nil {
		return Entry{}, fmt.Errorf("write catalog: %w", err)
	}
	return entry, nil
}

// Remove removes and returns an entry so callers can offer an Undo action. It
// changes only the catalog; it never deletes the source or any cached data.
func (catalog *Catalog) Remove(ctx context.Context, sourcePath string) (Entry, bool, error) {
	if err := catalog.ready(ctx); err != nil {
		return Entry{}, false, err
	}
	path, err := canonicalPath(sourcePath)
	if err != nil {
		return Entry{}, false, err
	}

	lock, err := acquireLock(ctx, catalog.path+".lock")
	if err != nil {
		return Entry{}, false, fmt.Errorf("lock catalog: %w", err)
	}
	defer lock.close()

	document, err := catalog.read()
	if err != nil {
		return Entry{}, false, err
	}
	for index, entry := range document.Entries {
		if entry.Path != path {
			continue
		}
		remaining := append(document.Entries[:index:index], document.Entries[index+1:]...)
		document.Entries = remaining
		if err := atomicfile.WriteJSON(ctx, catalog.path, document, 0o600); err != nil {
			return Entry{}, false, fmt.Errorf("write catalog: %w", err)
		}
		return entry, true, nil
	}
	return Entry{}, false, nil
}

func (catalog *Catalog) ready(ctx context.Context) error {
	if catalog == nil {
		return errors.New("catalog is not initialized")
	}
	if catalog.initErr != nil {
		return catalog.initErr
	}
	if ctx == nil {
		return errors.New("catalog operation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (catalog *Catalog) read() (diskCatalog, error) {
	data, err := os.ReadFile(catalog.path)
	if errors.Is(err, os.ErrNotExist) {
		return diskCatalog{Version: catalogVersion, Entries: []Entry{}}, nil
	}
	if err != nil {
		return diskCatalog{}, fmt.Errorf("read catalog: %w", err)
	}
	var document diskCatalog
	if err := json.Unmarshal(data, &document); err != nil {
		return diskCatalog{}, fmt.Errorf("decode catalog: %w", err)
	}
	if document.Version != catalogVersion {
		return diskCatalog{}, fmt.Errorf("unsupported catalog version %d", document.Version)
	}
	seen := make(map[string]struct{}, len(document.Entries))
	for index := range document.Entries {
		path, err := canonicalPath(document.Entries[index].Path)
		if err != nil || path != document.Entries[index].Path {
			return diskCatalog{}, fmt.Errorf("catalog entry %d has a non-canonical source path", index)
		}
		if _, duplicate := seen[path]; duplicate {
			return diskCatalog{}, fmt.Errorf("catalog contains duplicate source path %q", path)
		}
		seen[path] = struct{}{}
	}
	if document.Entries == nil {
		document.Entries = []Entry{}
	}
	return document, nil
}

func canonicalPath(sourcePath string) (string, error) {
	if sourcePath == "" {
		return "", errors.New("catalog source path is empty")
	}
	absolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", fmt.Errorf("resolve catalog source path: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].LastOpenedAt.Equal(entries[j].LastOpenedAt) {
			return entries[i].LastOpenedAt.After(entries[j].LastOpenedAt)
		}
		if !entries[i].UpdatedAt.Equal(entries[j].UpdatedAt) {
			return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
		}
		return entries[i].Path < entries[j].Path
	})
}
