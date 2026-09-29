package playback

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
)

type MPVOptions struct {
	Program        string
	SocketPath     string
	PlaybackSpeed  float64
	StartupTimeout time.Duration
	Stdin          io.Reader
	Stderr         io.Writer
}

type MPV struct {
	options MPVOptions

	startMu          sync.Mutex
	mu               sync.Mutex
	command          *exec.Cmd
	conn             net.Conn
	reader           *bufio.Reader
	socket           string
	seqs             []uint64
	items            []Item
	requestID        uint64
	loadingID        uint64
	loadedID         uint64
	loadGeneration   uint64
	loadedGeneration uint64
	loadFails        map[uint64]bool
	started          bool
	closed           bool
	done             chan struct{}
	exitErr          error
	closeOnce        sync.Once
}

const seekRecoveryTimeout = 750 * time.Millisecond

func NewMPV(options MPVOptions) *MPV { return &MPV{options: options} }

func (p *MPV) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("mpv requires a context")
	}
	p.startMu.Lock()
	defer p.startMu.Unlock()
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return nil
	}
	if p.closed {
		p.mu.Unlock()
		return errors.New("mpv player is already closed")
	}
	p.mu.Unlock()
	program := p.options.Program
	if program == "" {
		program = "mpv"
	}
	resolved, err := exec.LookPath(program)
	if err != nil {
		return fmt.Errorf("find mpv: %w", err)
	}
	socket := p.options.SocketPath
	if socket == "" {
		file, err := os.CreateTemp(os.TempDir(), "listen-*.sock")
		if err != nil {
			return fmt.Errorf("allocate mpv IPC path: %w", err)
		}
		socket = file.Name()
		if err := file.Close(); err != nil {
			_ = os.Remove(socket)
			return fmt.Errorf("prepare mpv IPC path: %w", err)
		}
		if err := os.Remove(socket); err != nil {
			return fmt.Errorf("prepare mpv IPC path: %w", err)
		}
		p.socket = socket
	} else {
		p.socket = socket
	}
	args := []string{
		"--idle=yes", "--force-window=no", "--no-video", "--audio-display=no", "--input-terminal=no", "--term-osd-bar=yes",
		"--input-ipc-server=" + socket,
	}
	if p.options.PlaybackSpeed > 0 {
		args = append(args, fmt.Sprintf("--speed=%g", p.options.PlaybackSpeed))
	}
	command := exec.CommandContext(ctx, resolved, args...)
	command.Stdin = p.options.Stdin
	command.Stdout = io.Discard
	if p.options.Stderr != nil {
		command.Stderr = p.options.Stderr
	} else {
		command.Stderr = io.Discard
	}
	if err := command.Start(); err != nil {
		_ = os.Remove(socket)
		return fmt.Errorf("start mpv: %w", err)
	}
	p.mu.Lock()
	p.command = command
	p.done = make(chan struct{})
	done := p.done
	p.mu.Unlock()
	go func() {
		err := command.Wait()
		p.mu.Lock()
		p.exitErr = err
		p.mu.Unlock()
		close(done)
	}()
	timeout := p.options.StartupTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	startupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var conn net.Conn
	for {
		conn, err = net.DialTimeout("unix", socket, 100*time.Millisecond)
		if err == nil {
			break
		}
		select {
		case <-startupCtx.Done():
			_ = command.Process.Kill()
			<-p.done
			_ = os.Remove(socket)
			return fmt.Errorf("wait for mpv IPC: %w", startupCtx.Err())
		case <-p.done:
			p.mu.Lock()
			exitErr := p.exitErr
			p.mu.Unlock()
			_ = os.Remove(socket)
			return fmt.Errorf("mpv exited before opening IPC: %w", exitErr)
		case <-time.After(25 * time.Millisecond):
		}
	}
	p.mu.Lock()
	p.conn = conn
	p.reader = bufio.NewReader(conn)
	p.started = true
	p.mu.Unlock()
	return nil
}

func (p *MPV) Append(ctx context.Context, item Item) error {
	return p.append(ctx, item, 0, false)
}

