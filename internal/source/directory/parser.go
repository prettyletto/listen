// Package directory resolves documentation directories into one ordered
// canonical Document IR.
package directory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/source"
)

const parserVersion = "documentation-directory-v1"

var ErrNoDocuments = errors.New("directory contains no readable documentation files")

type Parser struct {
	parsers []source.Parser
}

func NewParser(parsers ...source.Parser) Parser {
	return Parser{parsers: append([]source.Parser(nil), parsers...)}
}

func (Parser) Name() string { return "documentation directory" }

func (Parser) Probe(ctx context.Context, input source.Input) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if input.Path == "" || input.R != nil {
		return 0, nil
	}
	info, err := os.Stat(input.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	if info.IsDir() {
		return 1000, nil
	}
	return 0, nil
}

func (p Parser) Parse(ctx context.Context, input source.Input) (*domain.Document, error) {
	if ctx == nil {
		return nil, errors.New("directory parsing requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if input.Path == "" {
		return nil, errors.New("directory source path is empty")
	}
	root, err := filepath.Abs(input.Path)
	if err != nil {
		return nil, fmt.Errorf("resolve documentation directory: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve documentation directory: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect documentation directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("documentation source %q is not a directory", input.Path)
	}

	rules, err := ancestorIgnoreRules(ctx, root)
	if err != nil {
		return nil, err
	}
	var files []candidate
	if err := collect(ctx, root, root, rules, &files); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, ErrNoDocuments
	}
	sort.Slice(files, func(i, j int) bool {
		left, right := files[i], files[j]
		if left.priority != right.priority {
			return left.priority < right.priority
		}
		leftKey, rightKey := strings.ToLower(left.relative), strings.ToLower(right.relative)
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return left.relative < right.relative
	})

	parsed := make([]parsedFile, 0, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		input := source.Input{Name: filepath.Base(file.absolute), Path: file.absolute}
		parser, err := source.SelectParser(ctx, input, p.parsers...)
		if err != nil {
			return nil, fmt.Errorf("resolve documentation file %q: %w", file.relative, err)
		}
		doc, err := parser.Parse(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("read documentation file %q: %w", file.relative, err)
		}
		parsed = append(parsed, parsedFile{relative: file.relative, document: doc})
	}
	return compose(root, parsed)
}

type candidate struct {
	absolute string
	relative string
	priority int
}

type parsedFile struct {
	relative string
	document *domain.Document
}

func collect(ctx context.Context, root, directory string, inherited []ignoreRule, files *[]candidate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rules, err := appendIgnoreFile(ctx, inherited, directory)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read documentation directory %q: %w", directory, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Name() == ".git" || entry.Name() == ".hg" || entry.Name() == ".svn" {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect documentation entry %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if isIgnored(path, info.IsDir(), rules) {
			continue
		}
		if info.IsDir() {
			if err := collect(ctx, root, path, rules, files); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() || !isDocumentationExtension(filepath.Ext(entry.Name())) {
			continue
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("locate documentation file %q: %w", path, err)
		}
		relative = filepath.ToSlash(relative)
		*files = append(*files, candidate{absolute: path, relative: relative, priority: priority(relative, root)})
	}
	return nil
}

func isDocumentationExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".md", ".markdown", ".txt", ".html", ".htm", ".xhtml", ".pdf":
		return true
	default:
		return false
	}
}

func priority(relative, root string) int {
	clean := strings.Trim(filepath.ToSlash(relative), "/")
	parts := strings.Split(clean, "/")
	name := strings.ToLower(parts[len(parts)-1])
	depth := len(parts)
	if depth == 1 && isReadme(name) {
		return 0
	}
	if depth == 1 && name == "agents.md" {
		return 1
	}
	if isReadme(name) {
		return 2
	}
	if name == "agents.md" {
		return 3
	}
	if isADRPath(parts) {
		return 4
	}
	if strings.EqualFold(filepath.Base(root), "docs") || containsPathComponent(parts, "docs") {
		return 5
	}
	return 6
}

func isReadme(name string) bool {
	return name == "readme.md" || name == "readme.markdown" || name == "readme.txt" || name == "readme.html" || name == "readme.htm" || name == "readme.xhtml"
}

func isADRPath(parts []string) bool {
	for _, part := range parts[:len(parts)-1] {
		if strings.EqualFold(part, "adr") {
			return true
		}
	}
	name := strings.ToLower(parts[len(parts)-1])
	return strings.HasPrefix(name, "adr-") || strings.HasPrefix(name, "adr_") || strings.HasPrefix(name, "adr ")
}

func containsPathComponent(parts []string, wanted string) bool {
	for _, part := range parts[:len(parts)-1] {
		if strings.EqualFold(part, wanted) {
			return true
		}
	}
	return false
}
