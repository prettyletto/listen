package playerui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type readyMsg struct{}

type sessionUpdate struct {
	state  State
	phase  Phase
	screen *Model
}

type browserUpdate struct {
	path    string
	entries []BrowserEntry
	hidden  bool
	note    string
}

type previewFinished struct{ note string }
type preferencesNoteUpdate struct{ note string }

type pickerItem struct {
	label  string
	action string
	detail string
}

func (i pickerItem) FilterValue() string { return i.label + " " + i.action + " " + i.detail }

func homeListItems(screen Model) []list.Item {
	items := make([]list.Item, len(screen.homeIndices))
	for index, sourceIndex := range screen.homeIndices {
		if sourceIndex < 0 || sourceIndex >= len(screen.entries) {
			continue
		}
		entry := screen.entries[sourceIndex]
		title := entry.Title
		if title == "" {
			title = entry.Path
		}
		action := "Start listening"
		detail := "No saved position"
		switch {
		case entry.Missing:
			action = "Unavailable · locate or remove"
			detail = "Original file not found"
		case entry.Complete:
			action = "Start again"
			detail = "Finished previously"
		case entry.ResumeLabel != "":
			action = "Continue"
			detail = homePositionLabel(entry.ResumeLabel)
		}
		items[index] = pickerItem{label: title, action: action, detail: detail}
	}
	return items
}

func homePositionLabel(position string) string {
	position = strings.TrimSpace(position)
	switch strings.ToLower(position) {
	case "", "saved place", "continue reading", "continue from your saved place":
		if position == "" {
			return ""
		}
		return "Continue from your saved place"
	}
	if strings.HasPrefix(strings.ToLower(position), "saved at ") {
		return position
	}
	return "Saved at " + position
}

func browserListItems(entries []BrowserEntry) []list.Item {
	items := make([]list.Item, len(entries))
	for index, entry := range entries {
		label := entry.Name
		if entry.Directory {
			label += "/"
		}
		items[index] = pickerItem{label: label}
	}
	return items
}

type teaModel struct {
	screen        Model
	picker        list.Model
	homeList      list.Model
	browserList   list.Model
	pathInput     textinput.Model
	ocrInput      textinput.Model
	spinner       spinner.Model
	spinning      bool
	progressFrame int
	actions       chan<- Action
	ready         chan<- struct{}
	done          <-chan struct{}
	readyMu       *sync.Once
	onSize        func(width, height int)
}

func newTeaModel(screen Model, actions chan<- Action, ready chan<- struct{}) *teaModel {
	items := make([]list.Item, len(screen.choices))
	for index, choice := range screen.choices {
		label := choice.Label
		if choice.Resume {
			label = resumeLabel(choice.Label)
		}
		item := pickerItem{label: label}
		items[index] = item
	}
	picker := list.New(items, pickerDelegate{}, screen.width, max(1, screen.height))
	picker.SetShowTitle(false)
	picker.SetShowFilter(false)
	picker.SetShowStatusBar(false)
	picker.SetShowPagination(false)
	picker.SetShowHelp(false)
	picker.DisableQuitKeybindings()
	picker.SetFilteringEnabled(true)
	picker.FilterInput.Prompt = "Search: "
	picker.FilterInput.PromptStyle = eyebrowStyle
	picker.FilterInput.TextStyle = rowStyle
	homeItems := homeListItems(screen)
	homeList := list.New(homeItems, homePickerDelegate{}, screen.width, max(1, screen.height))
	homeList.SetShowTitle(false)
	homeList.SetShowFilter(false)
	homeList.SetShowStatusBar(false)
	homeList.SetShowPagination(false)
	homeList.SetShowHelp(false)
	homeList.DisableQuitKeybindings()
	homeList.SetFilteringEnabled(true)
	homeList.FilterInput.Prompt = "Search: "
	homeList.FilterInput.PromptStyle = eyebrowStyle
	homeList.FilterInput.TextStyle = rowStyle
	homeList.SetSize(max(1, min(screen.width, 112)-4), max(1, min(8, screen.height-10)))
	homeList.SetDelegate(homePickerDelegate{focused: !screen.continueFocused})
	browserList := list.New(browserListItems(screen.browserEntries), pickerDelegate{}, screen.width, max(1, screen.height))
	browserList.SetShowTitle(false)
	browserList.SetShowFilter(false)
	browserList.SetShowStatusBar(false)
	browserList.SetShowPagination(false)
	browserList.SetShowHelp(false)
	browserList.DisableQuitKeybindings()
	browserList.SetFilteringEnabled(true)
	browserList.FilterInput.Prompt = "Search: "
	browserList.FilterInput.PromptStyle = eyebrowStyle
	browserList.FilterInput.TextStyle = rowStyle
	pathInput := textinput.New()
	pathInput.Prompt = "Path: "
	pathInput.PromptStyle = eyebrowStyle
	pathInput.TextStyle = rowStyle
	pathInput.Placeholder = "/path/to/a/book.epub"
	pathInput.CharLimit = 4096
	pathInput.Width = max(1, screen.width-12)
	pathInput.SetValue(screen.pathText)
	ocrInput := textinput.New()
	ocrInput.Prompt = ""
	ocrInput.Placeholder = "eng+por"
	ocrInput.CharLimit = 520
	ocrInput.Width = max(1, screen.width-12)
	ocrInput.SetValue(screen.preferences.OCRLanguages)
	if screen.phase == PhaseAdvancedPreferences && screen.advancedFocus == 0 {
		_ = ocrInput.Focus()
	}
	for index := 0; index < screen.cursor; index++ {
		picker.CursorDown()
	}
	activitySpinner := spinner.New(
		spinner.WithSpinner(spinner.Points),
		spinner.WithStyle(activitySpinnerStyle),
	)
	return &teaModel{
		screen:      screen,
		picker:      picker,
		homeList:    homeList,
		browserList: browserList,
		pathInput:   pathInput,
		ocrInput:    ocrInput,
		spinner:     activitySpinner,
		actions:     actions,
		ready:       ready,
		readyMu:     &sync.Once{},
	}
}

func resumeLabel(label string) string {
	label = strings.TrimSpace(label)
	label = strings.TrimPrefix(label, "Resume at ")
	if label == "" {
		return "Continue reading"
	}
	return "Continue · " + label
}

func (m *teaModel) Init() tea.Cmd {
	commands := []tea.Cmd{func() tea.Msg { return readyMsg{} }}
	if m.loadingActive() {
		m.spinning = true
		commands = append(commands, m.spinner.Tick)
	}
	return tea.Batch(commands...)
}

func (m *teaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !m.loadingActive() {
			m.spinning = false
			return m, nil
		}
		updated, cmd := m.spinner.Update(msg)
		m.spinner = updated
		m.spinning = cmd != nil
		m.progressFrame++
		return m, cmd
	case readyMsg:
		if m.ready != nil {
			m.readyMu.Do(func() { close(m.ready) })
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.screen.SetSize(msg.Width, msg.Height)
		if m.onSize != nil {
			m.onSize(msg.Width, msg.Height)
		}
		m.resizePicker()
		m.homeList.SetSize(max(1, min(msg.Width, 112)-4), max(1, msg.Height-9))
		m.homeList.FilterInput.Width = max(1, min(msg.Width, 112)-12)
		m.browserList.SetSize(max(1, min(msg.Width, 112)-4), max(1, msg.Height-10))
		m.pathInput.Width = max(1, min(msg.Width, 112)-12)
		m.ocrInput.Width = max(1, min(msg.Width, 112)-12)
		return m, nil
	case sessionUpdate:
		if msg.screen != nil {
			previousSpinner := m.spinner
			replacement := newTeaModel(*msg.screen, m.actions, m.ready)
			replacement.done = m.done
			replacement.onSize = m.onSize
			replacement.spinner = previousSpinner
			if replacement.screen.phase == PhaseAddPath {
				return replacement, replacement.pathInput.Focus()
			}
			return replacement, nil
		}
		wasLoading := m.loadingActive()
		m.screen.SetState(msg.state)
		if msg.phase != 0 {
			m.screen.SetPhase(msg.phase)
		}
		return m, m.startLoadingAnimation(wasLoading)
	case browserUpdate:
		if m.screen.phase == PhaseBrowse {
			m.screen.SetBrowser(msg.path, msg.entries, msg.hidden, msg.note)
			cmd := m.browserList.SetItems(browserListItems(msg.entries))
			m.browserList.ResetSelected()
			return m, cmd
		}
		return m, nil
	case previewFinished:
		if m.screen.phase == PhaseVoicePreview || m.screen.phase == PhasePreferences {
			m.screen.phase = PhasePreferences
			m.screen.previewNote = msg.note
		}
		return m, nil
	case preferencesNoteUpdate:
		if m.screen.phase == PhasePreferences || m.screen.phase == PhaseAdvancedPreferences {
			m.screen.preferencesNote = msg.note
		}
		return m, nil
	case tea.KeyMsg:
		if m.screen.phase == PhaseHome {
			return m.updateHome(msg)
		}
		if m.screen.phase == PhaseAddPath {
			return m.updatePath(msg)
		}
		if m.screen.phase == PhaseSelect {
			return m.updatePicker(msg)
		}
		if m.screen.phase == PhaseBrowse {
			return m.updateBrowser(msg)
		}
		if m.screen.phase == PhaseMissing {
			return m.updateMissing(msg)
		}
		if m.screen.phase == PhaseChapters {
			return m.updateChapterPicker(msg)
		}
		if m.screen.phase == PhaseHelp {
			return m.updateHelp(msg)
		}
		if m.screen.phase == PhaseAdvancedPreferences {
			return m.updateAdvancedPreferences(msg)
		}
		if m.screen.phase == PhaseExportProgress {
			action, ok := m.screen.HandleKey(uiKey(msg))
			if ok {
				return m, m.dispatch(action)
			}
			return m, nil
		}
		key := uiKey(msg)
		previousPhase := m.screen.phase
		action, ok := m.screen.HandleKey(key)
		if previousPhase == PhasePreferences && m.screen.phase == PhaseAdvancedPreferences {
			m.ocrInput.SetValue(m.screen.preferences.OCRLanguages)
			return m, m.ocrInput.Focus()
		}
		if ok {
			if action.Kind == ActionOpenChapters {
				m.rebuildChapterList()
			}
			commands := []tea.Cmd{m.dispatch(action)}
			if action.Kind == ActionStartRecommendedSetup || action.Kind == ActionPreviewVoice {
				if cmd := m.startLoadingAnimation(false); cmd != nil {
					commands = append(commands, cmd)
				}
			}
			return m, tea.Batch(commands...)
		}
	default:
		// Bubbles list filtering returns its matches asynchronously. Forward
		// those messages to the list so the visible items and global selection
		// stay in sync with the search field.
		if m.screen.phase == PhaseSelect || m.screen.phase == PhaseChapters {
			updated, cmd := m.picker.Update(msg)
			m.picker = updated
			return m, cmd
		}
		if m.screen.phase == PhaseBrowse {
			updated, cmd := m.browserList.Update(msg)
			m.browserList = updated
			return m, cmd
		}
		if m.screen.phase == PhaseHome {
			updated, cmd := m.homeList.Update(msg)
			m.homeList = updated
			return m, cmd
		}
	}
	return m, nil
}