// AppendAt queues an item with its starting offset applied during loadfile.
// mpv's loadfile command is asynchronous, so issuing a later seek can race
// playback beginning at the start of the file.
func (p *MPV) AppendAt(ctx context.Context, item Item, offset time.Duration) error {
	if offset < 0 {
		return errors.New("mpv append offset cannot be negative")
	}
	return p.append(ctx, item, offset, true)
}

func (p *MPV) append(ctx context.Context, item Item, offset time.Duration, withOffset bool) error {
	if ctx == nil {
		return errors.New("mpv append requires a context")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started || p.closed {
		return errors.New("mpv player is not running")
	}
	mode := "append"
	if len(p.seqs) == 0 {
		mode = "replace"
	}
	command := []any{"loadfile", item.Path, mode}
	if withOffset && offset > 0 {
		command = append(command, -1, map[string]any{"start": fmt.Sprintf("%g", offset.Seconds())})
	}
	if _, err := p.commandLocked(ctx, command); err != nil {
		return err
	}
	p.seqs = append(p.seqs, item.Seq)
	p.items = append(p.items, item)
	return nil
}

func (p *MPV) Position(ctx context.Context) (uint64, time.Duration, error) {
	if ctx == nil {
		return 0, 0, errors.New("mpv position query requires a context")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started || p.closed {
		return 0, 0, errors.New("mpv player is not running")
	}
	playlist, err := p.commandLocked(ctx, []any{"get_property", "playlist-pos"})
	if err != nil {
		return 0, 0, err
	}
	timeValue, err := p.commandLocked(ctx, []any{"get_property", "time-pos"})
	if err != nil {
		return 0, 0, err
	}
	playlistPosition, ok := number(playlist["data"])
	if !ok || math.Trunc(playlistPosition) != playlistPosition || playlistPosition < 0 || playlistPosition >= float64(len(p.seqs)) {
		return 0, 0, errors.New("mpv returned an invalid playlist position")
	}
	seconds, ok := number(timeValue["data"])
	maxSeconds := float64(int64(^uint64(0)>>1)) / float64(time.Second)
	if !ok || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds >= maxSeconds {
		return 0, 0, errors.New("mpv returned an invalid playback offset")
	}
	index := int(playlistPosition)
	if index < 0 || index >= len(p.seqs) {
		return 0, 0, errors.New("mpv playlist position is outside the queued audio")
	}
	return p.seqs[index], time.Duration(seconds * float64(time.Second)), nil
}

func (p *MPV) CurrentDuration(ctx context.Context) (time.Duration, error) {
	if ctx == nil {
		return 0, errors.New("mpv duration query requires a context")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started || p.closed {
		return 0, errors.New("mpv player is not running")
	}
	value, err := p.commandLocked(ctx, []any{"get_property", "duration"})
	if err != nil {
		return 0, err
	}
	seconds, ok := number(value["data"])
	maxSeconds := float64(int64(^uint64(0)>>1)) / float64(time.Second)
	if !ok || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds >= maxSeconds {
		return 0, errors.New("mpv returned an invalid audio duration")
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// Seek sets the offset in the currently playing audio item.
func (p *MPV) Seek(ctx context.Context, offset time.Duration) error {
	if ctx == nil {
		return errors.New("mpv seek requires a context")
	}
	if offset < 0 {
		return errors.New("mpv seek offset cannot be negative")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started || p.closed {
		return errors.New("mpv player is not running")
	}
	_, err := p.commandLocked(ctx, []any{"set_property", "time-pos", offset.Seconds()})
	if err != nil {
		return fmt.Errorf("seek mpv playback: %w", err)
	}
	return nil
}

// SeekBy moves through the ordered, ready queue. If the target crosses into a
// different audio item, it selects that queued item and waits for its fresh
// load event before applying the target offset.
func (p *MPV) SeekBy(ctx context.Context, delta time.Duration) (Position, error) {
	if ctx == nil {
		return Position{}, errors.New("mpv seek requires a context")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started || p.closed {
		return Position{}, errors.New("mpv player is not running")
	}
	if err := ctx.Err(); err != nil {
		return Position{}, err
	}
	if len(p.items) == 0 || len(p.items) != len(p.seqs) {
		return Position{}, errors.New("mpv has no queued audio to seek")
	}
	maxSeconds := float64(int64(^uint64(0)>>1)) / float64(time.Second)
	currentIndex := 0
	seconds := 0.0
	stablePosition := false
	for attempt := 0; attempt < 3; attempt++ {
		before, err := p.playlistIndexLocked(ctx)
		if err != nil {
			return Position{}, err
		}
		timeValue, err := p.commandLocked(ctx, []any{"get_property", "time-pos"})
		if err != nil {
			return Position{}, err
		}
		readSeconds, ok := number(timeValue["data"])
		if !ok || math.IsNaN(readSeconds) || math.IsInf(readSeconds, 0) || readSeconds < 0 || readSeconds >= maxSeconds {
			return Position{}, errors.New("mpv returned an invalid playback offset")
		}
		seconds = readSeconds
		after, err := p.playlistIndexLocked(ctx)
		if err != nil {
			return Position{}, err
		}
		if before != after {
			continue
		}
		currentIndex = before
		stablePosition = true
		break
	}
	if !stablePosition {
		return Position{}, errors.New("mpv playback changed while reading the seek position")
	}

	targetIndex := currentIndex
	targetOffset := time.Duration(seconds*float64(time.Second)) + delta
	for targetOffset < 0 && targetIndex > 0 {
		targetIndex--
		previousDuration := p.items[targetIndex].Duration
		if previousDuration <= 0 {
			return Position{}, errors.New("mpv has no duration for a queued audio item")
		}
		targetOffset += previousDuration
	}
	if targetOffset < 0 {
		targetOffset = 0
	}
	for {
		duration := p.items[targetIndex].Duration
		if duration <= 0 {
			return Position{}, errors.New("mpv has no duration for a queued audio item")
		}
		if targetOffset < duration {
			break
		}
		if targetIndex+1 >= len(p.items) {
			return Position{}, ErrSeekNotBuffered
		}
		targetOffset -= duration
		targetIndex++
	}
	target := p.items[targetIndex]
	if targetIndex == currentIndex {
		if _, err := p.commandLocked(ctx, []any{"set_property", "time-pos", targetOffset.Seconds()}); err != nil {
			return Position{}, fmt.Errorf("seek mpv playback: %w", err)
		}
		return Position{Seq: target.Seq, Offset: targetOffset, Duration: target.Duration}, nil
	}

	currentEntryID, err := p.playlistEntryIDLocked(ctx, currentIndex)
	if err != nil {
		return Position{}, err
	}
	entryID, err := p.playlistEntryIDLocked(ctx, targetIndex)
	if err != nil {
		return Position{}, err
	}
	loadGeneration := p.loadGeneration
	p.loadedID = 0
	delete(p.loadFails, entryID)
	if _, err := p.commandLocked(ctx, []any{"playlist-play-index", targetIndex}); err != nil {
		if restoreErr := p.restorePositionAfterFailedSeekLocked(currentIndex, currentEntryID, seconds); restoreErr != nil {
			return Position{}, fmt.Errorf("select mpv seek target: %w (could not restore the previous position: %v)", err, restoreErr)
		}
		return Position{}, fmt.Errorf("select mpv seek target: %w", err)
	}
	if err := p.waitForEntryLoadedLocked(ctx, entryID, loadGeneration); err != nil {
		if restoreErr := p.restorePositionAfterFailedSeekLocked(currentIndex, currentEntryID, seconds); restoreErr != nil {
			return Position{}, fmt.Errorf("wait for mpv seek target: %w (could not restore the previous position: %v)", err, restoreErr)
		}
		return Position{}, fmt.Errorf("wait for mpv seek target: %w", err)
	}
	if _, err := p.commandLocked(ctx, []any{"set_property", "time-pos", targetOffset.Seconds()}); err != nil {
		if restoreErr := p.restorePositionAfterFailedSeekLocked(currentIndex, currentEntryID, seconds); restoreErr != nil {
			return Position{}, fmt.Errorf("seek mpv playback: %w (could not restore the previous position: %v)", err, restoreErr)
		}
		return Position{}, fmt.Errorf("seek mpv playback: %w", err)
	}
	return Position{Seq: target.Seq, Offset: targetOffset, Duration: target.Duration}, nil
}

func (p *MPV) playlistEntryIDLocked(ctx context.Context, index int) (uint64, error) {
	entry, err := p.commandLocked(ctx, []any{"get_property", fmt.Sprintf("playlist/%d/id", index)})
	if err != nil {
		return 0, err
	}
	entryIDValue, ok := number(entry["data"])
	maxSeconds := float64(int64(^uint64(0)>>1)) / float64(time.Second)
	if !ok || math.Trunc(entryIDValue) != entryIDValue || entryIDValue < 0 || entryIDValue >= maxSeconds {
		return 0, errors.New("mpv returned an invalid playlist entry ID")
	}
	return uint64(entryIDValue), nil
}

func (p *MPV) restorePositionAfterFailedSeekLocked(index int, entryID uint64, seconds float64) error {
	ctx, cancel := context.WithTimeout(context.Background(), seekRecoveryTimeout)
	defer cancel()
	loadGeneration := p.loadGeneration
	p.loadedID = 0
	delete(p.loadFails, entryID)
	if _, err := p.commandLocked(ctx, []any{"playlist-play-index", index}); err != nil {
		return fmt.Errorf("select previous playlist entry: %w", err)
	}
	if err := p.waitForEntryLoadedLocked(ctx, entryID, loadGeneration); err != nil {
		return fmt.Errorf("wait for previous playlist entry: %w", err)
	}
	if _, err := p.commandLocked(ctx, []any{"set_property", "time-pos", seconds}); err != nil {
		return fmt.Errorf("restore previous playback offset: %w", err)
	}
	return nil
}

func (p *MPV) waitForEntryLoadedLocked(ctx context.Context, entryID, afterGeneration uint64) error {
	if p.loadedID == entryID && p.loadedGeneration != afterGeneration {
		return nil
	}
	cleanupDeadline := p.watchContextDeadlineLocked(ctx)
	defer cleanupDeadline()
	for p.loadedID != entryID || p.loadedGeneration == afterGeneration {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := p.reader.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("read mpv load event: %w", contextSocketError(ctx, err))
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			return fmt.Errorf("decode mpv load event: %w", err)
		}
		p.consumeEventLocked(event)
		if p.loadFails[entryID] {
			return errors.New("mpv could not load the selected playlist entry")
		}
	}
	delete(p.loadFails, entryID)
	return nil
}

func (p *MPV) consumeEventLocked(message map[string]any) {
	event, ok := message["event"].(string)
	if !ok {
		return
	}
	switch event {
	case "start-file":
		p.loadGeneration++
		p.loadedID = 0
		p.loadingID = 0
		if value, ok := number(message["playlist_entry_id"]); ok && value >= 0 && math.Trunc(value) == value {
			p.loadingID = uint64(value)
		}
	case "file-loaded":
		p.loadedID = p.loadingID
		p.loadedGeneration = p.loadGeneration
	case "end-file":
		if message["reason"] != "error" {
			return
		}
		if value, ok := number(message["playlist_entry_id"]); ok && value >= 0 && math.Trunc(value) == value {
			if p.loadFails == nil {
				p.loadFails = make(map[uint64]bool)
			}
			p.loadFails[uint64(value)] = true
		}
	}
}

func (p *MPV) playlistIndexLocked(ctx context.Context) (int, error) {
	playlist, err := p.commandLocked(ctx, []any{"get_property", "playlist-pos"})
	if err != nil {
		return 0, err
	}
	playlistPosition, ok := number(playlist["data"])
	if !ok || math.Trunc(playlistPosition) != playlistPosition || playlistPosition < 0 || playlistPosition >= float64(len(p.items)) {
		return 0, errors.New("mpv returned an invalid playlist position")
	}
	return int(playlistPosition), nil
}

// SetPaused toggles mpv's pause property for the interactive Listen player.
func (p *MPV) SetPaused(ctx context.Context, paused bool) error {
	if ctx == nil {
		return errors.New("mpv pause control requires a context")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started || p.closed {
		return errors.New("mpv player is not running")
	}
	_, err := p.commandLocked(ctx, []any{"set_property", "pause", paused})
	return err
}

func (p *MPV) Wait(ctx context.Context) error {
	if ctx == nil {
		return errors.New("mpv wait requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	done := p.done
	started := p.started
	queued := len(p.seqs)
	p.mu.Unlock()
	if !started || done == nil {
		return errors.New("mpv player is not running")
	}
	if queued == 0 {
		return errors.New("mpv has no queued audio to finish")
	}
	check := time.NewTicker(100 * time.Millisecond)
	defer check.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return p.earlyExitError()
		case <-check.C:
		}

		p.mu.Lock()
		if p.closed || !p.started {
			p.mu.Unlock()
			return errors.New("mpv closed before playback completed")
		}
		response, err := p.commandLocked(ctx, []any{"get_property", "idle-active"})
		p.mu.Unlock()
		if err != nil {
			select {
			case <-done:
				return p.earlyExitError()
			default:
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("check mpv playback completion: %w", err)
		}
		idle, ok := response["data"].(bool)
		if !ok {
			return errors.New("mpv returned an invalid idle status")
		}
		if idle {
			return p.Close()
		}
	}
}

func (p *MPV) earlyExitError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exitErr != nil {
		return fmt.Errorf("mpv exited before playback completed: %w", p.exitErr)
	}
	return errors.New("mpv exited before playback completed")
}

func (p *MPV) commandLocked(ctx context.Context, command []any) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.requestID++
	requestID := p.requestID
	request := map[string]any{"command": command, "request_id": requestID}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	cleanupDeadline := p.watchContextDeadlineLocked(ctx)
	defer cleanupDeadline()
	if _, err := p.conn.Write(append(data, '\n')); err != nil {
		return nil, fmt.Errorf("send mpv command: %w", contextSocketError(ctx, err))
	}
	for {
		line, err := p.reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("read mpv response: %w", contextSocketError(ctx, err))
		}
		var response map[string]any
		if err := json.Unmarshal(line, &response); err != nil {
			return nil, fmt.Errorf("decode mpv response: %w", err)
		}
		if _, ok := response["event"]; ok {
			p.consumeEventLocked(response)
			continue
		}
		id, ok := number(response["request_id"])
		if !ok || uint64(id) != requestID {
			continue // Ignore unsolicited mpv events and other command replies.
		}
		if response["error"] != "success" {
			return nil, fmt.Errorf("mpv command failed: %v", response["error"])
		}
		return response, nil
	}
}

func (p *MPV) watchContextDeadlineLocked(ctx context.Context) func() {
	stopDeadline := make(chan struct{})
	deadlineDone := make(chan struct{})
	go func(conn net.Conn) {
		defer close(deadlineDone)
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-stopDeadline:
		}
	}(p.conn)
	if deadline, ok := ctx.Deadline(); ok {
		_ = p.conn.SetDeadline(deadline)
	}
	return func() {
		close(stopDeadline)
		<-deadlineDone
		_ = p.conn.SetDeadline(time.Time{})
	}
}

func contextSocketError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return err
}

func number(value any) (float64, bool) {
	valueFloat, ok := value.(float64)
	if !ok {
		return 0, false
	}
	return valueFloat, true
}

func (p *MPV) Close() error {
	var closeErr error
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		if p.conn != nil {
			request, _ := json.Marshal(map[string]any{"command": []string{"quit"}, "request_id": p.requestID + 1})
			_, _ = p.conn.Write(append(request, '\n'))
			_ = p.conn.Close()
		}
		done := p.done
		command := p.command
		socket := p.socket
		p.mu.Unlock()
		if done != nil && command != nil {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				if command.Process != nil {
					_ = command.Process.Kill()
				}
				<-done
			}
			p.mu.Lock()
			if p.exitErr != nil {
				closeErr = p.exitErr
			}
			p.mu.Unlock()
		}
		if socket != "" {
			_ = os.Remove(socket)
		}
	})
	return closeErr
}
