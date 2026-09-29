package playerui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestPickerStartsAtFullBookByDefault(t *testing.T) {
	chapters := []Chapter{
		{ID: "one", Title: "Chapter One", StartIndex: 0, StartSeq: 0},
		{ID: "two", Title: "Chapter Two", StartIndex: 3, StartSeq: 3},
	}
	model := NewPicker("Example Book", chapters, nil)
	action, ok := model.HandleKey("enter")
	if !ok || action.Kind != ActionStart || action.Start.Label != "Start from the beginning" || action.Start.StartIndex != 0 {
		t.Fatalf("initial selection = %#v, active=%v", action, ok)
	}
}

func TestPickerCanChooseChapterAndContinueFromItsFirstSegment(t *testing.T) {
	chapters := []Chapter{
		{ID: "one", Title: "Chapter One", StartIndex: 0, StartSeq: 0},
		{ID: "two", Title: "Chapter Two", StartIndex: 3, StartSeq: 3},
	}
	model := NewPicker("Example Book", chapters, nil)
	model.HandleKey("down") // Chapter One
	model.HandleKey("down") // Chapter Two
	action, ok := model.HandleKey("enter")
	if !ok || action.Kind != ActionStart || action.Start.StartIndex != 3 || action.Start.ChapterIndex != 1 {
		t.Fatalf("selected chapter = %#v, active=%v", action, ok)
	}
}

func TestPickerOffersResumeBeforeRestartChoices(t *testing.T) {
	chapters := []Chapter{{ID: "two", Title: "Chapter Two", StartIndex: 4, StartSeq: 4}}
	resume := &StartPoint{Label: "Resume at Chapter Two", StartIndex: 5, StartSeq: 5, Offset: 2 * time.Second, ChapterIndex: 0}
	model := NewPicker("Example Book", chapters, resume)
	action, ok := model.HandleKey("enter")
	if !ok || action.Kind != ActionStart || !action.Start.Resume || action.Start.StartSeq != 5 || action.Start.Offset != 2*time.Second {
		t.Fatalf("default resume choice = %#v, active=%v", action, ok)
	}
}

func TestPlayerKeysControlChaptersPauseAndStop(t *testing.T) {
	model := NewPlayer("Example Book", []Chapter{{Title: "One"}, {Title: "Two"}}, 0, 6)
	model.SetPhase(PhasePlaying)
	for _, test := range []struct {
		key  string
		want ActionKind
	}{{"left", ActionSeekBackward}, {"right", ActionSeekForward}, {"p", ActionPrevious}, {"n", ActionNext}, {"space", ActionTogglePause}, {"q", ActionStop}} {
		action, ok := model.HandleKey(test.key)
		if !ok || action.Kind != test.want {
			t.Errorf("key %q = %#v, active=%v; want action %d", test.key, action, ok, test.want)
		}
		if test.want == ActionStop {
			model.SetPhase(PhasePlaying)
		}
	}
}

func TestPickerShowsTheBookTitleOnceAndKeepsChapterRowsConcise(t *testing.T) {
	chapters := []Chapter{
		{Title: "Preface"},
		{Title: "Part I  ›  The first chapter"},
	}
	view := NewPicker("Vara: Building a Durable Job Runtime", chapters, nil).View()
	if got := strings.Count(view, "Vara: Building a Durable Job Runtime"); got != 1 {
		t.Fatalf("book title appears %d times, want once:\n%s", got, view)
	}
	if !strings.Contains(view, "Preface") || !strings.Contains(view, "Part I") {
		t.Fatalf("chapter rows are missing their concise labels:\n%s", view)
	}
	if strings.Contains(view, "Start at Vara:") {
		t.Fatalf("chapter rows repeat the book title:\n%s", view)
	}
}