func (m *teaModel) updateHome(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := uiKey(msg)
	filterState := m.homeList.FilterState()
	if key == "/" && filterState == list.Unfiltered {
		if len(m.screen.homeIndices) == 0 {
			return m, nil
		}
		m.screen.continueFocused = false
		m.homeList.SetDelegate(homePickerDelegate{focused: true})
		updated, cmd := m.homeList.Update(msg)
		m.homeList = updated
		return m, cmd
	}
	if filterState == list.Filtering {
		updated, cmd := m.homeList.Update(msg)
		m.homeList = updated
		return m, cmd
	}
	if filterState == list.FilterApplied && (key == "esc" || key == "q") {
		m.homeList.ResetFilter()
		return m, nil
	}
	if filterState != list.Filtering {
		switch key {
		case "a":
			m.screen.phase = PhaseAddPath
			m.screen.pathText = ""
			m.screen.pathErr = ""
			m.pathInput.SetValue("")
			m.pathInput.Focus()
			return m, textinput.Blink
		case "b":
			action := m.screen.BeginBrowser(".", "")
			return m, m.dispatch(action)
		case "?":
			m.screen.helpFrom = PhaseHome
			m.screen.phase = PhaseHelp
			return m, nil
		case "p":
			return m, m.dispatch(Action{Kind: ActionOpenPreferences})
		case "s":
			return m, m.dispatch(Action{Kind: ActionOpenSetup})
		case "u":
			if m.screen.undoEntry != nil {
				return m, m.dispatch(Action{Kind: ActionUndoRemove, Entry: *m.screen.undoEntry})
			}
		case "enter":
			if m.screen.continueFocused && m.screen.continueIndex >= 0 {
				return m.openHomeEntry(m.screen.continueIndex)
			}
			visibleIndex := m.homeList.GlobalIndex()
			if visibleIndex >= 0 && visibleIndex < len(m.screen.homeIndices) {
				return m.openHomeEntry(m.screen.homeIndices[visibleIndex])
			}
		case "down":
			if m.screen.continueFocused {
				if len(m.screen.homeIndices) > 0 {
					m.screen.continueFocused = false
					m.homeList.SetDelegate(homePickerDelegate{focused: true})
				}
				return m, nil
			}
		case "up":
			if m.screen.continueIndex >= 0 && m.homeList.GlobalIndex() == 0 {
				m.screen.continueFocused = true
				m.homeList.SetDelegate(homePickerDelegate{focused: false})
				return m, nil
			}
		case "q", "ctrl+c":
			m.screen.exitPhase = PhaseHome
			m.screen.phase = PhaseFinished
			return m, m.dispatch(Action{Kind: ActionExit})
		case "esc":
			return m, nil
		}
	}
	updated, cmd := m.homeList.Update(msg)
	m.homeList = updated
	return m, cmd
}

func (m *teaModel) openHomeEntry(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(m.screen.entries) {
		return m, nil
	}
	entry := m.screen.entries[index]
	if entry.Missing {
		m.screen.OpenMissing(entry.Path)
		return m, nil
	}
	return m, m.dispatch(Action{Kind: ActionOpenSource, Path: entry.Path})
}

func (m *teaModel) updatePath(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := uiKey(msg)
	if key == "esc" {
		m.screen.phase = PhaseHome
		m.pathInput.Blur()
		return m, nil
	}
	if key == "ctrl+o" {
		action := m.screen.BeginBrowser(".", "")
		m.pathInput.Blur()
		return m, m.dispatch(action)
	}
	if key == "?" {
		m.screen.helpFrom = PhaseAddPath
		m.screen.phase = PhaseHelp
		m.pathInput.Blur()
		return m, nil
	}
	if key == "enter" {
		path := strings.TrimSpace(m.pathInput.Value())
		if path == "" {
			m.screen.pathErr = "Enter a local file or folder path."
			return m, nil
		}
		m.screen.pathText = path
		m.screen.phase = PhaseHome
		m.pathInput.Blur()
		return m, m.dispatch(Action{Kind: ActionOpenSource, Path: path})
	}
	var cmd tea.Cmd
	m.pathInput, cmd = m.pathInput.Update(msg)
	m.screen.pathText = m.pathInput.Value()
	return m, cmd
}

func (m *teaModel) updateBrowser(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := uiKey(msg)
	filterState := m.browserList.FilterState()
	if key == "/" && filterState == list.Unfiltered {
		updated, cmd := m.browserList.Update(msg)
		m.browserList = updated
		return m, cmd
	}
	if filterState == list.Filtering {
		updated, cmd := m.browserList.Update(msg)
		m.browserList = updated
		return m, cmd
	}
	if filterState == list.FilterApplied && (key == "esc" || key == "q") {
		m.browserList.ResetFilter()
		return m, nil
	}
	if filterState != list.Filtering {
		switch key {
		case "?":
			m.screen.helpFrom = PhaseBrowse
			m.screen.phase = PhaseHelp
			return m, nil
		case "esc":
			m.screen.phase = m.screen.returnPhase
			if m.screen.phase == 0 {
				m.screen.phase = PhaseAddPath
			}
			if m.screen.phase == PhaseAddPath {
				m.pathInput.Focus()
				return m, textinput.Blink
			}
			return m, nil
		case ".", "h":
			m.screen.browserHidden = !m.screen.browserHidden
			return m, m.dispatch(m.screen.browseAction())
		case "backspace", "left":
			action, ok := m.screen.parentDirectory()
			if ok {
				return m, m.dispatch(action)
			}
		case "f":
			action, ok := m.screen.openBrowserPath(m.screen.browserPath)
			if ok {
				return m, m.dispatch(action)
			}
		case "enter":
			index := m.browserList.GlobalIndex()
			if index >= 0 && index < len(m.screen.browserEntries) {
				entry := m.screen.browserEntries[index]
				if entry.Directory {
					m.screen.browserPath = entry.Path
					return m, m.dispatch(m.screen.browseAction())
				}
				action, _ := m.screen.openBrowserPath(entry.Path)
				return m, m.dispatch(action)
			}
		case "q", "ctrl+c":
			m.screen.exitPhase = PhaseBrowse
			m.screen.phase = PhaseFinished
			return m, m.dispatch(Action{Kind: ActionExit})
		}
	}
	updated, cmd := m.browserList.Update(msg)
	m.browserList = updated
	return m, cmd
}

func (m *teaModel) updateMissing(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := uiKey(msg)
	switch key {
	case "esc":
		m.screen.phase = PhaseHome
		return m, nil
	case "?":
		m.screen.helpFrom = PhaseMissing
		m.screen.phase = PhaseHelp
		return m, nil
	case "l", "enter":
		action := m.screen.BeginBrowser(".", m.screen.missingPath)
		return m, m.dispatch(action)
	case "r":
		path := m.screen.missingPath
		m.screen.phase = PhaseHome
		return m, m.dispatch(Action{Kind: ActionRemoveSource, Path: path})
	case "q", "ctrl+c":
		m.screen.exitPhase = PhaseMissing
		m.screen.phase = PhaseFinished
		return m, m.dispatch(Action{Kind: ActionExit})
	}
	return m, nil
}

func (m *teaModel) updateHelp(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := uiKey(msg)
	if key == "esc" || key == "?" {
		m.screen.phase = m.screen.helpFrom
		if m.screen.phase == PhaseAdvancedPreferences && m.screen.advancedFocus == 0 {
			return m, m.ocrInput.Focus()
		}
	}
	return m, nil
}

