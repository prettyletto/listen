// Package kokoro implements the versioned, long-lived Kokoro worker protocol.
package kokoro

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/speech"
)

const protocolVersion = 1

type Options struct {
	Python       string
	WorkerPath   string
	CacheRoot    string
	Voice        string
	Stderr       io.Writer
	ReadyTimeout time.Duration

	// Program and Args are intended for protocol tests and packaged launchers.
	Program string
	Args    []string
}

type Client struct {
	ctx       context.Context
	cancel    context.CancelFunc
	command   *exec.Cmd
	stdin     io.WriteCloser
	lines     chan lineResult
	done      chan struct{}
	requestMu sync.Mutex
	requestID atomic.Uint64
	closeOnce sync.Once
	exitMu    sync.Mutex
	exitErr   error
}

const EngineID = "kokoro-82m-worker-v1"

type lineResult struct {
	line string
	err  error
}

type event struct {
	Type       string `json:"type"`
	Protocol   int    `json:"protocol"`
	Engine     string `json:"engine"`
	SampleRate int    `json:"sample_rate"`
	RequestID  string `json:"request_id"`
	SegmentID  string `json:"segment_id"`
	Seq        uint64 `json:"seq"`
	Path       string `json:"path"`
	DurationMS int64  `json:"duration_ms"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

type synthesizeRequest struct {
	Type      string  `json:"type"`
	RequestID string  `json:"request_id"`
	SegmentID string  `json:"segment_id"`
	Seq       uint64  `json:"seq"`
	Text      string  `json:"text"`
	Voice     string  `json:"voice"`
	Speed     float64 `json:"speed"`
	Output    string  `json:"output"`
}

func Start(ctx context.Context, options Options) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("Kokoro worker requires a context")
	}
	if options.CacheRoot == "" {
		return nil, errors.New("Kokoro worker requires a cache directory")
	}
	root, err := filepath.Abs(options.CacheRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve worker cache directory: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create worker cache directory: %w", err)
	}
	if options.Voice == "" {
		options.Voice = "af_heart"
	}
	program, args, err := workerCommand(ctx, options, root)
	if err != nil {
		return nil, err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(workerCtx, program, args...)
	command.Env = setEnvironment(os.Environ(),
		"HF_HOME="+filepath.Join(root, "huggingface"),
		"TORCH_HOME="+filepath.Join(root, "torch"),
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open Kokoro worker output: %w", err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open Kokoro worker input: %w", err)
	}
	if options.Stderr != nil {
		command.Stderr = options.Stderr
	} else {
		command.Stderr = io.Discard
	}
	if err := command.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start Kokoro worker: %w", err)
	}
	client := &Client{
		ctx: workerCtx, cancel: cancel, command: command, stdin: stdin,
		lines: make(chan lineResult, 8), done: make(chan struct{}),
	}
	go client.readStdout(stdout)
	go client.waitProcess()
	readyTimeout := options.ReadyTimeout
	if readyTimeout <= 0 {
		readyTimeout = 3 * time.Minute
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, readyTimeout)
	defer readyCancel()
	line, err := client.readLine(readyCtx)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("wait for Kokoro worker readiness: %w", err)
	}
	var ready event
	if err := json.Unmarshal([]byte(line), &ready); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("decode Kokoro worker readiness: %w", err)
	}
	if ready.Type == "startup_error" {
		_ = client.Close()
		return nil, fmt.Errorf("Kokoro worker startup failed (%s): %s", ready.Code, ready.Message)
	}
	if ready.Type != "ready" || ready.Protocol != protocolVersion || ready.Engine != "kokoro" || ready.SampleRate != 24000 {
		_ = client.Close()
		return nil, fmt.Errorf("Kokoro worker sent an incompatible readiness message")
	}
	return client, nil
}

func workerCommand(ctx context.Context, options Options, cacheRoot string) (string, []string, error) {
	if options.Program != "" {
		return options.Program, append([]string(nil), options.Args...), nil
	}
	python := options.Python
	if python == "" {
		python = os.Getenv("LISTEN_PYTHON")
	}
	if python == "" {
		userPaths, err := paths.FromEnv()
		if err != nil {
			return "", nil, fmt.Errorf("resolve managed Kokoro runtime: %w", err)
		}
		python = filepath.Join(userPaths.DataDir, "runtime", "venv", "bin", "python")
		if runtime.GOOS == "windows" {
			python = filepath.Join(userPaths.DataDir, "runtime", "venv", "Scripts", "python.exe")
		}
		if _, err := os.Stat(python); err != nil {
			return "", nil, fmt.Errorf("managed Kokoro runtime is missing; run `listen setup`: %w", err)
		}
	}
	resolvedPython, err := exec.LookPath(python)
	if err != nil {
		return "", nil, fmt.Errorf("find Python runtime: %w", err)
	}
	workerPath := options.WorkerPath
	if workerPath == "" {
		workerPath, err = installEmbeddedWorker(ctx, cacheRoot)
		if err != nil {
			return "", nil, err
		}
	}
	workerPath, err = filepath.Abs(workerPath)
	if err != nil {
		return "", nil, fmt.Errorf("resolve Kokoro worker script: %w", err)
	}
	if _, err := os.Stat(workerPath); err != nil {
		return "", nil, fmt.Errorf("find Kokoro worker script: %w", err)
	}
	return resolvedPython, []string{"-u", workerPath, "--cache-root", cacheRoot, "--voice", options.Voice}, nil
}

func setEnvironment(base []string, overrides ...string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	order := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, found := values[key]; !found {
			order = append(order, key)
		}
		values[key] = value
	}
	for _, entry := range overrides {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, found := values[key]; !found {
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

func (c *Client) readStdout(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		select {
		case c.lines <- lineResult{line: scanner.Text()}:
		case <-c.ctx.Done():
			return
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	select {
	case c.lines <- lineResult{err: err}:
	case <-c.ctx.Done():
	}
}

func (c *Client) waitProcess() {
	err := c.command.Wait()
	c.exitMu.Lock()
	c.exitErr = err
	c.exitMu.Unlock()
	close(c.done)
}

func (c *Client) readLine(ctx context.Context) (string, error) {
	select {
	case result := <-c.lines:
		return result.line, result.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-c.done:
		c.exitMu.Lock()
		err := c.exitErr
		c.exitMu.Unlock()
		if err != nil {
			return "", fmt.Errorf("worker process exited: %w", err)
		}
		return "", io.EOF
	}
}

func (c *Client) Synthesize(ctx context.Context, request speech.Request) (speech.Result, error) {
	if ctx == nil {
		return speech.Result{}, errors.New("synthesis requires a context")
	}
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	if err := ctx.Err(); err != nil {
		return speech.Result{}, err
	}
	if err := c.ctx.Err(); err != nil {
		return speech.Result{}, err
	}
	if request.Output == "" {
		return speech.Result{}, errors.New("synthesis output path is empty")
	}
	requestID := fmt.Sprintf("req-%d", c.requestID.Add(1))
	text := request.Segment.SpokenText
	if text == "" {
		text = request.Segment.SourceText
	}
	message := synthesizeRequest{
		Type: "synthesize", RequestID: requestID, SegmentID: request.Segment.ID,
		Seq: request.Segment.Seq, Text: text, Voice: request.Voice,
		Speed: request.Speed, Output: request.Output,
	}
	if err := json.NewEncoder(c.stdin).Encode(message); err != nil {
		return speech.Result{}, fmt.Errorf("send synthesis request to worker: %w", err)
	}
	line, err := c.readLine(ctx)
	if err != nil {
		if ctx.Err() != nil {
			c.cancel()
			return speech.Result{}, ctx.Err()
		}
		return speech.Result{}, fmt.Errorf("read synthesis response: %w", err)
	}
	var response event
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		return speech.Result{}, fmt.Errorf("decode synthesis response: %w", err)
	}
	if response.RequestID != requestID {
		return speech.Result{}, fmt.Errorf("worker response did not match request %s", requestID)
	}
	if response.Type == "error" {
		return speech.Result{}, fmt.Errorf("worker synthesis failed (%s): %s", response.Code, response.Message)
	}
	if response.Type != "synthesized" || response.SegmentID != request.Segment.ID || response.Seq != request.Segment.Seq || filepath.Clean(response.Path) != filepath.Clean(request.Output) || response.DurationMS <= 0 {
		return speech.Result{}, errors.New("worker returned an invalid synthesis response")
	}
	duration, err := speech.ValidateWAV(response.Path)
	if err != nil {
		return speech.Result{}, fmt.Errorf("worker audio is invalid: %w", err)
	}
	return speech.Result{SegmentID: response.SegmentID, Seq: response.Seq, Path: response.Path, Duration: duration}, nil
}

func (c *Client) ID() string { return EngineID }

func (c *Client) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		c.requestMu.Lock()
		if c.ctx.Err() == nil {
			_ = json.NewEncoder(c.stdin).Encode(struct {
				Type string `json:"type"`
			}{Type: "shutdown"})
		}
		_ = c.stdin.Close()
		c.requestMu.Unlock()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-c.done:
		case <-timer.C:
			c.cancel()
			<-c.done
		}
		c.cancel()
		c.exitMu.Lock()
		if c.exitErr != nil && !strings.Contains(c.exitErr.Error(), "signal: killed") {
			closeErr = c.exitErr
		}
		c.exitMu.Unlock()
	})
	return closeErr
}

var _ speech.Synthesizer = (*Client)(nil)
