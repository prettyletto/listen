package pdf

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/prettyletto/listen/internal/domain"
)

type orderedBlock struct {
	text      string
	kind      domain.BlockKind
	level     int
	page      int
	left, top float64
	fontSize  float64
}

func makeBlocks(ctx context.Context, pages []pageLayout, path string) []domain.Block {
	suppressMargins(pages)
	var bodyLines []pdfLine
	for _, page := range pages {
		if !looksScanned(page) {
			bodyLines = append(bodyLines, page.lines...)
		}
	}
	medianFont := medianFontSize(bodyLines)
	classPages := fontClassPages(bodyLines)
	var classified []orderedBlock
	for _, page := range pages {
		if err := ctx.Err(); err != nil || looksScanned(page) {
			continue
		}
		lines := orderedPageLines(page)
		for i, line := range lines {
			previousGap, nextGap := 0.0, 0.0
			if i > 0 {
				previousGap = line.top - lines[i-1].bottom
			}
			if i+1 < len(lines) {
				nextGap = lines[i+1].top - line.bottom
			}
			if isVisualHeading(page, line, medianFont, previousGap, nextGap, classPages) {
				classified = append(classified, orderedBlock{text: line.text, kind: domain.BlockHeading, page: line.page, left: line.left, top: line.top, fontSize: line.fontSize})
				continue
			}
			if len(classified) > 0 && canJoinParagraph(classified[len(classified)-1], line, page) {
				classified[len(classified)-1].text += " " + line.text
				continue
			}
			classified = append(classified, orderedBlock{text: line.text, kind: domain.BlockParagraph, page: line.page, left: line.left, top: line.top, fontSize: line.fontSize})
		}
	}
	headingSizes := makeHeadingLevels(classified)
	blocks := make([]domain.Block, 0, len(classified))
	occurrences := make(map[string]int)
	for _, item := range classified {
		if err := ctx.Err(); err != nil {
			return nil
		}
		level := 0
		if item.kind == domain.BlockHeading {
			level = headingSizes[headingSizeKey(item.fontSize)]
		}
		text := normalizeText(item.text)
		occurrenceKey := fmt.Sprintf("%d\x00%.2f\x00%.2f\x00%s", item.page, item.left, item.top, text)
		occurrence := occurrences[occurrenceKey]
		occurrences[occurrenceKey]++
		id := stableID(parserVersion, path, occurrenceKey, fmt.Sprint(occurrence))
		blocks = append(blocks, domain.Block{
			ID: id, Seq: uint64(len(blocks)), Kind: item.kind, Text: text, Level: level,
			Source: domain.SourceRef{Path: path, Page: item.page},
		})
	}
	return blocks
}

func canJoinParagraph(previous orderedBlock, line pdfLine, page pageLayout) bool {
	if previous.kind != domain.BlockParagraph || previous.page != line.page {
		return false
	}
	previousBottom := previous.top + math.Max(previous.fontSize, 8)
	verticalGap := line.top - previousBottom
	if verticalGap > math.Max(previous.fontSize*1.25, 12) || verticalGap < -previous.fontSize*0.5 {
		return false
	}
	if math.Abs(previous.left-line.left) > math.Max(previous.fontSize*1.1, 14) {
		return false
	}
	if previous.fontSize > 0 && line.fontSize > 0 && math.Abs(previous.fontSize-line.fontSize)/previous.fontSize > 0.12 {
		return false
	}
	if page.width > 0 && previous.left > page.width*0.78 && line.left < page.width*0.22 {
		return false
	}
	return true
}

func medianFontSize(lines []pdfLine) float64 {
	var sizes []float64
	for _, line := range lines {
		if line.fontSize > 0 && len(strings.Fields(line.text)) > 0 {
			sizes = append(sizes, line.fontSize)
		}
	}
	if len(sizes) == 0 {
		return 0
	}
	sort.Float64s(sizes)
	return sizes[len(sizes)/2]
}

func fontClassPages(lines []pdfLine) map[string]map[int]bool {
	result := make(map[string]map[int]bool)
	for _, line := range lines {
		key := fontClassKey(line)
		if result[key] == nil {
			result[key] = make(map[int]bool)
		}
		result[key][line.page] = true
	}
	return result
}

func fontClassKey(line pdfLine) string {
	return fmt.Sprintf("%.1f|%t|%s", line.fontSize, line.bold, strings.ToLower(line.family))
}

var numberedHeadingPattern = regexp.MustCompile(`(?i)^(?:(?:chapter|part|section|appendix|book|volume)\s+)?(?:\d+(?:\.\d+)*|[ivxlcdm]+)[.)]?\s+\S`)
var namedHeadingPattern = regexp.MustCompile(`(?i)^(?:chapter|part|section|appendix|book|volume)\b`)

func isVisualHeading(page pageLayout, line pdfLine, median, previousGap, nextGap float64, classPages map[string]map[int]bool) bool {
	text := strings.TrimSpace(line.text)
	if text == "" || len([]rune(text)) > 140 || line.fontSize <= 0 || median <= 0 {
		return false
	}
	if strings.HasSuffix(text, ".") || strings.HasSuffix(text, ",") || strings.HasSuffix(text, ";") {
		return false
	}
	ratio := line.fontSize / median
	if (strings.HasSuffix(text, "?") || strings.HasSuffix(text, "!")) && !line.bold && ratio < 1.35 && !namedHeadingPattern.MatchString(text) && !numberedHeadingPattern.MatchString(text) {
		return false
	}
	score := 0
	switch {
	case ratio >= 1.35:
		score += 3
	case ratio >= 1.18:
		score += 2
	case ratio >= 1.08:
		score++
	}
	if line.bold {
		score++
	}
	if len([]rune(text)) <= 100 {
		score++
	}
	centered := math.Abs((line.left+line.right)/2-page.width/2) <= page.width*0.12
	if centered {
		score++
	}
	if previousGap >= math.Max(line.fontSize*0.9, 8) || nextGap >= math.Max(line.fontSize*0.9, 8) {
		score++
	}
	if numberedHeadingPattern.MatchString(text) || namedHeadingPattern.MatchString(text) {
		score += 2
	}
	if len(classPages[fontClassKey(line)]) > 1 && ratio >= 1.08 {
		score++
	}
	return score >= 5 && (ratio >= 1.12 || line.bold || numberedHeadingPattern.MatchString(text) || namedHeadingPattern.MatchString(text))
}

func makeHeadingLevels(blocks []orderedBlock) map[int]int {
	var sizes []float64
	seen := make(map[int]bool)
	for _, block := range blocks {
		if block.kind != domain.BlockHeading || block.fontSize <= 0 {
			continue
		}
		key := headingSizeKey(block.fontSize)
		if !seen[key] {
			seen[key] = true
			sizes = append(sizes, float64(key)/2)
		}
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(sizes)))
	levels := make(map[int]int, len(sizes))
	for i, size := range sizes {
		level := i + 1
		if level > 6 {
			level = 6
		}
		levels[int(math.Round(size*2))] = level
	}
	return levels
}

func headingSizeKey(size float64) int { return int(math.Round(size * 2)) }

func normalizeText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}
