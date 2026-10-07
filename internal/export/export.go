package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/prettyletto/listen/internal/atomicfile"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/speech"
)

const manifestVersion = 1

type Runner interface {
	Run(context.Context, string, ...string) error
}

type Options struct {
	Root           string
	SourcePath     string
	DocumentID     string
	TargetLanguage string
	Title          string
	Authors        []string
	Voice          string
	Speed          float64
	Plan           segment.Plan
	Audio          []speech.Result
	FFmpeg         string
	Runner         Runner
	OnProgress     func(Progress)
}

type ProgressStage uint8

const (
	ProgressUsingExistingBuild ProgressStage = iota + 1
	ProgressFullBookMP3
	ProgressChapterMP3
	ProgressFullBookM4B
	ProgressFinalizing
)

type Progress struct {
	Stage        ProgressStage
	ChapterIndex int
	ChapterCount int
}

type Result struct {
	Directory string
	BuildID   string
	FullMP3   string
	FullM4B   string
	Playlist  string
	Chapters  int
}

type Manifest struct {
	Version        int       `json:"version"`
	BuildID        string    `json:"build_id"`
	DocumentID     string    `json:"document_id"`
	TargetLanguage string    `json:"target_language,omitempty"`
	SourcePath     string    `json:"source_path,omitempty"`
	Title          string    `json:"title"`
	Authors        []string  `json:"authors,omitempty"`
	Voice          string    `json:"voice"`
	Speed          float64   `json:"speed"`
	FullMP3        string    `json:"full_mp3"`
	FullM4B        string    `json:"full_m4b"`
	Playlist       string    `json:"playlist"`
	Chapters       []Chapter `json:"chapters"`
	BuiltAt        time.Time `json:"built_at"`
}

type Chapter struct {
	Title       string        `json:"title"`
	File        string        `json:"file"`
	FirstSeq    uint64        `json:"first_seq"`
	LastSeq     uint64        `json:"last_seq"`
	Duration    time.Duration `json:"duration_ns"`
	StartOffset time.Duration `json:"start_offset_ns"`
}

type Current struct {
	Version int    `json:"version"`
	BuildID string `json:"build_id"`
	Path    string `json:"path"`
}

type audioGroup struct {
	id       string
	title    string
	items    []speech.Result
	first    uint64
	last     uint64
	duration time.Duration
}

