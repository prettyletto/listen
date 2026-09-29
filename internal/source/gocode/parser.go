// Package gocode turns Go source files into short spoken explanations.
package gocode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/source"
)

const (
	ParserVersion        = "go-ast-narration-v2"
	MaxSourceBytes int64 = 8 << 20
	MaxASTNodes          = 250_000
)

var (
	ErrNotGoSource      = errors.New("code narration requires a Go source file")
	ErrInvalidGoSource  = errors.New("the Go source file could not be parsed")
	ErrEmptySource      = errors.New("the Go source file has no package declaration")
	ErrSourceTooLarge   = errors.New("the Go source file exceeds the narration size limit")
	ErrSourceTooComplex = errors.New("the Go source file exceeds the narration complexity limit")
)

type Parser struct{}

func NewParser() Parser { return Parser{} }

func (Parser) Name() string { return "Go code" }

func (Parser) Probe(ctx context.Context, input source.Input) (int, error) {
	if ctx == nil {
		return 0, errors.New("Go source probe requires a context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if strings.EqualFold(filepath.Ext(inputName(input)), ".go") {
		return 100, nil
	}
	return 0, nil
}

func (Parser) Parse(ctx context.Context, input source.Input) (*domain.Document, error) {
	if ctx == nil {
		return nil, errors.New("Go source parsing requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(inputName(input)), ".go") {
		return nil, ErrNotGoSource
	}
	content, err := readInput(ctx, input)
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		return nil, ErrEmptySource
	}
	if !utf8.Valid(content) {
		return nil, fmt.Errorf("%w: source is not valid UTF-8", ErrInvalidGoSource)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	file, parseErr := parser.ParseFile(fset, inputName(input), content, parser.ParseComments|parser.AllErrors|parser.SkipObjectResolution)
	if parseErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidGoSource, parseErr)
	}
	if file == nil || file.Name == nil || strings.TrimSpace(file.Name.Name) == "" {
		return nil, ErrEmptySource
	}
	if err := countASTNodes(ctx, file); err != nil {
		return nil, err
	}

	path := input.Path
	if path == "" {
		path = input.Name
	}
	if path != "" {
		if absolute, absErr := filepath.Abs(path); absErr == nil {
			path = absolute
		}
	}
	name := filepath.Base(inputName(input))
	title := strings.TrimSuffix(name, filepath.Ext(name))
	if title == "" || title == "." {
		title = "Go source"
	}

	contentHash := sha256.Sum256(content)
	docHash := sha256.Sum256([]byte(ParserVersion + "\x00" + hex.EncodeToString(contentHash[:])))
	doc := &domain.Document{
		ID:       hex.EncodeToString(docHash[:]),
		Format:   domain.FormatCode,
		Metadata: domain.Metadata{Title: title},
	}
	writer := documentWriter{doc: doc, path: path, fset: fset}
	writer.block(domain.BlockHeading, "Package "+identifierSpeech(file.Name.Name), 1, file.Name.Pos(), file.Name.End(), "package")
	if text := commentSpeech(file.Doc); text != "" {
		start, end := commentRange(file.Doc)
		writer.block(domain.BlockParagraph, text, 0, start, end, "doc-comment")
	}
	if len(file.Imports) > 0 {
		writer.imports(file.Imports)
	}
	for _, declaration := range file.Decls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch declaration := declaration.(type) {
		case *ast.GenDecl:
			if err := writer.generalDeclaration(ctx, declaration); err != nil {
				return nil, err
			}
		case *ast.FuncDecl:
			if err := writer.functionDeclaration(ctx, declaration); err != nil {
				return nil, err
			}
		}
	}
	if len(doc.Blocks) == 0 {
		return nil, ErrEmptySource
	}

	root := &domain.OutlineNode{
		ID:                stableID(doc.ID, "package", file.Name.Name),
		Title:             "Package " + identifierSpeech(file.Name.Name),
		Role:              domain.RoleBook,
		Depth:             0,
		StartBlock:        0,
		EndBlockExclusive: uint64(len(doc.Blocks)),
		Confidence:        domain.ConfidenceHigh,
		Provenance:        domain.StructureCodeAST,
	}
	for _, node := range writer.outline {
		root.Children = append(root.Children, node)
	}
	doc.Outline = []*domain.OutlineNode{root}
	return doc, nil
}

func inputName(input source.Input) string {
	if input.Name != "" {
		return input.Name
	}
	return input.Path
}

func readInput(ctx context.Context, input source.Input) ([]byte, error) {
	reader := input.R
	var file *os.File
	if reader == nil {
		if input.Path == "" {
			return nil, errors.New("Go source has no reader or path")
		}
		opened, err := os.Open(input.Path)
		if err != nil {
			return nil, fmt.Errorf("open Go source: %w", err)
		}
		file = opened
		reader = opened
	}
	if file != nil {
		defer file.Close()
	}
	content, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: reader}, MaxSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Go source: %w", err)
	}
	if int64(len(content)) > MaxSourceBytes {
		return nil, ErrSourceTooLarge
	}
	return content, nil
}

