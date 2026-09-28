package epub

import (
	"context"
	"encoding/xml"
	"errors"
	"strings"

	"golang.org/x/net/html"
)

type rawNavigationNode struct {
	title    string
	href     string
	children []*rawNavigationNode
}

func parseEPUB3Navigation(ctx context.Context, name string, data []byte) ([]*rawNavigationNode, error) {
	document, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	var toc *html.Node
	var find func(*html.Node)
	find = func(node *html.Node) {
		if toc != nil || node == nil {
			return
		}
		if node.Type == html.ElementNode && node.Data == "nav" && contains(nodeSemantics(node), "toc") {
			toc = node
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			find(child)
		}
	}
	find(document)
	if toc == nil {
		return nil, errors.New("EPUB navigation document has no toc nav element")
	}
	for child := toc.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && (child.Data == "ol" || child.Data == "ul") {
			return parseHTMLNavList(ctx, child)
		}
	}
	return nil, errors.New("EPUB toc nav has no ordered list")
}

func parseHTMLNavList(ctx context.Context, list *html.Node) ([]*rawNavigationNode, error) {
	var result []*rawNavigationNode
	for item := list.FirstChild; item != nil; item = item.NextSibling {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if item.Type != html.ElementNode || item.Data != "li" {
			continue
		}
		entry := &rawNavigationNode{}
		for child := item.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != html.ElementNode {
				continue
			}
			switch child.Data {
			case "a", "span":
				if entry.title == "" {
					title, _, err := collectTextAndIDs(ctx, child, collectOptions{skipNestedLists: true})
					if err != nil {
						return nil, err
					}
					entry.title = title
				}
				if entry.href == "" && child.Data == "a" {
					entry.href = strings.TrimSpace(attribute(child, "href"))
				}
			case "ol", "ul":
				children, err := parseHTMLNavList(ctx, child)
				if err != nil {
					return nil, err
				}
				entry.children = append(entry.children, children...)
			}
		}
		entry.title = strings.Join(strings.Fields(entry.title), " ")
		if entry.title != "" || entry.href != "" || len(entry.children) > 0 {
			result = append(result, entry)
		}
	}
	return result, nil
}

type ncxXML struct {
	NavMap struct {
		Points []ncxPointXML `xml:"navPoint"`
	} `xml:"navMap"`
}

type ncxPointXML struct {
	Label struct {
		Text string `xml:"text"`
	} `xml:"navLabel"`
	Content struct {
		Source string `xml:"src,attr"`
	} `xml:"content"`
	Children []ncxPointXML `xml:"navPoint"`
}

func parseNCX(ctx context.Context, data []byte) ([]*rawNavigationNode, error) {
	var document ncxXML
	if err := xml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	var convert func([]ncxPointXML) ([]*rawNavigationNode, error)
	convert = func(points []ncxPointXML) ([]*rawNavigationNode, error) {
		var result []*rawNavigationNode
		for _, point := range points {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			children, err := convert(point.Children)
			if err != nil {
				return nil, err
			}
			title := strings.Join(strings.Fields(point.Label.Text), " ")
			href := strings.TrimSpace(point.Content.Source)
			if title == "" && href == "" && len(children) == 0 {
				continue
			}
			result = append(result, &rawNavigationNode{title: title, href: href, children: children})
		}
		return result, nil
	}
	return convert(document.NavMap.Points)
}
