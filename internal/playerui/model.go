package playerui

import (
	"fmt"
	"path/filepath"
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
	Page       int
	// Primary marks the chapter-browser and p/n navigation stops. Nested
	// structural stops remain available when the chapter browser is searched.
	Primary bool
}

type StartPoint struct {
	Label        string
	StartIndex   int
	StartSeq     uint64
	Offset       time.Duration
	ChapterIndex int
	Resume       bool
	Page         int
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
	ActionReturnHome
	ActionOpenSource
	ActionExit
	ActionBrowseDirectory
	ActionRemoveSource
	ActionUndoRemove
	ActionRelinkSource
	ActionOpenChapters
	ActionSelectChapter
	ActionCancelChapters
	ActionOpenPreferences
	ActionSavePreferences
	ActionPreviewVoice
	ActionCancelPreview
	ActionStartRecommendedSetup
	ActionCustomizeSetup
	ActionCancelSetup
	ActionPlaybackRate
	ActionOpenSetup
)

type Action struct {
	Kind            ActionKind
	Start           StartPoint
	Path            string
	RelinkFrom      string
	ShowHidden      bool
	Entry           LibraryEntry
	Preferences     Preferences
	VoiceID         string
	Rate            float64
	ExitAfterCancel bool
}

type Phase uint8

const (
	PhaseSelect Phase = iota + 1
	PhasePreparing
	PhasePlaying
	PhaseFinished
	PhaseHome
	PhaseAddPath
	PhaseStopping
	PhaseBrowse
	PhaseMissing
	PhaseChapters
	PhaseHelp
	PhasePreferences
	PhaseAdvancedPreferences
	PhaseSetupChoice
	PhaseSetupProgress
	PhaseVoicePreview
	PhaseExportProgress
)

type VoiceChoice struct {
	ID         string
	Name       string
	Locale     string
	LocaleName string
}

type Preferences struct {
	DefaultVoice string
	SpeechSpeed  float64
	PlaybackRate float64
	OCRLanguages string
	CloudPolicy  string
	SetupReady   bool
	SetupStatus  string
	Voices       map[string]string
	Locales      []VoiceChoice
	VoiceOptions []VoiceChoice
}

// LibraryEntry contains local source metadata for the home screen. Source
// contents remain in their original location and are never copied here.
type LibraryEntry struct {
	Path           string
	Title          string
	Kind           string
	TargetLanguage string
	CodeMode       bool
	ResumeLabel    string
	UpdatedAt      time.Time
	Complete       bool
	Missing        bool
}

// BrowserEntry is one local filesystem item. Directories are shown before
// files, while explicit selection keeps documentation folders discoverable.
type BrowserEntry struct {
	Name      string
	Path      string
	Directory bool
}

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
	Page          int
	SeekPending   time.Duration
	PlaybackKnown bool
	PlaybackRate  float64
}

type Model struct {
	phase           Phase
	title           string
	chapters        []Chapter
	choices         []StartPoint
	chapterSearch   bool
	cursor          int
	width           int
	height          int
	state           State
	entries         []LibraryEntry
	homeNote        string
	pathText        string
	pathErr         string
	continueIndex   int
	continueFocused bool
	homeIndices     []int
	browserPath     string
	browserEntries  []BrowserEntry
	browserHidden   bool
	browserNote     string
	relinkFrom      string
	returnPhase     Phase
	exitPhase       Phase
	returnPaused    bool
	missingPath     string
	undoEntry       *LibraryEntry
	helpFrom        Phase
	chapterNav      bool
	chapterNavWhy   string
	preferences     Preferences
	prefCursor      int
	preferencesNote string
	advancedFocus   int
	setupSource     string
	setupNote       string
	previewVoice    string
	previewNote     string
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
		state:      State{Status: status, ChapterIndex: initialChapter, Total: total, BookTotal: total},
		chapterNav: true,
	}
}

func NewExportProgress(title string) Model {
	if strings.TrimSpace(title) == "" {
		title = "Audiobook export"
	}
	return Model{
		phase: PhaseExportProgress, title: title, width: 80, height: 24,
		state: State{Status: "Starting audiobook export", Total: 100},
	}
}

