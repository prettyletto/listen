package export

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/speech"
)

func TestBuildCreatesReusableAudiobookBundle(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	options := exportFixture(t)
	for index := range options.Plan.Segments {
		options.Plan.Segments[index].TargetLang = "pt-BR"
	}
	result, err := Build(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{result.FullMP3, result.FullM4B, result.Playlist, filepath.Join(result.Directory, "manifest.json"), filepath.Join(options.Root, "current.json")} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			t.Fatalf("expected non-empty export file %q, err=%v info=%v", path, err, info)
		}
	}
	if result.Chapters != 2 {
		t.Fatalf("exported %d chapters, want 2", result.Chapters)
	}
	manifestData, err := os.ReadFile(filepath.Join(result.Directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Chapters) != 2 || manifest.Chapters[0].Title != "Chapter 1" || manifest.Chapters[1].Title != "Chapter 2" {
		t.Fatalf("unexpected chapter manifest: %#v", manifest.Chapters)
	}
	if manifest.TargetLanguage != "pt-BR" {
		t.Fatalf("export manifest target language = %q, want pt-BR", manifest.TargetLanguage)
	}
	if ffprobe, err := exec.LookPath("ffprobe"); err == nil {
		output, err := exec.Command(ffprobe, "-v", "error", "-show_chapters", "-of", "json", result.FullM4B).Output()
		if err != nil {
			t.Fatalf("inspect M4B chapters: %v", err)
		}
		var probe struct {
			Chapters []json.RawMessage `json:"chapters"`
		}
		if err := json.Unmarshal(output, &probe); err != nil {
			t.Fatal(err)
		}
		if len(probe.Chapters) != 2 {
			t.Fatalf("M4B has %d chapters, want 2: %s", len(probe.Chapters), output)
		}
	}
	playlist, err := os.ReadFile(result.Playlist)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(playlist), "chapters/01-chapter-1.mp3") || strings.Contains(string(playlist), "../") {
		t.Fatalf("playlist paths are not safe and relative: %s", playlist)
	}

	again, err := Build(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if again.BuildID != result.BuildID || again.Directory != result.Directory {
		t.Fatalf("repeated export did not reuse the completed build: first=%#v again=%#v", result, again)
	}
}

func TestBuildIDIncludesTargetLanguage(t *testing.T) {
	options := exportFixture(t)
	english := buildID(options)
	options.TargetLanguage = "pt-BR"
	portuguese := buildID(options)
	if english == portuguese {
		t.Fatal("different audiobook target languages shared one build ID")
	}
}

func TestCanceledBuildPreservesCurrentManifestAndCleansStaging(t *testing.T) {
	options := exportFixture(t)
	initial, err := Build(context.Background(), options)
	if err != nil {
		if _, ffmpegErr := exec.LookPath("ffmpeg"); ffmpegErr != nil {
			t.Skip("ffmpeg is not installed")
		}
		t.Fatal(err)
	}
	currentPath := filepath.Join(options.Root, "current.json")
	before, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	options.Title = "A different build"
	options.Runner = waitRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err = Build(ctx, options)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled export error = %v, want context.Canceled", err)
	}
	after, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("canceled build replaced current manifest:\nbefore=%s\nafter=%s", before, after)
	}
	entries, err := os.ReadDir(filepath.Join(options.Root, "builds"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != initial.BuildID {
		t.Fatalf("canceled build left staging artifacts: %#v", entries)
	}
}

func TestBuildRejectsMismatchedAudioWithoutCreatingOutput(t *testing.T) {
	options := exportFixture(t)
	options.Audio[1].SegmentID = "wrong"
	if _, err := Build(context.Background(), options); err == nil {
		t.Fatal("accepted audio that does not match the segment plan")
	}
	if _, err := os.Stat(options.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid plan created export root: stat error=%v", err)
	}
}

func TestFFmpegRunnerStopsExternalProcessOnCancellation(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = (execRunner{}).Run(ctx, sleep, "30")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("external process error = %v, want context deadline", err)
	}
}

type waitRunner struct{}

func (waitRunner) Run(ctx context.Context, _ string, _ ...string) error {
	<-ctx.Done()
	return ctx.Err()
}

func exportFixture(t *testing.T) Options {
	t.Helper()
	base := t.TempDir()
	audioDir := filepath.Join(base, "audio's cache")
	if err := os.MkdirAll(audioDir, 0o700); err != nil {
		t.Fatal(err)
	}
	segments := []segment.Segment{
		{ID: "one", Seq: 0, MajorUnitID: "chapter-one", SectionPath: []string{"Book", "Chapter 1"}},
		{ID: "two", Seq: 1, MajorUnitID: "chapter-one", SectionPath: []string{"Book", "Chapter 1"}},
		{ID: "three", Seq: 2, MajorUnitID: "chapter-two", SectionPath: []string{"Book", "Chapter 2"}},
	}
	results := make([]speech.Result, len(segments))
	for index, item := range segments {
		path := filepath.Join(audioDir, item.ID+".wav")
		writeSilenceWAV(t, path)
		results[index] = speech.Result{SegmentID: item.ID, Seq: item.Seq, Path: path, Duration: 100 * time.Millisecond}
	}
	return Options{
		Root: filepath.Join(base, "book-audio"), SourcePath: filepath.Join(base, "book.epub"),
		DocumentID: "document-id", Title: "My Book", Authors: []string{"Author"}, Voice: "af_heart", Speed: 1,
		Plan: segment.Plan{DocumentID: "document-id", Segments: segments}, Audio: results,
	}
}

func writeSilenceWAV(t *testing.T, path string) {
	t.Helper()
	const sampleRate = uint32(24000)
	const frames = uint32(2400)
	dataBytes := frames * 2
	var header [44]byte
	copy(header[:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], 36+dataBytes)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], sampleRate)
	binary.LittleEndian.PutUint32(header[28:32], sampleRate*2)
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataBytes)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(make([]byte, dataBytes)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