func TestBubbleTeaPickerNavigationSearchAndSelection(t *testing.T) {
	chapters := []Chapter{
		{ID: "one", Title: "Preface", StartIndex: 0, StartSeq: 0},
		{ID: "two", Title: "Part I · Target Chapter", StartIndex: 3, StartSeq: 3},
	}
	actions := make(chan Action, 2)
	model := newTeaModel(NewPicker("Example Book", chapters, nil), actions, nil)
	model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if model.picker.GlobalIndex() != 1 {
		t.Fatalf("down arrow selected global index %d, want 1", model.picker.GlobalIndex())
	}
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if model.picker.FilterState() != list.Filtering {
		t.Fatal("slash did not open chapter search")
	}
	for _, char := range "Target" {
		_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{char}})
		runPickerCommand(model, cmd)
	}
	if got := len(model.picker.VisibleItems()); got != 1 {
		t.Fatalf("search left %d visible choices, want only the matching chapter", got)
	}
	if got := model.picker.GlobalIndex(); got != 2 {
		t.Fatalf("search selected global index %d, want original chapter index 2", got)
	}
	searchView := model.View()
	if !strings.Contains(searchView, "Search:") || !strings.Contains(searchView, "Target") {
		t.Fatalf("active search input is not visible in the picker:\n%s", searchView)
	}
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runPickerCommand(model, cmd)
	if model.picker.FilterState() != list.FilterApplied {
		t.Fatal("Enter did not apply chapter search")
	}
	if appliedView := model.View(); !strings.Contains(appliedView, "Search:") || !strings.Contains(appliedView, "Target") {
		t.Fatalf("applied search input is not visible in the picker:\n%s", appliedView)
	}
	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter did not dispatch the selected start point")
	}
	cmd()
	select {
	case action := <-actions:
		if action.Kind != ActionStart || action.Start.ChapterIndex != 1 || action.Start.StartIndex != 3 {
			t.Fatalf("selected action = %#v", action)
		}
	default:
		t.Fatal("selected action was not delivered")
	}
}

func runPickerCommand(model *teaModel, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, batched := range batch {
			runPickerCommand(model, batched)
		}
		return
	}
	model.Update(msg)
}

func TestBubbleTeaPlayerMapsSpaceAndChapterKeys(t *testing.T) {
	actions := make(chan Action, 4)
	model := newTeaModel(NewPlayer("Example Book", []Chapter{{Title: "One"}, {Title: "Two"}}, 0, 6), actions, nil)
	model.screen.SetPhase(PhasePlaying)
	for _, test := range []struct {
		msg  tea.KeyMsg
		want ActionKind
	}{
		{msg: tea.KeyMsg{Type: tea.KeySpace}, want: ActionTogglePause},
		{msg: tea.KeyMsg{Type: tea.KeyRight}, want: ActionSeekForward},
		{msg: tea.KeyMsg{Type: tea.KeyLeft}, want: ActionSeekBackward},
		{msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}, want: ActionNext},
		{msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}}, want: ActionPrevious},
	} {
		_, cmd := model.Update(test.msg)
		if cmd == nil {
			t.Fatalf("key %q did not dispatch an action", test.msg.String())
		}
		cmd()
		action := <-actions
		if action.Kind != test.want {
			t.Errorf("key %q action = %d, want %d", test.msg.String(), action.Kind, test.want)
		}
	}
}

func TestPlayerShowsStyledTimelineAndPendingSeekFeedback(t *testing.T) {
	model := NewPlayer("Example Book", []Chapter{{Title: "Opening"}}, 0, 10)
	model.SetPhase(PhasePlaying)
	model.SetState(State{
		Status: "Playing", Ready: 3, Total: 10, Sequence: 0,
		Position: 5 * time.Second, Duration: 20 * time.Second,
		Paused: true, SeekPending: 5 * time.Second, PlaybackKnown: true,
	})
	view := model.View()
	for _, expected := range []string{"Buffering +00:05", "+00:05", "00:05", "00:20", "AUDIO READY 3/10", "PASSAGE  1 / 10", "←/→ seek 5s"} {
		if !strings.Contains(view, expected) {
			t.Errorf("player view is missing %q:\n%s", expected, view)
		}
	}
}

