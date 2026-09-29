package pdf

import (
	"context"
	"os/exec"
	"strings"

	"github.com/prettyletto/listen/internal/domain"
)

type pdfInfo struct {
	metadata domain.Metadata
	tagged   bool
}

func readPDFInfo(ctx context.Context, run commandRunner, path string) pdfInfo {
	if _, err := exec.LookPath("pdfinfo"); err != nil {
		return pdfInfo{}
	}
	data, err := run(ctx, "pdfinfo", "-enc", "UTF-8", path)
	if err != nil || ctx.Err() != nil {
		return pdfInfo{}
	}
	var result pdfInfo
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "title":
			if !strings.EqualFold(value, "(none)") && !strings.EqualFold(value, "untitled") {
				result.metadata.Title = value
			}
		case "author":
			if value != "" && !strings.EqualFold(value, "(none)") {
				result.metadata.Authors = []string{value}
			}
		case "tagged":
			result.tagged = strings.EqualFold(value, "yes")
		}
	}
	return result
}
