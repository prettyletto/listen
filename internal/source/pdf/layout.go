package pdf

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

type fontSpec struct {
	size   float64
	family string
}

type rawText struct {
	text                     string
	left, top, width, height float64
	fontID                   int
	fontSize                 float64
	family                   string
	bold                     bool
}

type pdfImage struct {
	width, height float64
}

type pdfLine struct {
	text                     string
	left, top, right, bottom float64
	fontSize                 float64
	family                   string
	bold                     bool
	column                   int
	page                     int
}

type pageLayout struct {
	number int
	width  float64
	height float64
	raw    []rawText
	images []pdfImage
	lines  []pdfLine
}

func parseLayoutXML(ctx context.Context, data []byte) ([]pageLayout, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	fonts := make(map[int]fontSpec)
	var pages []pageLayout
	var current *pageLayout
	textFragments := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch item := token.(type) {
		case xml.StartElement:
			switch item.Name.Local {
			case "page":
				current = &pageLayout{
					number: intAttr(item, "number"),
					width:  floatAttr(item, "width"),
					height: floatAttr(item, "height"),
				}
			case "fontspec":
				if len(fonts) >= maxPDFFontSpecs {
					return nil, fmt.Errorf("%w: PDF defines more than %d fonts", ErrResourceLimit, maxPDFFontSpecs)
				}
				fonts[intAttr(item, "id")] = fontSpec{
					size:   floatAttr(item, "size"),
					family: attr(item, "family"),
				}
			case "text":
				if current == nil {
					continue
				}
				text, bold, readErr := readTextElement(ctx, decoder)
				if readErr != nil {
					return nil, readErr
				}
				if strings.TrimSpace(text) == "" {
					continue
				}
				if textFragments >= maxPDFTextFragments {
					return nil, fmt.Errorf("%w: PDF contains more than %d text fragments", ErrResourceLimit, maxPDFTextFragments)
				}
				textFragments++
				current.raw = append(current.raw, rawText{
					text:   normalizeText(text),
					left:   floatAttr(item, "left"),
					top:    floatAttr(item, "top"),
					width:  floatAttr(item, "width"),
					height: floatAttr(item, "height"),
					fontID: intAttr(item, "font"),
					bold:   bold,
				})
			}
		case xml.EndElement:
			if item.Name.Local == "page" && current != nil {
				if len(pages) >= maxPDFPages {
					return nil, fmt.Errorf("%w: PDF has more than %d pages", ErrResourceLimit, maxPDFPages)
				}
				for i := range current.raw {
					if font, ok := fonts[current.raw[i].fontID]; ok {
						current.raw[i].height = nonzero(current.raw[i].height, font.size)
						current.raw[i].fontSize = font.size
						current.raw[i].family = font.family
					}
				}
				pages = append(pages, *current)
				current = nil
			}
		}
	}
	return pages, nil
}

func readTextElement(ctx context.Context, decoder *xml.Decoder) (string, bool, error) {
	var text strings.Builder
	depth := 1
	bold := false
	for depth > 0 {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		token, err := decoder.Token()
		if err != nil {
			return "", false, err
		}
		switch item := token.(type) {
		case xml.StartElement:
			depth++
			if item.Name.Local == "b" || item.Name.Local == "strong" {
				bold = true
			}
			text.WriteByte(' ')
		case xml.CharData:
			text.Write(item)
		case xml.EndElement:
			depth--
			if depth > 0 {
				text.WriteByte(' ')
			}
		}
	}
	return text.String(), bold, nil
}

func intAttr(element xml.StartElement, name string) int {
	value, _ := strconv.Atoi(attr(element, name))
	return value
}

func floatAttr(element xml.StartElement, name string) float64 {
	value, err := strconv.ParseFloat(attr(element, name), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func attr(element xml.StartElement, name string) string {
	for _, item := range element.Attr {
		if item.Name.Local == name {
			return item.Value
		}
	}
	return ""
}

func nonzero(value, fallback float64) float64 {
	if value <= 0 {
		return fallback
	}
	return value
}
