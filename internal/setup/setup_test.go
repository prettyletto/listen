package setup

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
)

func TestProvisionUsesPrivateManagedPythonAndLockedWorkerPackages(t *testing.T) {
	userPaths := paths.Paths{DataDir: filepath.Join(t.TempDir(), "data"), CacheDir: filepath.Join(t.TempDir(), "cache")}
	runner := &recordingRunner{}
	manager := Manager{Runner: runner}
	if err := manager.Provision(context.Background(), userPaths); err != nil {
		t.Fatal(err)
	}
	if err := manager.Provision(context.Background(), userPaths); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 5 {
		t.Fatalf("setup command calls = %d, want 5 across first setup and repeat setup", len(runner.calls))
	}
	if !reflect.DeepEqual(runner.calls[0].args[:3], []string{"python", "install", PythonVersion}) || runner.calls[1].args[0] != "venv" || !reflect.DeepEqual(runner.calls[2].args[:2], []string{"pip", "sync"}) {
		t.Fatalf("runtime setup command order = %#v", runner.calls)
	}
	if runner.calls[3].args[0] != "python" || !reflect.DeepEqual(runner.calls[4].args[:2], []string{"pip", "sync"}) {
		t.Fatalf("repeat setup recreated the existing virtual environment: %#v", runner.calls[3:])
	}
	if !contains(runner.calls[1].args, "--managed-python") || !contains(runner.calls[2].args, "https://download.pytorch.org/whl/cpu") || !contains(runner.calls[2].args, "--require-hashes") || !contains(runner.calls[2].args, "--strict") {
		t.Fatalf("uv environment setup did not select managed Python and hash-verified CPU dependencies: %#v", runner.calls)
	}
	for _, call := range runner.calls {
		if contains(call.env, "UV_PYTHON_PREFERENCE=only-managed") || !contains(call.env, "UV_PYTHON_INSTALL_DIR="+filepath.Join(RuntimeRoot(userPaths), "python")) {
			t.Fatalf("uv environment not scoped to listen runtime: %#v", call.env)
		}
	}
	requirements, err := os.ReadFile(filepath.Join(RuntimeRoot(userPaths), "requirements.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(requirements), "kokoro=="+KokoroVersion) || !strings.Contains(string(requirements), "en_core_web_sm-3.8.0") || !strings.Contains(string(requirements), "torch==2.14.0+cpu") {
		t.Fatalf("runtime lock is missing pinned Kokoro, G2P, or CPU PyTorch packages")
	}
}

func TestProvisionClearsAnIncompleteExistingVirtualEnvironment(t *testing.T) {
	userPaths := paths.Paths{DataDir: filepath.Join(t.TempDir(), "data"), CacheDir: filepath.Join(t.TempDir(), "cache")}
	venvPath := filepath.Join(RuntimeRoot(userPaths), "venv")
	if err := os.MkdirAll(venvPath, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	if err := (Manager{Runner: runner}).Provision(context.Background(), userPaths); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) < 2 || runner.calls[1].args[0] != "venv" || !contains(runner.calls[1].args, "--clear") {
		t.Fatalf("incomplete virtual environment was not cleared for repair: %#v", runner.calls)
	}
}

func TestRequirementsLockPinsAndHashesCompleteDependencySet(t *testing.T) {
	lock, err := files.ReadFile("requirements.lock")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(lock), "\n")
	packageCount := 0
	for i, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		if !strings.Contains(line, "==") && !strings.Contains(line, " @ ") {
			t.Fatalf("unlocked requirement at line %d: %q", i+1, line)
		}
		packageCount++
		if !strings.HasSuffix(strings.TrimSpace(line), "\\") {
			t.Fatalf("requirement at line %d has no continuation to its hash: %q", i+1, line)
		}
		hasHash := false
		for _, hashLine := range lines[i+1:] {
			if !strings.HasPrefix(hashLine, "    --hash=sha256:") {
				break
			}
			hasHash = true
		}
		if !hasHash {
			t.Fatalf("requirement at line %d has no SHA-256 hash: %q", i+1, line)
		}
	}
	if packageCount < 80 {
		t.Fatalf("dependency lock only contains %d packages; expected the complete resolved runtime", packageCount)
	}
}

