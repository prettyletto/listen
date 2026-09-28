package epub

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
)

const (
	packageMediaType = "application/oebps-package+xml"
	contentMediaType = "application/xhtml+xml"
	ncxMediaType     = "application/x-dtbncx+xml"
)

type containerXML struct {
	Rootfiles []struct {
		FullPath  string `xml:"full-path,attr"`
		MediaType string `xml:"media-type,attr"`
	} `xml:"rootfiles>rootfile"`
}

type packageXML struct {
	Metadata struct {
		Titles     []string `xml:"title"`
		Creators   []string `xml:"creator"`
		Languages  []string `xml:"language"`
		Identifier []string `xml:"identifier"`
	} `xml:"metadata"`
	Manifest struct {
		Items []manifestItemXML `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		TOC   string         `xml:"toc,attr"`
		Items []spineItemXML `xml:"itemref"`
	} `xml:"spine"`
}

type manifestItemXML struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

type spineItemXML struct {
	IDRef  string `xml:"idref,attr"`
	Linear string `xml:"linear,attr"`
}

type manifestItem struct {
	id         string
	path       string
	mediaType  string
	properties map[string]bool
}

type packageDocument struct {
	path         string
	title        string
	authors      []string
	language     string
	identifier   string
	manifest     map[string]manifestItem
	manifestList []manifestItem
	spine        []spineItem
	spineTOC     string
}

type spineItem struct {
	manifestItem
	index  int
	linear bool
}

func readPackage(archive *epubArchive) (packageDocument, error) {
	containerData, err := archive.read("META-INF/container.xml")
	if err != nil {
		return packageDocument{}, fmt.Errorf("read EPUB container descriptor: %w", err)
	}
	var container containerXML
	if err := xml.Unmarshal(containerData, &container); err != nil {
		return packageDocument{}, fmt.Errorf("parse EPUB container descriptor: %w", err)
	}
	packagePath := ""
	for _, rootfile := range container.Rootfiles {
		if strings.TrimSpace(rootfile.FullPath) == "" {
			continue
		}
		if rootfile.MediaType == "" || rootfile.MediaType == packageMediaType {
			packagePath, err = canonicalArchivePath(rootfile.FullPath)
			if err != nil {
				return packageDocument{}, fmt.Errorf("invalid EPUB package path: %w", err)
			}
			break
		}
	}
	if packagePath == "" {
		return packageDocument{}, errors.New("EPUB container descriptor has no package document")
	}
	packageData, err := archive.read(packagePath)
	if err != nil {
		return packageDocument{}, fmt.Errorf("read EPUB package document: %w", err)
	}
	var parsed packageXML
	if err := xml.Unmarshal(packageData, &parsed); err != nil {
		return packageDocument{}, fmt.Errorf("parse EPUB package document: %w", err)
	}
	result := packageDocument{
		path:       packagePath,
		manifest:   make(map[string]manifestItem, len(parsed.Manifest.Items)),
		spineTOC:   strings.TrimSpace(parsed.Spine.TOC),
		title:      firstNonEmpty(parsed.Metadata.Titles),
		authors:    nonEmpty(parsed.Metadata.Creators),
		language:   firstNonEmpty(parsed.Metadata.Languages),
		identifier: firstNonEmpty(parsed.Metadata.Identifier),
	}
	for _, item := range parsed.Manifest.Items {
		id := strings.TrimSpace(item.ID)
		href := strings.TrimSpace(item.Href)
		if id == "" || href == "" {
			continue
		}
		resourcePath, _, err := resolveArchiveReference(packagePath, href)
		if err != nil {
			return packageDocument{}, fmt.Errorf("invalid EPUB manifest reference %q: %w", href, err)
		}
		if _, duplicate := result.manifest[id]; duplicate {
			return packageDocument{}, fmt.Errorf("EPUB package repeats manifest ID %q", id)
		}
		properties := make(map[string]bool)
		for _, property := range strings.Fields(item.Properties) {
			properties[strings.ToLower(property)] = true
		}
		parsedItem := manifestItem{
			id: id, path: resourcePath,
			mediaType:  strings.ToLower(strings.TrimSpace(item.MediaType)),
			properties: properties,
		}
		result.manifest[id] = parsedItem
		result.manifestList = append(result.manifestList, parsedItem)
	}
	for index, itemref := range parsed.Spine.Items {
		idref := strings.TrimSpace(itemref.IDRef)
		resource, exists := result.manifest[idref]
		if !exists {
			return packageDocument{}, fmt.Errorf("EPUB spine item %q is missing from the manifest", idref)
		}
		linear := !strings.EqualFold(strings.TrimSpace(itemref.Linear), "no")
		result.spine = append(result.spine, spineItem{manifestItem: resource, index: index, linear: linear})
	}
	if len(result.spine) == 0 {
		return packageDocument{}, errors.New("EPUB package has an empty reading spine")
	}
	return result, nil
}

func firstNonEmpty(values []string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}