func Build(ctx context.Context, options Options) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("export requires a context")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(options.Root) == "" {
		return Result{}, errors.New("export directory is empty")
	}
	if len(options.Plan.Segments) == 0 || len(options.Plan.Segments) != len(options.Audio) {
		return Result{}, errors.New("export requires ordered audio for every segment")
	}
	if options.Title == "" {
		options.Title = "Untitled audiobook"
	}
	if options.Speed <= 0 {
		options.Speed = 1
	}
	for index, item := range options.Plan.Segments {
		if index > 0 && item.Seq != options.Plan.Segments[index-1].Seq+1 {
			return Result{}, fmt.Errorf("export plan is not contiguous at segment %d", item.Seq)
		}
		result := options.Audio[index]
		if result.Seq != item.Seq || result.SegmentID != item.ID || result.Path == "" {
			return Result{}, fmt.Errorf("export audio does not match segment %d in the plan", item.Seq)
		}
		if result.Duration <= 0 {
			return Result{}, fmt.Errorf("export audio for segment %d has no duration", item.Seq)
		}
		if !filepath.IsAbs(result.Path) {
			return Result{}, fmt.Errorf("export audio for segment %d is not a local file", item.Seq)
		}
		info, err := os.Stat(result.Path)
		if err != nil {
			return Result{}, fmt.Errorf("read cached audio for segment %d: %w", item.Seq, err)
		}
		if !info.Mode().IsRegular() {
			return Result{}, fmt.Errorf("cached audio for segment %d is not a regular file", item.Seq)
		}
		if index == 0 {
			itemTarget := strings.TrimSpace(item.TargetLang)
			if options.TargetLanguage != "" && options.TargetLanguage != itemTarget {
				return Result{}, errors.New("export target language does not match its segments")
			}
			options.TargetLanguage = itemTarget
		} else if strings.TrimSpace(item.TargetLang) != options.TargetLanguage {
			return Result{}, errors.New("export segments do not share one target language")
		}
	}

	root, err := filepath.Abs(options.Root)
	if err != nil {
		return Result{}, fmt.Errorf("resolve export directory: %w", err)
	}
	if options.SourcePath != "" {
		sourcePath, err := filepath.Abs(options.SourcePath)
		if err != nil {
			return Result{}, fmt.Errorf("resolve source path: %w", err)
		}
		if filepath.Clean(root) == filepath.Clean(sourcePath) {
			return Result{}, errors.New("export directory cannot overwrite the source file")
		}
		options.SourcePath = sourcePath
	}
	buildID := buildID(options)
	buildsDir := filepath.Join(root, "builds")
	finalDir := filepath.Join(buildsDir, buildID)
	if _, err := os.Lstat(finalDir); err == nil {
		existing, err := loadCompleteBuild(finalDir)
		if err != nil {
			return Result{}, err
		}
		if existing.BuildID != buildID {
			return Result{}, errors.New("existing export build identity does not match its directory")
		}
		reportProgress(options, Progress{Stage: ProgressUsingExistingBuild})
		return publishCurrent(ctx, root, existing)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("inspect existing export build: %w", err)
	}
	if err := os.MkdirAll(buildsDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create export directory: %w", err)
	}
	stageDir, err := os.MkdirTemp(buildsDir, "."+buildID+".partial-")
	if err != nil {
		return Result{}, fmt.Errorf("create temporary export build: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stageDir)
		}
	}()

	program := options.FFmpeg
	if program == "" {
		program = "ffmpeg"
	}
	runner := options.Runner
	if runner == nil {
		resolved, err := exec.LookPath(program)
		if err != nil {
			return Result{}, fmt.Errorf("find ffmpeg: %w", err)
		}
		program = resolved
		runner = execRunner{}
	}
	groups := groupAudio(options.Title, options.Plan.Segments, options.Audio)
	if len(groups) == 0 {
		return Result{}, errors.New("export has no chapter audio")
	}
	chaptersDir := filepath.Join(stageDir, "chapters")
	if err := os.MkdirAll(chaptersDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create chapter export directory: %w", err)
	}
	allAudio := resultPaths(options.Audio)
	allList := filepath.Join(stageDir, "all-audio.txt")
	if err := writeConcatList(ctx, allList, allAudio); err != nil {
		return Result{}, err
	}
	fullMP3 := "full-book.mp3"
	reportProgress(options, Progress{Stage: ProgressFullBookMP3})
	if err := encodeMP3(ctx, runner, program, allList, filepath.Join(stageDir, fullMP3), options.Title, options.Authors); err != nil {
		return Result{}, fmt.Errorf("create full-book MP3: %w", err)
	}
	chapters := make([]Chapter, 0, len(groups))
	var bookOffset time.Duration
	playlist := strings.Builder{}
	playlist.WriteString("#EXTM3U\n")
	for index, group := range groups {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		reportProgress(options, Progress{
			Stage: ProgressChapterMP3, ChapterIndex: index + 1, ChapterCount: len(groups),
		})
		filename := fmt.Sprintf("%02d-%s.mp3", index+1, slug(group.title))
		relativePath := filepath.ToSlash(filepath.Join("chapters", filename))
		listPath := filepath.Join(stageDir, fmt.Sprintf("chapter-%02d-audio.txt", index+1))
		if err := writeConcatList(ctx, listPath, resultPaths(group.items)); err != nil {
			return Result{}, err
		}
		if err := encodeMP3(ctx, runner, program, listPath, filepath.Join(chaptersDir, filename), group.title, options.Authors); err != nil {
			return Result{}, fmt.Errorf("create MP3 for %s: %w", group.title, err)
		}
		chapters = append(chapters, Chapter{
			Title: group.title, File: relativePath, FirstSeq: group.first,
			LastSeq: group.last, Duration: group.duration, StartOffset: bookOffset,
		})
		fmt.Fprintf(&playlist, "#EXTINF:%d,%s\n%s\n", int(group.duration.Seconds()), m3uTitle(group.title), relativePath)
		bookOffset += group.duration
	}
	if err := atomicfile.Write(ctx, filepath.Join(stageDir, "all-chapters.m3u"), []byte(playlist.String()), 0o600); err != nil {
		return Result{}, fmt.Errorf("write chapter playlist: %w", err)
	}
	metadataPath := filepath.Join(stageDir, "chapters.ffmeta")
	if err := writeChapterMetadata(ctx, metadataPath, options.Title, chapters); err != nil {
		return Result{}, err
	}
	fullM4B := "full-book.m4b"
	reportProgress(options, Progress{Stage: ProgressFullBookM4B})
	if err := encodeM4B(ctx, runner, program, allList, metadataPath, filepath.Join(stageDir, fullM4B), options.Title, options.Authors); err != nil {
		return Result{}, fmt.Errorf("create full-book M4B: %w", err)
	}
	reportProgress(options, Progress{Stage: ProgressFinalizing})
	for _, output := range append([]string{filepath.Join(stageDir, fullMP3), filepath.Join(stageDir, fullM4B)}, chapterOutputPaths(stageDir, groups)...) {
		if err := syncFile(output); err != nil {
			return Result{}, fmt.Errorf("sync completed export audio: %w", err)
		}
	}
	for _, temporary := range []string{allList, metadataPath} {
		if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("remove temporary export input: %w", err)
		}
	}
	for index := range groups {
		if err := os.Remove(filepath.Join(stageDir, fmt.Sprintf("chapter-%02d-audio.txt", index+1))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Result{}, fmt.Errorf("remove temporary chapter input: %w", err)
		}
	}
	manifest := Manifest{
		Version: manifestVersion, BuildID: buildID, DocumentID: options.DocumentID,
		TargetLanguage: options.TargetLanguage,
		SourcePath:     options.SourcePath, Title: options.Title, Authors: append([]string(nil), options.Authors...),
		Voice: options.Voice, Speed: options.Speed, FullMP3: fullMP3, FullM4B: fullM4B,
		Playlist: "all-chapters.m3u", Chapters: chapters, BuiltAt: time.Now().UTC(),
	}
	if err := atomicfile.WriteJSON(ctx, filepath.Join(stageDir, "manifest.json"), manifest, 0o600); err != nil {
		return Result{}, fmt.Errorf("write export manifest: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := os.Rename(stageDir, finalDir); err != nil {
		return Result{}, fmt.Errorf("commit export build: %w", err)
	}
	committed = true
	if err := syncDirectory(buildsDir); err != nil {
		return Result{}, fmt.Errorf("sync export build directory: %w", err)
	}
	return publishCurrent(ctx, root, Result{
		Directory: finalDir, BuildID: buildID,
		FullMP3: filepath.Join(finalDir, fullMP3), FullM4B: filepath.Join(finalDir, fullM4B),
		Playlist: filepath.Join(finalDir, manifest.Playlist), Chapters: len(chapters),
	})
}

