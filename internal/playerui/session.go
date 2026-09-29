package playerui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

var ErrCancelled = errors.New("chapter selection cancelled")

type Session struct {
	input   *os.File
	output  io.Writer
	restore func() error
	model   Model

	actions chan Action
	updates chan sessionUpdate
	stop    chan struct{}
	done    chan struct{}
	close   sync.Once
	stateMu sync.Mutex
	state   State
}

type sessionUpdate struct {
	state State
	phase Phase
}

func Interactive(input io.Reader, output io.Writer) bool {
	in, inOK := input.(*os.File)
	out, outOK := output.(*os.File)
	return inOK && outOK && inTerminalSupported() && isTerminal(int(in.Fd())) && isTerminal(int(out.Fd()))
}

func ChooseStart(ctx context.Context, input io.Reader, output io.Writer, model Model) (StartPoint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	in, ok := input.(*os.File)
	if !ok || !Interactive(input, output) {
		return StartPoint{}, errors.New("interactive chapter selection requires a terminal")
	}
	session, err := newSession(in, output, model)
	if err != nil {
		return StartPoint{}, err
	}
	defer session.Close()
	for {
		select {
		case action := <-session.actions:
			switch action.Kind {
			case ActionStart:
				return action.Start, nil
			case ActionStop:
				return StartPoint{}, ErrCancelled
			}
		case <-session.done:
			return StartPoint{}, errors.New("chapter selector stopped unexpectedly")
		case <-ctx.Done():
			return StartPoint{}, ctx.Err()
		}
	}
}

func StartPlayback(input io.Reader, output io.Writer, model Model) (*Session, error) {
	in, ok := input.(*os.File)
	if !ok || !Interactive(input, output) {
		return nil, errors.New("interactive playback requires a terminal")
	}
	return newSession(in, output, model)
}

func newSession(input *os.File, output io.Writer, model Model) (*Session, error) {
	restore, err := enterTerminal(int(input.Fd()))
	if err != nil {
		return nil, fmt.Errorf("prepare terminal for playback controls: %w", err)
	}
	s := &Session{
		input: input, output: output, restore: restore, model: model,
		actions: make(chan Action, 8), updates: make(chan sessionUpdate, 1),
		stop: make(chan struct{}), done: make(chan struct{}), state: model.state,
	}
	if _, err := io.WriteString(output, "\x1b[?1049h\x1b[?25l"); err != nil {
		_ = restore()
		return nil, fmt.Errorf("open player screen: %w", err)
	}
	s.resize()
	s.render()
	go s.run()
	return s, nil
}

func (s *Session) Actions() <-chan Action { return s.actions }

func (s *Session) SetStatus(status string) {
	s.mutate(func(state *State) { state.Status = status }, 0)
}

func (s *Session) SetProgress(ready, total int) {
	s.mutate(func(state *State) { state.Ready, state.Total = ready, total }, 0)
}

func (s *Session) SetChapter(index int) {
	s.mutate(func(state *State) { state.ChapterIndex = index }, 0)
}

func (s *Session) SetPaused(paused bool) {
	s.mutate(func(state *State) { state.Paused = paused }, 0)
}

func (s *Session) SetPlaybackStarted() {
	s.mutate(func(state *State) {
		state.Status = "Playing"
		state.PlaybackKnown = true
	}, PhasePlaying)
}

func (s *Session) SetPosition(sequence uint64, offset, duration time.Duration) {
	s.mutate(func(state *State) {
		state.Sequence = sequence
		state.Position = offset
		state.Duration = duration
		state.PlaybackKnown = true
	}, 0)
}

func (s *Session) SetFinished(status string) {
	s.mutate(func(state *State) { state.Status = status }, PhaseFinished)
}

func (s *Session) mutate(change func(*State), phase Phase) {
	s.stateMu.Lock()
	change(&s.state)
	update := sessionUpdate{state: s.state, phase: phase}
	select {
	case s.updates <- update:
	default:
		select {
		case <-s.updates:
		default:
		}
		select {
		case s.updates <- update:
		default:
		}
	}
	s.stateMu.Unlock()
}

func (s *Session) run() {
	keys := make(chan string, 16)
	keysDone := make(chan struct{})
	go func() {
		defer close(keysDone)
		pollKeys(int(s.input.Fd()), keys, s.stop)
	}()
	defer func() {
		<-keysDone
		close(s.done)
	}()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case update := <-s.updates:
			s.model.SetState(update.state)
			if update.phase != 0 {
				s.model.SetPhase(update.phase)
			}
			s.resize()
			s.render()
		case key := <-keys:
			action, ok := s.model.HandleKey(key)
			if ok {
				select {
				case s.actions <- action:
				default:
				}
			}
			s.render()
		case <-ticker.C:
			if s.resize() {
				s.render()
			}
		}
	}
}

func (s *Session) resize() bool {
	width, height, err := terminalSize(int(s.input.Fd()))
	if err == nil {
		oldWidth, oldHeight := s.model.Size()
		s.model.SetSize(width, height)
		return oldWidth != width || oldHeight != height
	}
	return false
}

func (s *Session) render() {
	view := s.model.View()
	view = "\x1b[H\x1b[2J" + view
	_, _ = io.WriteString(s.output, carriageReturns(view))
}

func (s *Session) Close() {
	if s == nil {
		return
	}
	s.close.Do(func() {
		close(s.stop)
		<-s.done
		_, _ = io.WriteString(s.output, "\x1b[?25h\x1b[?1049l")
		_ = s.restore()
	})
}

func carriageReturns(value string) string {
	result := make([]byte, 0, len(value)+strings.Count(value, "\n"))
	for index := 0; index < len(value); index++ {
		if value[index] == '\n' {
			result = append(result, '\r')
		}
		result = append(result, value[index])
	}
	return string(result)
}