func (m *teaModel) updateAdvancedPreferences(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := uiKey(msg)
	switch key {
	case "esc", "enter":
		m.screen.preferences.OCRLanguages = strings.TrimSpace(m.ocrInput.Value())
		m.ocrInput.Blur()
		m.screen.phase = PhasePreferences
		return m, nil
	case "?":
		m.screen.preferences.OCRLanguages = strings.TrimSpace(m.ocrInput.Value())
		m.ocrInput.Blur()
		m.screen.helpFrom = PhaseAdvancedPreferences
		m.screen.phase = PhaseHelp
		return m, nil
	case "tab", "shift+tab", "up", "down":
		m.screen.preferences.OCRLanguages = strings.TrimSpace(m.ocrInput.Value())
		if m.screen.advancedFocus == 0 {
			m.screen.advancedFocus = 1
			m.ocrInput.Blur()
			return m, nil
		}
		m.screen.advancedFocus = 0
		return m, m.ocrInput.Focus()
	}
	if m.screen.advancedFocus == 0 {
		var cmd tea.Cmd
		m.ocrInput, cmd = m.ocrInput.Update(msg)
		m.screen.preferences.OCRLanguages = strings.TrimSpace(m.ocrInput.Value())
		return m, cmd
	}
	_, _ = m.screen.HandleKey(key)
	return m, nil
}

func (m *teaModel) updatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := uiKey(msg)
	if key == "ctrl+c" {
		m.screen.phase = PhaseFinished
		return m, m.dispatch(Action{Kind: ActionStop})
	}
	if m.picker.FilterState() != list.Filtering {
		switch key {
		case "q":
			m.screen.phase = PhaseFinished
			return m, m.dispatch(Action{Kind: ActionStop})
		case "enter":
			index := m.picker.GlobalIndex()
			if index >= 0 && index < len(m.screen.choices) {
				wasLoading := m.loadingActive()
				choice := m.screen.choices[index]
				m.screen.cursor = index
				m.screen.phase = PhasePreparing
				m.screen.state.Status = "Preparing " + choice.Label
				commands := []tea.Cmd{m.dispatch(Action{Kind: ActionStart, Start: choice})}
				if cmd := m.startLoadingAnimation(wasLoading); cmd != nil {
					commands = append(commands, cmd)
				}
				return m, tea.Batch(commands...)
			}
		}
	}
	updated, cmd := m.picker.Update(msg)
	m.picker = updated
	if index := m.picker.GlobalIndex(); index >= 0 && index < len(m.screen.choices) {
		m.screen.cursor = index
	}
	return m, cmd
}

func (m *teaModel) rebuildChapterList() {
	items := make([]list.Item, len(m.screen.choices))
	for index, choice := range m.screen.choices {
		items[index] = pickerItem{label: choice.Label}
	}
	m.picker = list.New(items, pickerDelegate{}, m.screen.width, max(1, m.screen.height))
	m.picker.SetShowTitle(false)
	m.picker.SetShowFilter(false)
	m.picker.SetShowStatusBar(false)
	m.picker.SetShowPagination(false)
	m.picker.SetShowHelp(false)
	m.picker.DisableQuitKeybindings()
	m.picker.SetFilteringEnabled(true)
	m.picker.FilterInput.Prompt = "Search: "
	m.picker.FilterInput.PromptStyle = eyebrowStyle
	m.picker.FilterInput.TextStyle = rowStyle
	m.resizePicker()
}

func (m *teaModel) updateChapterPicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := uiKey(msg)
	filterState := m.picker.FilterState()
	if filterState == list.Filtering {
		if key == "esc" {
			m.picker.ResetFilter()
			m.screen.ResetChapterSearch()
			m.rebuildChapterList()
			return m, nil
		}
		updated, cmd := m.picker.Update(msg)
		m.picker = updated
		return m, cmd
	}
	if filterState == list.FilterApplied && key == "esc" {
		m.picker.ResetFilter()
		m.screen.ResetChapterSearch()
		m.rebuildChapterList()
		return m, nil
	}
	if key == "ctrl+c" || key == "q" {
		m.screen.phase = PhaseFinished
		return m, m.dispatch(Action{Kind: ActionStop})
	}
	if key == "esc" {
		action, _ := m.screen.HandleKey("esc")
		return m, m.dispatch(action)
	}
	if key == "?" {
		m.screen.helpFrom = PhaseChapters
		m.screen.phase = PhaseHelp
		return m, nil
	}
	if key == "/" && filterState == list.Unfiltered {
		m.screen.EnableChapterSearch()
		m.rebuildChapterList()
		updated, cmd := m.picker.Update(msg)
		m.picker = updated
		return m, cmd
	}
	if key == "enter" && filterState != list.Filtering {
		index := m.picker.GlobalIndex()
		if index >= 0 && index < len(m.screen.choices) {
			choice := m.screen.choices[index]
			m.screen.cursor = index
			m.screen.phase = PhasePreparing
			m.screen.state.Status = "Preparing " + choice.Label
			return m, m.dispatch(Action{Kind: ActionSelectChapter, Start: choice})
		}
	}
	updated, cmd := m.picker.Update(msg)
	m.picker = updated
	if index := m.picker.GlobalIndex(); index >= 0 && index < len(m.screen.choices) {
		m.screen.cursor = index
	}
	return m, cmd
}

func uiKey(msg tea.KeyMsg) string {
	if msg.Type == tea.KeyCtrlC {
		return "ctrl+c"
	}
	if msg.Type == tea.KeySpace {
		return "space"
	}
	return msg.String()
}

func (m *teaModel) dispatch(action Action) tea.Cmd {
	if m.actions == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case m.actions <- action:
		case <-m.done:
		}
		return nil
	}
}

func (m *teaModel) loadingActive() bool {
	if m.screen.phase == PhaseSelect || m.screen.phase == PhaseFinished {
		return false
	}
	if m.screen.phase == PhaseSetupProgress || m.screen.phase == PhaseVoicePreview {
		return true
	}
	if m.screen.phase == PhaseExportProgress {
		return m.screen.state.Total > 0 && clamp(m.screen.state.Ready, 0, m.screen.state.Total) < m.screen.state.Total
	}
	if m.screen.phase == PhasePreparing || m.screen.state.SeekPending != 0 {
		return true
	}
	total := max(0, m.screen.state.Total)
	return total > 0 && clamp(m.screen.state.Ready, 0, total) < total
}

func (m *teaModel) startLoadingAnimation(wasLoading bool) tea.Cmd {
	if wasLoading || !m.loadingActive() || m.spinning {
		return nil
	}
	m.spinning = true
	return m.spinner.Tick
}

func (m *teaModel) resizePicker() {
	width, height := m.layoutSize()
	listHeight := max(1, height-m.pickerChromeLines())
	m.picker.SetSize(width, listHeight)
}

func (m *teaModel) layoutSize() (int, int) {
	width, height := m.screen.Size()
	contentWidth := min(width, 112)
	return max(1, contentWidth-4), max(1, height)
}

func (m *teaModel) pickerChromeLines() int {
	width, _ := m.layoutSize()
	height := lipgloss.Height(m.pickerHeader(width)) + 2 + lipgloss.Height(m.pickerFooter(width))
	if m.searchVisible() {
		height++ // visible search input
	}
	if m.screen.height >= 13 {
		height++ // selected-choice explanation
	}
	return height
}

func (m *teaModel) View() string {
	var content string
	if m.screen.width < 32 || m.screen.height < 10 {
		content = renderResizePrompt(m.screen.width, m.screen.height)
	} else if m.screen.phase == PhaseSelect || m.screen.phase == PhaseChapters {
		content = m.pickerView()
	} else if m.screen.phase == PhaseHome || m.screen.phase == PhaseAddPath {
		content = m.homeView()
	} else if m.screen.phase == PhaseBrowse {
		content = m.browserView()
	} else if m.screen.phase == PhaseMissing {
		content = m.missingView()
	} else if m.screen.phase == PhaseHelp {
		content = m.helpView()
	} else if m.screen.phase == PhasePreferences {
		content = m.preferencesView()
	} else if m.screen.phase == PhaseAdvancedPreferences {
		content = m.advancedPreferencesView()
	} else if m.screen.phase == PhaseSetupChoice {
		content = m.setupChoiceView()
	} else if m.screen.phase == PhaseSetupProgress {
		content = m.setupProgressView()
	} else if m.screen.phase == PhaseVoicePreview {
		content = m.voicePreviewView()
	} else if m.screen.phase == PhaseExportProgress {
		content = renderExportProgress(m.screen, m.spinner.View(), m.progressFrame)
	} else if m.screen.phase == PhaseFinished {
		switch m.screen.exitPhase {
		case PhaseHome:
			content = m.homeView()
		case PhaseAddPath:
			content = m.homeView()
		case PhaseBrowse:
			content = m.browserView()
		case PhaseMissing:
			content = m.missingView()
		case PhaseSetupChoice:
			content = m.setupChoiceView()
		case PhasePreferences:
			content = m.preferencesView()
		case PhaseAdvancedPreferences:
			content = m.advancedPreferencesView()
		case PhaseVoicePreview:
			content = m.voicePreviewView()
		default:
			content = renderPlayer(m.screen, m.spinner.View())
		}
	} else {
		content = renderPlayer(m.screen, m.spinner.View())
	}
	return content
}

