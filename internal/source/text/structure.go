package text

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/structure"
)

type sourceLine struct {
	text       string
	start, end int
}

type plainHeading struct {
	title      string
	role       domain.OutlineRole
	confidence domain.Confidence
	endLine    int
}

type headingEvidence struct {
	index     int
	text      string
	role      domain.OutlineRole
	score     int
	signature string
	explicit  bool
	repeated  bool
}

var (
	ordinalHeading = regexp.MustCompile(`(?i)^(chapter|chapitre|cap[ií]tulo|capitolo|part|parte|section|se[cç][aã]o|secci[oó]n)\s+([0-9ivxlcdm]+)(?:\s*[:.)—–-]\s*.*)?$`)
	numberedTitle  = regexp.MustCompile(`^\d+(?:\.\d+)*[.)]?\s+\S`)
	pageSuffix     = regexp.MustCompile(`\s+\d{1,4}\s*$`)
	appendixTitle  = regexp.MustCompile(`(?i)^appendix\s+(?:[a-z]|\d+|[ivxlcdm]+)(?:\s*[:—–-]\s*.+)?$`)
)

func parsePlainStructure(content []byte, path string, sourceOffset int, fallbackTitle string) ([]domain.Block, []*domain.OutlineNode, string, []string) {
	text := string(content)
	lines := splitSourceLines(text)
	toc := detectTOCLines(lines)
	boilerplate := detectGutenbergBoilerplate(lines)
	headings, title, heuristicUsed := detectPlainHeadings(lines, toc, boilerplate, fallbackTitle)
	blocks := emitPlainBlocks(lines, headings, toc, boilerplate, path, sourceOffset)
	if title == "" {
		title = fallbackTitle
	}
	outline := buildPlainOutline(blocks, title, path)
	warnings := []string{}
	if heuristicUsed {
		warnings = append(warnings, "Plain-text section headings were inferred from typography and isolation; review the outline if the source uses decorative all-caps lines.")
	}
	if len(toc) > 0 {
		warnings = append(warnings, "A probable table-of-contents region was retained and skipped by default.")
	}
	return blocks, outline, title, warnings
}

func splitSourceLines(text string) []sourceLine {
	var lines []sourceLine
	for offset := 0; offset < len(text); {
		start := offset
		for offset < len(text) && text[offset] != '\n' && text[offset] != '\r' {
			offset++
		}
		end := offset
		lines = append(lines, sourceLine{text: text[start:end], start: start, end: end})
		if offset < len(text) {
			if text[offset] == '\r' && offset+1 < len(text) && text[offset+1] == '\n' {
				offset += 2
			} else {
				offset++
			}
		}
	}
	if len(text) == 0 || strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\r") {
		lines = append(lines, sourceLine{start: len(text), end: len(text)})
	}
	return lines
}

func detectTOCLines(lines []sourceLine) map[int]bool {
	limit := len(lines)
	if limit > 80 {
		limit = 80
	}
	for start := 0; start < limit; start++ {
		label := normalizeLine(lines[start].text)
		if label != "contents" && label != "table of contents" && label != "sumario" && label != "sumário" {
			continue
		}
		marked := map[int]bool{start: true}
		entries := 0
		blankRun := 0
		for index := start + 1; index < limit; index++ {
			line := strings.TrimSpace(lines[index].text)
			if line == "" {
				blankRun++
				if entries > 0 && blankRun > 1 {
					break
				}
				marked[index] = true
				continue
			}
			if isTOCEntry(line) {
				blankRun = 0
				entries++
				marked[index] = true
				continue
			}
			break
		}
		if entries > 0 {
			return marked
		}
		// Some books omit page numbers. A repeated run of explicit chapter
		// labels after a contents label is still strong table evidence, while a
		// single Chapter 1 at the start of a book remains eligible prose.
		marked = map[int]bool{start: true}
		chapterEntries := 0
		blankRun = 0
		for index := start + 1; index < limit; index++ {
			line := strings.TrimSpace(lines[index].text)
			if line == "" {
				blankRun++
				if blankRun > 1 {
					break
				}
				marked[index] = true
				continue
			}
			if ordinalHeading.MatchString(line) && structure.ClassifyTitle(line) == domain.RoleChapter {
				chapterEntries++
				marked[index] = true
				blankRun = 0
				continue
			}
			break
		}
		if chapterEntries >= 2 {
			return marked
		}
		return nil
	}

	// Dotted leaders and trailing page numbers are strong TOC evidence even
	// without a Contents label. Require a short run so one unusual prose line
	// cannot suppress body text.
	for start := 0; start < limit; start++ {
		if !isTOCEntry(strings.TrimSpace(lines[start].text)) {
			continue
		}
		marked := map[int]bool{}
		entries, blankRun := 0, 0
		for index := start; index < limit; index++ {
			line := strings.TrimSpace(lines[index].text)
			if line == "" {
				blankRun++
				if blankRun > 1 {
					break
				}
				marked[index] = true
				continue
			}
			if !isTOCEntry(line) {
				break
			}
			entries++
			blankRun = 0
			marked[index] = true
		}
		if entries >= 2 {
			return marked
		}
	}
	return nil
}

