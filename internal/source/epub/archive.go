package epub

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/prettyletto/listen/internal/source"
)

const (
	maxArchiveInputBytes = 128 << 20
	maxArchiveExpanded   = 512 << 20
	maxArchiveEntries    = 4096
	maxResourceBytes     = 32 << 20
)

var errNotEPUB = errors.New("source is not a readable EPUB archive")

type epubArchive struct {
	reader *zip.Reader
	closer io.Closer
	files  map[string]*zip.File
	ctx    context.Context
}

func openEPUB(ctx context.Context, input source.Input) (*epubArchive, error) {
	if ctx == nil {
		return nil, errors.New("EPUB parser requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var reader *zip.Reader
	var closer io.Closer
	if input.R == nil && input.Path != "" {
		file, err := os.Open(input.Path)
		if err != nil {
			return nil, fmt.Errorf("open EPUB: %w", err)
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("inspect EPUB: %w", err)
		}
		if info.Size() > maxArchiveInputBytes {
			_ = file.Close()
			return nil, fmt.Errorf("EPUB archive exceeds the %d MiB input limit", maxArchiveInputBytes>>20)
		}
		reader, err = zip.NewReader(file, info.Size())
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("read EPUB archive: %w", err)
		}
		closer = file
	} else {
		inputReader := input.R
		if inputReader == nil {
			return nil, errors.New("EPUB source has no reader or path")
		}
		data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: inputReader}, maxArchiveInputBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read EPUB source: %w", err)
		}
		if len(data) > maxArchiveInputBytes {
			return nil, fmt.Errorf("EPUB archive exceeds the %d MiB input limit", maxArchiveInputBytes>>20)
		}
		reader, err = zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("read EPUB archive: %w", err)
		}
	}
	archive := &epubArchive{reader: reader, closer: closer, files: make(map[string]*zip.File), ctx: ctx}
	if err := archive.indexFiles(); err != nil {
		_ = archive.Close()
		return nil, err
	}
	return archive, nil
}

func (a *epubArchive) Close() error {
	if a == nil || a.closer == nil {
		return nil
	}
	err := a.closer.Close()
	a.closer = nil
	return err
}

func (a *epubArchive) indexFiles() error {
	if len(a.reader.File) == 0 {
		return errNotEPUB
	}
	if len(a.reader.File) > maxArchiveEntries {
		return fmt.Errorf("EPUB archive contains more than %d entries", maxArchiveEntries)
	}
	var expanded uint64
	for _, file := range a.reader.File {
		if err := a.ctx.Err(); err != nil {
			return err
		}
		name, err := canonicalArchivePath(file.Name)
		if err != nil {
			return fmt.Errorf("unsafe EPUB archive entry %q: %w", file.Name, err)
		}
		if _, exists := a.files[name]; exists {
			return fmt.Errorf("EPUB archive contains duplicate entry %q", name)
		}
		if file.UncompressedSize64 > maxArchiveExpanded || expanded > maxArchiveExpanded-file.UncompressedSize64 {
			return fmt.Errorf("EPUB archive expands beyond the %d MiB safety limit", maxArchiveExpanded>>20)
		}
		expanded += file.UncompressedSize64
		a.files[name] = file
	}
	return nil
}

func (a *epubArchive) read(name string) ([]byte, error) {
	if err := a.ctx.Err(); err != nil {
		return nil, err
	}
	file, exists := a.files[name]
	if !exists {
		return nil, fmt.Errorf("EPUB resource %q is missing", name)
	}
	if file.UncompressedSize64 > maxResourceBytes {
		return nil, fmt.Errorf("EPUB resource %q exceeds the %d MiB resource limit", name, maxResourceBytes>>20)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open EPUB resource %q: %w", name, err)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: a.ctx, reader: reader}, maxResourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read EPUB resource %q: %w", name, err)
	}
	if len(data) > maxResourceBytes {
		return nil, fmt.Errorf("EPUB resource %q exceeds the %d MiB resource limit", name, maxResourceBytes>>20)
	}
	return data, nil
}

func canonicalArchivePath(raw string) (string, error) {
	if raw == "" || strings.Contains(raw, "\\") {
		return "", errors.New("empty paths and backslash separators are not allowed")
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", fmt.Errorf("decode archive path: %w", err)
	}
	if strings.HasPrefix(decoded, "/") {
		return "", errors.New("absolute archive paths are not allowed")
	}
	clean := path.Clean(decoded)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("archive path escapes the container")
	}
	return clean, nil
}

func resolveArchiveReference(baseFile, href string) (string, string, error) {
	reference, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return "", "", fmt.Errorf("parse EPUB reference: %w", err)
	}
	if reference.IsAbs() || reference.Host != "" || reference.Opaque != "" {
		return "", "", errors.New("external EPUB references are not fetched")
	}
	if strings.Contains(reference.Path, "\\") {
		return "", "", errors.New("backslash separators are not allowed in EPUB references")
	}
	resolved := baseFile
	if reference.Path != "" {
		resolved = path.Join(path.Dir(baseFile), reference.Path)
	}
	canonical, err := canonicalArchivePath(resolved)
	if err != nil {
		return "", "", err
	}
	return canonical, reference.Fragment, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
