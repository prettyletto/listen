// Package argos provides the pinned local English to Brazilian Portuguese
// translator used by the reader.
package argos

import (
	"bufio"
	"context"
	"embed"
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

	"github.com/prettyletto/listen/internal/atomicfile"
	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/translate"
)

const protocolVersion = 1

//go:embed worker.py
var workerFiles embed.FS

type Options struct {
	Python       string
	WorkerPath   string
	ModelDir     string
	CacheRoot    string
	Stderr       io.Writer
	ReadyTimeout time.Duration
	Program      string
	Args         []string
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

type lineResult struct {
	line string
	err  error
}

type workerEvent struct {
	Type      string              `json:"type"`
	Protocol  int                 `json:"protocol"`
	Engine    string              `json:"engine"`
	RequestID string              `json:"request_id"`
	Segments  []workerTranslation `json:"segments"`
	Code      string              `json:"code"`
	Message   string              `json:"message"`
}

type workerTranslation struct {
	SegmentID string `json:"segment_id"`
	Text      string `json:"text"`
}

type translationRequest struct {
	Type      string            `json:"type"`
	RequestID string            `json:"request_id"`
	Target    string            `json:"target_language"`
	Segments  []workerInputText `json:"segments"`
}

type workerInputText struct {
	SegmentID string `json:"segment_id"`
	Text      string `json:"text"`
}

func Start(ctx context.Context, options Options) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("translation worker requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	userPaths, err := paths.FromEnv()
	if err != nil {
		return nil, fmt.Errorf("resolve local translation storage: %w", err)
	}
	if options.CacheRoot == "" {
		options.CacheRoot = userPaths.CacheDir
	}
	if options.ModelDir == "" {
		options.ModelDir = ModelRoot(userPaths)
	}
	program, args, err := workerCommand(ctx, options, userPaths)
	if err != nil {
		return nil, err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(workerCtx, program, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open local translation worker output: %w", err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open local translation worker input: %w", err)
	}
	if options.Stderr != nil {
		command.Stderr = options.Stderr
	} else {
		command.Stderr = io.Discard
	}
	if err := command.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start local translation worker: %w", err)
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
		return nil, fmt.Errorf("wait for local translation worker: %w", err)
	}
	var ready workerEvent
	if err := json.Unmarshal([]byte(line), &ready); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("decode local translation worker readiness: %w", err)
	}
	if ready.Type == "startup_error" {
		_ = client.Close()
		return nil, fmt.Errorf("local translation worker could not load its model (%s): %s", ready.Code, ready.Message)
	}
	if ready.Type != "ready" || ready.Protocol != protocolVersion || ready.Engine != BackendID {
		_ = client.Close()
		return nil, errors.New("local translation worker sent an incompatible readiness message")
	}
	return client, nil
}

func workerCommand(ctx context.Context, options Options, userPaths paths.Paths) (string, []string, error) {
	if options.Program != "" {
		return options.Program, append([]string(nil), options.Args...), nil
	}
	if options.ModelDir == "" {
		options.ModelDir = ModelRoot(userPaths)
	}
	if !ModelInstalled(options.ModelDir) {
		return "", nil, fmt.Errorf("local translation model is missing or incomplete; run `listen setup --translate %s`", LanguagePair)
	}
	python := options.Python
	if python == "" {
		python = os.Getenv("LISTEN_PYTHON")
	}
	if python == "" {
		python = filepath.Join(userPaths.DataDir, "runtime", "venv", "bin", "python")
		if runtime.GOOS == "windows" {
			python = filepath.Join(userPaths.DataDir, "runtime", "venv", "Scripts", "python.exe")
		}
		if _, err := os.Stat(python); err != nil {
			return "", nil, fmt.Errorf("managed translation runtime is missing; run `listen setup --translate %s`: %w", LanguagePair, err)
		}
	}
	resolvedPython, err := exec.LookPath(python)
	if err != nil {
		return "", nil, fmt.Errorf("find managed translation runtime: %w", err)
	}
	workerPath := options.WorkerPath
	if workerPath == "" {
		if options.CacheRoot == "" {
			return "", nil, errors.New("local translation worker requires a cache directory")
		}
		workerPath = filepath.Join(options.CacheRoot, "worker", "argos-translation-v1.py")
		worker, err := workerFiles.ReadFile("worker.py")
		if err != nil {
			return "", nil, fmt.Errorf("read embedded translation worker: %w", err)
		}
		if err := atomicfile.Write(ctx, workerPath, worker, 0o600); err != nil {
			return "", nil, fmt.Errorf("install local translation worker: %w", err)
		}
	}
	workerPath, err = filepath.Abs(workerPath)
	if err != nil {
		return "", nil, fmt.Errorf("resolve local translation worker: %w", err)
	}
	if _, err := os.Stat(workerPath); err != nil {
		return "", nil, fmt.Errorf("find local translation worker: %w", err)
	}
	modelPath, err := filepath.Abs(options.ModelDir)
	if err != nil {
		return "", nil, fmt.Errorf("resolve local translation model: %w", err)
	}
	return resolvedPython, []string{"-u", workerPath, "--model-dir", modelPath}, nil
}

func (client *Client) ID() string { return BackendID }

