package playerui

import (
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
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

func TestHomeActionOpensSelectedSourceAndEscapeReturnsFromPlayer(t *testing.T) {
	model := NewHome([]LibraryEntry{{Path: "/books/one.epub", Title: "One"}}, "")
	actions := make(chan Action, 2)
	ui := newTeaModel(model, actions, nil)
	updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = updated.(*teaModel)
	if cmd != nil {
		cmd()
	}
	if action := <-actions; action.Kind != ActionOpenSource || action.Path != "/books/one.epub" {
		t.Fatalf("home Enter action = %#v", action)
	}
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	ui = updated.(*teaModel)
	if ui.screen.Phase() != PhaseAddPath {
		t.Fatalf("Add key phase = %v, want path entry", ui.screen.Phase())
	}
	path := "/books/a book.epub"
	for _, char := range path {
		updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{char}})
		ui = updated.(*teaModel)
	}
	updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = updated.(*teaModel)
	if cmd != nil {
		cmd()
	}
	if action := <-actions; action.Kind != ActionOpenSource || action.Path != path {
		t.Fatalf("path entry action = %#v", action)
	}
	player := NewPlayer("One", []Chapter{{Title: "Opening"}}, 0, 1)
	player.SetPhase(PhasePlaying)
	action, ok := player.HandleKey("esc")
	if !ok || action.Kind != ActionReturnHome {
		t.Fatalf("Escape = %#v, active=%v; want return-home action", action, ok)
	}
}

func TestQuittingHomeAndSetupOverlaysDoesNotRenderThePlayerScreen(t *testing.T) {
	t.Run("empty home", func(t *testing.T) {
		actions := make(chan Action, 1)
		ui := newTeaModel(NewHome(nil, ""), actions, nil)
		updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
		ui = updated.(*teaModel)
		if cmd == nil {
			t.Fatal("home quit did not dispatch the exit action")
		}
		cmd()
		if action := <-actions; action.Kind != ActionExit {
			t.Fatalf("home quit action = %#v, want exit", action)
		}
		view := ui.View()
		if !strings.Contains(view, "Your listening home") || strings.Contains(view, "NOW PLAYING") {
			t.Fatalf("home quit rendered an unrelated screen:\n%s", view)
		}
	})

	t.Run("setup choice", func(t *testing.T) {
		model := NewHome(nil, "")
		model.SetSetupChoice("/books/book.epub", "Start when ready.")
		if action, ok := model.HandleKey("q"); !ok || action.Kind != ActionExit {
			t.Fatalf("setup quit = %#v active=%v", action, ok)
		}
		view := newTeaModel(model, nil, nil).View()
		if !strings.Contains(view, "Prepare speech for this source") || strings.Contains(view, "NOW PLAYING") {
			t.Fatalf("setup quit rendered an unrelated screen:\n%s", view)
		}
	})
}

func TestQuittingBrowseAndMissingScreensDoesNotRenderThePlayerScreen(t *testing.T) {
	browse := NewHome(nil, "")
	browse.SetBrowser("/books", []BrowserEntry{{Name: "book.epub", Path: "/books/book.epub"}}, false, "")
	missing := NewHome([]LibraryEntry{{Path: "/old/book.epub", Title: "Book", Missing: true}}, "")
	missing.OpenMissing("/old/book.epub")

	for _, test := range []struct {
		name     string
		model    Model
		expected string
	}{
		{name: "browse", model: browse, expected: "ADD FROM THIS FOLDER"},
		{name: "missing", model: missing, expected: "This source can’t be found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			actions := make(chan Action, 1)
			ui := newTeaModel(test.model, actions, nil)
			updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
			ui = updated.(*teaModel)
			if cmd == nil {
				t.Fatal("quit did not dispatch the exit action")
			}
			cmd()
			if action := <-actions; action.Kind != ActionExit {
				t.Fatalf("quit action = %#v, want exit", action)
			}
			view := ui.View()
			if !strings.Contains(view, test.expected) || strings.Contains(view, "NOW PLAYING") {
				t.Fatalf("quit rendered the wrong screen:\n%s", view)
			}
		})
	}
}

func TestInvalidAddedSourceReturnsToEditablePathForm(t *testing.T) {
	const path = "/books/a missing book.epub"
	const note = "Could not find the selected source. Check the path and choose it again."
	model := NewHome([]LibraryEntry{{Path: "/books/recent.epub", Title: "Recent"}}, "")
	model.SetAddPath(path, note)
	ui := newTeaModel(model, nil, nil)

	if ui.screen.Phase() != PhaseAddPath || ui.pathInput.Value() != path {
		t.Fatalf("invalid source form phase=%v value=%q; want editable %q", ui.screen.Phase(), ui.pathInput.Value(), path)
	}
	if view := ui.View(); !strings.Contains(view, path) || !strings.Contains(view, note) {
		t.Fatalf("invalid source form lost its correction path or error:\n%s", view)
	}
}

func TestHomeLayoutsFitCommonTerminalSizes(t *testing.T) {
	entries := []LibraryEntry{{Path: "/books/one.epub", Title: "A very long book title 日本語", ResumeLabel: "Saved place"}}
	for _, size := range []struct{ width, height int }{{40, 15}, {80, 24}, {120, 30}} {
		model := NewHome(entries, "Your place is saved. Select it to continue.")
		model.SetSize(size.width, size.height)
		view := model.View()
		if got := lipgloss.Height(view); got > size.height {
			t.Errorf("home at %dx%d uses %d rows:\n%s", size.width, size.height, got, view)
		}
		for lineNumber, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > size.width {
				t.Errorf("home at %dx%d line %d uses %d cells", size.width, size.height, lineNumber, got)
			}
		}
	}
}

func TestHomeShowsActionTitleAndSavedPositionWithLongTitlesWithoutColor(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	const title = "Fundamentals of Software Architecture: An Engineering Approach, Second Edition"
	const position = "Chapter 22"
	entries := []LibraryEntry{
		{Path: "/books/fundamentals.epub", Title: title, ResumeLabel: position},
		{Path: "/books/other.epub", Title: "A second book"},
	}
	for _, size := range [][2]int{{40, 15}, {80, 24}, {120, 30}} {
		screen := NewHome(entries, "Your place is saved.")
		screen.SetSize(size[0], size[1])
		view := newTeaModel(screen, nil, nil).View()
		if !strings.Contains(view, "Continue listening") || !strings.Contains(view, "Continue") {
			t.Errorf("home at %dx%d hides the continue action:\n%s", size[0], size[1], view)
		}
		if !strings.Contains(view, "Fundamentals") {
			t.Errorf("home at %dx%d hides the long source title:\n%s", size[0], size[1], view)
		}
		if got := strings.Count(view, "Fundamentals"); got != 1 {
			t.Errorf("home at %dx%d should show the featured source title once, got %d:\n%s", size[0], size[1], got, view)
		}
		if got := strings.Count(view, "Saved at "+position); got != 1 {
			t.Errorf("home at %dx%d should show a labeled saved location once, got %d:\n%s", size[0], size[1], got, view)
		}
		if !strings.Contains(view, "RECENT SOURCES") || !strings.Contains(view, "A second book") {
			t.Errorf("home at %dx%d should show other sources under Recents:\n%s", size[0], size[1], view)
		}
		if got := strings.Count(view, "›"); got != 0 {
			t.Errorf("home at %dx%d highlights a recent row while Continue has focus:\n%s", size[0], size[1], view)
		}
		if strings.Contains(view, "\x1b[") {
			t.Errorf("no-color home at %dx%d contains ANSI styling: %q", size[0], size[1], view)
		}
		if got := lipgloss.Height(view); got > size[1] {
			t.Errorf("home at %dx%d uses %d rows:\n%s", size[0], size[1], got, view)
		}
		for lineNumber, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > size[0] {
				t.Errorf("home at %dx%d line %d uses %d cells: %q", size[0], size[1], lineNumber, got, line)
			}
		}
	}
}

func TestNarrowEmptyHomeShowsEveryPrimaryActionAndOpensSetup(t *testing.T) {
	model := NewHome(nil, "")
	model.SetSize(40, 15)
	view := model.View()
	for _, affordance := range []string{"Add source", "Browse", "Setup / repair", "Settings", "? help", "q quit"} {
		if !strings.Contains(view, affordance) {
			t.Errorf("40x15 home hides %q:\n%s", affordance, view)
		}
	}
	if got := lipgloss.Height(view); got > 15 {
		t.Fatalf("40x15 home uses %d rows:\n%s", got, view)
	}
	actions := make(chan Action, 1)
	ui := newTeaModel(model, actions, nil)
	updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	ui = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("home setup shortcut did not dispatch")
	}
	cmd()
	if action := <-actions; action.Kind != ActionOpenSetup {
		t.Fatalf("home setup shortcut = %#v, want source-free setup", action)
	}
}

func TestCompactHomeHintsWhenRecentSourcesContinueBelowTheFold(t *testing.T) {
	entries := []LibraryEntry{
		{Path: "/books/current.epub", Title: "Current", ResumeLabel: "Chapter 3"},
		{Path: "/books/one.epub", Title: "One"},
		{Path: "/books/two.epub", Title: "Two"},
		{Path: "/books/three.epub", Title: "Three"},
	}
	model := NewHome(entries, "")
	model.SetSize(40, 15)
	view := newTeaModel(model, nil, nil).View()
	for _, text := range []string{"RECENT SOURCES · 3", "↓ more · / search · ? help · q quit", "s  Setup / repair", "p  Settings"} {
		if !strings.Contains(view, text) {
			t.Errorf("40x15 home does not communicate %q:\n%s", text, view)
		}
	}
	if got := lipgloss.Height(view); got > 15 {
		t.Fatalf("40x15 home uses %d rows:\n%s", got, view)
	}
	for lineNumber, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 40 {
			t.Errorf("40x15 line %d uses %d terminal cells", lineNumber, got)
		}
	}
}

func TestEmptyHomeUsesVerticalSpaceAndPresentsFirstStep(t *testing.T) {
	model := NewHome(nil, "")
	model.SetSize(80, 24)
	view := model.View()
	if !strings.Contains(view, "Start with a local book or document.") || !strings.Contains(view, "Add source") || !strings.Contains(view, "Browse") {
		t.Fatalf("empty invitation does not show its first actions:\n%s", view)
	}
	if got := strings.Count(view, "Add source"); got != 1 {
		t.Fatalf("empty invitation repeats Add source %d times:\n%s", got, view)
	}
	if got := strings.Count(view, "Browse"); got != 1 {
		t.Fatalf("empty invitation repeats Browse %d times:\n%s", got, view)
	}
	if got := lipgloss.Height(view); got != 24 {
		t.Fatalf("standard empty home height = %d, want it placed intentionally in 24 rows", got)
	}
	lines := strings.Split(view, "\n")
	first, last := -1, -1
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if first < 0 {
			first = index
		}
		last = index
	}
	if first < 2 || last >= len(lines)-2 {
		t.Fatalf("empty home was not balanced vertically (first=%d,last=%d):\n%s", first, last, view)
	}
}

func TestHomeWithoutRecentRowsCentersTheContinueShelf(t *testing.T) {
	model := NewHome([]LibraryEntry{{Path: "/books/current.epub", Title: "Current", ResumeLabel: "Chapter 2"}}, "")
	model.SetSize(80, 24)
	view := model.View()
	if got := lipgloss.Height(view); got != 24 {
		t.Fatalf("single-source home height = %d, want a balanced 24-row layout", got)
	}
	lines := strings.Split(view, "\n")
	first, last := -1, -1
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if first < 0 {
			first = index
		}
		last = index
	}
	if first < 2 || last >= len(lines)-2 {
		t.Fatalf("single-source home was not balanced vertically (first=%d,last=%d):\n%s", first, last, view)
	}
}

