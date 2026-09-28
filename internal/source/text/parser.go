// Package text parses plain UTF-8 text into the canonical document IR.
package text

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/source"
)

const parserVersion = "plain-text-v1"

var (
	ErrBinarySource = errors.New("the source contains binary data")
	ErrInvalidUTF8  = errors.New("the source is not valid UTF-8 text")
	ErrEmptySource  = errors.New("the source contains no readable text")
)

type Parser struct{}

func NewParser() Parser { return Parser{} }

func (Parser) Name() string { return "plain text" }

func (Parser) Probe(ctx context.Context, input source.Input) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	name := input.Name
	if filepath.Ext(name) == "" {
		name = input.Path
	}
	switch filepath.Ext(strings.ToLower(name)) {
	case ".md", ".markdown":
		return 0, nil
	case ".txt":
		return 100, nil
	}
	return 10, nil
}

func (Parser) Parse(ctx context.Context, input source.Input) (*domain.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	content, err := readInput(ctx, input)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return nil, fmt.Errorf("%w; listen expects readable text", ErrBinarySource)
	}
	contentStart := 0
	if bytes.HasPrefix(content, []byte{0xef, 0xbb, 0xbf}) {
		content = content[3:]
		contentStart = 3
	}
	if !utf8.Valid(content) {
		return nil, ErrInvalidUTF8
	}
	path := input.Path
	if path == "" {
		path = input.Name
	}
	if path != "" {
		if absolute, err := filepath.Abs(path); err == nil {
			path = absolute
		}
	}
	title := strings.TrimSpace(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	if title == "" || title == "." {
		title = "Untitled document"
	}

	sum := sha256.Sum256(content)
	documentID := hex.EncodeToString(sum[:])
	doc := &domain.Document{
		ID:       documentID,
		Format:   domain.FormatText,
		Metadata: domain.Metadata{Title: title},
	}
	doc.Blocks, doc.Outline, doc.Metadata.Title, doc.Warnings = parsePlainStructure(content, path, contentStart, title)
	if len(doc.Blocks) == 0 {
		return nil, ErrEmptySource
	}
	return doc, nil
}

func readInput(ctx context.Context, input source.Input) ([]byte, error) {
	reader := input.R
	var file *os.File
	if reader == nil {
		if input.Path == "" {
			return nil, errors.New("text source has no reader or path")
		}
		opened, err := os.Open(input.Path)
		if err != nil {
			return nil, fmt.Errorf("open text source: %w", err)
		}
		file = opened
		reader = opened
	}
	if file != nil {
		defer file.Close()
	}
	content, err := io.ReadAll(contextReader{ctx: ctx, reader: reader})
	if err != nil {
		return nil, fmt.Errorf("read text source: %w", err)
	}
	return content, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func stableID(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(hash, part)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}