func isTOCEntry(line string) bool {
	page := pageSuffix.MatchString(line) && len(strings.Fields(line)) > 1 && shortTitle(line) && !endsSentence(line)
	leader := (strings.Contains(line, "...") || strings.Contains(line, "…")) && page
	if leader || page {
		return true
	}
	return false
}

func detectGutenbergBoilerplate(lines []sourceLine) map[int]bool {
	marked := make(map[int]bool)
	start, end := -1, len(lines)
	for index, line := range lines {
		lower := strings.ToLower(line.text)
		if strings.Contains(lower, "*** start of the project gutenberg") || strings.Contains(lower, "*** start of this project gutenberg") {
			start = index
			break
		}
	}
	for index, line := range lines {
		lower := strings.ToLower(line.text)
		if strings.Contains(lower, "*** end of the project gutenberg") || strings.Contains(lower, "*** end of this project gutenberg") {
			end = index
			break
		}
	}
	if start >= 0 {
		for index := 0; index <= start; index++ {
			marked[index] = true
		}
	}
	if end < len(lines) {
		for index := end; index < len(lines); index++ {
			marked[index] = true
		}
	}
	return marked
}

func detectPlainHeadings(lines []sourceLine, toc, boilerplate map[int]bool, fallbackTitle string) (map[int]plainHeading, string, bool) {
	headings := make(map[int]plainHeading)
	firstContent := -1
	for index, line := range lines {
		trimmed := strings.TrimSpace(line.text)
		if trimmed != "" && !toc[index] && !boilerplate[index] {
			firstContent = index
			break
		}
	}
	title := ""
	if firstContent >= 0 && (isAllCapsTitle(strings.TrimSpace(lines[firstContent].text)) || (isIsolated(lines, firstContent) && isTitleCaseHeading(strings.TrimSpace(lines[firstContent].text)))) {
		title = strings.TrimSpace(lines[firstContent].text)
		headings[firstContent] = plainHeading{title: title, role: domain.RoleBook, confidence: domain.ConfidenceMedium, endLine: firstContent}
	}

	var evidence []headingEvidence
	signatureCounts := make(map[string]int)
	for index, line := range lines {
		text := strings.TrimSpace(line.text)
		if text == "" || toc[index] || boilerplate[index] || (title != "" && index == firstContent) {
			continue
		}
		role := structure.ClassifyTitle(text)
		if (role == domain.RoleFrontMatter || role == domain.RoleBackMatter || role == domain.RoleAppendix) && !isStandaloneMatterTitle(text, role) {
			continue
		}
		isolated := isIsolated(lines, index)
		upper := isAllCapsTitle(text)
		titleCase := isTitleCaseHeading(text)
		numbered := numberedTitle.MatchString(text)
		explicit := ordinalHeading.MatchString(text) || role == domain.RoleFrontMatter || role == domain.RoleBackMatter || role == domain.RoleAppendix
		score := scoreHeadingLine(text, role, isolated, upper, titleCase, numbered, explicit)
		signature := ""
		switch {
		case numbered && isolated:
			signature = headingSignature("numbered", text)
		case titleCase && !upper:
			signature = headingSignature("title_case", text)
		}
		if signature != "" {
			signatureCounts[signature]++
		}
		evidence = append(evidence, headingEvidence{index: index, text: text, role: role, score: score, signature: signature, explicit: explicit})
	}

	for index := range evidence {
		if evidence[index].signature != "" && signatureCounts[evidence[index].signature] >= 2 {
			evidence[index].repeated = true
			evidence[index].score += 2
		}
	}
	for _, item := range evidence {
		if item.score < 5 || (item.signature != "" && !item.repeated) {
			continue
		}
		if item.role == domain.RoleUnknown {
			item.role = domain.RoleSection
		}
		confidence := domain.ConfidenceLow
		if item.explicit {
			confidence = domain.ConfidenceHigh
		} else if isAllCapsTitle(item.text) || item.repeated {
			confidence = domain.ConfidenceMedium
		}
		headings[item.index] = plainHeading{title: item.text, role: item.role, confidence: confidence, endLine: item.index}
	}

	// A chapter label and its short, unpunctuated subtitle are one unit. This
	// covers common plain-text books where the title occupies the next line.
	for index, heading := range headings {
		if heading.role != domain.RoleChapter || index+1 >= len(lines) || strings.TrimSpace(lines[index+1].text) == "" || toc[index+1] || boilerplate[index+1] {
			continue
		}
		subtitle := strings.TrimSpace(lines[index+1].text)
		if shortTitle(subtitle) && !endsSentence(subtitle) && !ordinalHeading.MatchString(subtitle) && (isAllCapsTitle(subtitle) || isTitleCaseHeading(subtitle)) && (index+2 >= len(lines) || strings.TrimSpace(lines[index+2].text) == "") {
			heading.title += " — " + subtitle
			heading.endLine = index + 1
			headings[index] = heading
		}
	}

	// Strong explicit structure and repeated line patterns establish major
	// units. Isolated all-caps lines remain useful at a lower confidence.
	heuristicUsed := false
	if title == "" {
		title = fallbackTitle
	}
	for _, heading := range headings {
		if heading.confidence != domain.ConfidenceHigh {
			heuristicUsed = true
			break
		}
	}
	return headings, title, heuristicUsed
}

