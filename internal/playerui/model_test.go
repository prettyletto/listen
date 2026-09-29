package playerui

import (
	textwidth "golang.org/x/text/width"
	"strings"
	"testing"
	"time"
	"unicode"
)

func TestPickerStartsAtFullBookByDefault(t *testing.T) {
	chapters := []Chapter{
		{ID: "one", Title: "Chapter One", StartIndex: 0, StartSeq: 0},
		{ID: "two", Title: "Chapter Two", StartIndex: 3, StartSeq: 3},
	}
	model := NewPicker("Example Book", chapters, nil)
	action, ok := model.HandleKey("enter")
	if !ok || action.Kind != ActionStart || action.Start.Label != "Read the full book from the beginning" || action.Start.StartIndex != 0 {
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
	}{{"left", ActionPrevious}, {"p", ActionPrevious}, {"right", ActionNext}, {"n", ActionNext}, {"space", ActionTogglePause}, {"q", ActionStop}} {
		action, ok := model.HandleKey(test.key)
		if !ok || action.Kind != test.want {
			t.Errorf("key %q = %#v, active=%v; want action %d", test.key, action, ok, test.want)
		}
		if test.want == ActionStop {
			model.SetPhase(PhasePlaying)
		}
	}
}

func TestViewsFitNarrowAndWideTerminalWidths(t *testing.T) {
	chapters := []Chapter{{Title: "日本語の章 — A title longer than a narrow terminal"}}
	for _, width := range []int{20, 40, 60, 80, 120, 160} {
		model := NewPlayer("A book title with 日本語 characters", chapters, 0, 9)
		model.SetSize(width, 24)
		model.SetPhase(PhasePlaying)
		model.SetState(State{
			Status: "Preparing a very long sentence with detail", Total: 9, Ready: 7,
			Position: 53 * time.Second, Duration: 3*time.Minute + 4*time.Second,
			ChapterIndex: 0, PlaybackKnown: true,
		})
		for lineNumber, line := range strings.Split(model.View(), "\n") {
			if got := cellWidth(line); got > width {
				t.Errorf("width %d line %d uses %d terminal cells: %q", width, lineNumber, got, line)
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
	if strings.ContainsAny(pickerView, "\x1b\x07\u009b\u202e\u2066\u2069") {
		t.Fatalf("picker view contains terminal control characters: %q", pickerView)
	}

	player := NewPlayer("Book \x1b[2J\u202e", chapters, 0, 4)
	player.SetPhase(PhasePlaying)
	player.SetState(State{Status: "Playing\x1b]52;c;clipboard\x07\u2066"})
	playerView := player.View()
	if strings.ContainsAny(playerView, "\x1b\x07\u009b\u202e\u2066\u2069") {
		t.Fatalf("player view contains terminal control characters: %q", playerView)
	}
}

func cellWidth(value string) int {
	cells := 0
	for _, r := range value {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if kind := textwidth.LookupRune(r).Kind(); kind == textwidth.EastAsianWide || kind == textwidth.EastAsianFullwidth {
			cells += 2
		} else {
			cells++
		}
	}
	return cells
}
