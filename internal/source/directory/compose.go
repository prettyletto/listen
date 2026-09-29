package directory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/prettyletto/listen/internal/domain"
)

func compose(root string, files []parsedFile) (*domain.Document, error) {
	identity := struct {
		Version string `json:"version"`
		Files   []struct {
			Path string `json:"path"`
			ID   string `json:"id"`
		} `json:"files"`
	}{Version: parserVersion}
	for _, file := range files {
		if file.document == nil {
			return nil, fmt.Errorf("documentation file %q produced no document", file.relative)
		}
		identity.Files = append(identity.Files, struct {
			Path string `json:"path"`
			ID   string `json:"id"`
		}{Path: file.relative, ID: file.document.ID})
	}
	identityBytes, err := json.Marshal(identity)
	if err != nil {
		return nil, fmt.Errorf("identify documentation session: %w", err)
	}
	digest := sha256.Sum256(identityBytes)
	title := filepath.Base(root)
	if title == "" || title == "." || title == string(filepath.Separator) {
		title = "Documentation"
	}
	document := &domain.Document{
		ID:       hex.EncodeToString(digest[:]),
		Format:   domain.FormatDirectory,
		Metadata: domain.Metadata{Title: title},
	}
	var language string
	var mixedLanguages bool
	for _, file := range files {
		if candidate := strings.TrimSpace(file.document.Metadata.Language); candidate != "" {
			if language == "" {
				language = candidate
			} else if !strings.EqualFold(language, candidate) {
				mixedLanguages = true
			}
		}
	}
	if !mixedLanguages {
		document.Metadata.Language = language
	}

	for _, file := range files {
		base := uint64(len(document.Blocks))
		for index, block := range file.document.Blocks {
			block.ID = stableID(parserVersion, "block", file.relative, block.ID)
			block.Seq = base + uint64(index)
			document.Blocks = append(document.Blocks, block)
		}
		for _, warning := range file.document.Warnings {
			document.Warnings = append(document.Warnings, file.relative+": "+warning)
		}
		for _, navigation := range file.document.AuxiliaryNavigation {
			navigation.ID = stableID(parserVersion, "auxiliary-navigation", file.relative, navigation.ID)
			navigation.NavigationPath = append([]string(nil), navigation.NavigationPath...)
			document.AuxiliaryNavigation = append(document.AuxiliaryNavigation, navigation)
		}
		end := uint64(len(document.Blocks))
		if hasWholeBookRoot(file.document.Outline, uint64(len(file.document.Blocks))) {
			fileRoot := cloneOutline(file.document.Outline[0], 1, base, file.relative)
			fileRoot.Provenance = domain.StructureDirectory
			document.Outline = append(document.Outline, fileRoot)
			continue
		}
		fileTitle := strings.TrimSpace(file.document.Metadata.Title)
		if fileTitle == "" {
			fileTitle = strings.TrimSuffix(filepath.Base(file.relative), filepath.Ext(file.relative))
		}
		wrapper := &domain.OutlineNode{
			ID:                stableID(parserVersion, "file", file.relative, file.document.ID),
			Title:             fileTitle,
			Role:              domain.RoleSection,
			Depth:             1,
			StartBlock:        base,
			EndBlockExclusive: end,
			Confidence:        domain.ConfidenceHigh,
			Provenance:        domain.StructureDirectory,
		}
		if len(file.document.Blocks) > 0 {
			wrapper.SourceTarget = file.document.Blocks[0].Source
		}
		for _, node := range file.document.Outline {
			wrapper.Children = append(wrapper.Children, cloneOutline(node, 2, base, file.relative))
		}
		document.Outline = append(document.Outline, wrapper)
	}
	if len(document.Blocks) == 0 {
		return nil, ErrNoDocuments
	}
	document.Outline = []*domain.OutlineNode{{
		ID:                stableID(parserVersion, "root", document.ID),
		Title:             title,
		Role:              domain.RoleBook,
		Depth:             0,
		StartBlock:        0,
		EndBlockExclusive: uint64(len(document.Blocks)),
		Confidence:        domain.ConfidenceHigh,
		Provenance:        domain.StructureDirectory,
		Children:          document.Outline,
	}}
	return document, nil
}

func hasWholeBookRoot(nodes []*domain.OutlineNode, blockCount uint64) bool {
	return len(nodes) == 1 && nodes[0] != nil && nodes[0].Role == domain.RoleBook && nodes[0].StartBlock == 0 && nodes[0].EndBlockExclusive == blockCount
}

func cloneOutline(node *domain.OutlineNode, depthOffset int, blockOffset uint64, relativePath string) *domain.OutlineNode {
	if node == nil {
		return nil
	}
	clone := *node
	clone.ID = stableID(parserVersion, "outline", relativePath, node.ID)
	clone.Depth += depthOffset
	clone.StartBlock += blockOffset
	clone.EndBlockExclusive += blockOffset
	clone.Children = nil
	for _, child := range node.Children {
		clone.Children = append(clone.Children, cloneOutline(child, depthOffset, blockOffset, relativePath))
	}
	return &clone
}

func stableID(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte{byte(len(part) >> 24), byte(len(part) >> 16), byte(len(part) >> 8), byte(len(part))})
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}