func scoreHeadingLine(text string, role domain.OutlineRole, isolated, upper, titleCase, numbered, explicit bool) int {
	score := 0
	if isolated {
		score += 2
	}
	if shortTitle(text) {
		score++
	}
	if endsSentence(text) {
		score -= 3
	} else {
		score++
	}
	if upper {
		score += 3
	}
	if titleCase && !upper {
		score++
	}
	if numbered {
		score += 2
	}
	if explicit {
		score += 5
	}
	if role != domain.RoleUnknown && !explicit {
		score += 2
	}
	return score
}

func headingSignature(family, text string) string {
	wordCount := len(strings.Fields(text))
	lengthBucket := "short"
	if wordCount > 3 {
		lengthBucket = "long"
	}
	if family == "numbered" {
		prefix := strings.Fields(text)[0]
		depth := strings.Count(strings.TrimRight(prefix, ".)"), ".")
		return family + ":" + fmt.Sprint(depth) + ":" + lengthBucket
	}
	return family + ":" + lengthBucket
}

func isStandaloneMatterTitle(text string, role domain.OutlineRole) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	labels := []string{}
	switch role {
	case domain.RoleFrontMatter:
		labels = []string{"preface", "foreword", "prologue", "prólogo", "prologo", "introduction", "introdução", "introducao", "dedication", "dedicatória", "acknowledgment", "acknowledgement", "acknowledgments", "acknowledgements", "copyright", "title page"}
	case domain.RoleBackMatter:
		labels = []string{"epilogue", "epílogo", "epilogo", "conclusion", "postface", "bibliography", "bibliografia", "references", "glossary", "glossário", "glossario", "index", "índice", "indice", "endnotes"}
	case domain.RoleAppendix:
		return appendixTitle.MatchString(strings.TrimSpace(text)) || text == "appendix"
	}
	for _, label := range labels {
		if text == label || strings.HasPrefix(text, label+":") || strings.HasPrefix(text, label+" —") || strings.HasPrefix(text, label+" –") || strings.HasPrefix(text, label+" -") {
			return true
		}
	}
	return false
}