func renderExportProgress(screen Model, spinnerView string, progressFrame int) string {
	innerWidth := max(1, min(screen.width, 104)-4)
	compact := screen.width < 48 || screen.height < 16
	percent := 0
	if screen.state.Total > 0 {
		percent = clamp(screen.state.Ready*100/screen.state.Total, 0, 100)
	}
	status := strings.TrimSpace(screen.state.Status)
	if status == "" {
		status = "Starting audiobook export"
	}
	marker := "●"
	if spinnerView != "" && percent < 100 {
		marker = spinnerView
	}
	statusLine := statusStyle(status).Padding(0, 1).Width(innerWidth).Render(
		marker + "  " + clip(status, max(1, innerWidth-lipgloss.Width(marker)-5)),
	)
	progressLabel := fmt.Sprintf("%3d%%", percent)
	if percent == 0 && strings.HasPrefix(strings.ToLower(status), "generating audiobook audio:") {
		progressLabel = "WORKING"
	}
	barWidth := min(48, max(8, innerWidth-lipgloss.Width(progressLabel)-2))
	progressLine := lipgloss.JoinHorizontal(
		lipgloss.Left,
		mutedStyle.Render(progressLabel+" "),
		activeProgressBar(percent, progressFrame, barWidth),
	)
	sections := []string{
		headerEyebrow("AUDIOBOOK EXPORT"),
		titleStyle.Width(innerWidth).Render(clip(screen.title, innerWidth)),
		separatorStyle.Render(strings.Repeat("─", innerWidth)),
		statusLine,
	}
	if !compact {
		sections = append(sections, lipgloss.NewStyle().Height(1).Render(""))
	}
	sections = append(sections,
		sectionLabelStyle.Render("PROGRESS"),
		progressLine,
		mutedStyle.Width(innerWidth).Render(clip("Preparing the book, then creating its audiobook files.", innerWidth)),
	)
	if !compact {
		sections = append(sections, lipgloss.NewStyle().Height(1).Render(""))
	}
	sections = append(sections,
		separatorStyle.Render(strings.Repeat("─", innerWidth)),
		footerStyle.Width(innerWidth).Render(clip("Esc or q cancels export", innerWidth)),
	)
	body := lipgloss.JoinVertical(lipgloss.Left, sections...)
	if screen.width >= 64 && screen.height >= 18 {
		body = playerFrameStyle.Render(body)
		return lipgloss.Place(screen.width, screen.height, lipgloss.Center, lipgloss.Center, body)
	}
	return centerBlock(screen.width, body)
}

func activeProgressBar(percent, frame, width int) string {
	width = max(1, width)
	percent = clamp(percent, 0, 100)
	filled := width * percent / 100
	remaining := width - filled
	if remaining == 0 {
		return bufferActiveStyle.Render(strings.Repeat("━", width))
	}
	pulse := min(8, remaining)
	travel := remaining - pulse
	offset := 0
	if travel > 0 {
		cycle := travel * 2
		position := frame % cycle
		if position > travel {
			position = cycle - position
		}
		offset = position
	}
	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		bufferActiveStyle.Render(strings.Repeat("━", filled)),
		bufferTrackStyle.Render(strings.Repeat("─", offset)),
		sectionLabelStyle.Render(strings.Repeat("━", pulse)),
		bufferTrackStyle.Render(strings.Repeat("─", remaining-offset-pulse)),
	)
}

func (m *teaModel) homeView() string {
	width := max(1, min(m.screen.width, 112)-4)
	compact := m.screen.width < 48 || m.screen.height < 18
	searchState := m.homeList.FilterState()
	searchOpen := searchState == list.Filtering || searchState == list.FilterApplied
	recentItems := len(m.screen.homeIndices)
	if searchOpen {
		recentItems = len(m.homeList.VisibleItems())
	}
	mastheadRows := 4 // eyebrow, title, tagline, separator
	if compact {
		mastheadRows = 3 // keep the recent-source section visible on small terminals
	}
	reserved := mastheadRows + 3 // masthead, home actions, and exit help
	if m.screen.homeNote != "" {
		reserved++
	}
	if m.screen.continueIndex >= 0 {
		reserved += 4 // section label and three persistent continue lines
	} else if len(m.screen.entries) == 0 {
		reserved += 3 // empty-library invitation and first-step hint
	}
	if len(m.screen.homeIndices) > 0 {
		reserved++ // recent sources label
		if searchOpen {
			reserved++ // visible query field
			if recentItems == 0 {
				reserved++ // actionable empty-search message
			}
		}
	}
	visibleRecentCount := 0
	if recentCount := len(m.screen.homeIndices); recentCount > 0 {
		available := max(3, m.screen.height-reserved)
		maxVisible := max(1, available/3)
		if compact {
			maxVisible = min(maxVisible, 2)
		}
		if searchOpen {
			recentCount = recentItems
		}
		visibleRecentCount = min(recentCount, maxVisible)
		listHeight := max(1, visibleRecentCount*3)
		m.homeList.SetSize(width, listHeight)
	} else {
		m.homeList.SetSize(width, 1)
	}
	sections := []string{
		headerEyebrow("YOUR LIBRARY"),
		titleStyle.Width(width).Render("Your listening home"),
	}
	if !compact {
		sections = append(sections, mutedStyle.Width(width).Render(clip("Pick up where you left off.", width)))
	}
	sections = append(sections, separatorStyle.Render(strings.Repeat("─", width)))
	if m.screen.phase == PhaseAddPath {
		sections = append(sections, sectionEyebrow("ADD A SOURCE"), titleStyle.Render("Choose a book or document"), m.pathInput.View())
		if m.screen.pathErr != "" {
			sections = append(sections, warningStyle.Render(clip(m.screen.pathErr, width)))
		}
		sections = append(sections, footerStyle.Width(width).Render(clip("Enter listen · Ctrl+O browse · Esc back", width)))
	} else {
		if m.screen.homeNote != "" {
			sections = append(sections, noteLine(m.screen.homeNote, width))
		}
		if m.screen.continueIndex >= 0 {
			entry := m.screen.entries[m.screen.continueIndex]
			title := entry.Title
			if title == "" {
				title = entry.Path
			}
			style := continueCardStyle
			if !m.screen.continueFocused {
				style = continueIdleStyle
			}
			cardWidth := max(1, width-2)
			position := homePositionLabel(entry.ResumeLabel)
			if position == "" {
				position = "Continue from the beginning"
			}
			action := "  Enter  Continue listening"
			if m.screen.continueFocused {
				action = "> Enter  Continue listening"
			}
			cardContent := lipgloss.JoinVertical(lipgloss.Left,
				titleStyle.Render(clip(title, cardWidth)),
				actionKeyStyle.Render(clip(action, cardWidth)),
				mutedStyle.Render(clip(position, cardWidth)),
			)
			sections = append(sections, sectionEyebrow("CONTINUE LISTENING"), style.Render(cardContent))
		} else if len(m.screen.entries) == 0 {
			emptyActions := joinChips([]string{actionChip("a", "Add source"), actionChip("b", "Browse")}, width)
			sections = append(sections,
				sectionEyebrow("START YOUR LIBRARY"),
				titleStyle.Width(width).Render("Add something to listen to"),
				mutedStyle.Width(width).Render(clip("Start with a local book or document.", width)),
				emptyActions,
			)
		}
		if len(m.screen.homeIndices) > 0 {
			recentHeading := fmt.Sprintf("RECENT SOURCES · %d", len(m.screen.homeIndices))
			if searchOpen {
				recentHeading = fmt.Sprintf("SEARCH RESULTS · %d", recentItems)
			}
			sections = append(sections, sectionEyebrow(recentHeading))
			if searchOpen {
				sections = append(sections, m.homeList.FilterInput.View())
			}
			if searchOpen && recentItems == 0 {
				sections = append(sections, mutedStyle.Width(width).Render(clip("No recent sources match. Esc clears the search.", width)))
			} else {
				sections = append(sections, m.homeList.View())
			}
		}
		actions := homeActionRows(width)
		if len(m.screen.entries) == 0 {
			sections = append(sections, actions[1:]...)
		} else {
			sections = append(sections, actions...)
		}
		footer := "? help · q quit"
		if searchState == list.Filtering {
			footer = "Esc cancel search · Enter apply"
		} else if searchState == list.FilterApplied {
			footer = "Esc clear · Enter open · ↑/↓ move"
		} else if len(m.screen.homeIndices) > visibleRecentCount {
			footer = "↓ more · / search · ? help · q quit"
		}
		if m.screen.undoEntry != nil {
			if searchOpen {
				footer += " · u undo"
			} else {
				footer = "u undo remove · ? help · q quit"
			}
		}
		sections = append(sections, footerStyle.Width(width).Render(clip(footer, width)))
	}
	content := lipgloss.JoinVertical(lipgloss.Left, sections...)
	if m.screen.phase == PhaseHome && !compact {
		return lipgloss.Place(m.screen.width, m.screen.height, lipgloss.Center, lipgloss.Center, content)
	}
	return centerBlock(m.screen.width, content)
}

func headerEyebrow(section string) string {
	brand := brandStyle.Render(" LISTEN ")
	return lipgloss.JoinHorizontal(lipgloss.Center, brand, eyebrowStyle.Render("  /  "+section))
}

func sectionEyebrow(label string) string {
	return sectionLabelStyle.Render("◆  " + label)
}

func noteLine(note string, width int) string {
	style := noteStyle
	if strings.Contains(strings.ToLower(note), "could not") || strings.Contains(strings.ToLower(note), "failed") || strings.Contains(strings.ToLower(note), "unavailable") {
		style = warningStyle
	}
	return style.Width(width).Render(clip(note, width))
}

func actionChip(key, label string) string {
	return actionChipStyle.Render(actionKeyStyle.Render(" "+key+" ") + " " + label)
}

func homeActionRows(width int) []string {
	first := []string{actionChip("a", "Add source"), actionChip("b", "Browse")}
	settingsLabel := "Preferences"
	if width < 56 {
		settingsLabel = "Settings"
	}
	second := []string{actionChip("s", "Setup / repair"), actionChip("p", settingsLabel)}
	return []string{joinChips(first, width), joinChips(second, width)}
}

func joinChips(chips []string, width int) string {
	row := lipgloss.JoinHorizontal(lipgloss.Top, chips[0], " ", chips[1])
	if lipgloss.Width(row) <= width {
		return row
	}
	return lipgloss.JoinVertical(lipgloss.Left, chips...)
}