func TestPopulatedHomeCentersWhenWideAndShorterThanTerminal(t *testing.T) {
	entries := []LibraryEntry{
		{Path: "/books/current.epub", Title: "Current", ResumeLabel: "Chapter 2"},
		{Path: "/books/next.epub", Title: "Next book"},
	}
	model := NewHome(entries, "")
	model.SetSize(120, 30)
	view := newTeaModel(model, nil, nil).View()
	if got := lipgloss.Height(view); got != 30 {
		t.Fatalf("wide populated home height = %d, want intentional 30-row layout", got)
	}
	first, last := -1, -1
	for index, line := range strings.Split(view, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if first < 0 {
			first = index
		}
		last = index
	}
	if first < 3 || last >= 27 {
		t.Fatalf("wide populated home does not balance its vertical space (first=%d,last=%d):\n%s", first, last, view)
	}
	if !strings.Contains(view, "RECENT SOURCES · 1") || !strings.Contains(view, "Next book") {
		t.Fatalf("centering hid the recent shelf:\n%s", view)
	}
}

func TestHomeSearchDisplaysQueryZeroResultsAndEscapeRecovery(t *testing.T) {
	entries := []LibraryEntry{
		{Path: "/books/current.epub", Title: "Current", ResumeLabel: "Chapter 2"},
		{Path: "/books/other.epub", Title: "Another book"},
	}
	ui := newTeaModel(NewHome(entries, ""), nil, nil)
	updated, _ := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	ui = updated.(*teaModel)
	ui.homeList.SetFilterText("not found")
	view := ui.View()
	for _, want := range []string{"Search: not found", "SEARCH RESULTS · 0", "No recent sources match"} {
		if !strings.Contains(view, want) {
			t.Errorf("home search does not show %q:\n%s", want, view)
		}
	}
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyEsc})
	ui = updated.(*teaModel)
	if ui.homeList.FilterState() != list.Unfiltered {
		t.Fatal("Escape did not clear the applied home search")
	}
	view = ui.View()
	if strings.Contains(view, "Search:") || !strings.Contains(view, "Another book") {
		t.Fatalf("Escape did not restore the recent list:\n%s", view)
	}
}

func TestNarrowHomeSearchKeepsQueryAndExitControlInsideTerminal(t *testing.T) {
	entries := []LibraryEntry{
		{Path: "/books/current.epub", Title: "Current", ResumeLabel: "Chapter 2"},
		{Path: "/books/finished.epub", Title: "Finished source"},
	}
	screen := NewHome(entries, "")
	screen.SetSize(40, 15)
	ui := newTeaModel(screen, nil, nil)
	updated, _ := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	ui = updated.(*teaModel)
	ui.homeList.SetFilterText("Finished")
	view := ui.View()
	for _, want := range []string{"Search: Finished", "Finished source", "Esc clear · Enter open · ↑/↓ move"} {
		if !strings.Contains(view, want) {
			t.Errorf("narrow search hides %q:\n%s", want, view)
		}
	}
	if height := lipgloss.Height(view); height > 15 {
		t.Fatalf("40x15 search uses %d rows:\n%s", height, view)
	}
}

func TestSourceFreeSetupCanStartCustomizeCancelAndReturnHome(t *testing.T) {
	model := NewHome(nil, "")
	model.SetSetupChoice("", "Prepare or repair local speech for this device.")
	view := model.View()
	if !strings.Contains(view, "Prepare local speech") || !strings.Contains(view, "no book needed") {
		t.Fatalf("source-free setup does not explain its scope:\n%s", view)
	}
	if action, ok := model.HandleKey("c"); !ok || action.Kind != ActionCustomizeSetup {
		t.Fatalf("customize from source-free setup = %#v active=%v", action, ok)
	}
	model.SetSetupChoice("", "Ready when you are.")
	if action, ok := model.HandleKey("enter"); !ok || action.Kind != ActionStartRecommendedSetup || model.setupSource != "" {
		t.Fatalf("start source-free setup = %#v active=%v source=%q", action, ok, model.setupSource)
	}
	model.SetSetupChoice("", "Ready when you are.")
	if action, ok := model.HandleKey("esc"); !ok || action.Kind != ActionCancelSetup || model.Phase() != PhaseHome {
		t.Fatalf("cancel source-free setup = %#v active=%v phase=%v", action, ok, model.Phase())
	}
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })
	narrow := NewHome(nil, "")
	narrow.SetSize(40, 15)
	narrow.SetSetupChoice("", "")
	setupView := newTeaModel(narrow, nil, nil).View()
	if !strings.Contains(setupView, "Set voice and speed first") {
		t.Fatalf("40x15 setup customization action is hidden or wrapped away:\n%s", setupView)
	}
	if !strings.Contains(setupView, "Local voices · no book needed") {
		t.Fatalf("40x15 source-free setup description is clipped:\n%s", setupView)
	}
	if got := lipgloss.Height(setupView); got > 15 {
		t.Fatalf("40x15 setup screen uses %d rows:\n%s", got, setupView)
	}
}

func TestPreferenceValueStylingDoesNotLeakTruncatedANSIAtNarrowWidth(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previousProfile)
	lipgloss.SetColorProfile(termenv.TrueColor)
	model := NewHome(nil, "")
	model.SetSize(40, 15)
	model.SetPreferencesFor(Preferences{
		DefaultVoice: "af_heart", SpeechSpeed: 1, PlaybackRate: 1,
		OCRLanguages: "eng", CloudPolicy: "ask",
		Locales:      []VoiceChoice{{Locale: "en-US", LocaleName: "American English"}},
		VoiceOptions: []VoiceChoice{{ID: "af_heart", Name: "Heart", Locale: "en-US"}},
		Voices:       map[string]string{"en-US": "af_heart"},
	}, PhaseHome)
	view := model.View()
	completeSGR := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	plain := completeSGR.ReplaceAllString(view, "")
	if strings.Contains(plain, "\x1b[") || strings.Contains(plain, "[1;38;5;") || strings.Contains(plain, "[38;5;") {
		t.Fatalf("preference value truncation leaked styling codes:\n%s", plain)
	}
}

func TestAdvancedSettingsEditOCRAndTranslationPrivacyInOneSave(t *testing.T) {
	preferences := Preferences{
		DefaultVoice: "af_heart", SpeechSpeed: 1, PlaybackRate: 1,
		OCRLanguages: "eng", CloudPolicy: "ask", SetupReady: true,
		SetupStatus:  "Local speech is ready on this device.",
		Locales:      []VoiceChoice{{Locale: "en-US", LocaleName: "American English"}},
		VoiceOptions: []VoiceChoice{{ID: "af_heart", Name: "Heart", Locale: "en-US"}},
	}
	model := NewHome(nil, "")
	model.SetSize(40, 15)
	model.SetPreferencesFor(preferences, PhaseHome)
	actions := make(chan Action, 1)
	ui := newTeaModel(model, actions, nil)
	for range 4 { // default voice, locale, speeds, then advanced settings
		updated, _ := ui.Update(tea.KeyMsg{Type: tea.KeyDown})
		ui = updated.(*teaModel)
	}
	updated, _ := ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = updated.(*teaModel)
	if ui.screen.Phase() != PhaseAdvancedPreferences {
		t.Fatalf("Enter on Advanced settings opened phase %v", ui.screen.Phase())
	}
	view := ui.View()
	if got := lipgloss.Height(view); got > 15 {
		t.Fatalf("advanced settings at 40x15 uses %d rows:\n%s", got, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 40 {
			t.Fatalf("advanced settings row uses %d cells at width 40: %q", got, line)
		}
	}
	for _, char := range "+por" {
		updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{char}})
		ui = updated.(*teaModel)
	}
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyTab})
	ui = updated.(*teaModel)
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyRight})
	ui = updated.(*teaModel)
	if ui.screen.preferences.CloudPolicy != "never" {
		t.Fatalf("next privacy choice = %q, want never", ui.screen.preferences.CloudPolicy)
	}
	policyView := ui.View()
	if !strings.Contains(policyView, "Keep translation local") {
		t.Fatalf("40x15 selected privacy policy is hidden:\n%s", policyView)
	}
	if !strings.Contains(policyView, "Esc back") {
		t.Fatalf("40x15 advanced settings hide the Escape control:\n%s", policyView)
	}
	for _, line := range strings.Split(policyView, "\n") {
		if strings.Contains(line, "Keep translation local") && strings.HasSuffix(strings.TrimSpace(line), "…") {
			t.Fatalf("40x15 selected privacy policy wraps into a trailing ellipsis:\n%s", policyView)
		}
	}
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = updated.(*teaModel)
	if ui.screen.Phase() != PhasePreferences || ui.screen.preferences.OCRLanguages != "eng+por" {
		t.Fatalf("advanced settings return phase=%v OCR=%q", ui.screen.Phase(), ui.screen.preferences.OCRLanguages)
	}
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyDown}) // Save preferences
	ui = updated.(*teaModel)
	updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = updated.(*teaModel)
	if cmd != nil {
		cmd()
	}
	if action := <-actions; action.Kind != ActionSavePreferences || action.Preferences.OCRLanguages != "eng+por" || action.Preferences.CloudPolicy != "never" {
		t.Fatalf("saved advanced choices = %#v", action)
	}
	if ui.screen.Phase() != PhasePreferences {
		t.Fatalf("saving preferences changed the visible phase to %v", ui.screen.Phase())
	}
}

