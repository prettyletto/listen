package playerui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/width"
)

type Chapter struct {
	ID         string
	Title      string
	StartIndex int
	StartSeq   uint64
}

type StartPoint struct {
	Label        string
	StartIndex   int
	StartSeq     uint64
	Offset       time.Duration
	ChapterIndex int
	Resume       bool
}

type ActionKind uint8

const (
	ActionStart ActionKind = iota + 1
	ActionPrevious
	ActionNext
	ActionTogglePause
	ActionSeekBackward
	ActionSeekForward
	ActionStop
)

type Action struct {
	Kind  ActionKind
	Start StartPoint
}

type Phase uint8

const (
	PhaseSelect Phase = iota + 1
	PhasePreparing
	PhasePlaying
	PhaseFinished
)

type State struct {
	Status        string
	Ready         int
	Total         int
	BookTotal     int
	Sequence      uint64
	Position      time.Duration
	Duration      time.Duration
	ChapterIndex  int
	Paused        bool
	SeekPending   time.Duration
	PlaybackKnown bool
}

type Model struct {
	phase    Phase
	title    string
	chapters []Chapter
	choices  []StartPoint
	cursor   int
	width    int
	height   int
	state    State
}

func NewPicker(title string, chapters []Chapter, resume *StartPoint) Model {
	chapters = append([]Chapter(nil), chapters...)
	choices := make([]StartPoint, 0, len(chapters)+2)
	if resume != nil {
		point := *resume
		point.Resume = true
		choices = append(choices, point)
	}
	choices = append(choices, StartPoint{Label: "Start from the beginning", ChapterIndex: 0})
	for index, chapter := range chapters {
		choices = append(choices, StartPoint{
			Label:        chapter.Title,
			StartIndex:   chapter.StartIndex,
			StartSeq:     chapter.StartSeq,
			ChapterIndex: index,
		})
	}
	state := State{Status: "Choose a starting point"}
	return Model{phase: PhaseSelect, title: title, chapters: chapters, choices: choices, width: 80, height: 24, state: state}
}

func NewPlayer(title string, chapters []Chapter, initialChapter, total int) Model {
	chapters = append([]Chapter(nil), chapters...)
	if initialChapter < 0 || initialChapter >= len(chapters) {
		initialChapter = 0
	}
	status := "Preparing opening audio"
	return Model{
		phase: PhasePreparing, title: title, chapters: chapters, width: 80, height: 24,
		state: State{Status: status, ChapterIndex: initialChapter, Total: total, BookTotal: total},
	}
}

func (m *Model) SetSize(width, height int) {
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	m.width, m.height = width, height
}

func (m Model) Size() (int, int) { return m.width, m.height }

func (m *Model) SetPhase(phase Phase) { m.phase = phase }

func (m *Model) SetState(state State) { m.state = state }

func (m *Model) SetStatus(status string) { m.state.Status = status }

func (m *Model) SetProgress(ready, total int) {
	m.state.Ready = max(0, ready)
	m.state.Total = max(0, total)
}

func (m Model) Phase() Phase { return m.phase }

func (m Model) SelectedStart() (StartPoint, bool) {
	if m.phase != PhaseSelect || m.cursor < 0 || m.cursor >= len(m.choices) {
		return StartPoint{}, false
	}
	return m.choices[m.cursor], true
}

// HandleKey updates the model and returns any command that the application
// must execute. Rendering remains a pure projection of model state.
func (m *Model) HandleKey(key string) (Action, bool) {
	if m.phase == PhaseSelect {
		switch key {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor+1 < len(m.choices) {
				m.cursor++
			}
		case "enter":
			if choice, ok := m.SelectedStart(); ok {
				m.phase = PhasePreparing
				m.state.Status = "Preparing " + choice.Label
				return Action{Kind: ActionStart, Start: choice}, true
			}
		case "q", "ctrl+c":
			m.phase = PhaseFinished
			return Action{Kind: ActionStop}, true
		}
		return Action{}, false
	}
	if m.phase == PhaseFinished {
		return Action{}, false
	}
	if key == "q" || key == "ctrl+c" {
		m.phase = PhaseFinished
		m.state.Status = "Stopping playback"
		return Action{Kind: ActionStop}, true
	}
	if m.phase == PhasePreparing {
		return Action{}, false
	}
	switch key {
	case "space":
		return Action{Kind: ActionTogglePause}, true
	case "left":
		return Action{Kind: ActionSeekBackward}, true
	case "right":
		return Action{Kind: ActionSeekForward}, true
	case "p", "h":
		return Action{Kind: ActionPrevious}, true
	case "n", "l":
		return Action{Kind: ActionNext}, true
	}
	return Action{}, false
}

func (m Model) View() string {
	return newTeaModel(m, nil, nil).View()
}

func clock(value time.Duration) string {
	seconds := int64(value.Round(time.Second) / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	hours := seconds / 3600
	minutes := seconds % 3600 / 60
	seconds %= 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func clip(value string, maxCells int) string {
	if maxCells <= 0 {
		return ""
	}
	var out strings.Builder
	cells := 0
	runes := []rune(value)
	for index, r := range runes {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			r = ' '
		}
		cellWidth := 1
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
			cellWidth = 0
		} else if kind := width.LookupRune(r).Kind(); kind == width.EastAsianWide || kind == width.EastAsianFullwidth {
			cellWidth = 2
		}
		limit := maxCells
		if index+1 < len(runes) && maxCells > 1 {
			limit--
		}
		if cells+cellWidth > limit {
			if cells < maxCells {
				out.WriteRune('…')
			}
			return out.String()
		}
		out.WriteRune(r)
		cells += cellWidth
	}
	return out.String()
}

func clamp(value, lower, upper int) int {
	if value < lower {
		return lower
	}
	if value > upper {
		return upper
	}
	return value
}