func emitPlainBlocks(lines []sourceLine, headings map[int]plainHeading, toc, boilerplate map[int]bool, path string, sourceOffset int) []domain.Block {
	var blocks []domain.Block
	occurrences := make(map[string]int)
	appendBlock := func(kind domain.BlockKind, text string, start, end int, role domain.OutlineRole, confidence domain.Confidence, semantics []string, skip bool) {
		text = strings.Join(strings.Fields(text), " ")
		if text == "" {
			return
		}
		key := stableID(text)
		occurrence := occurrences[key]
		occurrences[key]++
		block := domain.Block{
			ID: stableID(parserVersion, path, key, fmt.Sprint(occurrence)), Seq: uint64(len(blocks)), Kind: kind,
			Text: text, Source: domain.SourceRef{Path: path, ByteStart: int64(start + sourceOffset), ByteEnd: int64(end + sourceOffset)},
			Semantics: append([]string(nil), semantics...), SkipByDefault: skip,
		}
		if kind == domain.BlockHeading {
			block.Level = plainHeadingLevel(role)
			if role == domain.RoleBook {
				block.Semantics = append(block.Semantics, "document_title")
			}
			if confidence == domain.ConfidenceLow {
				block.Semantics = append(block.Semantics, "structure:low_confidence")
			}
		}
		blocks = append(blocks, block)
	}

	for index := 0; index < len(lines); {
		if strings.TrimSpace(lines[index].text) == "" {
			index++
			continue
		}
		if heading, ok := headings[index]; ok && !toc[index] && !boilerplate[index] {
			endLine := heading.endLine
			if endLine >= len(lines) {
				endLine = len(lines) - 1
			}
			appendBlock(domain.BlockHeading, heading.title, lines[index].start, lines[endLine].end, heading.role, heading.confidence, nil, false)
			index = endLine + 1
			continue
		}
		isTOC, isBoiler := toc[index], boilerplate[index]
		start, end := lines[index].start, lines[index].end
		var parts []string
		if isTOC {
			appendBlock(domain.BlockParagraph, lines[index].text, start, end, domain.RoleUnknown, domain.ConfidenceLow, []string{"table_of_contents"}, true)
			index++
			continue
		}
		for index < len(lines) && strings.TrimSpace(lines[index].text) != "" && headings[index].title == "" && toc[index] == isTOC && boilerplate[index] == isBoiler {
			parts = append(parts, lines[index].text)
			end = lines[index].end
			index++
		}
		kind := domain.BlockParagraph
		semantics := []string(nil)
		skip := false
		if isTOC {
			semantics, skip = []string{"table_of_contents"}, true
		}
		if isBoiler {
			semantics, skip = []string{"gutenberg_boilerplate"}, true
		}
		appendBlock(kind, strings.Join(parts, " "), start, end, domain.RoleUnknown, domain.ConfidenceLow, semantics, skip)
	}
	return blocks
}