func TestPDFRestorationScreenKeepsEscapeCancellationVisibleAtNarrowSizes(t *testing.T) {
	for _, size := range [][2]int{{40, 15}, {80, 24}, {120, 30}} {
		model := NewPlayer("A scanned book", nil, 0, 0)
		model.SetStatus("Restoring your place · checking PDF page 2/8")
		model.SetSize(size[0], size[1])
		view := model.View()
		if !strings.Contains(view, "Esc cancel") || !strings.Contains(view, "q exit") {
			t.Errorf("restoration controls missing at %dx%d:\n%s", size[0], size[1], view)
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
	runPickerCommand(model, cmd)
	select {
	case action := <-actions:
		if action.Kind != ActionStart || action.Start.ChapterIndex != 1 || action.Start.StartIndex != 3 {
			t.Fatalf("selected action = %#v", action)
		}
	default:
		t.Fatal("selected action was not delivered")
	}
}

func TestChapterBrowserShowsPrimaryStopsAndSearchSelectsNestedBoundary(t *testing.T) {
	chapters := []Chapter{
		{ID: "chapter-1", Title: "Chapter 1", StartIndex: 0, StartSeq: 0, Primary: true},
		{ID: "section-1-1", Title: "Chapter 1  ›  Section 1.1", StartIndex: 2, StartSeq: 2},
		{ID: "section-1-2", Title: "Chapter 1  ›  Section 1.2", StartIndex: 4, StartSeq: 4},
		{ID: "chapter-2", Title: "Chapter 2", StartIndex: 7, StartSeq: 7, Primary: true},
	}
	model := NewPlayer("Example Book", chapters, 0, 10)
	model.SetPhase(PhasePlaying)
	model.SetState(State{Status: "Paused", Paused: true})
	actions := make(chan Action, 4)
	ui := newTeaModel(model, actions, nil)
	updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	ui = updated.(*teaModel)
	runPickerCommand(ui, cmd)
	if action := <-actions; action.Kind != ActionOpenChapters {
		t.Fatalf("open chapter browser action = %#v", action)
	}
	if len(ui.screen.choices) != 2 || ui.screen.choices[0].ChapterIndex != 0 || ui.screen.choices[1].ChapterIndex != 3 {
		t.Fatalf("default browser choices = %#v, want only the two primary stops", ui.screen.choices)
	}
	if view := ui.View(); strings.Contains(view, "Section 1.1") || strings.Contains(view, "Section 1.2") {
		t.Fatalf("default chapter view exposes nested sections:\n%s", view)
	}

	updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	ui = updated.(*teaModel)
	runPickerCommand(ui, cmd)
	updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Section 1.2")})
	ui = updated.(*teaModel)
	runPickerCommand(ui, cmd)
	if got := len(ui.picker.VisibleItems()); got != 1 {
		t.Fatalf("section search returned %d choices, want 1", got)
	}
	if got := ui.picker.GlobalIndex(); got != 2 {
		t.Fatalf("nested section search selected global index %d, want original full-list index 2", got)
	}
	_, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runPickerCommand(ui, cmd) // Apply the list filter.
	_, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runPickerCommand(ui, cmd) // Choose the filtered section.
	select {
	case action := <-actions:
		if action.Kind != ActionSelectChapter || action.Start.StartIndex != 4 || action.Start.StartSeq != 4 || action.Start.ChapterIndex != 2 {
			t.Fatalf("nested section selection action = %#v", action)
		}
	default:
		t.Fatal("nested section selection was not dispatched")
	}
}

func TestChapterSearchCanReachAppendixEntriesWithoutShowingThemAsMainChapters(t *testing.T) {
	chapters := []Chapter{
		{ID: "chapter-1", Title: "Chapter 1", StartIndex: 0, Primary: true},
		{ID: "discussion-question-17", Title: "Discussion Questions  ›  Chapter 17", StartIndex: 26, StartSeq: 26},
		{ID: "chapter-2", Title: "Part I  ›  Chapter 2", StartIndex: 31, Primary: true},
	}
	model := NewPlayer("Example Book", chapters, 0, 40)
	model.SetPhase(PhasePlaying)
	actions := make(chan Action, 4)
	ui := newTeaModel(model, actions, nil)
	updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	ui = updated.(*teaModel)
	runPickerCommand(ui, cmd)
	<-actions
	if len(ui.screen.choices) != 2 || strings.Contains(ui.View(), "Discussion Questions") {
		t.Fatalf("appendix entry appeared in the default chapter list: %#v\n%s", ui.screen.choices, ui.View())
	}
	updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	ui = updated.(*teaModel)
	runPickerCommand(ui, cmd)
	updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Discussion Questions")})
	ui = updated.(*teaModel)
	runPickerCommand(ui, cmd)
	if got := len(ui.picker.VisibleItems()); got != 1 {
		t.Fatalf("appendix search returned %d choices, want one", got)
	}
	_, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runPickerCommand(ui, cmd)
	_, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runPickerCommand(ui, cmd)
	select {
	case action := <-actions:
		if action.Kind != ActionSelectChapter || action.Start.ChapterIndex != 1 || action.Start.StartIndex != 26 || action.Start.StartSeq != 26 {
			t.Fatalf("appendix search selection = %#v", action)
		}
	default:
		t.Fatal("appendix search did not dispatch its chapter boundary")
	}
}

func TestChapterSearchCancellationReturnsToPrimaryListAndPreservesPauseState(t *testing.T) {
	model := NewPlayer("Example Book", []Chapter{
		{ID: "chapter-1", Title: "Chapter 1", Primary: true},
		{ID: "section-1", Title: "Chapter 1  ›  Section 1", StartIndex: 2},
		{ID: "chapter-2", Title: "Chapter 2", StartIndex: 5, Primary: true},
	}, 0, 8)
	model.SetPhase(PhasePlaying)
	model.SetState(State{Status: "Paused", Paused: true})
	actions := make(chan Action, 3)
	ui := newTeaModel(model, actions, nil)
	updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	ui = updated.(*teaModel)
	runPickerCommand(ui, cmd)
	<-actions
	updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	ui = updated.(*teaModel)
	runPickerCommand(ui, cmd)
	if len(ui.screen.choices) != 3 || !ui.screen.chapterSearch {
		t.Fatalf("search mode choices=%d active=%v, want all nested stops", len(ui.screen.choices), ui.screen.chapterSearch)
	}
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyEsc})
	ui = updated.(*teaModel)
	if ui.screen.Phase() != PhaseChapters || ui.screen.chapterSearch || len(ui.screen.choices) != 2 || !ui.screen.state.Paused {
		t.Fatalf("clearing search lost browser/pause state: phase=%v search=%v choices=%d paused=%v", ui.screen.Phase(), ui.screen.chapterSearch, len(ui.screen.choices), ui.screen.state.Paused)
	}
	updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyEsc})
	ui = updated.(*teaModel)
	runPickerCommand(ui, cmd)
	if ui.screen.Phase() != PhasePlaying || !ui.screen.state.Paused {
		t.Fatalf("cancel changed paused playback state: phase=%v paused=%v", ui.screen.Phase(), ui.screen.state.Paused)
	}
	if action := <-actions; action.Kind != ActionCancelChapters {
		t.Fatalf("cancel action = %#v, want ActionCancelChapters", action)
	}
}

func TestChapterBrowserLayoutsFitNarrowStandardAndWideTerminals(t *testing.T) {
	title := "Fundamentals of Software Architecture: An Engineering Approach 日本語"
	chapters := []Chapter{
		{ID: "chapter-1", Title: "Chapter 1 · Foundations", StartIndex: 0, Primary: true},
		{ID: "section-1", Title: "Chapter 1 · Foundations  ›  Deep section on architecture decisions", StartIndex: 2},
		{ID: "chapter-2", Title: "Part I  ›  Chapter 2 · Architecture styles", StartIndex: 4, Primary: true},
		{ID: "section-2", Title: "Part I  ›  Chapter 2 · Architecture styles  ›  Nested section on decomposition", StartIndex: 6},
		{ID: "chapter-3", Title: "Part II  ›  Chapter 3 · Quality attributes", StartIndex: 8, Primary: true},
		{ID: "section-3", Title: "Part II  ›  Chapter 3 · Quality attributes  ›  A nested quality scenario section", StartIndex: 10},
		{ID: "chapter-4", Title: "Part III  ›  Chapter 4 · Architectural fitness functions", StartIndex: 12, Primary: true},
		{ID: "section-4", Title: "Part III  ›  Chapter 4 · Architectural fitness functions  ›  Nested section", StartIndex: 14},
	}
	for _, size := range [][2]int{{40, 15}, {80, 24}, {120, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			model := NewPlayer(title, chapters, 0, 16)
			model.SetPhase(PhasePlaying)
			if _, ok := model.HandleKey("c"); !ok {
				t.Fatal("could not open chapter browser")
			}
			model.SetSize(size[0], size[1])
			ui := newTeaModel(model, nil, nil)
			assertFits := func(label string, view string) {
				t.Helper()
				if height := lipgloss.Height(view); height > size[1] {
					t.Errorf("%s view uses %d rows at %dx%d:\n%s", label, height, size[0], size[1], view)
				}
				for lineNumber, line := range strings.Split(view, "\n") {
					if width := lipgloss.Width(line); width > size[0] {
						t.Errorf("%s view line %d uses %d cells at %dx%d", label, lineNumber, width, size[0], size[1])
					}
				}
			}
			assertFits("primary chapter", ui.View())
			updated, _ := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
			ui = updated.(*teaModel)
			assertFits("all-section search", ui.View())
		})
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

func TestUnavailableChapterControlsExplainPDFRecognitionAndStayHidden(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	const reason = "PDF recognition continues."
	for _, size := range [][2]int{{40, 15}, {80, 24}, {120, 30}} {
		model := NewPlayer("A long scanned book title 日本語", []Chapter{{ID: "pdf", Title: "Reading order"}}, 0, 0)
		model.SetChapterNavigation(false, reason)
		model.SetSize(size[0], size[1])
		model.SetPhase(PhasePlaying)
		model.SetState(State{Status: "Playing", Page: 3, PlaybackKnown: true})
		actions := make(chan Action, 4)
		ui := newTeaModel(model, actions, nil)
		view := ui.View()
		for _, hiddenControl := range []string{"p/n chapter", "c chapters", "browse chapters", "previous or next chapter"} {
			if strings.Contains(view, hiddenControl) {
				t.Errorf("disabled scanned-PDF player advertises %q at %dx%d:\n%s", hiddenControl, size[0], size[1], view)
			}
		}
		if !strings.Contains(view, "Chapter controls unavailable") {
			t.Errorf("player omits disabled-control explanation at %dx%d:\n%s", size[0], size[1], view)
		}
		if got := lipgloss.Height(view); got > size[1] {
			t.Errorf("player at %dx%d uses %d rows:\n%s", size[0], size[1], got, view)
		}
		for lineNumber, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > size[0] {
				t.Errorf("player at %dx%d line %d uses %d cells", size[0], size[1], lineNumber, got)
			}
		}
		if strings.Contains(view, "\x1b[") {
			t.Errorf("no-color scanned-PDF player contains ANSI at %dx%d: %q", size[0], size[1], view)
		}

		for _, key := range []rune{'c', 'p', 'n'} {
			updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
			ui = updated.(*teaModel)
			if cmd != nil || ui.screen.Phase() != PhasePlaying || ui.screen.state.Status != reason {
				t.Errorf("key %q at %dx%d changed playback or failed to explain: phase=%v status=%q command=%v", key, size[0], size[1], ui.screen.Phase(), ui.screen.state.Status, cmd != nil)
			}
			if got := ui.View(); !strings.Contains(got, "PDF recognition continues") {
				t.Errorf("key %q at %dx%d did not show the reason:\n%s", key, size[0], size[1], got)
			}
			select {
			case action := <-actions:
				t.Errorf("disabled key %q dispatched action %#v", key, action)
			default:
			}
		}

		updated, _ := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
		ui = updated.(*teaModel)
		help := ui.View()
		if !strings.Contains(help, "Chapter controls are unavailable") || !strings.Contains(help, reason) {
			t.Errorf("help omits the scanned-PDF limitation at %dx%d:\n%s", size[0], size[1], help)
		}
		if strings.Contains(help, "p/n previous") || strings.Contains(help, "c browse chapters") {
			t.Errorf("help advertises disabled chapter controls at %dx%d:\n%s", size[0], size[1], help)
		}
		if strings.Contains(help, "\x1b[") {
			t.Errorf("no-color help contains ANSI at %dx%d: %q", size[0], size[1], help)
		}
		if got := lipgloss.Height(help); got > size[1] {
			t.Errorf("help at %dx%d uses %d rows:\n%s", size[0], size[1], got, help)
		}
		for lineNumber, line := range strings.Split(help, "\n") {
			if got := lipgloss.Width(line); got > size[0] {
				t.Errorf("help at %dx%d line %d uses %d cells", size[0], size[1], lineNumber, got)
			}
		}
	}
}

func TestMultiChapterControlsRemainAvailable(t *testing.T) {
	chapters := []Chapter{{Title: "Chapter One"}, {Title: "Chapter Two", StartIndex: 3, StartSeq: 3}}
	model := NewPlayer("Book", chapters, 0, 6)
	model.SetPhase(PhasePlaying)
	if enabled, reason := model.ChapterNavigation(); !enabled || reason != "" {
		t.Fatalf("multi-chapter capability = %v, reason=%q", enabled, reason)
	}
	view := newTeaModel(model, nil, nil).View()
	if !strings.Contains(view, "p/n main chapter") || !strings.Contains(view, "c chapters") {
		t.Fatalf("multi-chapter player hides its controls:\n%s", view)
	}
	actions := make(chan Action, 3)
	ui := newTeaModel(model, actions, nil)
	for _, test := range []struct {
		key  rune
		kind ActionKind
	}{{'p', ActionPrevious}, {'n', ActionNext}, {'c', ActionOpenChapters}} {
		updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{test.key}})
		ui = updated.(*teaModel)
		if cmd == nil {
			t.Fatalf("multi-chapter key %q did not dispatch", test.key)
		}
		cmd()
		if action := <-actions; action.Kind != test.kind {
			t.Errorf("multi-chapter key %q dispatched %v, want %v", test.key, action.Kind, test.kind)
		}
		if test.key == 'c' && ui.screen.Phase() != PhaseChapters {
			t.Fatal("c did not open the chapter browser for a multi-chapter source")
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
	for _, expected := range []string{"Buffering +00:05", "+00:05", "PASSAGE TIME 00:05 / 00:20", "AUDIO READY 3/10", "PASSAGE  1 / 10", "←/→ seek 5s"} {
		if !strings.Contains(view, expected) {
			t.Errorf("player view is missing %q:\n%s", expected, view)
		}
	}
}

