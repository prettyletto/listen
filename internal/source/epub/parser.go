// Package epub converts EPUB OCF archives into the canonical document IR.
package epub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/source"
)

const parserVersion = "epub-structure-v1"

type Parser struct{}

func NewParser() Parser { return Parser{} }

func (Parser) Name() string { return "EPUB" }

func (Parser) Probe(ctx context.Context, input source.Input) (int, error) {
	if ctx == nil {
		return 0, errors.New("EPUB parser requires a context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	name := input.Name
	if name == "" {
		name = input.Path
	}
	if strings.EqualFold(filepath.Ext(name), ".epub") {
		return 100, nil
	}
	if input.R != nil || input.Path == "" {
		return 0, nil
	}
	archive, err := openEPUB(ctx, input)
	if err != nil {
		return 0, nil
	}
	defer archive.Close()
	if _, exists := archive.files["META-INF/container.xml"]; exists {
		return 100, nil
	}
	return 0, nil
}

func (Parser) Parse(ctx context.Context, input source.Input) (*domain.Document, error) {
	archive, err := openEPUB(ctx, input)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	pkg, err := readPackage(archive)
	if err != nil {
		return nil, err
	}
	doc := &domain.Document{
		Format: domain.FormatEPUB,
		Metadata: domain.Metadata{
			Title: pkg.title, Authors: append([]string(nil), pkg.authors...), Language: pkg.language,
		},
	}
	if strings.TrimSpace(doc.Metadata.Title) == "" {
		name := input.Name
		if name == "" {
			name = input.Path
		}
		doc.Metadata.Title = strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
		if doc.Metadata.Title == "" || doc.Metadata.Title == "." {
			doc.Metadata.Title = "Untitled EPUB"
		}
	}
	pkg.title = doc.Metadata.Title
	var navPath string
	var navItem *manifestItem
	for _, item := range pkg.manifestList {
		if item.properties["nav"] {
			copy := item
			navItem = &copy
			navPath = item.path
			break
		}
	}
	files := make(map[string]*contentFile)
	for _, item := range pkg.spine {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !item.linear {
			addWarning(&doc.Warnings, fmt.Sprintf("Non-linear EPUB spine item %q was retained as provenance and excluded from default reading order.", item.path))
			continue
		}
		if !isXHTML(item) {
			addWarning(&doc.Warnings, fmt.Sprintf("Unsupported linear EPUB spine resource %q was omitted from the reading stream.", item.path))
			continue
		}
		data, err := archive.read(item.path)
		if err != nil {
			return nil, fmt.Errorf("read EPUB spine document %q: %w", item.path, err)
		}
		parsed, err := parseContentFile(ctx, item.path, item.index, uint64(len(doc.Blocks)), data)
		if err != nil {
			return nil, err
		}
		if len(parsed.blocks) == 0 {
			addWarning(&doc.Warnings, fmt.Sprintf("EPUB spine document %q contained no narratable blocks.", item.path))
			continue
		}
		if suppressTOCPage(&parsed, navPath) {
			addWarning(&doc.Warnings, fmt.Sprintf("EPUB table-of-contents page %q is skipped during default narration.", item.path))
		}
		doc.Blocks = append(doc.Blocks, parsed.blocks...)
		for _, warning := range parsed.warnings {
			addWarning(&doc.Warnings, warning)
		}
		if _, exists := files[item.path]; exists {
			addWarning(&doc.Warnings, fmt.Sprintf("EPUB spine repeats resource %q; fragment links resolve to its first occurrence.", item.path))
			continue
		}
		fileCopy := parsed
		files[item.path] = &fileCopy
	}
	if len(doc.Blocks) == 0 {
		return nil, errors.New("EPUB contains no readable content in its linear spine")
	}
	for seq, block := range doc.Blocks {
		if block.Seq != uint64(seq) {
			return nil, fmt.Errorf("EPUB parser produced a non-contiguous block sequence at %d", seq)
		}
	}

	var entries []*rawNavigationNode
	provenance := domain.StructureSource("")
	if navItem != nil {
		if data, readErr := archive.read(navPath); readErr == nil {
			entries, err = parseEPUB3Navigation(ctx, navPath, data)
			if err == nil && len(entries) > 0 {
				provenance = domain.StructureEPUBNav
			} else {
				addWarning(&doc.Warnings, "EPUB 3 navigation was missing or unusable; a legacy NCX or content headings were used.")
			}
		} else {
			addWarning(&doc.Warnings, fmt.Sprintf("EPUB 3 navigation resource %q could not be read; a legacy NCX or content headings were used.", navPath))
		}
	}
	if len(entries) == 0 {
		ncxItem, found := findNCX(pkg)
		if found {
			navPath = ncxItem.path
			if data, readErr := archive.read(navPath); readErr == nil {
				entries, err = parseNCX(ctx, data)
				if err == nil && len(entries) > 0 {
					provenance = domain.StructureNCX
					addWarning(&doc.Warnings, "EPUB 2 NCX provided the navigation hierarchy.")
				} else {
					addWarning(&doc.Warnings, "EPUB NCX was missing or unusable; XHTML headings and spine boundaries were used.")
				}
			} else {
				addWarning(&doc.Warnings, "EPUB NCX could not be read; XHTML headings and spine boundaries were used.")
			}
		}
	}
	if len(entries) == 0 && provenance == "" {
		addWarning(&doc.Warnings, "EPUB navigation and NCX were missing or unusable; XHTML headings and spine boundaries were used.")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(entries) > 0 && provenance != "" {
		doc.AuxiliaryNavigation, err = collectAuxiliaryNavigation(ctx, navPath, entries, provenance, pkg, files)
		if err != nil {
			return nil, err
		}
	}
	root := buildOutline(ctx, pkg, navPath, entries, provenance, files, doc.Blocks, &doc.Warnings)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	doc.Outline = root
	if err := validateDocument(doc); err != nil {
		return nil, err
	}
	doc.ID, err = documentID(doc)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func isXHTML(item spineItem) bool {
	if item.mediaType == contentMediaType {
		return true
	}
	switch strings.ToLower(filepath.Ext(item.path)) {
	case ".xhtml", ".html", ".htm":
		return true
	default:
		return false
	}
}

func findNCX(pkg packageDocument) (manifestItem, bool) {
	if pkg.spineTOC != "" {
		if item, exists := pkg.manifest[pkg.spineTOC]; exists {
			return item, true
		}
	}
	for _, item := range pkg.manifestList {
		if item.mediaType == ncxMediaType {
			return item, true
		}
	}
	return manifestItem{}, false
}

func validateDocument(doc *domain.Document) error {
	count := uint64(len(doc.Blocks))
	var validate func(*domain.OutlineNode, uint64, uint64) error
	validate = func(node *domain.OutlineNode, parentStart, parentEnd uint64) error {
		if node == nil {
			return errors.New("EPUB outline contains a nil node")
		}
		if node.StartBlock >= node.EndBlockExclusive || node.StartBlock < parentStart || node.EndBlockExclusive > parentEnd || node.EndBlockExclusive > count {
			return fmt.Errorf("EPUB outline node %q has an invalid range [%d,%d)", node.Title, node.StartBlock, node.EndBlockExclusive)
		}
		previousEnd := node.StartBlock
		for _, child := range node.Children {
			if child.StartBlock < previousEnd {
				return fmt.Errorf("EPUB outline children of %q overlap or are out of order", node.Title)
			}
			if err := validate(child, node.StartBlock, node.EndBlockExclusive); err != nil {
				return err
			}
			previousEnd = child.EndBlockExclusive
		}
		return nil
	}
	if len(doc.Outline) == 0 {
		return errors.New("EPUB parser produced no outline")
	}
	previousEnd := uint64(0)
	for _, node := range doc.Outline {
		if node.StartBlock < previousEnd {
			return errors.New("EPUB top-level outline entries overlap or are out of order")
		}
		if err := validate(node, 0, count); err != nil {
			return err
		}
		previousEnd = node.EndBlockExclusive
	}
	return nil
}

func documentID(doc *domain.Document) (string, error) {
	copy := *doc
	copy.ID = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("hash EPUB document structure: %w", err)
	}
	sum := sha256.Sum256(append([]byte(parserVersion+"\x00"), data...))
	return hex.EncodeToString(sum[:]), nil
}

var _ source.Parser = Parser{}