func reportProgress(options Options, progress Progress) {
	if options.OnProgress != nil {
		options.OnProgress(progress)
	}
}

func chapterOutputPaths(stageDir string, groups []audioGroup) []string {
	paths := make([]string, len(groups))
	for index, group := range groups {
		filename := fmt.Sprintf("%02d-%s.mp3", index+1, slug(group.title))
		paths[index] = filepath.Join(stageDir, "chapters", filename)
	}
	return paths
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func buildID(options Options) string {
	segments := make([]string, len(options.Plan.Segments))
	audioPaths := make([]string, len(options.Audio))
	for index, item := range options.Plan.Segments {
		segments[index] = item.ID
		audioPaths[index] = options.Audio[index].Path
	}
	data, _ := json.Marshal(struct {
		Version                                  int
		DocumentID, TargetLanguage, Title, Voice string
		Speed                                    float64
		Authors                                  []string
		Segments, AudioPaths                     []string
	}{manifestVersion, options.DocumentID, options.TargetLanguage, options.Title, options.Voice, options.Speed, options.Authors, segments, audioPaths})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:12])
}

func publishCurrent(ctx context.Context, root string, result Result) (Result, error) {
	current := Current{Version: manifestVersion, BuildID: result.BuildID, Path: filepath.ToSlash(filepath.Join("builds", result.BuildID))}
	if err := atomicfile.WriteJSON(ctx, filepath.Join(root, "current.json"), current, 0o600); err != nil {
		return Result{}, fmt.Errorf("update current audiobook: %w", err)
	}
	return result, nil
}