func TestPlayerLabelsPDFPageAndPassageLocalTime(t *testing.T) {
	model := NewPlayer("Scanned book with a long title 日本語", []Chapter{{Title: "Reading order"}}, 0, 8)
	model.SetSize(80, 24)
	model.SetPhase(PhasePlaying)
	model.SetState(State{
		Status: "Playing", Sequence: 4, Position: 7 * time.Second, Duration: 19 * time.Second,
		Page: 12, PlaybackKnown: true,
	})
	view := model.View()
	for _, expected := range []string{"PDF PAGE 12", "PASSAGE TIME 00:07 / 00:19"} {
		if !strings.Contains(view, expected) {
			t.Errorf("PDF player missing %q:\n%s", expected, view)
		}
	}
	for _, misleading := range []string{"BOOK TIME", "CHAPTER TIME", "BOOK DURATION", "CHAPTER DURATION"} {
		if strings.Contains(view, misleading) {
			t.Errorf("player implies unsupported whole-book timing with %q:\n%s", misleading, view)
		}
	}
}

func TestChapterBrowserDispatchesPauseCancelAndChapterBoundaryActions(t *testing.T) {
	chapters := []Chapter{
		{ID: "one", Title: "Chapter One", StartIndex: 0, StartSeq: 0},
		{ID: "two", Title: "Chapter Two", StartIndex: 4, StartSeq: 4},
	}
	for _, initiallyPaused := range []bool{false, true} {
		actions := make(chan Action, 4)
		model := NewPlayer("Book", chapters, 0, 8)
		model.SetPhase(PhasePlaying)
		model.SetState(State{Status: "Playing", Paused: initiallyPaused})
		ui := newTeaModel(model, actions, nil)
		updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
		ui = updated.(*teaModel)
		if cmd == nil || ui.screen.Phase() != PhaseChapters || ui.screen.returnPaused != initiallyPaused {
			t.Fatalf("initially paused=%v: chapter browse did not retain original state", initiallyPaused)
		}
		cmd()
		if action := <-actions; action.Kind != ActionOpenChapters {
			t.Fatalf("chapter-open action = %#v", action)
		}
		// Playback is paused while the user browses, regardless of its entry state.
		ui.screen.SetState(State{Status: "Chapters · playback paused", Paused: true})
		updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyEsc})
		ui = updated.(*teaModel)
		if cmd == nil || ui.screen.Phase() != PhasePlaying {
			t.Fatalf("initially paused=%v: Escape did not return to playback", initiallyPaused)
		}
		cmd()
		if action := <-actions; action.Kind != ActionCancelChapters {
			t.Fatalf("chapter-cancel action = %#v", action)
		}
		ui.screen.SetState(State{Status: map[bool]string{true: "Paused", false: "Playing"}[initiallyPaused], Paused: initiallyPaused})
		if ui.screen.state.Paused != initiallyPaused {
			t.Fatalf("initially paused=%v: pause state was not restored", initiallyPaused)
		}
	}

	actions := make(chan Action, 2)
	model := NewPlayer("Book", chapters, 0, 8)
	model.SetPhase(PhasePlaying)
	ui := newTeaModel(model, actions, nil)
	updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	ui = updated.(*teaModel)
	cmd()
	<-actions // ActionOpenChapters
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyDown})
	ui = updated.(*teaModel)
	updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = updated.(*teaModel)
	if cmd == nil || ui.screen.Phase() != PhasePreparing {
		t.Fatal("chapter selection did not start preparation")
	}
	cmd()
	if action := <-actions; action.Kind != ActionSelectChapter || action.Start.StartIndex != 4 || action.Start.StartSeq != 4 || action.Start.ChapterIndex != 1 {
		t.Fatalf("chapter selection = %#v", action)
	}
}

func TestPlayerShowsWhenBackgroundAudioIsStillBuilding(t *testing.T) {
	model := NewPlayer("Example Book", []Chapter{{Title: "Opening"}}, 0, 10)
	model.SetPhase(PhasePlaying)
	model.SetState(State{Status: "Playing", Ready: 3, Total: 10, PlaybackKnown: true})
	view := model.View()
	for _, expected := range []string{"Playing", "BUILDING AUDIO 3/10"} {
		if !strings.Contains(view, expected) {
			t.Errorf("playing view is missing %q:\n%s", expected, view)
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
			if view.name == "player" && size.width == 40 && size.height == 14 && !strings.Contains(view.text, "READY") {
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

func TestPlayerShowsLoadingActivityAndStopsSpinnerWhenAudioIsReady(t *testing.T) {
	model := newTeaModel(NewPlayer("Example Book", []Chapter{{Title: "Opening"}}, 0, 6), nil, nil)
	if cmd := model.Init(); cmd == nil || !model.spinning {
		t.Fatal("player did not start its loading animation")
	}
	view := model.View()
	for _, expected := range []string{"Preparing opening audio", "READY 0/6", "q stop"} {
		if !strings.Contains(view, expected) {
			t.Errorf("loading view is missing %q:\n%s", expected, view)
		}
	}

	firstFrame := model.spinner.View()
	_, cmd := model.Update(model.spinner.Tick())
	if cmd == nil || model.spinner.View() == firstFrame {
		t.Fatal("spinner did not advance and schedule its next frame")
	}

	_, _ = model.Update(sessionUpdate{
		state: State{Status: "Playing", Ready: 6, Total: 6, PlaybackKnown: true},
		phase: PhasePlaying,
	})
	_, cmd = model.Update(model.spinner.Tick())
	if cmd != nil || model.spinning {
		t.Fatal("spinner kept running after audio preparation completed")
	}

	_, cmd = model.Update(sessionUpdate{
		state: State{Status: "Buffering +00:05", Ready: 6, Total: 6, SeekPending: 5 * time.Second},
		phase: PhasePlaying,
	})
	if cmd == nil || !model.spinning {
		t.Fatal("spinner did not restart when a buffered seek began")
	}

	_, _ = model.Update(sessionUpdate{
		state: State{Status: "Playing", Ready: 6, Total: 6, PlaybackKnown: true},
		phase: PhasePlaying,
	})
	_, _ = model.Update(model.spinner.Tick())
	if model.spinning {
		t.Fatal("spinner kept running after buffered seek completed")
	}
	_, cmd = model.Update(sessionUpdate{
		state: State{Status: "Preparing audio", Ready: 0, Total: 6},
		phase: PhasePreparing,
	})
	if cmd == nil || !model.spinning {
		t.Fatal("spinner did not restart when chapter preparation began")
	}
}

func TestShortestPlayerLayoutKeepsColoredAudioBuildProgressVisible(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	model := newTeaModel(NewPlayer("Example Book", []Chapter{{Title: "Opening"}}, 0, 10), nil, nil)
	model.screen.SetSize(32, 10)
	model.screen.SetPhase(PhasePlaying)
	model.screen.SetState(State{Status: "Playing", Ready: 3, Total: 10, PlaybackKnown: true})
	if cmd := model.Init(); cmd == nil || !model.spinning {
		t.Fatal("background audio generation did not start its activity animation")
	}

	view := model.View()
	if !strings.Contains(view, "BUILDING AUDIO 3/10") {
		t.Fatalf("short player layout hides the audio-build count:\n%s", view)
	}
	if !strings.Contains(view, "━") {
		t.Fatalf("short player layout hides the determinate progress bar:\n%s", view)
	}
	for lineNumber, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 32 {
			t.Errorf("colored player line %d uses %d terminal cells, want at most 32", lineNumber, got)
		}
	}
	if got := lipgloss.Height(view); got > 10 {
		t.Fatalf("short player view uses %d rows, want at most 10:\n%s", got, view)
	}
}

func TestPickerQuitCancelsSelection(t *testing.T) {
	model := NewPicker("Example Book", []Chapter{{Title: "One"}}, nil)
	action, ok := model.HandleKey("q")
	if !ok || action.Kind != ActionStop || model.Phase() != PhaseFinished {
		t.Fatalf("quit action = %#v, active=%v phase=%d", action, ok, model.Phase())
	}
}

func TestExportProgressShowsStartupBarAndCanBeCancelled(t *testing.T) {
	model := NewExportProgress("Example Book")
	model.SetSize(80, 24)
	view := model.View()
	for _, want := range []string{"AUDIOBOOK EXPORT", "Example Book", "Starting audiobook export", "0%", "Esc or q cancels export"} {
		if !strings.Contains(view, want) {
			t.Errorf("export progress startup screen is missing %q:\n%s", want, view)
		}
	}

	model.SetProgress(45, 100)
	model.SetStatus("Creating chapter tracks: 3/12")
	view = model.View()
	if !strings.Contains(view, "45%") || !strings.Contains(view, "Creating chapter tracks: 3/12") {
		t.Fatalf("export progress screen does not show the current stage and progress:\n%s", view)
	}
	action, ok := model.HandleKey("esc")
	if !ok || action.Kind != ActionStop || model.Phase() != PhaseExportProgress {
		t.Fatalf("cancel action = %#v, active=%v phase=%d", action, ok, model.Phase())
	}
}

func TestExportProgressShowsActiveSynthesisInsteadOfAStaticZeroBar(t *testing.T) {
	model := NewExportProgress("Example Book")
	model.SetSize(80, 24)
	model.SetStatus("Generating audiobook audio: segment 1/240")
	first := renderExportProgress(model, "⠋", 0)
	second := renderExportProgress(model, "⠙", 1)
	for _, want := range []string{"Generating audiobook audio: segment 1/240", "WORKING"} {
		if !strings.Contains(first, want) {
			t.Errorf("active export screen is missing %q:\n%s", want, first)
		}
	}
	if strings.Contains(first, "0%") {
		t.Fatalf("active synthesis is still presented as a static 0%%:\n%s", first)
	}
	if first == second {
		t.Fatal("active export bar did not move while synthesis was running")
	}

	model.SetProgress(5, 100)
	model.SetStatus("Generating audiobook audio: segment 16/240")
	view := renderExportProgress(model, "⠋", 4)
	if !strings.Contains(view, "5%") || strings.Contains(view, "0%") {
		t.Fatalf("active synthesis did not retain completed progress:\n%s", view)
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

func TestHomeContinueActionIsFocusedBeforeRecentSources(t *testing.T) {
	entries := []LibraryEntry{
		{Path: "/books/current.epub", Title: "Current", ResumeLabel: "PDF page 7"},
		{Path: "/books/finished.epub", Title: "Finished", Complete: true},
	}
	model := NewHome(entries, "")
	actions := make(chan Action, 4)
	ui := newTeaModel(model, actions, nil)
	if !ui.screen.continueFocused || ui.screen.continueIndex != 0 {
		t.Fatalf("initial home focus/index = %v/%d, want Continue listening at entry 0", ui.screen.continueFocused, ui.screen.continueIndex)
	}
	view := ui.View()
	for _, want := range []string{"CONTINUE LISTENING", "Current", "Saved at PDF page 7", "RECENT SOURCES", "Finished", "Start again"} {
		if !strings.Contains(view, want) {
			t.Errorf("home view missing %q:\n%s", want, view)
		}
	}
	if got := strings.Count(view, "Current"); got != 1 {
		t.Errorf("featured source appears %d times; want one visible target:\n%s", got, view)
	}
	viewLines := strings.Split(view, "\n")
	finishedLine := -1
	for index, line := range viewLines {
		if strings.Contains(line, "Finished") {
			finishedLine = index
			break
		}
	}
	if finishedLine < 0 || finishedLine+1 >= len(viewLines) || !strings.Contains(viewLines[finishedLine+1], "Start again") {
		t.Errorf("completed recent source should show its Start again action on the next row:\n%s", view)
	}
	updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("Enter did not open the focused Continue listening source")
	}
	cmd()
	select {
	case action := <-actions:
		if action.Kind != ActionOpenSource || action.Path != "/books/current.epub" {
			t.Fatalf("Continue action = %#v", action)
		}
	default:
		t.Fatal("Continue action was not delivered")
	}
	updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyDown})
	ui = updated.(*teaModel)
	if cmd != nil || ui.screen.continueFocused {
		t.Fatal("down arrow did not move focus to recent sources")
	}
	updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("Enter did not open the selected recent source")
	}
	cmd()
	if action := <-actions; action.Path != "/books/finished.epub" {
		t.Fatalf("recent action = %#v, want the selected complete source", action)
	}
}

func TestHomeRecentSelectionMapsAroundFeaturedSourceAndFilteredResults(t *testing.T) {
	entries := []LibraryEntry{
		{Path: "/books/new.epub", Title: "New source"},
		{Path: "/books/current.epub", Title: "Current source", ResumeLabel: "Chapter 2"},
		{Path: "/books/finished.epub", Title: "Finished source", Complete: true},
	}
	if got := NewHome(entries, "").homeIndices; !reflect.DeepEqual(got, []int{0, 2}) {
		t.Fatalf("recent source index map = %v, want [0 2] with the featured source omitted", got)
	}

	makeUI := func() (*teaModel, chan Action) {
		actions := make(chan Action, 4)
		return newTeaModel(NewHome(entries, ""), actions, nil), actions
	}
	open := func(t *testing.T, ui *teaModel, actions chan Action, key tea.KeyMsg, wantPath string) *teaModel {
		t.Helper()
		updated, cmd := ui.Update(key)
		ui = updated.(*teaModel)
		if cmd == nil {
			t.Fatalf("Enter did not open %s", wantPath)
		}
		cmd()
		if got := <-actions; got.Kind != ActionOpenSource || got.Path != wantPath {
			t.Fatalf("Enter action = %#v, want source %q", got, wantPath)
		}
		return ui
	}

	t.Run("keyboard selection", func(t *testing.T) {
		ui, actions := makeUI()
		ui = open(t, ui, actions, tea.KeyMsg{Type: tea.KeyEnter}, "/books/current.epub")
		updated, _ := ui.Update(tea.KeyMsg{Type: tea.KeyDown})
		ui = updated.(*teaModel)
		ui = open(t, ui, actions, tea.KeyMsg{Type: tea.KeyEnter}, "/books/new.epub")
		updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyDown})
		ui = updated.(*teaModel)
		_ = open(t, ui, actions, tea.KeyMsg{Type: tea.KeyEnter}, "/books/finished.epub")
	})

	t.Run("filtered selection", func(t *testing.T) {
		ui, actions := makeUI()
		updated, _ := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
		ui = updated.(*teaModel)
		ui.homeList.SetFilterText("Finished")
		_ = open(t, ui, actions, tea.KeyMsg{Type: tea.KeyEnter}, "/books/finished.epub")
	})
}