func TestViewsFitResponsiveTerminalWidthsAndHeights(t *testing.T) {
	chapters := []Chapter{{Title: "日本語の章 — A title longer than a narrow terminal"}}
	for _, size := range []struct{ width, height int }{
		{20, 8}, {32, 10}, {40, 14}, {60, 18}, {80, 24}, {120, 30}, {160, 44},
	} {
		picker := NewPicker("A book title with 日本語 characters that is deliberately very long", chapters, nil)
		picker.SetSize(size.width, size.height)
		player := NewPlayer("A book title with 日本語 characters that is deliberately very long", chapters, 0, 9)
		player.SetSize(size.width, size.height)
		player.SetPhase(PhasePlaying)
		player.SetState(State{
			Status: "Preparing a very long sentence with detail", Total: 9, Ready: 7,
			Position: 53 * time.Second, Duration: 3*time.Minute + 4*time.Second,
			ChapterIndex: 0, PlaybackKnown: true,
		})
		filteredPicker := newTeaModel(NewPicker(
			"A book title with 日本語 characters that is deliberately very long", chapters, nil,
		), nil, nil)
		filteredPicker.screen.SetSize(size.width, size.height)
		filteredPicker.picker.SetFilterText("title")
		for _, view := range []struct {
			name string
			text string
		}{{"picker", picker.View()}, {"filtered picker", filteredPicker.View()}, {"player", player.View()}} {
			if view.name == "player" && size.width == 40 && size.height == 14 && !strings.Contains(view.text, "AUDIO READY") {
				t.Errorf("player at %dx%d hides streaming readiness feedback:\n%s", size.width, size.height, view.text)
			}
			if view.name == "filtered picker" && size.width == 32 && !strings.Contains(view.text, "q cancel") {
				t.Errorf("filtered picker at %dx%d hides the cancel key:\n%s", size.width, size.height, view.text)
			}
			for lineNumber, line := range strings.Split(view.text, "\n") {
				if got := lipgloss.Width(line); got > size.width {
					t.Errorf("%s at %dx%d line %d uses %d terminal cells", view.name, size.width, size.height, lineNumber, got)
				}
			}
			if got := lipgloss.Height(view.text); got > size.height {
				t.Errorf("%s at %dx%d uses %d rows:\n%s", view.name, size.width, size.height, got, view.text)
			}
		}
	}
}

func TestPickerQuitCancelsSelection(t *testing.T) {
	model := NewPicker("Example Book", []Chapter{{Title: "One"}}, nil)
	action, ok := model.HandleKey("q")
	if !ok || action.Kind != ActionStop || model.Phase() != PhaseFinished {
		t.Fatalf("quit action = %#v, active=%v phase=%d", action, ok, model.Phase())
	}
}

func TestViewsReplaceDocumentControlCharactersBeforeWritingToTerminal(t *testing.T) {
	chapters := []Chapter{{Title: "Chapter \x1b[31mRed\x1b[0m\u009b2J \u202ehidden\u2066"}}
	picker := NewPicker("Book \x1b]52;c;clipboard\x07 \u202ehidden\u2069", chapters, nil)
	pickerView := picker.View()
	if containsTerminalInjection(pickerView) {
		t.Fatalf("picker view contains terminal control characters: %q", pickerView)
	}

	player := NewPlayer("Book \x1b[2J\u202e", chapters, 0, 4)
	player.SetPhase(PhasePlaying)
	player.SetState(State{Status: "Playing\x1b]52;c;clipboard\x07\u2066"})
	playerView := player.View()
	if containsTerminalInjection(playerView) {
		t.Fatalf("player view contains terminal control characters: %q", playerView)
	}
}

func containsTerminalInjection(value string) bool {
	for _, forbidden := range []string{"\x1b[31m", "\x1b]52;", "\x07", "\u009b", "\u202e", "\u2066", "\u2069"} {
		if strings.Contains(value, forbidden) {
			return true
		}
	}
	return false
}