type astWalkStop struct{ err error }

func countASTNodes(ctx context.Context, file *ast.File) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			if stop, ok := recovered.(astWalkStop); ok {
				err = stop.err
				return
			}
			panic(recovered)
		}
	}()
	count := 0
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			return false
		}
		count++
		if count%256 == 0 {
			if err := ctx.Err(); err != nil {
				panic(astWalkStop{err: err})
			}
		}
		if count > MaxASTNodes {
			panic(astWalkStop{err: ErrSourceTooComplex})
		}
		return true
	})
	return ctx.Err()
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

type documentWriter struct {
	doc     *domain.Document
	path    string
	fset    *token.FileSet
	outline []*domain.OutlineNode
}

func (w *documentWriter) block(kind domain.BlockKind, text string, level int, start, end token.Pos, semantics ...string) uint64 {
	text = strings.TrimSpace(text)
	seq := uint64(len(w.doc.Blocks))
	startOffset, endOffset := w.offset(start), w.offset(end)
	if endOffset < startOffset {
		endOffset = startOffset
	}
	w.doc.Blocks = append(w.doc.Blocks, domain.Block{
		ID:        stableID(w.doc.ID, strconv.FormatUint(seq, 10), string(kind), text),
		Seq:       seq,
		Kind:      kind,
		Text:      text,
		Level:     level,
		Source:    domain.SourceRef{Path: w.path, ByteStart: int64(startOffset), ByteEnd: int64(endOffset)},
		Semantics: append([]string{"language:go"}, semantics...),
	})
	return seq
}

func (w *documentWriter) offset(pos token.Pos) int {
	if !pos.IsValid() {
		return 0
	}
	return w.fset.Position(pos).Offset
}

func (w *documentWriter) addOutline(title, identity string, start uint64, nodeStart, nodeEnd token.Pos) {
	if start >= uint64(len(w.doc.Blocks)) {
		return
	}
	w.outline = append(w.outline, &domain.OutlineNode{
		ID:                stableID(w.doc.ID, "declaration", identity),
		Title:             title,
		Role:              domain.RoleSection,
		Depth:             1,
		StartBlock:        start,
		EndBlockExclusive: uint64(len(w.doc.Blocks)),
		SourceTarget:      domain.SourceRef{Path: w.path, ByteStart: int64(w.offset(nodeStart)), ByteEnd: int64(w.offset(nodeEnd))},
		Confidence:        domain.ConfidenceHigh,
		Provenance:        domain.StructureCodeAST,
	})
}

func (w *documentWriter) imports(imports []*ast.ImportSpec) {
	var names []string
	for _, item := range imports {
		path, err := strconv.Unquote(item.Path.Value)
		if err != nil || path == "" {
			continue
		}
		label := filepath.Base(path)
		if label == "." || label == "/" {
			label = path
		}
		if item.Name != nil && item.Name.Name != "_" && item.Name.Name != "." {
			label = identifierSpeech(item.Name.Name) + " (" + identifierSpeech(label) + ")"
		}
		names = append(names, label)
	}
	if len(names) == 0 {
		return
	}
	start := imports[0].Pos()
	end := imports[len(imports)-1].End()
	first := w.block(domain.BlockHeading, "Imports", 2, start, end, "imports")
	text := "Imports " + joinNatural(names) + "."
	w.block(domain.BlockParagraph, text, 0, start, end, "imports")
	w.outline = append(w.outline, &domain.OutlineNode{
		ID:                stableID(w.doc.ID, "imports"),
		Title:             "Imports",
		Role:              domain.RoleSection,
		Depth:             1,
		StartBlock:        first,
		EndBlockExclusive: uint64(len(w.doc.Blocks)),
		SourceTarget:      domain.SourceRef{Path: w.path, ByteStart: int64(w.offset(start)), ByteEnd: int64(w.offset(end))},
		Confidence:        domain.ConfidenceHigh,
		Provenance:        domain.StructureCodeAST,
	})
}