func NewHome(entries []LibraryEntry, note string) Model {
	m := Model{phase: PhaseHome, entries: append([]LibraryEntry(nil), entries...), homeNote: note, width: 80, height: 24, continueIndex: -1}
	m.refreshHomeSelection()
	return m
}

func (m *Model) SetHome(entries []LibraryEntry, note string) {
	m.entries = append([]LibraryEntry(nil), entries...)
	m.homeNote = note
	m.phase = PhaseHome
	m.undoEntry = nil
	m.refreshHomeSelection()
}

func (m *Model) SetHomeWithUndo(entries []LibraryEntry, note string, undo *LibraryEntry) {
	m.SetHome(entries, note)
	if undo != nil {
		copy := *undo
		m.undoEntry = &copy
	}
}

func (m *Model) SetAddPath(path, note string) {
	m.pathText = path
	m.pathErr = note
	m.phase = PhaseAddPath
}

func (m *Model) refreshHomeSelection() {
	m.continueIndex = -1
	for index, entry := range m.entries {
		if !entry.Complete && entry.ResumeLabel != "" && !entry.Missing {
			m.continueIndex = index
			break
		}
	}
	m.homeIndices = m.homeIndices[:0]
	for index := range m.entries {
		if index != m.continueIndex {
			m.homeIndices = append(m.homeIndices, index)
		}
	}
	m.continueFocused = m.continueIndex >= 0
}

func (m Model) HomeEntries() []LibraryEntry { return append([]LibraryEntry(nil), m.entries...) }

func (m *Model) SetPreferences(preferences Preferences) {
	m.SetPreferencesFor(preferences, PhaseHome)
}

func (m *Model) SetPreferencesFor(preferences Preferences, returnTo Phase) {
	preferences.Voices = cloneVoiceMap(preferences.Voices)
	preferences.Locales = append([]VoiceChoice(nil), preferences.Locales...)
	preferences.VoiceOptions = append([]VoiceChoice(nil), preferences.VoiceOptions...)
	m.preferences = preferences
	m.prefCursor = 0
	m.preferencesNote = ""
	m.returnPhase = returnTo
	m.phase = PhasePreferences
}

func (m *Model) SetSetupChoice(source, note string) {
	m.setupSource = source
	m.setupNote = note
	m.phase = PhaseSetupChoice
}

func (m *Model) SetSetupProgress(note string) {
	m.setupNote = note
	m.state.Status = note
	m.phase = PhaseSetupProgress
}