func TestProvisionHonorsCancellationBeforeStartingCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &recordingRunner{}
	err := (Manager{Runner: runner}).Provision(ctx, paths.Paths{DataDir: t.TempDir(), CacheDir: t.TempDir()})
	if !errors.Is(err, context.Canceled) || len(runner.calls) != 0 {
		t.Fatalf("provision err=%v calls=%d", err, len(runner.calls))
	}
}

func TestVerifyRequiresPinnedKokoroVersionInManagedEnvironment(t *testing.T) {
	userPaths := paths.Paths{DataDir: t.TempDir()}
	python := PythonPath(userPaths)
	if err := os.MkdirAll(filepath.Dir(python), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(python, []byte("managed python"), 0o700); err != nil {
		t.Fatal(err)
	}
	version, err := (Manager{Runner: versionRunner{version: KokoroVersion}}).Verify(context.Background(), userPaths)
	if err != nil || version != KokoroVersion {
		t.Fatalf("verify version=%q err=%v", version, err)
	}
	version, err = (Manager{Runner: versionRunner{version: "0.10.0"}}).Verify(context.Background(), userPaths)
	if err == nil || version != "0.10.0" {
		t.Fatalf("accepted an unpinned Kokoro version: version=%q err=%v", version, err)
	}
}

func TestHealthSynthesisChecksAndRemovesLocalSample(t *testing.T) {
	cacheRoot := t.TempDir()
	var seen speech.Request
	closed := false
	start := func(_ context.Context, options kokoro.Options) (speech.Synthesizer, error) {
		if options.CacheRoot != cacheRoot || options.Voice != "pf_dora" {
			t.Fatalf("health worker options = %#v", options)
		}
		return fakeSynth{onSynthesize: func(request speech.Request) (speech.Result, error) {
			seen = request
			if err := writeTestWAV(request.Output); err != nil {
				return speech.Result{}, err
			}
			return speech.Result{SegmentID: request.Segment.ID, Seq: request.Segment.Seq, Path: request.Output, Duration: time.Second}, nil
		}, onClose: func() error { closed = true; return nil }}, nil
	}
	if err := HealthSynthesize(context.Background(), cacheRoot, "pf_dora", start); err != nil {
		t.Fatal(err)
	}
	if seen.Segment.SpokenText != "Listen is ready." || seen.Voice != "pf_dora" || seen.Speed != 1 {
		t.Fatalf("health synthesis request = %#v", seen)
	}
	if !closed {
		t.Fatal("health worker was not shut down")
	}
	if _, err := os.Stat(seen.Output); !os.IsNotExist(err) {
		t.Fatalf("health sample was not removed: %v", err)
	}
}

type commandCall struct {
	program string
	args    []string
	env     []string
}

type recordingRunner struct{ calls []commandCall }

func (runner *recordingRunner) Run(_ context.Context, program string, args, env []string, _, _ io.Writer) error {
	runner.calls = append(runner.calls, commandCall{
		program: program,
		args:    append([]string(nil), args...),
		env:     append([]string(nil), env...),
	})
	if len(args) > 0 && args[0] == "venv" {
		python := filepath.Join(args[len(args)-1], "bin", "python")
		if err := os.MkdirAll(filepath.Dir(python), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(python, []byte("managed python"), 0o700); err != nil {
			return err
		}
	}
	return nil
}

type versionRunner struct{ version string }

func (runner versionRunner) Run(_ context.Context, _ string, _ []string, _ []string, stdout, _ io.Writer) error {
	_, err := io.WriteString(stdout, runner.version+"\n")
	return err
}

type fakeSynth struct {
	onSynthesize func(speech.Request) (speech.Result, error)
	onClose      func() error
}

func (synth fakeSynth) ID() string { return kokoro.EngineID }

func (synth fakeSynth) Synthesize(_ context.Context, request speech.Request) (speech.Result, error) {
	return synth.onSynthesize(request)
}

func (synth fakeSynth) Close() error {
	if synth.onClose == nil {
		return nil
	}
	return synth.onClose()
}

func writeTestWAV(path string) error {
	const sampleRate = uint32(24000)
	const frames = sampleRate
	dataBytes := frames * 2
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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
	binary.LittleEndian.PutUint32(header[24:28], sampleRate)
	binary.LittleEndian.PutUint32(header[28:32], sampleRate*2)
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataBytes)
	return os.WriteFile(path, append(header[:], make([]byte, dataBytes)...), 0o600)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