func (w *documentWriter) generalDeclaration(ctx context.Context, declaration *ast.GenDecl) error {
	if declaration.Tok == token.IMPORT {
		return nil
	}
	groupComment := declaration.Doc
	for index, spec := range declaration.Specs {
		if err := ctx.Err(); err != nil {
			return err
		}
		start := spec.Pos()
		var end token.Pos = spec.End()
		title := ""
		kind := "declaration"
		identity := ""
		var doc *ast.CommentGroup
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			title = "Type " + identifierSpeech(spec.Name.Name)
			identity = "type:" + spec.Name.Name
			doc = spec.Doc
			if doc == nil && index == 0 {
				doc = groupComment
			}
		case *ast.ValueSpec:
			kind = strings.ToLower(declaration.Tok.String())
			if len(spec.Names) > 0 {
				title = capitalize(kind) + " " + identifierSpeech(spec.Names[0].Name)
				if len(spec.Names) > 1 {
					title += " and " + joinIdentifiers(spec.Names[1:])
				}
				identity = kind + ":" + strings.Join(rawIdentifiers(spec.Names), ",")
			}
			doc = spec.Doc
			if doc == nil && index == 0 {
				doc = groupComment
			}
		default:
			continue
		}
		if specComment := specComment(spec); specComment != nil {
			doc = specComment
		}
		if doc != nil && doc.Pos() < start {
			start = doc.Pos()
		}
		first := w.block(domain.BlockHeading, title, 2, start, end, kind)
		if text := commentSpeech(doc); text != "" {
			commentStart, commentEnd := commentRange(doc)
			w.block(domain.BlockParagraph, text, 0, commentStart, commentEnd, "doc-comment")
		}
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			w.block(domain.BlockParagraph, typeSummary(spec), 0, spec.Pos(), spec.End(), "type-signature")
		case *ast.ValueSpec:
			w.block(domain.BlockParagraph, valueSummary(declaration.Tok, spec), 0, spec.Pos(), spec.End(), kind)
		}
		w.addOutline(title, identity, first, start, end)
	}
	return nil
}

func specComment(spec ast.Spec) *ast.CommentGroup {
	switch spec := spec.(type) {
	case *ast.TypeSpec:
		if spec.Doc != nil {
			return spec.Doc
		}
		return spec.Comment
	case *ast.ValueSpec:
		if spec.Doc != nil {
			return spec.Doc
		}
		return spec.Comment
	default:
		return nil
	}
}

func (w *documentWriter) functionDeclaration(ctx context.Context, declaration *ast.FuncDecl) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	kind, title := "function", "Function "
	identity := "function:" + declaration.Name.Name
	if declaration.Recv != nil && len(declaration.Recv.List) > 0 {
		kind, title = "method", "Method "
		receiver := declaration.Recv.List[0].Type
		title += identifierSpeech(declaration.Name.Name) + " on " + typeSpeech(receiver)
		identity = "method:" + receiverIdentity(receiver) + ":" + declaration.Name.Name
	} else {
		title += identifierSpeech(declaration.Name.Name)
	}
	start := declaration.Pos()
	if declaration.Doc != nil && declaration.Doc.Pos() < start {
		start = declaration.Doc.Pos()
	}
	first := w.block(domain.BlockHeading, title, 2, start, declaration.End(), kind)
	if text := commentSpeech(declaration.Doc); text != "" {
		commentStart, commentEnd := commentRange(declaration.Doc)
		w.block(domain.BlockParagraph, text, 0, commentStart, commentEnd, "doc-comment")
	}
	w.block(domain.BlockParagraph, functionSummary(declaration), 0, declaration.Type.Pos(), declaration.Type.End(), "function-signature")
	if declaration.Body != nil {
		items, err := bodyNarration(ctx, declaration.Body)
		if err != nil {
			return err
		}
		for _, item := range items {
			w.block(domain.BlockParagraph, item.text, 0, item.statement.Pos(), item.statement.End(), "control-flow")
		}
	}
	w.addOutline(title, identity, first, start, declaration.End())
	return nil
}

func rawIdentifiers(identifiers []*ast.Ident) []string {
	result := make([]string, 0, len(identifiers))
	for _, identifier := range identifiers {
		if identifier != nil {
			result = append(result, identifier.Name)
		}
	}
	return result
}

func receiverIdentity(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.StarExpr:
		return "*" + receiverIdentity(expression.X)
	case *ast.IndexExpr:
		return receiverIdentity(expression.X) + "[" + expressionIdentity(expression.Index) + "]"
	case *ast.IndexListExpr:
		var arguments []string
		for _, argument := range expression.Indices {
			arguments = append(arguments, expressionIdentity(argument))
		}
		return receiverIdentity(expression.X) + "[" + strings.Join(arguments, ",") + "]"
	case *ast.ParenExpr:
		return receiverIdentity(expression.X)
	default:
		return "receiver"
	}
}

func expressionIdentity(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.SelectorExpr:
		return expressionIdentity(expression.X) + "." + expression.Sel.Name
	default:
		return "type-argument"
	}
}

func commentRange(group *ast.CommentGroup) (token.Pos, token.Pos) {
	if group == nil {
		return token.NoPos, token.NoPos
	}
	return group.Pos(), group.End()
}

func stableID(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(hash, part)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}
