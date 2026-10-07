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
	"github.com/charmbracelet/x/term"
)

var ErrCancelled = errors.New("chapter selection cancelled")

type Session struct {
	program           *tea.Program
	actions           chan Action
	ready             chan struct{}
	done              chan struct{}
	close             sync.Once
	errMu             sync.Mutex
	err               error
	stateMu           sync.Mutex
	state             State
	width             int
	height            int
	homeMode          bool
	homeModel         Model
	activeModel       Model
	phase             Phase
	exitAfterPlayback bool
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

// StartHome starts the listening home. Playback screens can be installed into
// the same Bubble Tea program with ShowPlayback, keeping navigation in one
// terminal session.
func StartHome(input io.Reader, output io.Writer, model Model) (*Session, error) {
	in, ok := input.(*os.File)
	if !ok || !Interactive(input, output) {
		return nil, errors.New("the listening home requires an interactive terminal")
	}
	if model.phase != PhaseHome {
		return nil, errors.New("the listening home model is invalid")
	}
	s, err := newSession(in, output, model)
	if err != nil {
		return nil, err
	}
	s.stateMu.Lock()
	model.SetSize(s.width, s.height)
	s.homeMode = true
	s.homeModel = model
	s.activeModel = model
	s.phase = PhaseHome
	s.stateMu.Unlock()
	return s, nil
}

func newSession(input *os.File, output io.Writer, model Model) (*Session, error) {
	if output == nil {
		return nil, errors.New("interactive playback requires an output stream")
	}
	width, height := initialSessionSize(output, model, term.GetSize)
	model.SetSize(width, height)
	s := &Session{
		actions: make(chan Action, 8),
		ready:   make(chan struct{}),
		done:    make(chan struct{}),
		state:   model.state,
		phase:   model.phase,
		width:   width,
		height:  height,
	}
	ui := newTeaModel(model, s.actions, s.ready)
	ui.done = s.done
	ui.onSize = s.recordSize
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

func initialSessionSize(output io.Writer, fallback Model, getSize func(uintptr) (int, int, error)) (int, int) {
	width, height := fallback.Size()
	if width < 1 || height < 1 {
		fallback.SetSize(width, height)
		width, height = fallback.Size()
	}
	file, ok := output.(*os.File)
	if !ok || file == nil || getSize == nil {
		return width, height
	}
	terminalWidth, terminalHeight, err := getSize(file.Fd())
	if err != nil || terminalWidth < 1 || terminalHeight < 1 {
		return width, height
	}
	return terminalWidth, terminalHeight
}

func (s *Session) Actions() <-chan Action { return s.actions }

func (s *Session) QueueAction(action Action) {
	if s == nil || s.program == nil {
		return
	}
	select {
	case s.actions <- action:
	case <-s.done:
	}
}

func (s *Session) ShowPlayback(model Model) (*Session, error) {
	if s == nil || s.program == nil || !s.homeMode {
		return nil, errors.New("the listening home is not active")
	}
	s.stateMu.Lock()
	model.SetSize(s.width, s.height)
	s.phase = model.phase
	s.state = model.state
	s.activeModel = model
	s.stateMu.Unlock()
	s.program.Send(sessionUpdate{screen: &model})
	return s, nil
}

func (s *Session) SetHome(entries []LibraryEntry, note string) {
	if s == nil || s.program == nil || !s.homeMode {
		return
	}
	model := NewHome(entries, note)
	s.stateMu.Lock()
	model.SetSize(s.width, s.height)
	s.homeModel = model
	s.activeModel = model
	s.phase = PhaseHome
	s.stateMu.Unlock()
	s.program.Send(sessionUpdate{screen: &model})
}

func (s *Session) SetHomeWithUndo(entries []LibraryEntry, note string, undo *LibraryEntry) {
	if s == nil || s.program == nil || !s.homeMode {
		return
	}
	model := NewHome(entries, note)
	model.SetUndoEntry(undo)
	s.stateMu.Lock()
	model.SetSize(s.width, s.height)
	s.homeModel = model
	s.activeModel = model
	s.phase = PhaseHome
	s.stateMu.Unlock()
	s.program.Send(sessionUpdate{screen: &model})
}

func (s *Session) SetAddPath(path, note string) {
	if s == nil || s.program == nil || !s.homeMode {
		return
	}
	s.stateMu.Lock()
	model := s.homeModel
	s.stateMu.Unlock()
	model.SetAddPath(path, note)
	s.setScreen(model)
}

func (s *Session) SetSetupChoice(source, note string) {
	if s == nil || s.program == nil || !s.homeMode {
		return
	}
	s.stateMu.Lock()
	model := homeOverlayModel(s.homeModel, s.activeModel)
	s.stateMu.Unlock()
	model.SetSetupChoice(source, note)
	s.setScreen(model)
}

func (s *Session) SetSetupProgress(source, note string) {
	if s == nil || s.program == nil || !s.homeMode {
		return
	}
	s.stateMu.Lock()
	model := homeOverlayModel(s.homeModel, s.activeModel)
	s.stateMu.Unlock()
	model.SetSetupChoice(source, "")
	model.SetSetupProgress(note)
	s.setScreen(model)
}

func (s *Session) SetPreferences(preferences Preferences, returnTo Phase) {
	if s == nil || s.program == nil || !s.homeMode {
		return
	}
	s.stateMu.Lock()
	model := homeOverlayModel(s.homeModel, s.activeModel)
	s.stateMu.Unlock()
	model.SetPreferencesFor(preferences, returnTo)
	s.setScreen(model)
}

func homeOverlayModel(home, active Model) Model {
	switch active.phase {
	case PhaseHome, PhaseSetupChoice, PhaseSetupProgress, PhasePreferences, PhaseAdvancedPreferences, PhaseVoicePreview:
		return active
	default:
		return home
	}
}

func (s *Session) FinishVoicePreview(note string) {
	if s == nil || s.program == nil || !s.homeMode {
		return
	}
	s.program.Send(previewFinished{note: note})
}

func (s *Session) SetPreferenceNote(note string) {
	if s == nil || s.program == nil || !s.homeMode {
		return
	}
	s.program.Send(preferencesNoteUpdate{note: note})
}

func (s *Session) setScreen(model Model) {
	s.stateMu.Lock()
	model.SetSize(s.width, s.height)
	s.phase = model.phase
	s.state = model.state
	s.activeModel = model
	s.stateMu.Unlock()
	s.program.Send(sessionUpdate{screen: &model})
}

func (s *Session) recordSize(width, height int) {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	s.width, s.height = width, height
	s.homeModel.SetSize(width, height)
	s.activeModel.SetSize(width, height)
	s.stateMu.Unlock()
}

func (s *Session) SetBrowser(path string, entries []BrowserEntry, hidden bool, note string) {
	if s == nil || s.program == nil || !s.homeMode {
		return
	}
	s.program.Send(browserUpdate{path: path, entries: entries, hidden: hidden, note: note})
}

func (s *Session) ExitAfterPlayback() {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	s.exitAfterPlayback = true
	s.stateMu.Unlock()
}

func (s *Session) Shutdown() {
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

func (s *Session) SetPage(page int) {
	s.mutate(func(state *State) { state.Page = max(0, page) }, 0)
}

func (s *Session) CancelChapterBrowser() {
	if s == nil || s.program == nil {
		return
	}
	s.stateMu.Lock()
	s.phase = PhasePlaying
	state := s.state
	s.stateMu.Unlock()
	s.program.Send(sessionUpdate{state: state, phase: PhasePlaying})
}

func (s *Session) SetPaused(paused bool) {
	s.mutate(func(state *State) { state.Paused = paused }, 0)
}

func (s *Session) SetPlaybackRate(rate float64) {
	s.mutate(func(state *State) { state.PlaybackRate = rate }, 0)
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
	if phase != 0 {
		s.phase = phase
	}
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
	if s.homeMode {
		s.stateMu.Lock()
		model := s.homeModel
		returnHome := s.phase != PhaseFinished && !s.exitAfterPlayback
		if returnHome {
			s.phase = PhaseHome
		}
		s.stateMu.Unlock()
		if returnHome {
			s.program.Send(sessionUpdate{screen: &model})
			return
		}
	}
	s.Shutdown()
}
