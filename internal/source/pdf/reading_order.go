package pdf

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

func groupTextLines(page pageLayout) []pdfLine {
	raw := append([]rawText(nil), page.raw...)
	sort.SliceStable(raw, func(i, j int) bool { return geometryLess(raw[i].top, raw[i].left, raw[j].top, raw[j].left) })
	var fragments [][]rawText
	for _, item := range raw {
		if item.text == "" {
			continue
		}
		matched := -1
		for i := len(fragments) - 1; i >= 0 && i >= len(fragments)-5; i-- {
			line := lineFromFragments(page.number, fragments[i])
			maxFont := math.Max(item.fontSize, line.fontSize)
			verticalTolerance := math.Max(2.5, maxFont*0.35)
			if math.Abs(item.top-line.top) > verticalTolerance {
				continue
			}
			gap := item.left - line.right
			if gap <= math.Max(12, maxFont*1.6) && item.left+item.width >= line.left-2 {
				matched = i
				break
			}
		}
		if matched >= 0 {
			fragments[matched] = append(fragments[matched], item)
		} else {
			fragments = append(fragments, []rawText{item})
		}
	}
	lines := make([]pdfLine, 0, len(fragments))
	for _, lineFragments := range fragments {
		lines = append(lines, lineFromFragments(page.number, lineFragments))
	}
	sort.SliceStable(lines, func(i, j int) bool { return geometryLess(lines[i].top, lines[i].left, lines[j].top, lines[j].left) })
	return lines
}

func lineFromFragments(page int, fragments []rawText) pdfLine {
	ordered := append([]rawText(nil), fragments...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].left < ordered[j].left })
	line := pdfLine{page: page, left: math.MaxFloat64, top: math.MaxFloat64}
	var text []string
	var sizes []float64
	for _, fragment := range ordered {
		text = append(text, fragment.text)
		line.left = math.Min(line.left, fragment.left)
		line.top = math.Min(line.top, fragment.top)
		line.right = math.Max(line.right, fragment.left+fragment.width)
		line.bottom = math.Max(line.bottom, fragment.top+fragment.height)
		line.bold = line.bold || fragment.bold || strings.Contains(strings.ToLower(fragment.family), "bold")
		if fragment.family != "" {
			line.family = fragment.family
		}
		if fragment.fontSize > 0 {
			sizes = append(sizes, fragment.fontSize)
		}
	}
	sort.Float64s(sizes)
	if len(sizes) > 0 {
		line.fontSize = sizes[len(sizes)/2]
	}
	line.text = normalizeText(strings.Join(text, " "))
	return line
}

func looksScanned(page pageLayout) bool {
	if page.ocr {
		return false
	}
	if page.width <= 0 || page.height <= 0 {
		return false
	}
	words := 0
	for _, line := range page.lines {
		words += len(strings.Fields(line.text))
	}
	if words >= 8 || len(page.images) == 0 {
		return false
	}
	area := 0.0
	for _, image := range page.images {
		area += math.Max(0, image.width) * math.Max(0, image.height)
	}
	coverage := math.Min(1, area/(page.width*page.height))
	return coverage >= 0.35
}

var pageNumberPattern = regexp.MustCompile(`(?i)^(?:\(?[0-9]+\)?|[ivxlcdm]+)$`)

func suppressMargins(pages []pageLayout) {
	type occurrence struct {
		pages map[int]bool
		tops  []float64
	}
	seen := make(map[string]*occurrence)
	for _, page := range pages {
		for _, line := range page.lines {
			if !isMarginLine(page, line) {
				continue
			}
			key := marginKey(line.text)
			if key == "" {
				continue
			}
			if seen[key] == nil {
				seen[key] = &occurrence{pages: make(map[int]bool)}
			}
			seen[key].pages[page.number] = true
			seen[key].tops = append(seen[key].tops, line.top/page.height)
		}
	}
	minimum := int(math.Ceil(float64(len(pages)) * 0.6))
	if minimum < 2 {
		minimum = 2
	}
	remove := make(map[string]bool)
	for key, occurrence := range seen {
		if len(occurrence.pages) < minimum {
			continue
		}
		if pageNumberPattern.MatchString(key) || consistentMarginPosition(occurrence.tops) {
			remove[key] = true
		}
	}
	for pageIndex := range pages {
		kept := pages[pageIndex].lines[:0]
		for _, line := range pages[pageIndex].lines {
			if isMarginLine(pages[pageIndex], line) && (pageNumberPattern.MatchString(marginKey(line.text)) || remove[marginKey(line.text)]) {
				continue
			}
			kept = append(kept, line)
		}
		pages[pageIndex].lines = kept
	}
}

func isMarginLine(page pageLayout, line pdfLine) bool {
	if page.height <= 0 {
		return false
	}
	return line.top/page.height <= 0.12 || line.bottom/page.height >= 0.88
}