func (m *teaModel) browserView() string {
	width := max(1, min(m.screen.width, 112)-4)
	narrow := m.screen.width < 64
	footerLines := 1
	if narrow {
		footerLines = 3
	}
	head := "ADD FROM THIS FOLDER"
	if m.screen.relinkFrom != "" {
		head = "LOCATE A REPLACEMENT"
	}
	listHeight := max(1, min(8, m.screen.height-10))
	if narrow {
		chromeLines := 4 + footerLines
		if m.screen.browserNote != "" {
			chromeLines++
		}
		if m.browserList.FilterState() == list.Filtering || m.browserList.FilterState() == list.FilterApplied {
			chromeLines++
		}
		listHeight = max(1, min(8, m.screen.height-chromeLines))
	}
	m.browserList.SetSize(width, listHeight)
	footer := m.browserFooter(width)
	sections := []string{
		headerEyebrow("BROWSE"),
		titleStyle.Width(width).Render(head),
		mutedStyle.Width(width).Render(browserPathLabel(m.screen.browserPath, width, narrow)),
		separatorStyle.Render(strings.Repeat("─", width)),
	}
	if m.screen.browserNote != "" {
		sections = append(sections, warningStyle.Width(width).Render(clip(m.screen.browserNote, width)))
	}
	if m.browserList.FilterState() == list.Filtering || m.browserList.FilterState() == list.FilterApplied {
		sections = append(sections, lipgloss.NewStyle().Width(width).Render(m.browserList.FilterInput.View()))
	}
	if len(m.browserList.VisibleItems()) == 0 {
		emptyMessage := "No visible items in this folder."
		if m.browserList.FilterState() != list.Unfiltered {
			emptyMessage = "No matching items. Press Esc to clear search."
		}
		sections = append(sections, mutedStyle.Width(width).Render(clip(emptyMessage, width)))
	} else {
		sections = append(sections, m.browserList.View())
	}
	for _, line := range footer {
		sections = append(sections, footerStyle.Width(width).Render(clip(line, width)))
	}
	return lipgloss.PlaceHorizontal(m.screen.width, lipgloss.Left, lipgloss.JoinVertical(lipgloss.Left, sections...))
}

func (m *teaModel) browserFooter(width int) []string {
	if m.screen.width >= 64 {
		return []string{"Enter open · f use folder · . hidden · / search · Backspace up · Esc back"}
	}
	filterState := m.browserList.FilterState()
	if filterState == list.Filtering {
		return []string{
			"Type to search · Enter apply",
			"Backspace edit · Esc cancel",
			m.browserCountCue(width),
		}
	}
	escapeAction := "back"
	if filterState == list.FilterApplied {
		escapeAction = "clear"
	}
	return []string{
		"Enter open · f select · . hidden",
		"/ search · Bksp up · Esc " + escapeAction,
		m.browserCountCue(width),
	}
}

func (m *teaModel) browserCountCue(width int) string {
	items := len(m.browserList.VisibleItems())
	if items == 0 {
		help := " · ? help"
		if m.browserList.FilterState() == list.Filtering {
			help = ""
		}
		if m.browserList.FilterState() != list.Unfiltered {
			return clip("0 matches"+help, width)
		}
		return clip("0 items"+help, width)
	}
	perPage := max(1, m.browserList.Paginator.PerPage)
	start := m.browserList.Paginator.Page*perPage + 1
	end := min(items, start+perPage-1)
	cue := fmt.Sprintf("%d-%d of %d", start, end, items)
	if start > 1 {
		cue += " · ↑ previous"
	}
	if end < items {
		cue += " · ↓ more"
	}
	if m.browserList.FilterState() != list.Filtering {
		cue += " · ? help"
	}
	return clip(cue, width)
}

func browserPathLabel(path string, width int, narrow bool) string {
	if !narrow || lipgloss.Width(path) <= width {
		return clip(path, width)
	}
	path = filepath.Clean(path)
	separator := string(filepath.Separator)
	parts := strings.Split(strings.Trim(path, separator), separator)
	var suffix string
	for index := len(parts) - 1; index >= 0; index-- {
		part := parts[index]
		if part == "" {
			continue
		}
		candidate := part
		if suffix != "" {
			candidate = part + separator + suffix
		}
		if lipgloss.Width(candidate)+2 > width {
			if suffix == "" {
				suffix = tailCells(part, max(1, width-2))
			}
			break
		}
		suffix = candidate
	}
	if suffix == "" {
		return clip(path, width)
	}
	return "…" + separator + suffix
}

func tailCells(value string, maxCells int) string {
	runes := []rune(value)
	start := len(runes)
	width := 0
	for start > 0 {
		last := runes[start-1]
		cellWidth := lipgloss.Width(string(last))
		if width+cellWidth > maxCells {
			break
		}
		width += cellWidth
		start--
	}
	return string(runes[start:])
}

func (m *teaModel) missingView() string {
	width := max(1, min(m.screen.width, 112)-4)
	path := clip(m.screen.missingPath, width)
	sections := []string{
		headerEyebrow("SOURCE UNAVAILABLE"),
		titleStyle.Width(width).Render("This source can’t be found"),
		mutedStyle.Width(width).Render(path),
		separatorStyle.Render(strings.Repeat("─", width)),
		selectedRowStyle.Width(width).Render("l  Locate a replacement"),
		rowStyle.Width(width).Render("r  Remove from home"),
		footerStyle.Width(width).Render("Esc back · ? help"),
	}
	return lipgloss.PlaceHorizontal(m.screen.width, lipgloss.Left, lipgloss.JoinVertical(lipgloss.Left, sections...))
}

func (m *teaModel) helpView() string {
	width := max(1, min(m.screen.width, 112)-4)
	title := "Keyboard help"
	var rows []string
	switch m.screen.helpFrom {
	case PhaseHome:
		title = "Your listening home"
		rows = []string{"Enter open focused item · ↑/↓ move selection", "/ search recent sources", "a add · b browse · s set up or repair speech", "p preferences · u undo · q quit"}
	case PhaseAddPath:
		title = "Add a source"
		rows = []string{"Enter open the typed path", "Ctrl+O browse from the current folder", "Esc return to your listening home"}
	case PhaseBrowse:
		title = "Choose a local source"
		if m.screen.width < 64 {
			title = "Browse keys"
			rows = []string{"Enter open · f select folder", ". hidden files · / search", "Bksp parent · Esc back"}
		} else {
			rows = []string{"Enter open a file or navigate into a folder", "f select this folder as documentation", ". show or hide hidden files · / search", "Backspace go up · Esc back"}
		}
	case PhaseMissing:
		title = "Recover a source"
		rows = []string{"l locate the moved source", "r remove its entry from home", "Esc keep it in home and go back"}
	case PhaseChapters:
		title = "Choose a chapter"
		rows = []string{"Enter start here · / search all sections", "Esc return to playback · q stop"}
	case PhaseSetupChoice:
		title = "Prepare local speech"
		rows = []string{"Enter or s start recommended setup", "c customize voices and listening speeds", "Esc go back · q quit"}
	case PhasePreferences:
		title = "Listening preferences"
		rows = []string{"↑/↓ choose · ←/→ adjust voice or speed", "v preview voice · s set up or repair local speech", "Enter open or save · Esc go back"}
	case PhaseAdvancedPreferences:
		title = "Advanced listening settings"
		rows = []string{"Edit PDF scan languages, such as eng+por", "Tab to translation privacy · ←/→ choose", "Enter return to preferences · Esc return"}
	case PhaseVoicePreview:
		title = "Voice preview"
		rows = []string{"A short sample is playing locally.", "Esc stop and return to preferences"}
	default:
		title = "Playback controls"
		rows = []string{"Space pause or resume · ←/→ skip 5 seconds", "p/n main chapter · c browse chapters", "Esc save your place and return home · q save and exit"}
		if !m.screen.chapterNav {
			rows = []string{"Space pause or resume · ←/→ skip 5 seconds", "Chapter controls are unavailable.", m.screen.chapterNavWhy, "Esc save your place and return home · q save and exit"}
		}
	}
	sections := []string{headerEyebrow("HELP"), titleStyle.Width(width).Render(title)}
	for _, row := range rows {
		sections = append(sections, mutedStyle.Width(width).Render(clip(row, width)))
	}
	sections = append(sections, footerStyle.Width(width).Render("Esc or ? back"))
	return lipgloss.PlaceHorizontal(m.screen.width, lipgloss.Left, lipgloss.JoinVertical(lipgloss.Left, sections...))
}

func (m *teaModel) setupChoiceView() string {
	width := max(1, min(m.screen.width, 112)-4)
	source := m.screen.setupSource
	title := "Prepare local speech"
	description := "Local voices · no book needed"
	if source != "" {
		title = "Prepare speech for this source"
		description = "Selected: " + filepath.Base(source)
	}
	sections := []string{
		headerEyebrow("SETUP & REPAIR"),
		titleStyle.Width(width).Render(title),
		mutedStyle.Width(width).Render(clip(description, width)),
		separatorStyle.Render(strings.Repeat("─", width)),
		mutedStyle.Width(width).Render(clip("Downloads stay on this device.", width)),
		mutedStyle.Width(width).Render(clip("First setup may take minutes.", width)),
		selectedRowStyle.Width(width).Render("Enter  Start setup or repair"),
		rowStyle.Width(width).Render("c  Set voice and speed first"),
	}
	if m.screen.setupNote != "" {
		sections = append(sections, warningStyle.Width(width).Render(clip(m.screen.setupNote, width)))
	}
	sections = append(sections, footerStyle.Width(width).Render(clip("Esc home · ? help · q quit", width)))
	content := lipgloss.JoinVertical(lipgloss.Left, sections...)
	if m.screen.width >= 64 && lipgloss.Height(content) <= m.screen.height {
		return lipgloss.Place(m.screen.width, m.screen.height, lipgloss.Center, lipgloss.Center, content)
	}
	return lipgloss.PlaceHorizontal(m.screen.width, lipgloss.Left, content)
}