func cloneVoiceMap(voices map[string]string) map[string]string {
	copy := make(map[string]string, len(voices))
	for locale, voice := range voices {
		copy[locale] = voice
	}
	return copy
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

func (m *Model) SetPage(page int) { m.state.Page = max(0, page) }

func (m *Model) SetPlaybackRate(rate float64) { m.state.PlaybackRate = rate }

// SetChapterNavigation declares whether chapter controls are meaningful for
// this playback session. A disabled control keeps its reason in the player so
// both keyboard feedback and contextual help can explain the limitation.
func (m *Model) SetChapterNavigation(enabled bool, reason string) {
	m.chapterNav = enabled
	if enabled {
		m.chapterNavWhy = ""
		return
	}
	if strings.TrimSpace(reason) == "" {
		reason = "Chapter navigation is unavailable for this source."
	}
	m.chapterNavWhy = reason
}

func (m Model) ChapterNavigation() (bool, string) {
	return m.chapterNav, m.chapterNavWhy
}

func (m *Model) SetProgress(ready, total int) {
	m.state.Ready = max(0, ready)
	m.state.Total = max(0, total)
}

func (m Model) Phase() Phase { return m.phase }

func (m Model) SelectedStart() (StartPoint, bool) {
	if (m.phase != PhaseSelect && m.phase != PhaseChapters) || m.cursor < 0 || m.cursor >= len(m.choices) {
		return StartPoint{}, false
	}
	return m.choices[m.cursor], true
}

func (m *Model) OpenMissing(path string) {
	m.missingPath = path
	m.phase = PhaseMissing
}

func (m *Model) BeginBrowser(path, relinkFrom string) Action {
	returnPhase := m.phase
	if returnPhase != PhaseHome && returnPhase != PhaseAddPath {
		returnPhase = PhaseAddPath
	}
	if relinkFrom != "" {
		returnPhase = PhaseMissing
	}
	return m.beginBrowse(path, relinkFrom, returnPhase)
}

func (m *Model) SetBrowser(path string, entries []BrowserEntry, hidden bool, note string) {
	m.browserPath = path
	m.browserEntries = append([]BrowserEntry(nil), entries...)
	m.browserHidden = hidden
	m.browserNote = note
	m.phase = PhaseBrowse
}

func (m *Model) SetUndoEntry(entry *LibraryEntry) {
	if entry == nil {
		m.undoEntry = nil
		return
	}
	copy := *entry
	m.undoEntry = &copy
}

func (m *Model) CancelChapterBrowser() {
	if m.phase == PhaseChapters || (m.phase == PhaseHelp && m.helpFrom == PhaseChapters) {
		m.phase = PhasePlaying
	}
}

func (m *Model) openChapters() {
	m.returnPhase = m.phase
	m.returnPaused = m.state.Paused
	m.chapterSearch = false
	m.setChapterChoices(false)
	m.cursor = 0
	m.phase = PhaseChapters
}

func (m *Model) setChapterChoices(includeNested bool) {
	primaryAvailable := false
	for _, chapter := range m.chapters {
		if chapter.Primary {
			primaryAvailable = true
			break
		}
	}
	m.choices = make([]StartPoint, 0, len(m.chapters))
	for index, chapter := range m.chapters {
		if !includeNested && primaryAvailable && !chapter.Primary {
			continue
		}
		m.choices = append(m.choices, StartPoint{
			Label: chapter.Title, StartIndex: chapter.StartIndex, StartSeq: chapter.StartSeq, ChapterIndex: index,
		})
	}
}

func (m *Model) EnableChapterSearch() bool {
	if m.phase != PhaseChapters || m.chapterSearch {
		return false
	}
	m.chapterSearch = true
	m.setChapterChoices(true)
	m.cursor = 0
	return true
}

func (m *Model) ResetChapterSearch() bool {
	if m.phase != PhaseChapters || !m.chapterSearch {
		return false
	}
	m.chapterSearch = false
	m.setChapterChoices(false)
	m.cursor = 0
	return true
}

func (m *Model) beginBrowse(path, relinkFrom string, returnPhase Phase) Action {
	m.browserPath = path
	m.browserHidden = false
	m.browserNote = ""
	m.browserEntries = nil
	m.relinkFrom = relinkFrom
	m.returnPhase = returnPhase
	m.phase = PhaseBrowse
	return m.browseAction()
}

func (m *Model) browseAction() Action {
	return Action{Kind: ActionBrowseDirectory, Path: m.browserPath, ShowHidden: m.browserHidden, RelinkFrom: m.relinkFrom}
}

func (m *Model) parentDirectory() (Action, bool) {
	if m.browserPath == "" {
		return Action{}, false
	}
	parent := filepath.Dir(m.browserPath)
	if parent == m.browserPath {
		return Action{}, false
	}
	m.browserPath = parent
	return m.browseAction(), true
}

func (m *Model) openBrowserPath(path string) (Action, bool) {
	if m.relinkFrom != "" {
		return Action{Kind: ActionRelinkSource, Path: path, RelinkFrom: m.relinkFrom}, true
	}
	return Action{Kind: ActionOpenSource, Path: path}, true
}

// HandleKey updates the model and returns any command that the application
// must execute. Rendering remains a pure projection of model state.
func (m *Model) HandleKey(key string) (Action, bool) {
	if m.phase == PhaseHome {
		switch key {
		case "a":
			m.phase = PhaseAddPath
			m.pathText = ""
		case "b":
			return m.beginBrowse(".", "", PhaseAddPath), true
		case "p":
			return Action{Kind: ActionOpenPreferences}, true
		case "s":
			return Action{Kind: ActionOpenSetup}, true
		case "?":
			m.helpFrom = PhaseHome
			m.phase = PhaseHelp
		case "u":
			if m.undoEntry != nil {
				return Action{Kind: ActionUndoRemove, Entry: *m.undoEntry}, true
			}
		case "q", "ctrl+c":
			m.exitPhase = PhaseHome
			m.phase = PhaseFinished
			return Action{Kind: ActionExit}, true
		}
		return Action{}, false
	}
	if m.phase == PhaseAddPath {
		switch key {
		case "esc":
			m.phase = PhaseHome
		case "b":
			return m.beginBrowse(".", "", PhaseAddPath), true
		case "?":
			m.helpFrom = PhaseAddPath
			m.phase = PhaseHelp
		case "enter":
			path := strings.TrimSpace(m.pathText)
			m.phase = PhaseHome
			if path != "" {
				return Action{Kind: ActionOpenSource, Path: path}, true
			}
		}
		return Action{}, false
	}
	if m.phase == PhaseBrowse {
		switch key {
		case "esc":
			if m.relinkFrom != "" {
				m.phase = PhaseMissing
			} else {
				m.phase = PhaseAddPath
			}
		case "?":
			m.helpFrom = PhaseBrowse
			m.phase = PhaseHelp
		case "hidden":
			m.browserHidden = !m.browserHidden
			return m.browseAction(), true
		case "parent":
			return m.parentDirectory()
		case "folder":
			return m.openBrowserPath(m.browserPath)
		}
		return Action{}, false
	}
	if m.phase == PhaseMissing {
		switch key {
		case "esc":
			m.phase = PhaseHome
		case "?":
			m.helpFrom = PhaseMissing
			m.phase = PhaseHelp
		case "l", "enter":
			return m.beginBrowse(".", m.missingPath, PhaseMissing), true
		case "r":
			path := m.missingPath
			m.phase = PhaseHome
			return Action{Kind: ActionRemoveSource, Path: path}, true
		}
		return Action{}, false
	}
	if m.phase == PhaseChapters {
		switch key {
		case "?":
			m.helpFrom = PhaseChapters
			m.phase = PhaseHelp
			return Action{}, false
		case "esc":
			m.phase = m.returnPhase
			return Action{Kind: ActionCancelChapters}, true
		case "q", "ctrl+c":
			m.phase = PhaseFinished
			return Action{Kind: ActionStop}, true
		}
	}
	if m.phase == PhaseHelp {
		if key == "esc" || key == "?" {
			m.phase = m.helpFrom
		}
		return Action{}, false
	}
	if m.phase == PhaseSetupChoice {
		switch key {
		case "enter", "s":
			m.phase = PhaseSetupProgress
			m.setupNote = "Preparing local speech…"
			return Action{Kind: ActionStartRecommendedSetup}, true
		case "c":
			return Action{Kind: ActionCustomizeSetup}, true
		case "esc", "b":
			m.phase = PhaseHome
			return Action{Kind: ActionCancelSetup}, true
		case "q", "ctrl+c":
			m.exitPhase = PhaseSetupChoice
			m.phase = PhaseFinished
			return Action{Kind: ActionExit}, true
		case "?":
			m.helpFrom = PhaseSetupChoice
			m.phase = PhaseHelp
		}
		return Action{}, false
	}
	if m.phase == PhaseSetupProgress {
		if key == "esc" {
			m.setupNote = "Stopping setup safely…"
			return Action{Kind: ActionCancelSetup}, true
		}
		if key == "q" || key == "ctrl+c" {
			m.setupNote = "Stopping setup safely…"
			return Action{Kind: ActionCancelSetup, ExitAfterCancel: true}, true
		}
		return Action{}, false
	}
	if m.phase == PhaseAdvancedPreferences {
		switch key {
		case "esc", "enter":
			m.phase = PhasePreferences
		case "tab", "shift+tab", "up", "down":
			if m.advancedFocus == 0 {
				m.advancedFocus = 1
			} else {
				m.advancedFocus = 0
			}
		case "left", "h":
			if m.advancedFocus == 1 {
				m.adjustCloudPolicy(-1)
			}
		case "right", "l":
			if m.advancedFocus == 1 {
				m.adjustCloudPolicy(1)
			}
		case "?":
			m.helpFrom = PhaseAdvancedPreferences
			m.phase = PhaseHelp
		}
		return Action{}, false
	}
	if m.phase == PhaseVoicePreview {
		if key == "esc" {
			m.phase = PhasePreferences
			m.previewNote = "Stopping preview…"
			return Action{Kind: ActionCancelPreview}, true
		}
		if key == "q" || key == "ctrl+c" {
			m.exitPhase = PhaseVoicePreview
			m.phase = PhaseFinished
			m.previewNote = "Stopping preview…"
			return Action{Kind: ActionCancelPreview, ExitAfterCancel: true}, true
		}
		// Moving away from a preview always stops it before applying the
		// requested preference navigation or adjustment.
		m.phase = PhasePreferences
		switch key {
		case "up", "k":
			m.prefCursor = max(0, m.prefCursor-1)
		case "down", "j":
			m.prefCursor = min(m.preferenceRows()-1, m.prefCursor+1)
		case "left", "h", "right", "l":
			m.adjustPreference(key == "right" || key == "l")
		}
		return Action{Kind: ActionCancelPreview}, true
	}
	if m.phase == PhasePreferences {
		count := m.preferenceRows()
		switch key {
		case "esc":
			m.returnFromPreferences()
		case "q", "ctrl+c":
			m.exitPhase = PhasePreferences
			m.phase = PhaseFinished
			return Action{Kind: ActionExit}, true
		case "up", "k":
			m.prefCursor = max(0, m.prefCursor-1)
		case "down", "j":
			m.prefCursor = min(count-1, m.prefCursor+1)
		case "left", "h", "right", "l":
			m.adjustPreference(key == "right" || key == "l")
		case "v":
			return m.previewAction(), true
		case "s":
			return Action{Kind: ActionOpenSetup, Preferences: copyPreferences(m.preferences)}, true
		case "enter":
			if m.prefCursor == count-3 {
				m.phase = PhaseAdvancedPreferences
				m.advancedFocus = 0
				return Action{}, false
			}
			if m.prefCursor == count-2 {
				return Action{Kind: ActionSavePreferences, Preferences: copyPreferences(m.preferences)}, true
			}
			if m.prefCursor == count-1 {
				m.returnFromPreferences()
			}
		case "?":
			m.helpFrom = PhasePreferences
			m.phase = PhaseHelp
		}
		return Action{}, false
	}
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
	if m.phase == PhaseExportProgress {
		if key == "esc" || key == "q" || key == "ctrl+c" {
			return Action{Kind: ActionStop}, true
		}
		return Action{}, false
	}
	if m.phase == PhaseStopping {
		return Action{}, false
	}
	if key == "esc" {
		m.phase = PhaseStopping
		m.state.Status = "Saving your place"
		return Action{Kind: ActionReturnHome}, true
	}
	if key == "q" || key == "ctrl+c" {
		m.phase = PhaseFinished
		m.state.Status = "Stopping playback"
		return Action{Kind: ActionStop}, true
	}
	if key == "?" {
		m.helpFrom = m.phase
		m.phase = PhaseHelp
		return Action{}, false
	}
	if key == "+" || key == "=" || key == "-" || key == "_" {
		rate := m.state.PlaybackRate
		if rate < 0.5 || rate > 3 {
			rate = 1
		}
		delta := 0.1
		if key == "-" || key == "_" {
			delta = -0.1
		}
		rate = min(3.0, max(0.5, rate+delta))
		m.state.PlaybackRate = rate
		return Action{Kind: ActionPlaybackRate, Rate: rate}, true
	}
	if !m.chapterNav && (key == "c" || key == "p" || key == "n" || key == "h" || key == "l") {
		m.state.Status = m.chapterNavWhy
		return Action{}, false
	}
	if key == "c" && m.phase == PhasePlaying {
		m.openChapters()
		return Action{Kind: ActionOpenChapters}, true
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

func (m *Model) returnFromPreferences() {
	m.phase = m.returnPhase
	if m.phase == 0 {
		m.phase = PhaseHome
	}
}

func copyPreferences(preferences Preferences) Preferences {
	preferences.Voices = cloneVoiceMap(preferences.Voices)
	preferences.Locales = append([]VoiceChoice(nil), preferences.Locales...)
	preferences.VoiceOptions = append([]VoiceChoice(nil), preferences.VoiceOptions...)
	return preferences
}

func (m Model) preferenceRows() int { return len(m.preferences.Locales) + 6 }

func (m *Model) adjustPreference(increase bool) {
	delta := 0.1
	if !increase {
		delta = -0.1
	}
	localeStart := 1
	speedRow := localeStart + len(m.preferences.Locales)
	rateRow := speedRow + 1
	switch {
	case m.prefCursor == 0:
		if len(m.preferences.VoiceOptions) == 0 {
			return
		}
		index := 0
		for current, voice := range m.preferences.VoiceOptions {
			if voice.ID == m.preferences.DefaultVoice {
				index = current
				break
			}
		}
		index = wrap(index+map[bool]int{true: 1, false: -1}[increase], len(m.preferences.VoiceOptions))
		voice := m.preferences.VoiceOptions[index]
		m.preferences.DefaultVoice = voice.ID
		m.preferences.Voices[voice.Locale] = voice.ID
	case m.prefCursor >= localeStart && m.prefCursor < speedRow:
		locale := m.preferences.Locales[m.prefCursor-localeStart]
		choices := voicesForPreferenceLocale(m.preferences.VoiceOptions, locale.Locale)
		if len(choices) == 0 {
			return
		}
		index := 0
		found := false
		for current, voice := range choices {
			if voice.ID == m.preferences.Voices[locale.Locale] {
				index = current
				found = true
				break
			}
		}
		if !found {
			if increase {
				index = -1
			} else {
				index = 0
			}
		}
		index = wrap(index+map[bool]int{true: 1, false: -1}[increase], len(choices))
		m.preferences.Voices[locale.Locale] = choices[index].ID
	case m.prefCursor == speedRow:
		m.preferences.SpeechSpeed = roundTenth(min(2.0, max(0.5, m.preferences.SpeechSpeed+delta)))
	case m.prefCursor == rateRow:
		m.preferences.PlaybackRate = roundTenth(min(3.0, max(0.5, m.preferences.PlaybackRate+delta)))
	}
}

func (m *Model) adjustCloudPolicy(direction int) {
	choices := []string{"ask", "never", "allowed"}
	index := 0
	for current, choice := range choices {
		if m.preferences.CloudPolicy == choice {
			index = current
			break
		}
	}
	index = wrap(index+direction, len(choices))
	m.preferences.CloudPolicy = choices[index]
}

func (m *Model) previewAction() Action {
	voiceID := m.preferences.DefaultVoice
	localeStart := 1
	speedRow := localeStart + len(m.preferences.Locales)
	if m.prefCursor >= localeStart && m.prefCursor < speedRow {
		voiceID = m.preferences.Voices[m.preferences.Locales[m.prefCursor-localeStart].Locale]
	}
	if voiceID == "" {
		voiceID = m.preferences.DefaultVoice
	}
	m.previewVoice = voiceID
	m.previewNote = "Preparing a short local voice preview…"
	m.phase = PhaseVoicePreview
	return Action{Kind: ActionPreviewVoice, VoiceID: voiceID}
}

func voicesForPreferenceLocale(voices []VoiceChoice, locale string) []VoiceChoice {
	var matches []VoiceChoice
	for _, voice := range voices {
		if voice.Locale == locale {
			matches = append(matches, voice)
		}
	}
	return matches
}

func wrap(index, count int) int {
	if count == 0 {
		return 0
	}
	for index < 0 {
		index += count
	}
	return index % count
}

func roundTenth(value float64) float64 { return float64(int(value*10+0.5)) / 10 }

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
