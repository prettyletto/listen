package playerui

import (
	"errors"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestInitialSessionSizeUsesTerminalDimensionsWhenValid(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "terminal-output")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	fallback := NewPlayer("Example", nil, 0, 0)
	fallback.SetSize(83, 27)
	queried := false
	width, height := initialSessionSize(output, fallback, func(fd uintptr) (int, int, error) {
		queried = true
		if fd != output.Fd() {
			t.Fatalf("terminal-size query received fd %d, want output fd %d", fd, output.Fd())
		}
		return 120, 30, nil
	})
	if !queried || width != 120 || height != 30 {
		t.Fatalf("initial session size = %dx%d (queried=%v), want 120x30", width, height, queried)
	}
}

func TestInitialSessionSizeFallsBackOnQueryFailureOrInvalidDimensions(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "terminal-output")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	fallback := NewPlayer("Example", nil, 0, 0)
	fallback.SetSize(83, 27)
	for _, test := range []struct {
		name  string
		query func(uintptr) (int, int, error)
	}{
		{name: "query error", query: func(uintptr) (int, int, error) { return 120, 30, errors.New("not a terminal") }},
		{name: "zero width", query: func(uintptr) (int, int, error) { return 0, 30, nil }},
		{name: "zero height", query: func(uintptr) (int, int, error) { return 120, 0, nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			width, height := initialSessionSize(output, fallback, test.query)
			if width != 83 || height != 27 {
				t.Fatalf("invalid terminal size produced %dx%d, want model defaults 83x27", width, height)
			}
		})
	}
}

func TestHomeSessionOverlaysRetainLibraryAndPendingSource(t *testing.T) {
	const source = "/books/selected.epub"
	entries := []LibraryEntry{{Path: "/books/recent.epub", Title: "Recent", ResumeLabel: "Chapter 2"}}
	home := NewHome(entries, "")
	active := home
	active.SetSetupChoice(source, "Prepare this source.")

	for _, phase := range []Phase{PhaseSetupChoice, PhaseSetupProgress, PhasePreferences, PhaseVoicePreview} {
		overlay := active
		switch phase {
		case PhaseSetupProgress:
			overlay.SetSetupProgress("Downloading local speech…")
		case PhasePreferences:
			overlay.SetPreferencesFor(Preferences{DefaultVoice: "af_heart"}, PhaseSetupChoice)
		case PhaseVoicePreview:
			overlay.SetPreferencesFor(Preferences{DefaultVoice: "af_heart"}, PhaseSetupChoice)
			overlay.phase = PhaseVoicePreview
		}
		got := homeOverlayModel(home, overlay)
		if got.Phase() != phase || got.setupSource != source {
			t.Errorf("overlay phase %v restored phase=%v source=%q", phase, got.Phase(), got.setupSource)
		}
		if gotEntries := got.HomeEntries(); len(gotEntries) != 1 || gotEntries[0].Path != entries[0].Path {
			t.Errorf("overlay phase %v lost library entries: %#v", phase, gotEntries)
		}
	}
}

func TestHomeSessionResizeIsInheritedByPreferencesOpenedLater(t *testing.T) {
	home := NewHome(nil, "")
	session := &Session{
		width: 80, height: 24, homeMode: true,
		homeModel: home, activeModel: home, phase: PhaseHome,
	}
	ui := newTeaModel(home, nil, nil)
	ui.onSize = session.recordSize
	updated, _ := ui.Update(tea.WindowSizeMsg{Width: 40, Height: 15})
	ui = updated.(*teaModel)
	if width, height := ui.screen.Size(); width != 40 || height != 15 {
		t.Fatalf("active home size = %dx%d, want 40x15", width, height)
	}
	session.stateMu.Lock()
	storedHome, storedActive := session.homeModel, session.activeModel
	storedWidth, storedHeight := session.width, session.height
	session.stateMu.Unlock()
	if storedWidth != 40 || storedHeight != 15 {
		t.Fatalf("session remembered %dx%d after resize, want 40x15", storedWidth, storedHeight)
	}
	preferences := homeOverlayModel(storedHome, storedActive)
	preferences.SetPreferencesFor(Preferences{
		DefaultVoice: "af_heart", SpeechSpeed: 1, PlaybackRate: 1,
		OCRLanguages: "eng", CloudPolicy: "ask",
		Locales:      []VoiceChoice{{Locale: "en-US", LocaleName: "American English"}},
		VoiceOptions: []VoiceChoice{{ID: "af_heart", Name: "Heart", Locale: "en-US"}},
	}, PhaseHome)
	if width, height := preferences.Size(); width != 40 || height != 15 {
		t.Fatalf("opened preferences size = %dx%d, want inherited 40x15", width, height)
	}
	view := preferences.View()
	if lipgloss.Height(view) > 15 {
		t.Fatalf("preferences opened after resize overflowed 40x15:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > 40 {
			t.Fatalf("preferences opened after resize overflowed width 40: %q", line)
		}
	}
}
