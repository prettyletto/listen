package segment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/structure"
)

const segmentationVersion = "semantic-v1"

type Options struct {
	TargetWords int
	MaxWords    int
	IncludeCode bool
}

func (o Options) normalized() Options {
	if o.TargetWords <= 0 {
		o.TargetWords = 70
	}
	if o.MaxWords <= 0 { // Roughly one minute at a measured 150 words per minute.
		o.MaxWords = 150
	}
	if o.MaxWords < o.TargetWords {
		o.MaxWords = o.TargetWords
	}
	return o
}

// PlanDocument converts ordered IR blocks into stable, semantic TTS units.
// It prefers paragraph and sentence boundaries and never combines different
// major units from the outline.
func PlanDocument(doc *domain.Document, options Options) (Plan, error) {
	if doc == nil {
		return Plan{}, errors.New("cannot segment a nil document")
	}
	options = options.normalized()
	units := blockUnits(doc)
	var segments []Segment
	var current *segmentBuilder
	flush := func() {
		if current == nil || strings.TrimSpace(current.text) == "" {
			current = nil
			return
		}
		segments = append(segments, current.segment(doc, uint64(len(segments))))
		current = nil
	}

	for _, block := range doc.Blocks {
		if block.SkipByDefault || block.Kind == domain.BlockBreak || (block.Kind == domain.BlockCode && !options.IncludeCode) {
			continue
		}
		text := strings.Join(strings.Fields(block.Text), " ")
		if text == "" {
			continue
		}
		unit := units[block.Seq]
		parts := splitBlock(block, text, options.MaxWords)
		for _, part := range parts {
			if current != nil && current.majorUnit != unit.id {
				flush()
			}
			separator := " "
			if current != nil && current.lastBlock != block.Seq {
				separator = "\n\n"
			}
			candidateWords := wordCount(part.text)
			if current != nil {
				candidateWords += wordCount(current.text)
				if candidateWords > options.MaxWords || (candidateWords > options.TargetWords && wordCount(current.text) >= options.TargetWords*2/3) {
					flush()
					separator = " "
				}
			}
			if current == nil {
				kind := segmentKind(block.Kind)
				current = &segmentBuilder{majorUnit: unit.id, sectionPath: append([]string(nil), unit.path...), kind: kind, source: block.Source}
				separator = ""
			}
			current.add(part.text, separator, block)
			if wordCount(current.text) >= options.TargetWords {
				flush()
			}
		}
	}
	flush()
	return Plan{DocumentID: doc.ID, Segments: segments}, nil
}

type blockUnit struct {
	id   string
	path []string
}

func blockUnits(doc *domain.Document) map[uint64]blockUnit {
	units := make(map[uint64]blockUnit, len(doc.Blocks))
	defaultID := doc.ID
	for _, block := range doc.Blocks {
		units[block.Seq] = blockUnit{id: defaultID}
	}
	var visit func(*domain.OutlineNode, []string, string)
	visit = func(node *domain.OutlineNode, parentPath []string, parentID string) {
		if node == nil {
			return
		}
		path := append(append([]string(nil), parentPath...), strings.TrimSpace(node.Title))
		unitID := parentID
		if structure.IsMajorRole(node.Role) || (node.Provenance == domain.StructureDirectory && node.Depth > 0) || (unitID == "" && node.Role == domain.RoleBook) {
			unitID = node.ID
		}
		if unitID == "" {
			unitID = defaultID
		}
		start := node.StartBlock
		end := node.EndBlockExclusive
		if end > uint64(len(doc.Blocks)) {
			end = uint64(len(doc.Blocks))
		}
		for seq := start; seq < end; seq++ {
			units[seq] = blockUnit{id: unitID, path: append([]string(nil), path...)}
		}
		for _, child := range node.Children {
			visit(child, path, unitID)
		}
	}
	for _, node := range doc.Outline {
		visit(node, nil, "")
	}
	return units
}

type piece struct {
	text  string
	block domain.Block
}

func splitBlock(block domain.Block, text string, maxWords int) []piece {
	sentences := splitSentences(text)
	var result []piece
	for _, sentence := range sentences {
		words := strings.Fields(sentence)
		for start := 0; start < len(words); start += maxWords {
			end := start + maxWords
			if end > len(words) {
				end = len(words)
			}
			result = append(result, piece{text: strings.Join(words[start:end], " "), block: block})
		}
	}
	return result
}

func splitSentences(text string) []string {
	runes := []rune(text)
	var result []string
	start := 0
	for i, r := range runes {
		if r != '.' && r != '?' && r != '!' && r != '…' && r != '。' && r != '？' && r != '！' {
			continue
		}
		end := i + 1
		for end < len(runes) && isClosingPunctuation(runes[end]) {
			end++
		}
		if end < len(runes) && !unicode.IsSpace(runes[end]) {
			continue
		}
		if r == '.' && i > start && i+1 < len(runes) && unicode.IsDigit(runes[i-1]) && unicode.IsDigit(runes[i+1]) {
			continue
		}
		if sentence := strings.TrimSpace(string(runes[start:end])); sentence != "" {
			result = append(result, sentence)
		}
		start = end
		for start < len(runes) && unicode.IsSpace(runes[start]) {
			start++
		}
	}
	if tail := strings.TrimSpace(string(runes[start:])); tail != "" {
		result = append(result, tail)
	}
	return result
}

func isClosingPunctuation(r rune) bool {
	switch r {
	case '"', '\'', '”', '’', ')', ']', '}', '»':
		return true
	default:
		return false
	}
}

func segmentKind(kind domain.BlockKind) Kind {
	switch kind {
	case domain.BlockHeading:
		return KindHeading
	case domain.BlockCode:
		return KindCode
	default:
		return KindProse
	}
}

type segmentBuilder struct {
	text        string
	majorUnit   string
	sectionPath []string
	kind        Kind
	lastBlock   uint64
	blocks      []uint64
	source      domain.SourceRef
}

func (b *segmentBuilder) add(text, separator string, block domain.Block) {
	b.text += separator + text
	if len(b.blocks) == 0 || b.blocks[len(b.blocks)-1] != block.Seq {
		b.blocks = append(b.blocks, block.Seq)
	}
	b.lastBlock = block.Seq
	if b.source.Path == "" {
		b.source = block.Source
	}
}

func (b *segmentBuilder) segment(doc *domain.Document, seq uint64) Segment {
	text := strings.TrimSpace(b.text)
	ids := make([]string, 0, len(b.blocks))
	for _, blockSeq := range b.blocks {
		if blockSeq < uint64(len(doc.Blocks)) {
			ids = append(ids, doc.Blocks[blockSeq].ID)
		}
	}
	textHash := sha256.Sum256([]byte(text))
	identity, _ := json.Marshal(struct {
		Version string
		Path    string
		Blocks  []string
		Text    string
	}{Version: segmentationVersion, Path: b.source.Path, Blocks: ids, Text: hex.EncodeToString(textHash[:])})
	segmentHash := sha256.Sum256(identity)
	return Segment{
		ID:           hex.EncodeToString(segmentHash[:16]),
		Seq:          seq,
		Kind:         b.kind,
		SourceText:   text,
		SpokenText:   text,
		SourceLang:   doc.Metadata.Language,
		SectionPath:  append([]string(nil), b.sectionPath...),
		MajorUnitID:  b.majorUnit,
		SourceBlocks: append([]uint64(nil), b.blocks...),
		Source:       b.source,
	}
}

func wordCount(text string) int { return len(strings.Fields(text)) }
