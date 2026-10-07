package app

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/prettyletto/listen/internal/apperr"
	"github.com/prettyletto/listen/internal/paths"
)

// selectedSourceNeedsSetup checks that a user-selected path can be opened as
// a supported source before asking whether local speech needs installation.
// The preflight reads at most a small header and never parses a document or
// invokes OCR.
func selectedSourceNeedsSetup(ctx context.Context, deps runtimeDeps, userPaths paths.Paths, options listenOptions) (bool, error) {
	if err := validateSourceBeforeSetup(ctx, options); err != nil {
		return false, err
	}
	return speechSetupNeeded(ctx, deps, userPaths)
}

func validateSourceBeforeSetup(ctx context.Context, options listenOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.source == "-" {
		return apperr.New("source_unavailable", "Standard input cannot be added to the listening home.", "Use `listen -` directly from the shell for piped text.", nil)
	}
	path := expandHome(options.source)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return apperr.New("source_missing", "Could not find the selected source.", "Check the path and choose an existing file or documentation folder before setup.", err)
		}
		return apperr.New("source_unavailable", "Could not inspect the selected source.", "Check the path and its permissions, then choose it again.", err)
	}
	if info.IsDir() {
		if options.code {
			return apperr.New("code_source_unsupported", "Code narration needs a Go source file.", "Choose a readable .go file before setup.", nil)
		}
		return validateDocumentationFolder(ctx, path)
	}
	if !info.Mode().IsRegular() {
		return apperr.New("source_unsupported", "This path is not a supported listening source.", "Choose a regular text, Markdown, HTML, EPUB, or PDF file, or a documentation folder.", nil)
	}
	return validateSourceFile(ctx, path, options.code)
}

func validateDocumentationFolder(ctx context.Context, root string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return apperr.New("source_unavailable", "Could not open this documentation folder.", "Check the folder path and its permissions, then choose it again.", err)
	}
	root = resolvedRoot
	found := false
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path != root && entry.IsDir() && isVersionControlDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !isDocumentationFile(entry.Name()) {
			return nil
		}
		if err := validateSourceFile(ctx, path, false); err == nil {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return apperr.New("source_unreadable", "Could not read this documentation folder.", "Check its permissions and choose a readable folder before setup.", err)
	}
	if !found {
		return apperr.New("source_unsupported", "This folder has no readable documentation files.", "Choose a folder containing text, Markdown, HTML, EPUB, or PDF documents before setup.", nil)
	}
	return nil
}

func validateSourceFile(ctx context.Context, path string, codeMode bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return apperr.New("source_unavailable", "Could not inspect the selected source.", "Check the path and its permissions, then choose it again.", err)
	}
	if !info.Mode().IsRegular() {
		return apperr.New("source_unsupported", "This path is not a supported listening source.", "Choose a regular file before setup.", nil)
	}
	if codeMode && !strings.EqualFold(filepath.Ext(path), ".go") {
		return apperr.New("code_source_unsupported", "Code narration requires a Go source file.", "Choose a readable .go file before setup.", nil)
	}
	if info.Size() == 0 {
		return apperr.New("source_empty", "This file is empty.", "Choose a source containing readable content before setup.", nil)
	}
	file, err := os.Open(path)
	if err != nil {
		return apperr.New("source_unreadable", "Could not read the selected source.", "Check the file permissions and choose a readable file before setup.", err)
	}
	defer file.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	header, err := io.ReadAll(io.LimitReader(file, 1024))
	if err != nil {
		return apperr.New("source_unreadable", "Could not read the selected source.", "Check the file permissions and choose a readable file before setup.", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	extension := strings.ToLower(filepath.Ext(path))
	if extension == ".pdf" || bytes.Contains(header, []byte("%PDF-")) {
		if !bytes.Contains(header, []byte("%PDF-")) {
			return unsupportedSelectedSource(fmt.Errorf("PDF signature is missing"))
		}
		return nil
	}
	if extension == ".epub" {
		archive, zipErr := zip.OpenReader(path)
		if zipErr != nil {
			return unsupportedSelectedSource(zipErr)
		}
		defer archive.Close()
		for _, item := range archive.File {
			if item.Name == "META-INF/container.xml" {
				return nil
			}
		}
		return unsupportedSelectedSource(errors.New("EPUB package descriptor is missing"))
	}
	if bytes.IndexByte(header, 0) >= 0 || !validUTF8Prefix(header, len(header) < 1024) {
		return unsupportedSelectedSource(errors.New("file content is not readable UTF-8 text"))
	}
	return nil
}

func validUTF8Prefix(content []byte, complete bool) bool {
	if utf8.Valid(content) || len(content) == 0 {
		return true
	}
	if complete {
		return false
	}
	start := len(content)
	for start > 0 && content[start-1]&0xC0 == 0x80 {
		start--
	}
	if start == len(content) {
		last := content[len(content)-1]
		if last < utf8.RuneSelf {
			return false
		}
		start--
	}
	lead := content[start]
	want := 0
	switch {
	case lead&0xE0 == 0xC0:
		want = 2
	case lead&0xF0 == 0xE0:
		want = 3
	case lead&0xF8 == 0xF0:
		want = 4
	default:
		return false
	}
	return len(content)-start < want && utf8.Valid(content[:start])
}

func isDocumentationFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".txt", ".html", ".htm", ".xhtml", ".pdf":
		return true
	default:
		return false
	}
}

func isVersionControlDirectory(name string) bool {
	switch name {
	case ".git", ".hg", ".svn":
		return true
	default:
		return false
	}
}

func unsupportedSelectedSource(cause error) error {
	return apperr.New("source_unsupported", "This file is not a supported listening source.", "Choose readable UTF-8 text, Markdown, HTML, a valid EPUB or PDF, or a documentation folder.", cause)
}