func TestHomeUsesGenericSavedPlaceCopyWhenNoSpecificLocationExists(t *testing.T) {
	for _, saved := range []string{"Saved place", "Continue reading"} {
		if got := homePositionLabel(saved); got != "Continue from your saved place" {
			t.Errorf("homePositionLabel(%q) = %q", saved, got)
		}
	}
	if got := homePositionLabel("PDF page 7"); got != "Saved at PDF page 7" {
		t.Fatalf("specific location label = %q", got)
	}
	if got := homePositionLabel(""); got != "" {
		t.Fatalf("empty location label = %q, want empty", got)
	}
}

func TestAddPathAcceptsPastedUnicodePathAndReturnsFromBrowserToFocusedField(t *testing.T) {
	actions := make(chan Action, 4)
	ui := newTeaModel(NewHome(nil, ""), actions, nil)
	updated, _ := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	ui = updated.(*teaModel)
	if ui.screen.Phase() != PhaseAddPath || !ui.pathInput.Focused() {
		t.Fatal("a did not open and focus the path field")
	}
	const pastedPath = "/home/reader/Book – 日本語.epub"
	ui.pathInput.SetValue(pastedPath)
	updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("Enter did not dispatch the pasted path")
	}
	cmd()
	if action := <-actions; action.Kind != ActionOpenSource || action.Path != pastedPath {
		t.Fatalf("pasted path action = %#v", action)
	}

	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	ui = updated.(*teaModel)
	updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	ui = updated.(*teaModel)
	if cmd == nil || ui.screen.Phase() != PhaseBrowse {
		t.Fatal("Ctrl+O did not open the filesystem browser")
	}
	cmd()
	<-actions // ActionBrowseDirectory
	updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyEsc})
	ui = updated.(*teaModel)
	if ui.screen.Phase() != PhaseAddPath || !ui.pathInput.Focused() || cmd == nil {
		t.Fatal("Escape from browse did not return focus to the path field")
	}
}

func TestBrowserNavigatesFiltersHiddenFilesAndSelectsFolders(t *testing.T) {
	actions := make(chan Action, 4)
	model := newTeaModel(NewHome(nil, ""), actions, nil)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	model = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("b did not start browsing")
	}
	cmd()
	if action := <-actions; action.Kind != ActionBrowseDirectory || action.Path != "." {
		t.Fatalf("browse start action = %#v", action)
	}
	model.screen.SetBrowser("/books", []BrowserEntry{
		{Name: "folder", Path: "/books/folder", Directory: true},
		{Name: "book ü.epub", Path: "/books/book ü.epub"},
	}, false, "")
	model.browserList.SetItems(browserListItems(model.screen.browserEntries))
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("Enter did not navigate into the selected folder")
	}
	cmd()
	if action := <-actions; action.Kind != ActionBrowseDirectory || action.Path != "/books/folder" {
		t.Fatalf("folder navigation action = %#v", action)
	}
	model.screen.SetBrowser("/books/folder", nil, false, "")
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}})
	model = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("dot did not toggle hidden file visibility")
	}
	cmd()
	if action := <-actions; action.Kind != ActionBrowseDirectory || !action.ShowHidden {
		t.Fatalf("hidden-file action = %#v", action)
	}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	model = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("f did not select the current folder")
	}
	cmd()
	if action := <-actions; action.Kind != ActionOpenSource || action.Path != "/books/folder" {
		t.Fatalf("folder source action = %#v", action)
	}
}

func TestHomeBrowseEscapeReturnsDirectlyToHome(t *testing.T) {
	actions := make(chan Action, 1)
	ui := newTeaModel(NewHome(nil, ""), actions, nil)
	updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	ui = updated.(*teaModel)
	if cmd == nil || ui.screen.Phase() != PhaseBrowse {
		t.Fatal("b did not start browsing from Home")
	}
	cmd()
	if action := <-actions; action.Kind != ActionBrowseDirectory || action.Path != "." {
		t.Fatalf("Home browse action = %#v", action)
	}
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyEsc})
	ui = updated.(*teaModel)
	if ui.screen.Phase() != PhaseHome {
		t.Fatalf("Escape from Home-launched Browse returned to phase %v, want Home", ui.screen.Phase())
	}
}

func TestMissingSourceCanRelinkRemoveUndoAndBackOutOfSearchFirst(t *testing.T) {
	entry := LibraryEntry{Path: "/old/book.epub", Title: "Moved book", ResumeLabel: "Chapter 4", Missing: true}
	actions := make(chan Action, 5)
	model := newTeaModel(NewHome([]LibraryEntry{entry}, ""), actions, nil)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*teaModel)
	if model.screen.Phase() != PhaseMissing || !strings.Contains(model.View(), "This source can’t be found") {
		t.Fatalf("missing-source screen not shown:\n%s", model.View())
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	model = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("l did not start replacement browsing")
	}
	cmd()
	if action := <-actions; action.Kind != ActionBrowseDirectory || action.RelinkFrom != entry.Path {
		t.Fatalf("locate action = %#v", action)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(*teaModel)
	if model.screen.Phase() != PhaseMissing {
		t.Fatalf("Escape from relink Browse returned to %v, want Missing", model.screen.Phase())
	}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	model = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("second Locate did not restart replacement browsing")
	}
	cmd()
	if action := <-actions; action.Kind != ActionBrowseDirectory || action.RelinkFrom != entry.Path {
		t.Fatalf("second locate action = %#v", action)
	}
	model.screen.SetBrowser("/new", []BrowserEntry{{Name: "book.epub", Path: "/new/book.epub"}}, false, "")
	model.browserList.SetItems(browserListItems(model.screen.browserEntries))
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("replacement file was not selectable")
	}
	cmd()
	if action := <-actions; action.Kind != ActionRelinkSource || action.Path != "/new/book.epub" || action.RelinkFrom != entry.Path {
		t.Fatalf("relink action = %#v", action)
	}
	model.screen.SetHomeWithUndo(nil, "Removed from home", &entry)
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	model = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("u did not dispatch undo")
	}
	cmd()
	if action := <-actions; action.Kind != ActionUndoRemove || action.Entry.Path != entry.Path {
		t.Fatalf("undo action = %#v", action)
	}
}

func TestBrowserSearchEscapeClearsBeforeLeavingAndHomeShowsUndo(t *testing.T) {
	entry := LibraryEntry{Path: "/books/one.epub", Title: "One"}
	model := NewHome([]LibraryEntry{entry}, "Removed from home. Press u to undo.")
	model.SetUndoEntry(&entry)
	ui := newTeaModel(model, make(chan Action, 2), nil)
	if !strings.Contains(ui.View(), "u undo remove") {
		t.Fatalf("undo hint missing:\n%s", ui.View())
	}
	ui.screen.BeginBrowser("/books", "")
	ui.screen.SetBrowser("/books", []BrowserEntry{{Name: "one.epub", Path: "/books/one.epub"}}, false, "")
	ui.browserList.SetItems(browserListItems(ui.screen.browserEntries))
	updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	ui = updated.(*teaModel)
	if cmd == nil || ui.browserList.FilterState() != list.Filtering {
		t.Fatal("slash did not enter browser search")
	}
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyEsc})
	ui = updated.(*teaModel)
	if ui.screen.Phase() != PhaseBrowse || ui.browserList.FilterState() != list.Unfiltered {
		t.Fatal("Escape navigated away before clearing browser search")
	}
}

