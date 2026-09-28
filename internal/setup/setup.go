// Package setup manages listen's private Kokoro runtime and its first-run check.
package setup

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/prettyletto/listen/internal/atomicfile"
	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
)

const PythonVersion = "3.13"
const KokoroVersion = "0.9.4"

//go:embed requirements.lock
var files embed.FS

type CommandRunner interface {
	Run(context.Context, string, []string, []string, io.Writer, io.Writer) error
}

type CommandRunnerFunc func(context.Context, string, []string, []string, io.Writer, io.Writer) error

func (run CommandRunnerFunc) Run(ctx context.Context, program string, args, env []string, stdout, stderr io.Writer) error {
	return run(ctx, program, args, env, stdout, stderr)
}

type Manager struct {
	Program string
	Runner  CommandRunner
	Stdout  io.Writer
	Stderr  io.Writer
}

func RuntimeRoot(userPaths paths.Paths) string {
	return filepath.Join(userPaths.DataDir, "runtime")
}

func PythonPath(userPaths paths.Paths) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(RuntimeRoot(userPaths), "venv", "Scripts", "python.exe")
	}
	return filepath.Join(RuntimeRoot(userPaths), "venv", "bin", "python")
}

func (manager Manager) Provision(ctx context.Context, userPaths paths.Paths) error {
	if ctx == nil {
		return errors.New("runtime setup requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("managed speech setup currently supports Linux x86_64; found %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if userPaths.DataDir == "" || userPaths.CacheDir == "" {
		return errors.New("runtime setup requires XDG data and cache directories")
	}
	root := RuntimeRoot(userPaths)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create managed runtime directory: %w", err)
	}
	requirements, err := files.ReadFile("requirements.lock")
	if err != nil {
		return fmt.Errorf("read locked Kokoro dependencies: %w", err)
	}
	requirementsPath := filepath.Join(root, "requirements.lock")
	if err := atomicfile.Write(ctx, requirementsPath, requirements, 0o600); err != nil {
		return fmt.Errorf("write locked Kokoro dependencies: %w", err)
	}
	pythonInstallDir := filepath.Join(root, "python")
	venvPath := filepath.Join(root, "venv")
	python := PythonPath(userPaths)
	program := manager.Program
	if program == "" {
		program = "uv"
	}
	env := []string{
		"UV_PYTHON_INSTALL_DIR=" + pythonInstallDir,
		"UV_PYTHON_PREFERENCE=only-managed",
		"UV_CACHE_DIR=" + filepath.Join(userPaths.CacheDir, "uv"),
	}
	runner := manager.runner()
	stdout, stderr := manager.streams()
	commands := [][]string{
		{"python", "install", PythonVersion, "--install-dir", pythonInstallDir},
		{"venv", "--managed-python", "--python", PythonVersion, venvPath},
		{"pip", "sync", "--python", python, "--index", "https://download.pytorch.org/whl/cpu", "--require-hashes", "--strict", requirementsPath},
	}
	for _, args := range commands {
		if err := runner.Run(ctx, program, args, env, stdout, stderr); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("run %s %s: %w", program, strings.Join(args, " "), err)
		}
	}
	return nil
}

func (manager Manager) Verify(ctx context.Context, userPaths paths.Paths) (string, error) {
	if ctx == nil {
		return "", errors.New("runtime check requires a context")
	}
	python := PythonPath(userPaths)
	if _, err := os.Stat(python); err != nil {
		return "", fmt.Errorf("managed Kokoro Python runtime is missing; run `listen setup`: %w", err)
	}
	var output strings.Builder
	if err := manager.runner().Run(ctx, python, []string{"-c", "import importlib.metadata as m; print(m.version('kokoro'))"}, nil, &output, manager.stderr()); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("managed Kokoro package check failed: %w", err)
	}
	version := strings.TrimSpace(output.String())
	if version == "" {
		return "", errors.New("managed Kokoro package check returned no version")
	}
	if version != KokoroVersion {
		return version, fmt.Errorf("managed Kokoro version is %s; listen requires %s", version, KokoroVersion)
	}
	return version, nil
}

func (manager Manager) runner() CommandRunner {
	if manager.Runner != nil {
		return manager.Runner
	}
	return systemCommandRunner{}
}

func (manager Manager) streams() (io.Writer, io.Writer) {
	stdout, stderr := manager.Stdout, manager.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return stdout, stderr
}

func (manager Manager) stderr() io.Writer {
	_, stderr := manager.streams()
	return stderr
}

type systemCommandRunner struct{}

func (systemCommandRunner) Run(ctx context.Context, program string, args, env []string, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, program, args...)
	if len(env) > 0 {
		command.Env = mergeEnvironment(os.Environ(), env)
	}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("command failed: %w", err)
	}
	return nil
}

func mergeEnvironment(base, overrides []string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	order := make([]string, 0, len(base)+len(overrides))
	for _, entry := range append(append([]string(nil), base...), overrides...) {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	result := make([]string, 0, len(values))
	for _, key := range order {
		result = append(result, key+"="+values[key])
	}
	return result
}

type SynthesizerStarter func(context.Context, kokoro.Options) (speech.Synthesizer, error)

func HealthSynthesize(ctx context.Context, cacheRoot, voice string, start SynthesizerStarter) (err error) {
	if ctx == nil {
		return errors.New("speech health check requires a context")
	}
	if voice == "" {
		voice = "af_heart"
	}
	if start == nil {
		start = func(ctx context.Context, options kokoro.Options) (speech.Synthesizer, error) {
			return kokoro.Start(ctx, options)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	output := filepath.Join(cacheRoot, "setup", "health-check.wav")
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return fmt.Errorf("prepare local speech check: %w", err)
	}
	defer func() {
		removeErr := os.Remove(output)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && err == nil {
			err = fmt.Errorf("remove speech health sample: %w", removeErr)
		}
	}()
	client, err := start(ctx, kokoro.Options{CacheRoot: cacheRoot, Voice: voice})
	if err != nil {
		return fmt.Errorf("start local speech worker: %w", err)
	}
	if client == nil {
		return errors.New("local speech worker did not start")
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close local speech worker: %w", closeErr)
		}
	}()
	result, err := client.Synthesize(ctx, speech.Request{
		Segment: segment.Segment{ID: "setup-health-check", SpokenText: "Listen is ready."},
		Voice:   voice, Speed: 1, Output: output,
	})
	if err != nil {
		return fmt.Errorf("synthesize local speech health sample: %w", err)
	}
	if result.SegmentID != "setup-health-check" || result.Path != output || result.Duration <= 0 {
		return errors.New("local speech worker returned an invalid health sample")
	}
	if _, err := speech.ValidateWAV(output); err != nil {
		return fmt.Errorf("validate local speech health sample: %w", err)
	}
	return nil
}
