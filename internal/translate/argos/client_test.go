package argos

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/translate"
)

const helperArg = "--listen-argos-helper"

func TestClientUsesOneLongLivedWorkerAndPreservesSegmentIDs(t *testing.T) {
	client, err := Start(context.Background(), helperOptions(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := client.Capabilities(context.Background())
	if err != nil || !capabilities.Local || !capabilities.SupportsBatch || !capabilities.SupportsLanguagePair("en", "pt-BR") || capabilities.SupportsLanguagePair("fr", "pt-BR") {
		t.Fatalf("translator capabilities = %#v, err=%v", capabilities, err)
	}
	results, err := client.Translate(context.Background(), translate.Request{
		TargetLanguage: "pt-BR",
		Segments: []segment.Segment{
			{ID: "seg-1", SourceLang: "en", SourceText: "First."},
			{ID: "seg-2", SourceLang: "en-US", SourceText: "Second."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].SegmentID != "seg-2" || results[1].SegmentID != "seg-1" {
		t.Fatalf("worker results did not preserve returned IDs: %#v", results)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsUnsupportedPairBeforeWorkerCall(t *testing.T) {
	client, err := Start(context.Background(), helperOptions(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Translate(context.Background(), translate.Request{
		TargetLanguage: "pt-BR",
		Segments:       []segment.Segment{{ID: "seg-1", SourceLang: "fr", SourceText: "Bonjour."}},
	})
	if err == nil || !strings.Contains(err.Error(), LanguagePair) {
		t.Fatalf("unsupported pair error = %v", err)
	}
}

func TestClientCancellationTerminatesWorker(t *testing.T) {
	client, err := Start(context.Background(), helperOptions(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = client.Translate(ctx, translate.Request{
		TargetLanguage: "pt-BR",
		Segments:       []segment.Segment{{ID: "seg-1", SourceLang: "en", SourceText: "block"}},
	})
	if err != context.DeadlineExceeded {
		t.Fatalf("translation error = %v, want deadline exceeded", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsMalformedWorkerResponse(t *testing.T) {
	client, err := Start(context.Background(), helperOptions(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Translate(context.Background(), translate.Request{
		TargetLanguage: "pt-BR",
		Segments:       []segment.Segment{{ID: "seg-1", SourceLang: "en", SourceText: "malformed"}},
	})
	if err == nil || !strings.Contains(err.Error(), "decode local translation response") {
		t.Fatalf("malformed worker response error = %v", err)
	}
}

func TestWorkerCommandRequiresInstalledModelAndManagedRuntime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("LISTEN_PYTHON", "")
	userPaths, err := paths.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = workerCommand(context.Background(), Options{CacheRoot: userPaths.CacheDir}, userPaths)
	if err == nil || !strings.Contains(err.Error(), "listen setup --translate") {
		t.Fatalf("worker setup error = %v", err)
	}
}

func helperOptions(cacheRoot string) Options {
	return Options{
		Program:      os.Args[0],
		Args:         []string{"-test.run=TestClientWorkerHelperProcess", "--", helperArg},
		CacheRoot:    cacheRoot,
		ReadyTimeout: time.Second,
	}
}

// TestClientWorkerHelperProcess is a tiny fake JSONL worker invoked as a child
// test binary so protocol tests need no Python model or network access.
func TestClientWorkerHelperProcess(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), helperArg) {
		return
	}
	write := func(value any) { _ = json.NewEncoder(os.Stdout).Encode(value) }
	write(map[string]any{"type": "ready", "protocol": protocolVersion, "engine": BackendID})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 20*translate.MaxTranslationBytes)
	for scanner.Scan() {
		var request map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			write(map[string]any{"type": "error", "code": "bad_json", "message": err.Error()})
			continue
		}
		if request["type"] == "shutdown" {
			os.Exit(0)
		}
		segments, _ := request["segments"].([]any)
		if len(segments) > 0 {
			first, _ := segments[0].(map[string]any)
			if first["text"] == "block" {
				for {
					time.Sleep(time.Second)
				}
			}
			if first["text"] == "malformed" {
				_, _ = fmt.Fprintln(os.Stdout, "not-json")
				continue
			}
		}
		results := make([]map[string]any, 0, len(segments))
		for i := len(segments) - 1; i >= 0; i-- {
			item, _ := segments[i].(map[string]any)
			text, _ := item["text"].(string)
			id, _ := item["segment_id"].(string)
			results = append(results, map[string]any{"segment_id": id, "text": "pt-BR " + text})
		}
		write(map[string]any{"type": "translated", "request_id": request["request_id"], "segments": results})
	}
	os.Exit(0)
}