func TestNarrowBrowserShowsControlsCountsAndDirectoryTail(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previousProfile)
	lipgloss.SetColorProfile(termenv.Ascii)

	const path = "/home/prettyletto/Projects/GO/listen-cli"
	makeEntries := func(count int) []BrowserEntry {
		entries := make([]BrowserEntry, count)
		for index := range entries {
			name := fmt.Sprintf("item-%02d.epub", index+1)
			entries[index] = BrowserEntry{Name: name, Path: filepath.Join(path, name)}
		}
		return entries
	}
	for _, test := range []struct {
		name       string
		count      int
		want       []string
		wantAbsent []string
	}{
		{name: "empty", count: 0, want: []string{"No visible items", "0 items", "Enter open", "f select", ". hidden", "/ search", "Bksp up", "Esc back", "? help"}},
		{name: "short", count: 3, want: []string{"item-01.epub", "1-3 of 3", "f select", "Bksp up", "Esc back", "? help"}, wantAbsent: []string{"more"}},
		{name: "long", count: 24, want: []string{"1-8 of 24", "more", "↓", "? help"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries := makeEntries(test.count)
			model := NewHome(nil, "")
			model.SetSize(40, 15)
			model.SetBrowser(path, entries, false, "")
			ui := newTeaModel(model, nil, nil)
			view := ui.View()
			if got := lipgloss.Height(view); got > 15 {
				t.Fatalf("40x15 browser uses %d rows:\n%s", got, view)
			}
			for lineNo, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > 40 {
					t.Errorf("40x15 line %d uses %d cells:\n%s", lineNo+1, got, view)
				}
			}
			if strings.Contains(view, "\x1b[") {
				t.Fatalf("no-color browser contains ANSI escapes: %q", view)
			}
			for _, want := range test.want {
				if !strings.Contains(view, want) {
					t.Errorf("browser is missing %q:\n%s", want, view)
				}
			}
			for _, absent := range test.wantAbsent {
				if strings.Contains(view, absent) {
					t.Errorf("browser unexpectedly shows %q:\n%s", absent, view)
				}
			}
		})
	}

	model := NewHome(nil, "")
	model.SetSize(40, 15)
	model.SetBrowser(path, nil, false, "")
	view := newTeaModel(model, nil, nil).View()
	if !strings.Contains(view, "Projects/GO/listen-cli") {
		t.Fatalf("narrow browser hid the useful directory tail:\n%s", view)
	}
	model.SetSize(80, 24)
	view = newTeaModel(model, nil, nil).View()
	if !strings.Contains(view, path) {
		t.Fatalf("wide browser did not preserve the full directory path:\n%s", view)
	}
	model.SetSize(40, 15)
	model.SetBrowser(path, nil, false, "")
	helpShortcut := newTeaModel(model, nil, nil)
	updated, _ := helpShortcut.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if updated.(*teaModel).screen.Phase() != PhaseHelp {
		t.Fatal("the visible ? help shortcut did not open browser help")
	}

	help := NewHome(nil, "")
	help.SetSize(40, 15)
	help.SetPhase(PhaseHelp)
	help.helpFrom = PhaseBrowse
	helpView := newTeaModel(help, nil, nil).View()
	for _, want := range []string{"Enter open", "f select folder", ". hidden", "/ search", "Bksp parent", "Esc back"} {
		if !strings.Contains(helpView, want) {
			t.Errorf("narrow browser help is missing %q:\n%s", want, helpView)
		}
	}
}

func TestNarrowBrowserFilteredFooterDescribesSearchAndCountsMatches(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previousProfile)
	lipgloss.SetColorProfile(termenv.Ascii)

	entries := make([]BrowserEntry, 20)
	for index := range entries {
		name := fmt.Sprintf("book-%02d.epub", index+1)
		entries[index] = BrowserEntry{Name: name, Path: filepath.Join("/books", name)}
	}
	entries[17] = BrowserEntry{Name: "needle.epub", Path: "/books/needle.epub"}
	model := NewHome(nil, "")
	model.SetSize(40, 15)
	model.SetBrowser("/books", entries, false, "")
	ui := newTeaModel(model, nil, nil)
	updated, _ := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	ui = updated.(*teaModel)
	updated, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	ui = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("typing a search did not schedule directory filtering")
	}
	filterMsg := cmd()
	if batch, ok := filterMsg.(tea.BatchMsg); ok {
		filterMsg = nil
		for _, subcommand := range batch {
			if subcommand == nil {
				continue
			}
			if result := subcommand(); result != nil {
				if _, ok := result.(list.FilterMatchesMsg); ok {
					filterMsg = result
				}
			}
		}
	}
	if _, ok := filterMsg.(list.FilterMatchesMsg); !ok {
		t.Fatalf("filter update did not return matches: %T", filterMsg)
	}
	updated, _ = ui.Update(filterMsg)
	ui = updated.(*teaModel)
	searchView := ui.View()
	if !strings.Contains(searchView, "needle.epub") {
		t.Fatalf("active filtering shows the match count but not the matching source label:\n%s", searchView)
	}
	for _, want := range []string{"Type to search", "Enter apply", "Backspace edit", "Esc cancel"} {
		if !strings.Contains(searchView, want) {
			t.Errorf("active 40x15 search is missing %q:\n%s", want, searchView)
		}
	}
	if strings.Contains(searchView, "Enter open") || strings.Contains(searchView, "? help") {
		t.Fatalf("active search advertises actions that are captured as filter text:\n%s", searchView)
	}
	if got := lipgloss.Height(searchView); got > 15 {
		t.Fatalf("active 40x15 search uses %d rows:\n%s", got, searchView)
	}
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = updated.(*teaModel)
	view := ui.View()
	for _, want := range []string{"needle.epub", "1-1 of 1", "Esc clear", "Bksp up", "? help"} {
		if !strings.Contains(view, want) {
			t.Errorf("filtered 40x15 browser is missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "more") {
		t.Fatalf("single filtered result incorrectly shows overflow:\n%s", view)
	}
	if strings.Contains(view, "\x1b[") {
		t.Fatalf("filtered no-color browser contains ANSI escapes: %q", view)
	}
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if updated.(*teaModel).screen.Phase() != PhaseHelp {
		t.Fatal("the ? help shortcut did not work with an applied search")
	}
}

func TestBrowserLabelsRemainVisibleWhenDirectoryResultsArriveDuringSearch(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previousProfile)
	lipgloss.SetColorProfile(termenv.Ascii)

	const path = "/repo/internal"
	model := NewHome(nil, "")
	model.SetSize(40, 15)
	model.SetBrowser(path, []BrowserEntry{{Name: "previous", Path: path + "/previous", Directory: true}}, false, "")
	ui := newTeaModel(model, nil, nil)
	ui.browserList.SetFilterText("app")
	ui.browserList.SetFilterState(list.Filtering)
	updated, cmd := ui.Update(browserUpdate{
		path: path,
		entries: []BrowserEntry{
			{Name: "app", Path: path + "/app", Directory: true},
			{Name: "apperr", Path: path + "/apperr", Directory: true},
		},
	})
	ui = updated.(*teaModel)
	if cmd == nil {
		t.Fatal("browserUpdate discarded the list filter command after replacing directory items")
	}
	filterMsg := cmd()
	if _, ok := filterMsg.(list.FilterMatchesMsg); !ok {
		t.Fatalf("browserUpdate returned %T, want Bubbles filter matches", filterMsg)
	}
	updated, _ = ui.Update(filterMsg)
	ui = updated.(*teaModel)
	assertVisible := func(state string) {
		t.Helper()
		view := ui.View()
		for _, want := range []string{"app/", "apperr/", "1-2 of 2"} {
			if !strings.Contains(view, want) {
				t.Errorf("%s state lost matching directory label/count %q:\n%s", state, want, view)
			}
		}
	}
	assertVisible("filtering")
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = updated.(*teaModel)
	assertVisible("applied")
}

func TestBrowserDirectoryLoadedBeforeKeyByKeySearchKeepsMatchesVisible(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previousProfile)
	lipgloss.SetColorProfile(termenv.Ascii)

	const path = "/repo/internal"
	model := NewHome(nil, "")
	model.SetSize(40, 15)
	model.SetBrowser(path, nil, false, "")
	ui := newTeaModel(model, nil, nil)
	updated, _ := ui.Update(tea.WindowSizeMsg{Width: 40, Height: 15})
	ui = updated.(*teaModel)
	updated, cmd := ui.Update(browserUpdate{
		path: path,
		entries: browserDirectoryEntries(path, []string{
			"app", "apperr", "atomicfile", "cache", "catalog", "config", "domain", "export", "paths", "pipeline", "playback", "playerui", "segment", "session", "settings", "setup", "source", "speech", "structure", "testkit", "translate", "ui", "viability",
		}),
	})
	ui = updated.(*teaModel)
	if cmd != nil {
		t.Fatal("loading an unfiltered directory unexpectedly scheduled filtering")
	}
	ui.View() // Match the terminal's initial render and responsive list sizing.
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	ui = updated.(*teaModel)
	filterMatches := func(cmd tea.Cmd) {
		t.Helper()
		if cmd == nil {
			return
		}
		var deliver func(tea.Cmd)
		deliver = func(next tea.Cmd) {
			if next == nil {
				return
			}
			msg := next()
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, child := range batch {
					deliver(child)
				}
				return
			}
			if _, ok := msg.(list.FilterMatchesMsg); ok {
				updated, _ := ui.Update(msg)
				ui = updated.(*teaModel)
			}
		}
		deliver(cmd)
	}
	assertVisible := func(state string) {
		t.Helper()
		view := ui.View()
		for _, want := range []string{"app/", "apperr/"} {
			if !strings.Contains(view, want) {
				t.Fatalf("%s state lost %q after key-by-key search:\n%s", state, want, view)
			}
		}
		if state == "app" && !strings.Contains(view, "1-2 of 2") {
			t.Fatalf("final match count is wrong after key-by-key search:\n%s", view)
		}
	}
	for _, runeKey := range []rune{'a', 'p', 'p'} {
		updated, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{runeKey}})
		ui = updated.(*teaModel)
		filterMatches(cmd)
		assertVisible(string([]rune{runeKey}))
	}
	updated, _ = ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = updated.(*teaModel)
	assertVisible("app")
}

func browserDirectoryEntries(parent string, names []string) []BrowserEntry {
	entries := make([]BrowserEntry, len(names))
	for index, name := range names {
		entries[index] = BrowserEntry{Name: name, Path: filepath.Join(parent, name), Directory: true}
	}
	return entries
}

func TestHomeBrowserMissingAndHelpFitSmallTerminalSizes(t *testing.T) {
	entry := LibraryEntry{Path: "/books/very long file name ü.epub", Title: "A very long book title 日本語", ResumeLabel: "PDF page 7"}
	for _, size := range [][2]int{{40, 15}, {80, 24}, {120, 30}} {
		model := NewHome([]LibraryEntry{entry}, "Your place is saved. Select it to continue.")
		model.SetSize(size[0], size[1])
		for _, phase := range []Phase{PhaseHome, PhaseAddPath, PhaseBrowse, PhaseMissing, PhaseHelp} {
			model.SetPhase(phase)
			model.browserPath = "/books"
			model.browserEntries = []BrowserEntry{{Name: "folder", Path: "/books/folder", Directory: true}}
			model.missingPath = entry.Path
			model.helpFrom = PhaseHome
			view := newTeaModel(model, nil, nil).View()
			if got := lipgloss.Height(view); got > size[1] {
				t.Errorf("phase %d at %dx%d uses %d rows:\n%s", phase, size[0], size[1], got, view)
			}
			for lineNumber, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > size[0] {
					t.Errorf("phase %d at %dx%d line %d uses %d cells", phase, size[0], size[1], lineNumber, got)
				}
			}
		}
	}
}