func (client *Client) Capabilities(ctx context.Context) (translate.Capabilities, error) {
	if ctx == nil {
		return translate.Capabilities{}, errors.New("read translator capabilities requires a context")
	}
	if err := ctx.Err(); err != nil {
		return translate.Capabilities{}, err
	}
	return translate.Capabilities{
		Name:          "Argos OPUS-MT English to Brazilian Portuguese",
		Local:         true,
		LanguagePairs: map[string]bool{LanguagePair: true},
		SupportsBatch: true,
	}, nil
}

func (client *Client) Translate(ctx context.Context, request translate.Request) ([]translate.Result, error) {
	if ctx == nil {
		return nil, errors.New("translation requires a context")
	}
	client.requestMu.Lock()
	defer client.requestMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := client.ctx.Err(); err != nil {
		return nil, err
	}
	if len(request.Segments) == 0 || len(request.Segments) > 16 {
		return nil, errors.New("translation batch must contain between 1 and 16 segments")
	}
	target, err := translate.NormalizeLanguageTag(request.TargetLanguage)
	if err != nil {
		return nil, err
	}
	if target != "pt-BR" {
		return nil, fmt.Errorf("local translator supports %s only", LanguagePair)
	}
	requestID := fmt.Sprintf("req-%d", client.requestID.Add(1))
	message := translationRequest{Type: "translate", RequestID: requestID, Target: target, Segments: make([]workerInputText, 0, len(request.Segments))}
	seen := make(map[string]bool, len(request.Segments))
	for _, item := range request.Segments {
		if item.ID == "" || seen[item.ID] {
			return nil, errors.New("translation batch contains an empty or duplicate segment ID")
		}
		seen[item.ID] = true
		pair, err := translate.LanguagePair(item.SourceLang, target)
		if err != nil {
			return nil, fmt.Errorf("segment %q has no supported source language: %w", item.ID, err)
		}
		if pair != LanguagePair {
			return nil, fmt.Errorf("local translator supports %s only; segment %q is %s", LanguagePair, item.ID, pair)
		}
		if len(item.SourceText) == 0 || len(item.SourceText) > translate.MaxTranslationBytes {
			return nil, fmt.Errorf("segment %q source text is empty or too large", item.ID)
		}
		message.Segments = append(message.Segments, workerInputText{SegmentID: item.ID, Text: item.SourceText})
	}
	if err := json.NewEncoder(client.stdin).Encode(message); err != nil {
		return nil, fmt.Errorf("send request to local translation worker: %w", err)
	}
	line, err := client.readLine(ctx)
	if err != nil {
		if ctx.Err() != nil {
			client.cancel()
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("read local translation response: %w", err)
	}
	var response workerEvent
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		return nil, fmt.Errorf("decode local translation response: %w", err)
	}
	if response.RequestID != requestID {
		return nil, fmt.Errorf("local translation response did not match request %s", requestID)
	}
	if response.Type == "error" {
		return nil, fmt.Errorf("local translation failed (%s): %s", response.Code, response.Message)
	}
	if response.Type != "translated" || len(response.Segments) != len(message.Segments) {
		return nil, errors.New("local translation worker returned an invalid response")
	}
	results := make([]translate.Result, 0, len(response.Segments))
	returned := make(map[string]bool, len(response.Segments))
	for _, result := range response.Segments {
		if !seen[result.SegmentID] || returned[result.SegmentID] {
			return nil, fmt.Errorf("local translation worker returned an unknown or duplicate segment ID %q", result.SegmentID)
		}
		text, err := translate.NormalizeTranslationText(result.Text)
		if err != nil {
			return nil, fmt.Errorf("local translation worker returned invalid text for segment %q: %w", result.SegmentID, err)
		}
		returned[result.SegmentID] = true
		results = append(results, translate.Result{SegmentID: result.SegmentID, Text: text})
	}
	return results, nil
}

func (client *Client) readStdout(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 20*translate.MaxTranslationBytes)
	for scanner.Scan() {
		select {
		case client.lines <- lineResult{line: scanner.Text()}:
		case <-client.ctx.Done():
			return
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	select {
	case client.lines <- lineResult{err: err}:
	case <-client.ctx.Done():
	}
}

func (client *Client) waitProcess() {
	err := client.command.Wait()
	client.exitMu.Lock()
	client.exitErr = err
	client.exitMu.Unlock()
	close(client.done)
}

func (client *Client) readLine(ctx context.Context) (string, error) {
	select {
	case result := <-client.lines:
		return result.line, result.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-client.done:
		client.exitMu.Lock()
		err := client.exitErr
		client.exitMu.Unlock()
		if err != nil {
			return "", fmt.Errorf("worker process exited: %w", err)
		}
		return "", io.EOF
	}
}

func (client *Client) Close() error {
	var closeErr error
	client.closeOnce.Do(func() {
		client.requestMu.Lock()
		if client.ctx.Err() == nil {
			_ = json.NewEncoder(client.stdin).Encode(struct {
				Type string `json:"type"`
			}{Type: "shutdown"})
		}
		_ = client.stdin.Close()
		client.requestMu.Unlock()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-client.done:
		case <-timer.C:
			client.cancel()
			<-client.done
		}
		client.cancel()
		client.exitMu.Lock()
		if client.exitErr != nil && !strings.Contains(client.exitErr.Error(), "signal: killed") {
			closeErr = client.exitErr
		}
		client.exitMu.Unlock()
	})
	return closeErr
}

var _ translate.Translator = (*Client)(nil)