func (m *teaModel) setupProgressView() string {
	width := max(1, min(m.screen.width, 112)-4)
	note := m.screen.setupNote
	if note == "" {
		note = "Preparing local speech…"
	}
	marker := m.spinner.View()
	if marker == "" {
		marker = "•"
	}
	sections := []string{
		headerEyebrow("SETUP IN PROGRESS"),
		titleStyle.Width(width).Render("Setting up speech on this device"),
		separatorStyle.Render(strings.Repeat("─", width)),
		setupProgressCardStyle.Width(max(1, width-4)).Render(statusStyle(note).Render(marker + "  " + clip(note, max(1, width-7)))),
		mutedStyle.Width(width).Render(clip("Downloaded files stay in your local listen data directory.", width)),
		footerStyle.Width(width).Render(clip("Esc cancel setup · q cancel and quit", width)),
	}
	return centerBlock(m.screen.width, lipgloss.JoinVertical(lipgloss.Left, sections...))
}

func (m *teaModel) preferencesView() string {
	width := max(1, min(m.screen.width, 112)-4)
	rows := m.preferenceLines()
	header := []string{
		headerEyebrow("PREFERENCES"),
		titleStyle.Width(width).Render("Make listening yours"),
		mutedStyle.Width(width).Render(clip("Voice and speech speed apply next session.", width)),
		preferenceReadiness(m.screen.preferences.SetupReady, m.screen.preferences.SetupStatus, width),
		separatorStyle.Render(strings.Repeat("─", width)),
	}
	footer := m.preferencesFooter(width)
	notes := make([]string, 0, 2)
	if m.screen.previewNote != "" {
		notes = append(notes, mutedStyle.Width(width).Render(clip(m.screen.previewNote, width)))
	}
	if m.screen.preferencesNote != "" {
		notes = append(notes, warningStyle.Width(width).Render(clip(m.screen.preferencesNote, width)))
	}
	fixedRows := renderedLineCount(header) + renderedLineCount(notes) + renderedLineCount(footer)
	visible := max(1, m.screen.height-fixedRows)
	if len(rows) > visible {
		visible = max(1, visible-1) // reserve a row for the position cue
	}
	start := 0
	if m.screen.prefCursor >= visible {
		start = m.screen.prefCursor - visible + 1
	}
	end := min(len(rows), start+visible)
	sections := append([]string(nil), header...)
	for index := start; index < end; index++ {
		style := rowStyle
		marker := "  "
		if index == m.screen.prefCursor {
			style = selectedRowStyle
			marker = "› "
			selectedContentWidth := max(1, width-style.GetPaddingLeft())
			sections = append(sections, style.Width(width).Render(clip(marker+rows[index], selectedContentWidth)))
			continue
		}
		line := renderPreferenceLine(rows[index], max(1, width-2), style)
		sections = append(sections, lipgloss.JoinHorizontal(lipgloss.Left, marker, line))
	}
	if start > 0 || end < len(rows) {
		position := fmt.Sprintf("%d-%d of %d", start+1, end, len(rows))
		if start > 0 {
			position += " · ↑ previous"
		}
		if end < len(rows) {
			position += " · ↓ more"
		}
		sections = append(sections, mutedStyle.Width(width).Render(clip(position, width)))
	}
	sections = append(sections, notes...)
	sections = append(sections, footer...)
	return centerBlock(m.screen.width, lipgloss.JoinVertical(lipgloss.Left, sections...))
}

func (m *teaModel) preferencesFooter(width int) []string {
	controls := []string{
		"↑/↓ move · ←/→ change · v preview · s setup/repair",
		"Enter open/save · Esc back · q quit",
	}
	if width < 64 {
		controls = []string{
			"↑/↓ move · ←/→ change",
			"v preview · s setup/repair",
			"Enter open/save · Esc back · q quit",
		}
	}
	lines := make([]string, len(controls))
	for index, control := range controls {
		lines[index] = footerStyle.Width(width).Render(clip(control, width))
	}
	return lines
}

func renderedLineCount(sections []string) int {
	lines := 0
	for _, section := range sections {
		lines += lipgloss.Height(section)
	}
	return lines
}

func (m *teaModel) preferenceLines() []string {
	p := m.screen.preferences
	lines := []string{"Default voice  " + voiceLabel(p.VoiceOptions, p.DefaultVoice)}
	for _, locale := range p.Locales {
		lines = append(lines, locale.LocaleName+"  "+voiceLabel(voicesForPreferenceLocale(p.VoiceOptions, locale.Locale), p.Voices[locale.Locale]))
	}
	lines = append(lines,
		fmt.Sprintf("Speech speed  %.1fx", p.SpeechSpeed),
		fmt.Sprintf("Playback speed  %.1fx", p.PlaybackRate),
		"Advanced settings  PDF languages · translation privacy",
		"Save preferences",
		"Back without saving",
	)
	return lines
}

func renderPreferenceLine(line string, width int, style lipgloss.Style) string {
	separator := strings.Index(line, "  ")
	if separator < 0 {
		return style.Width(width).Render(clip(line, width))
	}
	label := strings.TrimRight(line[:separator], " ")
	value := strings.TrimSpace(line[separator:])
	// The rendered line contains two spaces between label and value and the
	// value chip adds another space on each side of its text.
	valueWidth := max(1, width-lipgloss.Width(label)-4)
	value = clip(value, valueWidth)
	return style.Width(width).Render(label + "  " + valueChip(value))
}

func valueChip(value string) string {
	return valueChipStyle.Render(" " + value + " ")
}

func preferenceReadiness(ready bool, status string, width int) string {
	if status == "" {
		status = "Local speech setup status is unknown."
	}
	style := noteStyle
	marker := "READY"
	if !ready {
		style = warningStyle
		marker = "SETUP NEEDED"
	}
	label := marker + "  " + status
	return style.Width(width).Render(clip(label, width))
}

func (m *teaModel) advancedPreferencesView() string {
	width := max(1, min(m.screen.width, 112)-4)
	sections := []string{
		headerEyebrow("ADVANCED SETTINGS"),
		titleStyle.Width(width).Render("Privacy and PDF scanning"),
		mutedStyle.Width(width).Render(clip("These choices apply when the matching feature is used.", width)),
		separatorStyle.Render(strings.Repeat("─", width)),
		sectionEyebrow("PDF SCAN LANGUAGES"),
		m.ocrInput.View(),
		mutedStyle.Width(width).Render(clip("For scanned PDFs; use + between languages, e.g. eng+por.", width)),
		sectionEyebrow("TRANSLATION PRIVACY"),
	}
	policyLabel := cloudPolicyLabel(m.screen.preferences.CloudPolicy)
	policyRow := "←/→  " + policyLabel
	if m.screen.advancedFocus == 1 {
		sections = append(sections, selectedRowStyle.Width(width).Render(clip(policyRow, max(1, width-1))))
	} else {
		sections = append(sections, settingRowStyle.Width(width).Render(clip(policyRow, width)))
	}
	sections = append(sections,
		mutedStyle.Width(width).Render(clip(cloudPolicyDescription(m.screen.preferences.CloudPolicy), width)),
	)
	if m.screen.preferencesNote != "" {
		sections = append(sections, warningStyle.Width(width).Render(clip(m.screen.preferencesNote, width)))
	}
	sections = append(sections,
		footerStyle.Width(width).Render(clip("Tab field · ←/→ change", width)),
		footerStyle.Width(width).Render(clip("Enter apply · Esc back · ? help", width)),
	)
	return centerBlock(m.screen.width, lipgloss.JoinVertical(lipgloss.Left, sections...))
}

func cloudPolicyLabel(policy string) string {
	switch policy {
	case "never":
		return "Keep translation local"
	case "allowed":
		return "Allow cloud translation"
	default:
		return "Ask before cloud translation"
	}
}

func cloudPolicyDescription(policy string) string {
	switch policy {
	case "never":
		return "Text is never sent to a remote translator."
	case "allowed":
		return "Remote translation may start without another prompt."
	default:
		return "Ask before sending text for translation."
	}
}

func voiceLabel(voices []VoiceChoice, id string) string {
	for _, voice := range voices {
		if voice.ID == id {
			if voice.Name == "" || voice.Name == voice.ID {
				return voice.ID
			}
			return voice.Name + " (" + voice.ID + ")"
		}
	}
	if id == "" {
		return "Choose a voice"
	}
	return id
}

func (m *teaModel) voicePreviewView() string {
	width := max(1, min(m.screen.width, 112)-4)
	note := m.screen.previewNote
	if note == "" {
		note = "Preparing a short local voice preview…"
	}
	sections := []string{
		headerEyebrow("VOICE PREVIEW"),
		titleStyle.Width(width).Render("Trying " + m.screen.previewVoice),
		separatorStyle.Render(strings.Repeat("─", width)),
		statusStyle(note).Padding(0, 1).Render(m.spinner.View() + " " + clip(note, max(1, width-4))),
		mutedStyle.Width(width).Render(clip("This fixed sample is synthesized and played locally.", width)),
		footerStyle.Width(width).Render(clip("Esc stop · any navigation key stops the sample", width)),
	}
	return lipgloss.PlaceHorizontal(m.screen.width, lipgloss.Left, lipgloss.JoinVertical(lipgloss.Left, sections...))
}