func TestPlayerStatesAndNoColorLayoutsFitAcceptanceSizes(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previousProfile)
	chapters := []Chapter{{Title: "Part I · A long chapter title 日本語", StartIndex: 0, Page: 3}}
	for _, size := range [][2]int{{40, 15}, {80, 24}, {120, 30}} {
		for _, state := range []struct {
			name  string
			phase Phase
			value State
		}{
			{name: "loading", phase: PhasePreparing, value: State{Status: "Preparing opening audio", Total: 12}},
			{name: "restoring", phase: PhasePreparing, value: State{Status: "Restoring your place · checking PDF page 5/18", Page: 5}},
			{name: "playing", phase: PhasePlaying, value: State{Status: "Playing", Ready: 6, Total: 12, Sequence: 5, Position: 8 * time.Second, Duration: 21 * time.Second, Page: 5, PlaybackKnown: true}},
			{name: "paused", phase: PhasePlaying, value: State{Status: "Paused", Paused: true, Ready: 9, Total: 12, Sequence: 8, Position: 13 * time.Second, Duration: 21 * time.Second, Page: 8, PlaybackKnown: true}},
			{name: "error", phase: PhasePlaying, value: State{Status: "Warning: audio could not be prepared", Ready: 4, Total: 12}},
			{name: "complete", phase: PhaseFinished, value: State{Status: "Finished listening"}},
		} {
			model := NewPlayer("A long book title with Unicode 日本語 and enough words to wrap on small screens", chapters, 0, 12)
			model.SetSize(size[0], size[1])
			model.SetPhase(state.phase)
			model.SetState(state.value)
			view := model.View()
			switch state.name {
			case "restoring":
				if !strings.Contains(view, "Restoring your place") || !strings.Contains(view, "PDF PAGE 5") {
					t.Errorf("restoring state at %dx%d lost its progress or page:\n%s", size[0], size[1], view)
				}
			case "playing", "paused":
				if !strings.Contains(view, "PDF PAGE") || !strings.Contains(view, "PASSAGE TIME") {
					t.Errorf("%s state at %dx%d lost page-aware passage progress:\n%s", state.name, size[0], size[1], view)
				}
			case "complete":
				if !strings.Contains(view, "Finished listening") {
					t.Errorf("completion state at %dx%d is not visible:\n%s", size[0], size[1], view)
				}
			}
			for lineNumber, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > size[0] {
					t.Errorf("%s at %dx%d line %d uses %d cells", state.name, size[0], size[1], lineNumber, got)
				}
			}
			if got := lipgloss.Height(view); got > size[1] {
				t.Errorf("%s at %dx%d uses %d rows:\n%s", state.name, size[0], size[1], got, view)
			}
		}
	}
	lipgloss.SetColorProfile(termenv.Ascii)
	model := NewPlayer("Plain output", nil, 0, 1)
	model.SetPhase(PhasePlaying)
	model.SetState(State{Status: "Playing", PlaybackKnown: true, Position: time.Second, Duration: 2 * time.Second})
	view := model.View()
	if strings.Contains(view, "\x1b[") || !strings.Contains(view, "PASSAGE TIME") {
		t.Fatalf("no-color player output contains ANSI or lost timing label: %q", view)
	}
	for _, screen := range []Model{
		NewHome([]LibraryEntry{{Path: "/books/book.epub", Title: "Book", ResumeLabel: "Chapter 2"}}, ""),
		func() Model {
			browser := NewHome(nil, "")
			browser.SetPhase(PhaseBrowse)
			browser.browserPath = "/books"
			browser.browserEntries = []BrowserEntry{{Name: "book.epub", Path: "/books/book.epub"}}
			return browser
		}(),
		func() Model {
			missing := NewHome([]LibraryEntry{{Path: "/old/book.epub", Title: "Book", Missing: true}}, "")
			missing.OpenMissing("/old/book.epub")
			return missing
		}(),
	} {
		view := screen.View()
		if strings.Contains(view, "\x1b[") {
			t.Fatalf("no-color home/browser/recovery output contains ANSI: %q", view)
		}
	}
}

func TestResizePromptExplainsMinimumSizeAndExit(t *testing.T) {
	model := NewHome(nil, "")
	model.SetSize(31, 9)
	view := model.View()
	if !strings.Contains(view, "32 columns minimum") || !strings.Contains(view, "10 rows minimum") || !strings.Contains(view, "q or Ctrl-C") {
		t.Fatalf("minimum-size prompt is incomplete:\n%s", view)
	}
	if got := lipgloss.Height(view); got > 9 {
		t.Fatalf("resize prompt uses %d rows at 31×9, want at most 9:\n%s", got, view)
	}
}

func TestSetupRequiresAnExplicitStartAndRetainsSourceForRetry(t *testing.T) {
	const source = "/books/a book.epub"
	model := NewHome([]LibraryEntry{{Path: source, Title: "A book"}}, "")
	model.SetSetupChoice(source, "Select setup to download local speech files.")

	if action, ok := model.HandleKey("esc"); !ok || action.Kind != ActionCancelSetup || model.Phase() != PhaseHome {
		t.Fatalf("leaving setup choice without starting = %#v, active=%v, phase=%v", action, ok, model.Phase())
	}
	model.SetSetupChoice(source, "This source is selected.")
	if action, ok := model.HandleKey("enter"); !ok || action.Kind != ActionStartRecommendedSetup {
		t.Fatalf("explicit setup start = %#v, active=%v", action, ok)
	}
	if model.setupSource != source {
		t.Fatalf("setup start lost selected source %q", model.setupSource)
	}
	model.SetSetupChoice(source, "Setup stopped. Press Enter to retry.")
	if action, ok := model.HandleKey("enter"); !ok || action.Kind != ActionStartRecommendedSetup || model.setupSource != source {
		t.Fatalf("retry did not retain selected source: action=%#v active=%v source=%q", action, ok, model.setupSource)
	}
	model.SetSetupProgress("Downloading local speech files…")
	if action, ok := model.HandleKey("esc"); !ok || action.Kind != ActionCancelSetup || model.setupSource != source {
		t.Fatalf("setup cancellation lost selected source: action=%#v active=%v source=%q", action, ok, model.setupSource)
	}
}

func TestPreferencesSaveAndDiscardReturnToPendingSetupSource(t *testing.T) {
	const source = "/books/selected source.epub"
	entries := []LibraryEntry{{Path: "/books/recent.epub", Title: "Recent", ResumeLabel: "Chapter 2"}}
	preferences := Preferences{
		DefaultVoice: "af_heart", SpeechSpeed: 1, PlaybackRate: 1,
		Locales:      []VoiceChoice{{Locale: "en-US", LocaleName: "American English"}},
		VoiceOptions: []VoiceChoice{{ID: "af_heart", Name: "Heart", Locale: "en-US"}},
		Voices:       map[string]string{"en-US": "af_heart"},
	}

	newPendingPreferences := func() (Model, Model) {
		home := NewHome(entries, "")
		active := home
		active.SetSetupChoice(source, "Prepare the selected source.")
		active.SetPreferencesFor(preferences, PhaseSetupChoice)
		return home, active
	}

	t.Run("discard", func(t *testing.T) {
		_, model := newPendingPreferences()
		if _, ok := model.HandleKey("esc"); ok || model.Phase() != PhaseSetupChoice || model.setupSource != source {
			t.Fatalf("Esc from preferences returned phase=%v source=%q active=%v", model.Phase(), model.setupSource, ok)
		}
	})

	t.Run("back row", func(t *testing.T) {
		_, model := newPendingPreferences()
		for range 6 {
			model.HandleKey("down")
		}
		if _, ok := model.HandleKey("enter"); ok || model.Phase() != PhaseSetupChoice || model.setupSource != source {
			t.Fatalf("Back without saving returned phase=%v source=%q active=%v", model.Phase(), model.setupSource, ok)
		}
	})

	t.Run("save", func(t *testing.T) {
		home, model := newPendingPreferences()
		for range 5 {
			model.HandleKey("down")
		}
		action, ok := model.HandleKey("enter")
		if !ok || action.Kind != ActionSavePreferences {
			t.Fatalf("save preference action = %#v active=%v", action, ok)
		}
		// The Session retains its last active model while Bubble Tea handles the
		// save key, then reopens setup with that model's source and library.
		overlay := homeOverlayModel(home, model)
		overlay.SetSetupChoice(source, "Preferences saved.")
		if overlay.Phase() != PhaseSetupChoice || overlay.setupSource != source || len(overlay.HomeEntries()) != 1 || overlay.HomeEntries()[0].Path != entries[0].Path {
			t.Fatalf("saved preferences lost setup/library context: phase=%v source=%q entries=%#v", overlay.Phase(), overlay.setupSource, overlay.HomeEntries())
		}
	})

	t.Run("quit", func(t *testing.T) {
		_, model := newPendingPreferences()
		action, ok := model.HandleKey("q")
		if !ok || action.Kind != ActionExit || model.Phase() != PhaseFinished {
			t.Fatalf("q from preferences = %#v active=%v phase=%v", action, ok, model.Phase())
		}
		view := newTeaModel(model, nil, nil).View()
		if !strings.Contains(view, "Make listening yours") || strings.Contains(view, "NOW PLAYING") {
			t.Fatalf("preferences quit rendered an unrelated screen:\n%s", view)
		}
	})
}

func TestSetupPreferencesAndPreviewLayoutsFitAcceptanceSizes(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previousProfile)

	preferences := Preferences{
		DefaultVoice: "af_heart", SpeechSpeed: 1, PlaybackRate: 1,
		Voices:       map[string]string{"en-US": "af_heart"},
		Locales:      []VoiceChoice{{Locale: "en-US", LocaleName: "American English"}},
		VoiceOptions: []VoiceChoice{{ID: "af_heart", Name: "Heart", Locale: "en-US"}},
	}
	for _, size := range [][2]int{{40, 15}, {80, 24}, {120, 30}} {
		setup := NewHome(nil, "")
		setup.SetSize(size[0], size[1])
		setup.SetSetupChoice("/books/a very long selected title 日本語.epub", "Local files stay on this device.")
		retry := setup
		retry.SetSetupChoice("/books/a very long selected title 日本語.epub", "Setup stopped. Press Enter to retry after checking your connection and available disk space.")
		progress := setup
		progress.SetSetupProgress("Checking local voices and preparing the selected source…")
		preferencesModel := NewHome(nil, "")
		preferencesModel.SetSize(size[0], size[1])
		preferencesModel.SetPreferencesFor(preferences, PhaseHome)
		preferencesModel.preferences.SetupStatus = "Local speech is ready on this device."
		advanced := preferencesModel
		advanced.phase = PhaseAdvancedPreferences
		preview := preferencesModel
		if action, ok := preview.HandleKey("v"); !ok || preview.Phase() != PhaseVoicePreview {
			t.Fatalf("preview transition at %dx%d failed: action=%#v active=%v phase=%v", size[0], size[1], action, ok, preview.Phase())
		}
		for name, model := range map[string]Model{
			"setup": setup, "setup retry": retry, "setup progress": progress,
			"preferences": preferencesModel, "advanced preferences": advanced, "preview": preview,
		} {
			view := model.View()
			if got := lipgloss.Height(view); got > size[1] {
				t.Errorf("%s screen at %dx%d uses %d rows:\n%s", name, size[0], size[1], got, view)
			}
			for lineNumber, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > size[0] {
					t.Errorf("%s screen at %dx%d line %d uses %d cells", name, size[0], size[1], lineNumber, got)
				}
			}
		}
	}
	lipgloss.SetColorProfile(termenv.Ascii)
	noColorChoice := NewHome(nil, "")
	noColorChoice.SetSize(80, 24)
	noColorChoice.SetSetupChoice("/books/a book.epub", "Start setup when you are ready.")
	noColorProgress := noColorChoice
	noColorProgress.SetSetupProgress("Downloading local speech files…")
	noColorPreferences := NewHome(nil, "")
	noColorPreferences.SetSize(80, 24)
	noColorPreferences.SetPreferencesFor(preferences, PhaseHome)
	noColorAdvanced := noColorPreferences
	noColorAdvanced.phase = PhaseAdvancedPreferences
	noColorPreview := noColorPreferences
	if action, ok := noColorPreview.HandleKey("v"); !ok || action.Kind != ActionPreviewVoice {
		t.Fatalf("no-color preview transition = %#v active=%v", action, ok)
	}
	for name, model := range map[string]Model{
		"setup choice": noColorChoice, "setup progress": noColorProgress,
		"preferences": noColorPreferences, "advanced preferences": noColorAdvanced, "voice preview": noColorPreview,
	} {
		if view := model.View(); strings.Contains(view, "\x1b[") {
			t.Errorf("no-color %s output contains ANSI: %q", name, view)
		}
	}
}