func loadCompleteBuild(path string) (Result, error) {
	data, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if err != nil {
		return Result{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Result{}, fmt.Errorf("read existing export manifest: %w", err)
	}
	if manifest.Version != manifestVersion || manifest.BuildID == "" || manifest.FullMP3 == "" || manifest.FullM4B == "" || manifest.Playlist == "" {
		return Result{}, errors.New("existing export build manifest is incomplete")
	}
	for _, relative := range []string{manifest.FullMP3, manifest.FullM4B, manifest.Playlist} {
		fullPath, err := safeBuildPath(path, relative)
		if err != nil {
			return Result{}, err
		}
		if !fileExists(fullPath) {
			return Result{}, fmt.Errorf("existing export build is missing %s", relative)
		}
	}
	for _, chapter := range manifest.Chapters {
		fullPath, err := safeBuildPath(path, chapter.File)
		if err != nil {
			return Result{}, err
		}
		if !fileExists(fullPath) {
			return Result{}, fmt.Errorf("existing export build is missing chapter %s", chapter.File)
		}
	}
	return Result{
		Directory: path, BuildID: manifest.BuildID,
		FullMP3: filepath.Join(path, filepath.Clean(manifest.FullMP3)), FullM4B: filepath.Join(path, filepath.Clean(manifest.FullM4B)),
		Playlist: filepath.Join(path, filepath.Clean(manifest.Playlist)), Chapters: len(manifest.Chapters),
	}, nil
}

func safeBuildPath(root, relative string) (string, error) {
	clean := filepath.Clean(relative)
	if relative == "" || filepath.IsAbs(relative) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("existing export manifest contains an unsafe path")
	}
	return filepath.Join(root, clean), nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func groupAudio(documentTitle string, plan []segment.Segment, audio []speech.Result) []audioGroup {
	var groups []audioGroup
	for index, item := range plan {
		key := item.MajorUnitID
		title := strings.TrimSpace(documentTitle)
		if len(item.SectionPath) > 0 {
			title = strings.TrimSpace(item.SectionPath[len(item.SectionPath)-1])
		}
		if key == "" {
			key = strings.Join(item.SectionPath, "\x00")
		}
		if key == "" {
			key = "document"
		}
		if len(groups) == 0 || groups[len(groups)-1].id != key {
			if title == "" {
				title = fmt.Sprintf("Chapter %d", len(groups)+1)
			}
			groups = append(groups, audioGroup{id: key, title: title, first: item.Seq, last: item.Seq})
		}
		group := &groups[len(groups)-1]
		group.items = append(group.items, audio[index])
		group.last = item.Seq
		group.duration += audio[index].Duration
	}
	return groups
}

func resultPaths(results []speech.Result) []string {
	paths := make([]string, len(results))
	for index, item := range results {
		paths[index] = item.Path
	}
	return paths
}

func slug(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
			lastDash = false
			continue
		}
		if builder.Len() > 0 && !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "chapter"
	}
	runes := []rune(result)
	if len(runes) > 60 {
		result = string(runes[:60])
	}
	return strings.Trim(result, "-")
}

func m3uTitle(title string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(title)
}