func buildPlainOutline(blocks []domain.Block, title, path string) []*domain.OutlineNode {
	root := &domain.OutlineNode{ID: stableID(parserVersion, path, "book", title), Title: title, Role: domain.RoleBook, Depth: 0, StartBlock: 0, EndBlockExclusive: uint64(len(blocks)), Confidence: domain.ConfidenceLow, Provenance: domain.StructureHeuristic}
	var headings []*domain.OutlineNode
	for _, block := range blocks {
		if block.Kind != domain.BlockHeading || block.SkipByDefault {
			continue
		}
		role := structure.ClassifyBlock(block)
		if containsSemantic(block.Semantics, "document_title") {
			role = domain.RoleBook
		} else if role == domain.RoleUnknown && block.Level >= 3 {
			role = domain.RoleSection
		}
		confidence := domain.ConfidenceHigh
		if containsSemantic(block.Semantics, "structure:low_confidence") {
			confidence = domain.ConfidenceLow
		} else if role == domain.RoleSection {
			confidence = domain.ConfidenceMedium
		}
		headings = append(headings, &domain.OutlineNode{ID: stableID(parserVersion, "heading", block.ID), Title: block.Text, Role: role, Depth: block.Level, StartBlock: block.Seq, EndBlockExclusive: uint64(len(blocks)), SourceTarget: block.Source, Confidence: confidence, Provenance: domain.StructureHeuristic})
	}
	stack := []*domain.OutlineNode{root}
	for _, node := range headings {
		depth := node.Depth
		if depth < 1 {
			depth = 1
			node.Depth = depth
		}
		for len(stack) > 1 && stack[len(stack)-1].Depth >= depth {
			stack = stack[:len(stack)-1]
		}
		parent := stack[len(stack)-1]
		node.Depth = parent.Depth + 1
		parent.Children = append(parent.Children, node)
		stack = append(stack, node)
	}
	for _, node := range headings {
		node.EndBlockExclusive = uint64(len(blocks))
	}
	for index, node := range headings {
		for next := index + 1; next < len(headings); next++ {
			if headings[next].Depth <= node.Depth {
				node.EndBlockExclusive = headings[next].StartBlock
				break
			}
		}
	}
	structure.ClassifyMajorUnits(root, blocks)
	if len(headings) > 0 {
		root.Confidence = domain.ConfidenceMedium
	}
	return []*domain.OutlineNode{root}
}

func plainHeadingLevel(role domain.OutlineRole) int {
	switch role {
	case domain.RoleBook, domain.RolePart, domain.RoleFrontMatter, domain.RoleBackMatter, domain.RoleAppendix:
		return 1
	case domain.RoleChapter:
		return 2
	case domain.RoleSection:
		return 3
	default:
		return 1
	}
}

func isIsolated(lines []sourceLine, index int) bool {
	beforeBlank := index == 0 || strings.TrimSpace(lines[index-1].text) == ""
	afterBlank := index+1 >= len(lines) || strings.TrimSpace(lines[index+1].text) == ""
	return beforeBlank && afterBlank
}

func isAllCapsTitle(text string) bool {
	words := strings.Fields(text)
	if len(words) == 0 || len(words) > 8 || len([]rune(text)) > 80 || endsSentence(text) {
		return false
	}
	letters, upper := 0, 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	return letters > 0 && upper*100/letters >= 85
}

func isTitleCaseHeading(text string) bool {
	words := strings.Fields(text)
	if len(words) < 2 || len(words) > 8 || len([]rune(text)) > 80 || endsSentence(text) {
		return false
	}
	minor := map[string]bool{"a": true, "an": true, "and": true, "as": true, "at": true, "by": true, "for": true, "in": true, "of": true, "on": true, "or": true, "the": true, "to": true, "with": true}
	for index, word := range words {
		clean := strings.Trim(word, "\"'([{*_`")
		if clean == "" {
			return false
		}
		first, _ := utf8.DecodeRuneInString(clean)
		if unicode.IsUpper(first) || (index > 0 && minor[strings.ToLower(clean)]) {
			continue
		}
		return false
	}
	return true
}

func shortTitle(text string) bool {
	return len(strings.Fields(text)) <= 10 && len([]rune(text)) <= 100
}

func endsSentence(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	last, _ := utf8LastRune(text)
	switch last {
	case '.', '?', '!', '…', '。', '？', '！', ':', ';':
		return true
	default:
		return false
	}
}

func normalizeLine(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	text = strings.Trim(text, "#*_=:- ")
	return strings.Join(strings.Fields(text), " ")
}

func containsSemantic(semantics []string, wanted string) bool {
	for _, semantic := range semantics {
		if semantic == wanted {
			return true
		}
	}
	return false
}

func utf8LastRune(text string) (rune, int) {
	return utf8.DecodeLastRuneInString(text)
}