func TestPreferencesResponsiveRowsExposeScrollPositionAndControls(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previousProfile)
	lipgloss.SetColorProfile(termenv.Ascii)

	preferences := preferencesForResponsiveLayout()
	for _, size := range [][2]int{{40, 15}, {80, 24}, {120, 30}} {
		model := NewHome(nil, "")
		model.SetSize(size[0], size[1])
		model.SetPreferencesFor(preferences, PhaseHome)
		ui := newTeaModel(model, nil, nil)
		view := ui.View()
		if got := lipgloss.Height(view); got > size[1] {
			t.Errorf("preferences at %dx%d use %d rows:\n%s", size[0], size[1], got, view)
		}
		for lineNumber, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > size[0] {
				t.Errorf("preferences at %dx%d line %d uses %d cells:\n%s", size[0], size[1], lineNumber, got, view)
			}
		}
		for _, control := range []string{"move", "change", "preview", "setup", "open/save", "Esc back", "q quit"} {
			if !strings.Contains(view, control) {
				t.Errorf("preferences at %dx%d hide keyboard control %q:\n%s", size[0], size[1], control, view)
			}
		}
		if size[0] == 40 {
			if !strings.Contains(view, "1-6 of 13 · ↓ more") {
				t.Errorf("40x15 preferences lack the first-page position cue:\n%s", view)
			}
			if !strings.Contains(view, "Locale 05") || strings.Contains(view, "Locale 06") {
				t.Errorf("40x15 preferences do not show six rows:\n%s", view)
			}
		} else {
			if !strings.Contains(view, "Locale 07") || !strings.Contains(view, "Back without saving") {
				t.Errorf("%dx%d preferences do not show all options:\n%s", size[0], size[1], view)
			}
			if strings.Contains(view, "of 13") {
				t.Errorf("%dx%d preferences show an unnecessary scroll cue:\n%s", size[0], size[1], view)
			}
		}
	}

	model := NewHome(nil, "")
	model.SetSize(40, 15)
	model.SetPreferencesFor(preferences, PhaseHome)
	ui := newTeaModel(model, nil, nil)
	for range 7 {
		updated, _ := ui.Update(tea.KeyMsg{Type: tea.KeyDown})
		ui = updated.(*teaModel)
	}
	view := ui.View()
	if !strings.Contains(view, "3-8 of 13 · ↑ previous · ↓ more") {
		t.Fatalf("mid-list position cue is incorrect after scrolling:\n%s", view)
	}
	for range 5 {
		updated, _ := ui.Update(tea.KeyMsg{Type: tea.KeyDown})
		ui = updated.(*teaModel)
	}
	view = ui.View()
	if !strings.Contains(view, "8-13 of 13 · ↑ previous") || strings.Contains(view, "↓ more") {
		t.Fatalf("last-page position cue is incorrect after scrolling:\n%s", view)
	}
	if !strings.Contains(view, "Back without saving") {
		t.Fatalf("last preference action is not visible after scrolling:\n%s", view)
	}
}

func TestPreferencesNotesReduceVisibleRowsAndRemainNoColor(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previousProfile)
	t.Setenv("NO_COLOR", "1")
	lipgloss.SetColorProfile(termenv.Ascii)

	model := NewHome(nil, "")
	model.SetSize(40, 15)
	model.SetPreferencesFor(preferencesForResponsiveLayout(), PhaseHome)
	model.previewNote = "Voice preview stopped."
	model.preferencesNote = "Preferences saved locally."
	view := newTeaModel(model, nil, nil).View()
	if !strings.Contains(view, "1-4 of 13 · ↓ more") {
		t.Fatalf("preference notes did not reduce the visible row count:\n%s", view)
	}
	for _, note := range []string{"Voice preview stopped.", "Preferences saved locally."} {
		if !strings.Contains(view, note) {
			t.Errorf("preference note %q is hidden:\n%s", note, view)
		}
	}
	for _, control := range []string{"v preview", "s setup/repair", "Enter open/save", "Esc back", "q quit"} {
		if !strings.Contains(view, control) {
			t.Errorf("40x15 note layout hides keyboard control %q:\n%s", control, view)
		}
	}
	if strings.Contains(view, "\x1b[") {
		t.Fatalf("NO_COLOR preference output contains ANSI escapes: %q", view)
	}
	if got := lipgloss.Height(view); got > 15 {
		t.Fatalf("40x15 preferences with notes use %d rows:\n%s", got, view)
	}
}

func preferencesForResponsiveLayout() Preferences {
	locales := make([]VoiceChoice, 7)
	for index := range locales {
		locale := fmt.Sprintf("locale-%02d", index+1)
		locales[index] = VoiceChoice{Locale: locale, LocaleName: fmt.Sprintf("Locale %02d", index+1)}
	}
	voices := make(map[string]string, len(locales))
	for _, locale := range locales {
		voices[locale.Locale] = "voice"
	}
	return Preferences{
		DefaultVoice: "voice", SpeechSpeed: 1, PlaybackRate: 1,
		Voices: voices, Locales: locales,
		VoiceOptions: []VoiceChoice{{ID: "voice", Name: "Voice", Locale: locales[0].Locale}},
		SetupStatus:  "Local speech is ready.", SetupReady: true,
	}
}

func TestPreferencesLongSelectedVoiceRowsStaySingleLineAtNarrowSize(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previousProfile)
	lipgloss.SetColorProfile(termenv.Ascii)

	preferences := Preferences{
		DefaultVoice: "af_bella", SpeechSpeed: 1, PlaybackRate: 1,
		Locales: []VoiceChoice{
			{Locale: "en-US", LocaleName: "American English"},
			{Locale: "pt-BR", LocaleName: "Brazilian Portuguese"},
			{Locale: "es-ES", LocaleName: "Spanish"},
			{Locale: "fr-FR", LocaleName: "French"},
			{Locale: "de-DE", LocaleName: "German"},
			{Locale: "it-IT", LocaleName: "Italian"},
			{Locale: "ja-JP", LocaleName: "Japanese"},
		},
		VoiceOptions: []VoiceChoice{
			{ID: "af_bella", Name: "Bella", Locale: "en-US"},
			{ID: "pt_BR_brazilian_narrator_voice_with_an_extra_long_identifier", Name: "Brazilian voice", Locale: "pt-BR"},
		},
		Voices: map[string]string{
			"en-US": "af_bella",
			"pt-BR": "pt_BR_brazilian_narrator_voice_with_an_extra_long_identifier",
		},
		SetupStatus: "Local speech is ready.", SetupReady: true,
	}
	tests := []struct {
		name       string
		cursor     int
		withNotes  bool
		wantCue    string
		wantLabels []string
	}{
		{name: "initial", cursor: 0, wantCue: "1-6 of 13 · ↓ more", wantLabels: []string{"Bella (af_bella)"}},
		{name: "American English selected", cursor: 1, wantCue: "1-6 of 13 · ↓ more", wantLabels: []string{"American English", "Bella (af_bella)"}},
		{name: "long Brazilian voice selected", cursor: 2, wantCue: "1-6 of 13 · ↓ more", wantLabels: []string{"Brazilian Portuguese", "Brazilian …"}},
		{name: "mid-scroll", cursor: 7, wantCue: "3-8 of 13 · ↑ previous · ↓ more", wantLabels: []string{"Japanese"}},
		{name: "end-scroll", cursor: 12, wantCue: "8-13 of 13 · ↑ previous", wantLabels: []string{"Back without saving"}},
		{name: "notes", cursor: 2, withNotes: true, wantCue: "1-4 of 13 · ↓ more", wantLabels: []string{"Brazilian Portuguese", "Voice preview stopped.", "Preferences saved locally."}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := NewHome(nil, "")
			model.SetSize(40, 15)
			model.SetPreferencesFor(preferences, PhaseHome)
			if test.withNotes {
				model.previewNote = "Voice preview stopped."
				model.preferencesNote = "Preferences saved locally."
			}
			ui := newTeaModel(model, nil, nil)
			for range test.cursor {
				updated, _ := ui.Update(tea.KeyMsg{Type: tea.KeyDown})
				ui = updated.(*teaModel)
			}
			view := ui.View()
			if got := lipgloss.Height(view); got != 15 {
				t.Fatalf("40x15 preferences use %d physical rows, want 15; a selected row may have wrapped:\n%s", got, view)
			}
			for lineNumber, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > 40 {
					t.Errorf("40x15 line %d uses %d cells:\n%s", lineNumber, got, view)
				}
			}
			for _, want := range append([]string{"PREFERENCES", "q quit", test.wantCue}, test.wantLabels...) {
				if !strings.Contains(view, want) {
					t.Errorf("40x15 %s state is missing %q:\n%s", test.name, want, view)
				}
			}
		})
	}
}

func TestSourceSetupChoiceIdentifiesBookAndBalancesWhenItFits(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previousProfile)
	lipgloss.SetColorProfile(termenv.Ascii)

	const source = "/home/reader/Documents/Books/Fundamentals of Software Architecture.epub"
	basename := filepath.Base(source)
	for _, size := range [][2]int{{40, 15}, {80, 24}, {120, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			model := NewHome(nil, "")
			model.SetSize(size[0], size[1])
			model.SetSetupChoice(source, "")
			view := newTeaModel(model, nil, nil).View()
			if got := lipgloss.Height(view); got > size[1] {
				t.Fatalf("source setup at %dx%d uses %d rows:\n%s", size[0], size[1], got, view)
			}
			for lineNumber, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > size[0] {
					t.Errorf("source setup at %dx%d line %d uses %d cells:\n%s", size[0], size[1], lineNumber, got, view)
				}
			}
			if !strings.Contains(view, "Fundamentals") || strings.Contains(view, "/home/reader/Documents/Books") {
				t.Errorf("source setup at %dx%d does not lead with an identifiable basename:\n%s", size[0], size[1], view)
			}
			for _, want := range []string{
				"Enter  Start setup or repair", "Set voice and speed first",
				"Downloads stay on this device.", "First setup may take minutes.", "Esc home", "q quit",
			} {
				if !strings.Contains(view, want) {
					t.Errorf("source setup at %dx%d is missing %q:\n%s", size[0], size[1], want, view)
				}
			}
			if size[0] >= 64 {
				if !strings.Contains(view, basename) {
					t.Errorf("wide source setup hides the full basename %q:\n%s", basename, view)
				}
				lines := strings.Split(view, "\n")
				first, last := firstAndLastVisibleLine(lines)
				topBlank := first
				bottomBlank := len(lines) - 1 - last
				blankDifference := topBlank - bottomBlank
				if blankDifference < 0 {
					blankDifference = -blankDifference
				}
				if topBlank == 0 || blankDifference > 1 {
					t.Errorf("source setup at %dx%d is not vertically balanced (top=%d bottom=%d):\n%s", size[0], size[1], topBlank, bottomBlank, view)
				}
				indent := len(lines[first]) - len(strings.TrimLeft(lines[first], " "))
				if indent == 0 {
					t.Errorf("source setup at %dx%d is not horizontally balanced:\n%s", size[0], size[1], view)
				}
			}
		})
	}

	model := NewHome(nil, "")
	model.SetSize(40, 15)
	model.SetSetupChoice(source, "Downloads paused. Press Enter to retry after checking local storage.")
	view := newTeaModel(model, nil, nil).View()
	if got := lipgloss.Height(view); got > 15 {
		t.Fatalf("narrow setup retry note uses %d rows:\n%s", got, view)
	}
	for _, want := range []string{"Fundamentals", "Downloads paused.", "Enter  Start setup or repair", "Set voice and speed first", "Esc home", "q quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("narrow setup retry hides %q:\n%s", want, view)
		}
	}
}

func firstAndLastVisibleLine(lines []string) (first int, last int) {
	first = len(lines)
	last = -1
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if first == len(lines) {
			first = index
		}
		last = index
	}
	return first, last
}
