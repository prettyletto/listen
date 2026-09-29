package playerui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var ErrCancelled = errors.New("chapter selection cancelled")

type Session struct {
	program *tea.Program
	actions chan Action
	ready   chan struct{}
	done    chan struct{}
	close   sync.Once
	errMu   sync.Mutex
	err     error
	stateMu sync.Mutex
	state   State
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
			return StartPoint{}, session.runError("chapter selector stopped unexpectedly")
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
	if output == nil {
		return nil, errors.New("interactive playback requires an output stream")
	}
	s := &Session{
		actions: make(chan Action, 8),
		ready:   make(chan struct{}),
		done:    make(chan struct{}),
		state:   model.state,
	}
	ui := newTeaModel(model, s.actions, s.ready)
	ui.done = s.done
	s.program = tea.NewProgram(ui, tea.WithInput(input), tea.WithOutput(output), tea.WithAltScreen(), tea.WithoutSignalHandler())
	go func() {
		_, err := s.program.Run()
		s.errMu.Lock()
		s.err = err
		s.errMu.Unlock()
		close(s.done)
	}()
	select {
	case <-s.ready:
		return s, nil
	case <-s.done:
		return nil, s.runError("start the interactive screen")
	}
}

func (s *Session) Actions() <-chan Action { return s.actions }

func (s *Session) SetStatus(status string) {
	s.mutate(func(state *State) { state.Status = status }, 0)
}

func (s *Session) SetProgress(ready, total int) {
	s.mutate(func(state *State) { state.Ready, state.Total = ready, total }, 0)
}

func (s *Session) SetChapter(index int) {
	s.mutate(func(state *State) {
		state.ChapterIndex = index
		state.SeekPending = 0
	}, 0)
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

func (s *Session) SetSeekPending(delta time.Duration) {
	s.mutate(func(state *State) { state.SeekPending = delta }, 0)
}

func (s *Session) SetFinished(status string) {
	s.mutate(func(state *State) { state.Status = status }, PhaseFinished)
}

func (s *Session) mutate(change func(*State), phase Phase) {
	if s == nil || s.program == nil {
		return
	}
	s.stateMu.Lock()
	select {
	case <-s.done:
		s.stateMu.Unlock()
		return
	default:
	}
	change(&s.state)
	update := sessionUpdate{state: s.state, phase: phase}
	s.stateMu.Unlock()
	s.program.Send(update)
}

func (s *Session) runError(prefix string) error {
	if s == nil {
		return errors.New(prefix)
	}
	s.errMu.Lock()
	err := s.err
	s.errMu.Unlock()
	if err == nil {
		return errors.New(prefix)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

func (s *Session) Close() {
	if s == nil {
		return
	}
	s.close.Do(func() {
		select {
		case <-s.done:
		default:
			s.program.Quit()
		}
		<-s.done
	})
}
