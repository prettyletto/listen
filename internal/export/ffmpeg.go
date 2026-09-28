package export

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/prettyletto/listen/internal/atomicfile"
)

type execRunner struct{}

func (execRunner) Run(ctx context.Context, program string, args ...string) error {
	command := exec.CommandContext(ctx, program, args...)
	command.Stdout = io.Discard
	stderr := &limitedBuffer{limit: 8 * 1024}
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return fmt.Errorf("run ffmpeg: %w", err)
		}
		return fmt.Errorf("run ffmpeg: %w: %s", err, message)
	}
	return nil
}

type limitedBuffer struct {
	data  bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.data.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.data.Write(data)
	}
	return original, nil
}

func (b *limitedBuffer) String() string { return b.data.String() }

func writeConcatList(ctx context.Context, path string, inputPaths []string) error {
	var content strings.Builder
	for _, inputPath := range inputPaths {
		if strings.ContainsAny(inputPath, "\r\n") {
			return fmt.Errorf("audio path contains a line break and cannot be exported")
		}
		absolute, err := filepath.Abs(inputPath)
		if err != nil {
			return fmt.Errorf("resolve audio path: %w", err)
		}
		content.WriteString("file ")
		content.WriteString(concatQuote(absolute))
		content.WriteByte('\n')
	}
	return atomicfile.Write(ctx, path, []byte(content.String()), 0o600)
}

func concatQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "'", "'\\''")
	return "'" + value + "'"
}

func encodeMP3(ctx context.Context, runner Runner, program, concatPath, outputPath, title string, authors []string) error {
	args := []string{
		"-hide_banner", "-nostdin", "-v", "error", "-y", "-f", "concat", "-safe", "0", "-i", concatPath,
		"-map", "0:a:0", "-vn", "-c:a", "libmp3lame", "-q:a", "2", "-id3v2_version", "3",
		"-metadata", "title=" + metadataValue(title),
	}
	if len(authors) > 0 {
		args = append(args, "-metadata", "artist="+metadataValue(strings.Join(authors, ", ")))
	}
	args = append(args, "-f", "mp3", outputPath)
	if err := runner.Run(ctx, program, args...); err != nil {
		return err
	}
	return requireOutput(outputPath)
}

func encodeM4B(ctx context.Context, runner Runner, program, concatPath, metadataPath, outputPath, title string, authors []string) error {
	args := []string{
		"-hide_banner", "-nostdin", "-v", "error", "-y", "-f", "concat", "-safe", "0", "-i", concatPath,
		"-f", "ffmetadata", "-i", metadataPath, "-map", "0:a:0", "-map_metadata", "1", "-map_chapters", "1",
		"-vn", "-c:a", "aac", "-b:a", "96k", "-metadata", "title=" + metadataValue(title),
	}
	if len(authors) > 0 {
		args = append(args, "-metadata", "artist="+metadataValue(strings.Join(authors, ", ")))
	}
	args = append(args, "-f", "ipod", outputPath)
	if err := runner.Run(ctx, program, args...); err != nil {
		return err
	}
	return requireOutput(outputPath)
}

func writeChapterMetadata(ctx context.Context, path, bookTitle string, chapters []Chapter) error {
	var content strings.Builder
	content.WriteString(";FFMETADATA1\n")
	content.WriteString("title=")
	content.WriteString(escapeFFMetadata(bookTitle))
	content.WriteByte('\n')
	for _, chapter := range chapters {
		start := chapter.StartOffset.Milliseconds()
		end := (chapter.StartOffset + chapter.Duration).Milliseconds()
		if end <= start {
			end = start + 1
		}
		fmt.Fprintf(&content, "\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n", start, end, escapeFFMetadata(chapter.Title))
	}
	return atomicfile.Write(ctx, path, []byte(content.String()), 0o600)
}

func escapeFFMetadata(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.NewReplacer("=", "\\=", ";", "\\;", "#", "\\#", "\r", "", "\n", "\\n").Replace(value)
	return value
}

func metadataValue(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
}

func requireOutput(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("ffmpeg did not create the expected audio file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("ffmpeg produced an empty or invalid audio file")
	}
	return nil
}