func (m *teaModel) pickerView() string {
	width, height := m.layoutSize()
	header := m.pickerHeader(width)
	footer := m.pickerFooter(width)
	searchRows := 0
	if m.searchVisible() {
		searchRows = 1
	}
	detailRows := 0
	if m.screen.height >= 13 {
		detailRows = 1
	}
	listHeight := max(1, height-lipgloss.Height(header)-2-lipgloss.Height(footer)-searchRows-detailRows)
	m.picker.SetSize(width, listHeight)

	selected := m.selectedChoice()
	detail := pickerDetail(selected, m.picker.FilterState())
	visibleCount := len(m.picker.VisibleItems())
	if visibleCount == 0 {
		detail = "No matching chapters · Esc clears the search"
	} else {
		detail = fmt.Sprintf("%d of %d choices · %s", m.picker.Index()+1, visibleCount, detail)
	}
	separator := separatorStyle.Render(strings.Repeat("─", width))
	sections := []string{header, separator, m.picker.View()}
	if searchRows != 0 {
		sections = []string{header, separator, lipgloss.NewStyle().Width(width).Render(m.picker.FilterInput.View()), m.picker.View()}
	}
	if detailRows != 0 {
		sections = append(sections, mutedStyle.Width(width).Render(clip(detail, width)))
	}
	sections = append(sections, separator, footer)
	body := lipgloss.JoinVertical(lipgloss.Left, sections...)
	return centerBlock(m.screen.width, body)
}

func (m *teaModel) searchVisible() bool {
	return m.picker.FilterState() == list.Filtering || m.picker.FilterState() == list.FilterApplied
}

func (m *teaModel) pickerHeader(width int) string {
	bookTitle := titleStyle.Width(width).Render(clip(m.screen.title, width))
	if m.screen.phase == PhaseChapters {
		intro := "Main chapters · / searches all sections"
		if width < 56 {
			intro = "Main chapters · / finds sections"
		}
		if m.screen.chapterSearch {
			intro = "Searching all chapters and sections"
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			headerEyebrow("CHAPTERS"), bookTitle,
			mutedStyle.Width(width).Render(clip(intro, width)),
		)
	}
	introText := "Choose a place to begin. Playback continues through the book."
	if width < 40 {
		introText = "Choose a starting chapter."
	}
	intro := mutedStyle.Width(width).Render(introText)
	return lipgloss.JoinVertical(lipgloss.Left,
		headerEyebrow("START READING"),
		bookTitle,
		lipgloss.NewStyle().Height(1).Render(""),
		intro,
	)
}

func (m *teaModel) pickerFooter(width int) string {
	if m.screen.phase == PhaseChapters {
		line := "↑/↓ move   Enter jump   / search all sections   Esc return"
		if width < 56 {
			line = "Enter jump · / search · Esc back"
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			footerStyle.Width(width).Render(clip(line, width)),
			footerStyle.Width(width).Render(clip("q stop playback · ? help", width)),
		)
	}
	if width < 56 {
		lines := []string{
			footerStyle.Width(width).Render(clip("↑/↓ move · enter select", width)),
			footerStyle.Width(width).Render(clip("/ search · q cancel", width)),
		}
		if m.screen.height < 14 {
			return lipgloss.JoinVertical(lipgloss.Left, lines...)
		}
		return lipgloss.JoinVertical(lipgloss.Left, append([]string{lipgloss.NewStyle().Height(1).Render("")}, lines...)...)
	}
	keys := "↑/↓ move   enter choose   / search   q cancel"
	line := footerStyle.Width(width).Render(clip(keys, width))
	if m.screen.height < 14 {
		return line
	}
	return lipgloss.JoinVertical(lipgloss.Left, lipgloss.NewStyle().Height(1).Render(""), line)
}

func (m *teaModel) selectedChoice() StartPoint {
	index := m.picker.GlobalIndex()
	if index < 0 || index >= len(m.screen.choices) {
		return StartPoint{}
	}
	return m.screen.choices[index]
}

type pickerDelegate struct{}

func (pickerDelegate) Height() int                         { return 1 }
func (pickerDelegate) Spacing() int                        { return 0 }
func (pickerDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (pickerDelegate) Render(w io.Writer, model list.Model, index int, item list.Item) {
	choice, ok := item.(pickerItem)
	if !ok || model.Width() < 1 {
		return
	}
	marker := "  "
	style := rowStyle
	padding := 0
	if index == model.Index() {
		marker = "› "
		style = selectedRowStyle
		padding = 1
	}
	label := clip(choice.label, max(0, model.Width()-2-padding-padding))
	_, _ = io.WriteString(w, style.Width(max(1, model.Width()-padding)).MaxWidth(model.Width()).Render(marker+label))
}

type homePickerDelegate struct{ focused bool }

func (homePickerDelegate) Height() int                         { return 3 }
func (homePickerDelegate) Spacing() int                        { return 0 }
func (homePickerDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (delegate homePickerDelegate) Render(w io.Writer, model list.Model, index int, item list.Item) {
	choice, ok := item.(pickerItem)
	if !ok || model.Width() < 1 {
		return
	}
	marker := "  "
	titleStyle := rowStyle
	detailStyle := homeDetailStyle
	if delegate.focused && index == model.Index() {
		marker = "› "
		titleStyle = selectedRowStyle
		detailStyle = homeDetailSelectedStyle
	}
	width := model.Width()
	contentWidth := max(0, width-3)
	lines := []string{
		titleStyle.Width(width).MaxWidth(width).Render(marker + clip(choice.label, contentWidth)),
		detailStyle.Width(width).MaxWidth(width).Render("   " + clip(choice.action, contentWidth)),
		homePositionStyle.Width(width).MaxWidth(width).Render("   " + clip(choice.detail, contentWidth)),
	}
	_, _ = io.WriteString(w, lipgloss.JoinVertical(lipgloss.Left, lines...))
}

func pickerDetail(choice StartPoint, filterState list.FilterState) string {
	if filterState == list.Filtering || filterState == list.FilterApplied {
		return "Search chapters by title · Esc clears the search"
	}
	if choice.Resume {
		return "Continue from your saved position"
	}
	if choice.Label == "Start from the beginning" {
		return "Read the complete book from the first chapter"
	}
	return "Start here and continue through the remaining chapters"
}

func renderResizePrompt(width, height int) string {
	width = max(1, width)
	lines := []string{}
	if width < 32 {
		lines = append(lines, mutedStyle.Render(clip("Resize: 32 columns minimum", width)))
		if height >= 2 {
			lines = append(lines, mutedStyle.Render(clip("10 rows minimum", width)))
		}
	} else {
		lines = append(lines, mutedStyle.Render(clip("Resize to at least 10 rows", width)))
	}
	if height >= 3 {
		lines = append(lines, footerStyle.Render(clip("q or Ctrl-C · stop", width)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func centerBlock(terminalWidth int, value string) string {
	if lipgloss.Width(value) >= terminalWidth {
		return value
	}
	return lipgloss.PlaceHorizontal(terminalWidth, lipgloss.Center, value)
}

var (
	eyebrowStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("#79D7C1")).Bold(true)
	brandStyle              = lipgloss.NewStyle().Foreground(lipgloss.Color("#102522")).Background(lipgloss.Color("#79D7C1")).Bold(true)
	titleStyle              = lipgloss.NewStyle().Foreground(lipgloss.Color("#F0F4FA")).Bold(true)
	mutedStyle              = lipgloss.NewStyle().Foreground(lipgloss.Color("#AAB5C8"))
	footerStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("#B7C2D5"))
	sectionLabelStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#79D7C1")).Bold(true)
	separatorStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("#344155"))
	rowStyle                = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD5E3"))
	homeDetailStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#AAB5C8"))
	homeDetailSelectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#79D7C1")).Bold(true)
	homePositionStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#AAB5C8"))
	selectedRowStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5FAFF")).Background(lipgloss.Color("#263A4E")).Bold(true).PaddingLeft(1)
	settingRowStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD5E3")).Background(lipgloss.Color("#19252E")).PaddingLeft(1)
	valueChipStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("#B9EEE2")).Background(lipgloss.Color("#203A39")).Bold(true)
	actionChipStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD5E3")).Background(lipgloss.Color("#19252E")).Padding(0, 1)
	actionKeyStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("#79D7C1")).Bold(true)
	continueCardStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5FAFF")).Background(lipgloss.Color("#203A39")).BorderLeft(true).BorderStyle(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color("#79D7C1")).PaddingLeft(1).Bold(true)
	continueIdleStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#D2DCE8")).Background(lipgloss.Color("#19252E")).BorderLeft(true).BorderStyle(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color("#526579")).PaddingLeft(1)
	setupProgressCardStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#D2DCE8")).Background(lipgloss.Color("#19252E")).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#344155")).Padding(0, 1)
	noteStyle               = lipgloss.NewStyle().Foreground(lipgloss.Color("#92D7C3"))
	bufferActiveStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#79D7C1"))
	bufferTrackStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#344155"))
	activitySpinnerStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#79D7C1")).Bold(true)
	warningStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("#F0C674"))
	timelineActiveStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#79D7C1")).Bold(true)
	timelineKnobStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#F0F4FA")).Bold(true)
	playerFrameStyle        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#344155")).Padding(1, 2)
)

