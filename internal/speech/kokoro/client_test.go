package kokoro

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/speech"
)

const helperArg = "--listen-kokoro-helper"

func TestClientUsesOneLongLivedWorker(t *testing.T) {
	cacheRoot := t.TempDir()
	client, err := Start(context.Background(), helperOptions(cacheRoot))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cacheRoot, "speech", "one.wav")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := client.Synthesize(context.Background(), speech.Request{
		Segment: segment.Segment{ID: "seg-1", Seq: 0, SpokenText: "Hello."},
		Voice:   "af_heart", Speed: 1, Output: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SegmentID != "seg-1" || result.Seq != 0 || result.Duration != time.Second {
		t.Fatalf("unexpected worker result: %#v", result)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerCommandEmbedsItsWorkerForInstalledBinaries(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	options := Options{Python: executable, CacheRoot: root}
	program, args, err := workerCommand(context.Background(), options, root)
	if err != nil {
		t.Fatal(err)
	}
	if program != executable || len(args) < 2 || args[1] != filepath.Join(root, "worker", "kokoro-worker-v1.py") {
		t.Fatalf("worker command = %q %#v", program, args)
	}
	contents, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "Long-lived JSONL inference worker") {
		t.Fatal("installed worker script does not contain the embedded runtime")
	}
	if err := os.WriteFile(args[1], []byte("outdated worker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := workerCommand(context.Background(), options, root); err != nil {
		t.Fatal(err)
	}
	contents, err = os.ReadFile(args[1])
	if err != nil || !strings.Contains(string(contents), "Long-lived JSONL inference worker") {
		t.Fatalf("embedded worker was not updated atomically: err=%v content=%q", err, contents)
	}
}

func TestWorkerCommandRequiresTheManagedRuntimeByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("LISTEN_PYTHON", "")
	_, _, err := workerCommand(context.Background(), Options{CacheRoot: t.TempDir()}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "run `listen setup`") {
		t.Fatalf("worker command error = %v, want managed runtime setup guidance", err)
	}
}

func TestClientRejectsMalformedResponse(t *testing.T) {
	client, err := Start(context.Background(), helperOptions(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Synthesize(context.Background(), speech.Request{
		Segment: segment.Segment{ID: "bad", SpokenText: "malformed"},
		Voice:   "af_heart", Speed: 1, Output: filepath.Join(t.TempDir(), "bad.wav"),
	})
	if err == nil || !strings.Contains(err.Error(), "decode synthesis response") {
		t.Fatalf("got %v, want malformed-response error", err)
	}
	_ = client.Close()
}

func TestClientCancellationTerminatesWorker(t *testing.T) {
	client, err := Start(context.Background(), helperOptions(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = client.Synthesize(ctx, speech.Request{
		Segment: segment.Segment{ID: "wait", SpokenText: "block"},
		Voice:   "af_heart", Speed: 1, Output: filepath.Join(t.TempDir(), "blocked.wav"),
	})
	if err != context.DeadlineExceeded {
		t.Fatalf("got %v, want deadline exceeded", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
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
// test binary so protocol tests do not need Python, Kokoro, or network access.
func TestClientWorkerHelperProcess(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), helperArg) {
		return
	}
	write := func(value any) {
		_ = json.NewEncoder(os.Stdout).Encode(value)
	}
	write(map[string]any{"type": "ready", "protocol": protocolVersion, "engine": "kokoro", "sample_rate": 24000})
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			write(map[string]any{"type": "error", "code": "bad_json", "message": err.Error()})
			continue
		}
		if request["type"] == "shutdown" {
			os.Exit(0)
		}
		if request["text"] == "block" {
			for {
				time.Sleep(time.Second)
			}
		}
		if request["text"] == "malformed" {
			_, _ = fmt.Fprintln(os.Stdout, "not-json")
			continue
		}
		path, _ := request["output"].(string)
		if err := writePCM(path); err != nil {
			write(map[string]any{"type": "error", "request_id": request["request_id"], "code": "write_failed", "message": err.Error()})
			continue
		}
		seq, _ := request["seq"].(float64)
		write(map[string]any{
			"type": "synthesized", "request_id": request["request_id"],
			"segment_id": request["segment_id"], "seq": uint64(seq),
			"path": path, "duration_ms": 1000,
		})
	}
	os.Exit(0)
}

func writePCM(path string) error {
	const frames = uint32(24000)
	dataBytes := frames * 2
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	var header [44]byte
	copy(header[:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], 36+dataBytes)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], 24000)
	binary.LittleEndian.PutUint32(header[28:32], 48000)
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataBytes)
	_, writeErr := file.Write(header[:])
	if writeErr == nil {
		_, writeErr = file.Write(make([]byte, dataBytes))
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return writeErr
}