func marginKey(text string) string {
	fields := strings.Fields(normalizeMarginText(text))
	if len(fields) > 1 && pageNumberPattern.MatchString(fields[len(fields)-1]) {
		fields = fields[:len(fields)-1]
	}
	return strings.Join(fields, " ")
}

func normalizeMarginText(text string) string {
	var result strings.Builder
	space := false
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if space && result.Len() > 0 {
				result.WriteByte(' ')
			}
			result.WriteRune(r)
			space = false
		} else {
			space = true
		}
	}
	return result.String()
}

func consistentMarginPosition(values []float64) bool {
	if len(values) < 2 {
		return false
	}
	minimum, maximum := values[0], values[0]
	for _, value := range values[1:] {
		minimum = math.Min(minimum, value)
		maximum = math.Max(maximum, value)
	}
	return maximum-minimum <= 0.035
}

func orderedPageLines(page pageLayout) []pdfLine {
	lines := append([]pdfLine(nil), page.lines...)
	if page.ocr {
		// Tesseract's TSV rows are already emitted in its detected reading
		// order. Geometry sorting would reverse RTL lines and can interleave
		// columns that Tesseract separated.
		return lines
	}
	split, found := detectColumnSplit(page, lines)
	if !found {
		sortPageLines(lines)
		return lines
	}
	for i := range lines {
		switch {
		case lines[i].right < split:
			lines[i].column = 0
		case lines[i].left > split:
			lines[i].column = 1
		default:
			lines[i].column = -1
		}
	}
	sortPageLines(lines)
	var ordered []pdfLine
	var left, right []pdfLine
	flushColumns := func() {
		sortPageLines(left)
		sortPageLines(right)
		ordered = append(ordered, left...)
		ordered = append(ordered, right...)
		left = nil
		right = nil
	}
	for _, line := range lines {
		centeredTitle := line.bold && math.Abs((line.left+line.right)/2-page.width/2) <= page.width*0.07 && line.right-line.left < page.width*0.6 && len([]rune(line.text)) <= 120
		if line.column < 0 || centeredTitle {
			flushColumns()
			ordered = append(ordered, line)
			continue
		}
		if line.column == 0 {
			left = append(left, line)
		} else {
			right = append(right, line)
		}
	}
	flushColumns()
	return ordered
}

func detectColumnSplit(page pageLayout, lines []pdfLine) (float64, bool) {
	if page.width <= 0 || page.height <= 0 || len(lines) < 4 {
		return 0, false
	}
	var candidates []float64
	for _, line := range lines {
		if line.top/page.height < 0.14 || line.bottom/page.height > 0.87 || line.right-line.left > page.width*0.62 {
			continue
		}
		candidates = append(candidates, line.left, line.right)
	}
	sort.Float64s(candidates)
	bestSplit, bestScore := 0.0, -1.0
	for i := 0; i+1 < len(candidates); i++ {
		if candidates[i+1]-candidates[i] < 8 {
			continue
		}
		split := (candidates[i] + candidates[i+1]) / 2
		if split < page.width*0.25 || split > page.width*0.75 {
			continue
		}
		leftCount, rightCount, crossing := 0, 0, 0
		leftEdge, rightEdge := 0.0, page.width
		var leftLines, rightLines []pdfLine
		for _, line := range lines {
			if line.top/page.height < 0.14 || line.bottom/page.height > 0.87 || line.right-line.left > page.width*0.62 {
				continue
			}
			switch {
			case line.right <= split:
				leftCount++
				leftLines = append(leftLines, line)
				leftEdge = math.Max(leftEdge, line.right)
			case line.left >= split:
				rightCount++
				rightLines = append(rightLines, line)
				rightEdge = math.Min(rightEdge, line.left)
			default:
				crossing++
			}
		}
		if leftCount < 2 || rightCount < 2 {
			continue
		}
		alignedRows := 0
		for _, leftLine := range leftLines {
			for _, rightLine := range rightLines {
				tolerance := math.Max(4, math.Min(leftLine.bottom-leftLine.top, rightLine.bottom-rightLine.top)*0.6)
				if math.Abs(leftLine.top-rightLine.top) <= tolerance {
					alignedRows++
					break
				}
			}
		}
		if alignedRows == 0 {
			continue
		}
		gap := math.Min(split-leftEdge, rightEdge-split)
		balance := float64(min(leftCount, rightCount)) / float64(max(leftCount, rightCount))
		score := float64(leftCount+rightCount)*balance + math.Max(0, gap)*0.05 - float64(crossing)*2
		if score > bestScore {
			bestScore, bestSplit = score, split
		}
	}
	return bestSplit, bestScore >= 0
}

func sortPageLines(lines []pdfLine) {
	sort.SliceStable(lines, func(i, j int) bool { return geometryLess(lines[i].top, lines[i].left, lines[j].top, lines[j].left) })
}

func geometryLess(aTop, aLeft, bTop, bLeft float64) bool {
	if aTop != bTop {
		return aTop < bTop
	}
	return aLeft < bLeft
}