func renderPlayer(screen Model, spinnerView string) string {
	framed := screen.width >= 64 && screen.height >= 22
	innerWidth := max(1, min(screen.width, 104)-4)
	if framed {
		innerWidth = max(1, min(98, screen.width-6))
	}
	compact := screen.height < 18 || screen.width < 48
	tight := screen.height < 14
	chapterTitle, chapterNumber := currentChapter(screen)
	bookTitle := titleStyle.Width(innerWidth).Render(clip(screen.title, innerWidth))
	chapterLine := eyebrowStyle.Render(clip(chapterLabel(chapterNumber, len(screen.chapters)), innerWidth))
	chapterHeading := titleStyle.Width(innerWidth).Render(clip(chapterTitle, innerWidth))
	pageLine := ""
	if screen.state.Page > 0 {
		pageLine = mutedStyle.Render(clip(fmt.Sprintf("PDF PAGE %d", screen.state.Page), innerWidth))
	}
	status := screen.state.Status
	if screen.state.SeekPending != 0 {
		status = "Buffering " + signedSeek(screen.state.SeekPending)
	} else if status == "" && screen.state.Paused {
		status = "Paused"
	}
	if status == "" {
		status = "Preparing opening audio"
	}
	preparing := screen.phase == PhasePreparing || isLoadingStatus(status)
	seeking := screen.state.SeekPending != 0
	building := screen.phase == PhasePlaying && screen.state.Total > 0 &&
		clamp(screen.state.Ready, 0, screen.state.Total) < screen.state.Total && !preparing && !seeking
	statusMarker := "●"
	if (preparing || seeking) && spinnerView != "" {
		statusMarker = spinnerView
	}
	statusTextWidth := max(1, innerWidth-2-lipgloss.Width(statusMarker)-1)
	statusLine := statusStyle(status).Padding(0, 1).Render(
		statusMarker + " " + clip(status, statusTextWidth),
	)
	readyLabel := "AUDIO READY"
	if preparing {
		readyLabel = "READY"
	} else if building {
		readyLabel = "BUILDING AUDIO"
	}
	readySpinner := ""
	if building {
		readySpinner = spinnerView
	}
	readyLine := readinessLine(screen.state, innerWidth, readyLabel, readySpinner)
	progressLine := readyLine
	if screen.state.PlaybackKnown {
		progressLine = timelineLine(screen.state, innerWidth)
		if building && tight && screen.height < 12 {
			// At the minimum supported height there is no separate readiness
			// row. Give the audio-build counter and spinner the progress slot.
			progressLine = readyLine
		}
	}
	positionLine := ""
	if screen.state.PlaybackKnown {
		bookTotal := screen.state.BookTotal
		if bookTotal <= 0 {
			bookTotal = screen.state.Total
		}
		if bookTotal > 0 {
			positionLine = mutedStyle.Render(clip(fmt.Sprintf("PASSAGE  %d / %d", screen.state.Sequence+1, bookTotal), innerWidth))
		}
	}
	rate := screen.state.PlaybackRate
	if rate < 0.5 || rate > 3 {
		rate = 1
	}
	rateHint := fmt.Sprintf("+/− speed %.1fx", rate)
	helpLines := []string{footerStyle.Width(innerWidth).Render(clip("←/→ seek 5s   p/n main chapter   c chapters   Space pause   "+rateHint+"   q stop", innerWidth))}
	if !screen.chapterNav {
		if screen.phase == PhasePreparing {
			helpLines = []string{
				footerStyle.Width(innerWidth).Render(clip("Esc cancel · q exit", innerWidth)),
				footerStyle.Width(innerWidth).Render(clip("PDF recognition running · chapters unavailable", innerWidth)),
			}
		} else {
			helpLines = []string{
				footerStyle.Width(innerWidth).Render(clip("←/→ seek · Space pause · "+rateHint, innerWidth)),
				footerStyle.Width(innerWidth).Render(clip("Chapter controls unavailable", innerWidth)),
				footerStyle.Width(innerWidth).Render(clip(screen.chapterNavWhy, innerWidth)),
			}
		}
	} else if screen.phase == PhasePreparing {
		preparingHelp := "q stop · waiting for first audio"
		if strings.HasPrefix(strings.ToLower(status), "restoring your place") {
			preparingHelp = "Esc cancel · q exit"
		}
		helpLines = []string{footerStyle.Width(innerWidth).Render(clip(preparingHelp, innerWidth))}
	} else if innerWidth < 68 {
		helpLines = []string{
			footerStyle.Width(innerWidth).Render(clip("←/→ seek ±5s · p/n main chapter", innerWidth)),
			footerStyle.Width(innerWidth).Render(clip("c chapters · Space pause · "+rateHint+" · q stop", innerWidth)),
		}
	}
	sections := []string{
		headerEyebrow("NOW PLAYING"),
		bookTitle,
	}
	if !compact {
		sections = append(sections, lipgloss.NewStyle().Height(1).Render(""))
	}
	sections = append(sections, separatorStyle.Render(strings.Repeat("─", innerWidth)))
	if !compact {
		sections = append(sections, lipgloss.NewStyle().Height(1).Render(""))
	}
	sections = append(sections, chapterLine, chapterHeading, statusLine)
	if pageLine != "" {
		sections = append(sections, pageLine)
	}
	if !compact {
		sections = append(sections, lipgloss.NewStyle().Height(1).Render(""))
	}
	sections = append(sections, progressLine)
	if screen.state.PlaybackKnown && (!tight || screen.height >= 12) {
		sections = append(sections, readyLine)
		if !tight && positionLine != "" {
			sections = append(sections, positionLine)
		}
	}
	if !compact {
		sections = append(sections, lipgloss.NewStyle().Height(1).Render(""))
	}
	sections = append(sections, separatorStyle.Render(strings.Repeat("─", innerWidth)))
	sections = append(sections, helpLines...)
	body := lipgloss.JoinVertical(lipgloss.Left, sections...)
	if framed {
		body = playerFrameStyle.Render(body)
		return lipgloss.Place(screen.width, screen.height, lipgloss.Center, lipgloss.Center, body)
	}
	return centerBlock(screen.width, body)
}

func readinessLine(state State, width int, label, indicator string) string {
	total := max(0, state.Total)
	ready := clamp(state.Ready, 0, total)
	text := clip(fmt.Sprintf("%s %d/%d", label, ready, total), max(1, width-lipgloss.Width(indicator)-2))
	barWidth := min(32, max(1, width-lipgloss.Width(indicator)-lipgloss.Width(text)-1))
	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		indicator,
		mutedStyle.Render(text+" "),
		readinessBar(ready, total, barWidth),
	)
}

func isLoadingStatus(status string) bool {
	lower := strings.ToLower(strings.TrimSpace(status))
	return strings.HasPrefix(lower, "preparing") || strings.HasPrefix(lower, "loading") || strings.HasPrefix(lower, "translating")
}

func readinessBar(ready, total, width int) string {
	width = max(1, width)
	total = max(1, total)
	filled := int(float64(width) * float64(clamp(ready, 0, total)) / float64(total))
	return bufferActiveStyle.Render(strings.Repeat("━", filled)) + bufferTrackStyle.Render(strings.Repeat("─", width-filled))
}

func timelineLine(state State, width int) string {
	if state.Duration <= 0 {
		return mutedStyle.Render(clip("Passage time starting…", width))
	}
	position := state.Position
	if position < 0 {
		position = 0
	}
	if position > state.Duration {
		position = state.Duration
	}
	label := fmt.Sprintf("PASSAGE TIME %s / %s", clock(position), clock(state.Duration))
	barWidth := width - lipgloss.Width(label) - 1
	if barWidth < 6 {
		return mutedStyle.Render(clip(label, width))
	}
	barWidth = min(barWidth, 24)
	return lipgloss.JoinHorizontal(lipgloss.Left, mutedStyle.Render(label+" "), timelineBar(position, state.Duration, barWidth))
}

func timelineBar(position, duration time.Duration, width int) string {
	width = max(6, width)
	if duration <= 0 {
		return bufferTrackStyle.Render(strings.Repeat("─", width))
	}
	if position < 0 {
		position = 0
	}
	if position > duration {
		position = duration
	}
	knob := int(float64(width-1) * float64(position) / float64(duration))
	return timelineActiveStyle.Render(strings.Repeat("━", knob)) + timelineKnobStyle.Render("●") + bufferTrackStyle.Render(strings.Repeat("─", width-knob-1))
}

func signedSeek(delta time.Duration) string {
	if delta > 0 {
		return "+" + clock(delta)
	}
	return "−" + clock(-delta)
}

func currentChapter(screen Model) (string, int) {
	if len(screen.chapters) == 0 {
		return "Full document", 0
	}
	index := clamp(screen.state.ChapterIndex, 0, len(screen.chapters)-1)
	return screen.chapters[index].Title, index + 1
}

func chapterLabel(number, count int) string {
	if count <= 1 {
		return "READING"
	}
	return fmt.Sprintf("CHAPTER %d OF %d", number, count)
}

func statusStyle(status string) lipgloss.Style {
	color := lipgloss.Color("#79D7C1")
	background := lipgloss.Color("#19312E")
	lower := strings.ToLower(status)
	if strings.Contains(lower, "paus") || strings.Contains(lower, "buffer") || strings.Contains(lower, "wait") ||
		strings.Contains(lower, "needed") || strings.Contains(lower, "end of ready audio") {
		color = lipgloss.Color("#E5BB74")
		background = lipgloss.Color("#3A3023")
	} else if strings.Contains(lower, "warning") || strings.Contains(lower, "could not") ||
		strings.Contains(lower, "did not") || strings.Contains(lower, "unavailable") || strings.Contains(lower, "failed") {
		color = lipgloss.Color("#E5A383")
		background = lipgloss.Color("#3A2624")
	}
	return lipgloss.NewStyle().Foreground(color).Background(background).Bold(true)
}

func (m *teaModel) renderForTest() string { return m.View() }

var _ tea.Model = (*teaModel)(nil)
