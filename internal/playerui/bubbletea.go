package playerui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type readyMsg struct{}

type sessionUpdate struct {
	state State
	phase Phase
}

type pickerItem struct {
	label string
}

func (i pickerItem) FilterValue() string { return i.label }

type teaModel struct {
	screen   Model
	picker   list.Model
	spinner  spinner.Model
	spinning bool
	actions  chan<- Action
	ready    chan<- struct{}
	done     <-chan struct{}
	readyMu  *sync.Once
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
	for index := 0; index < screen.cursor; index++ {
		picker.CursorDown()
	}
	activitySpinner := spinner.New(
		spinner.WithSpinner(spinner.Points),
		spinner.WithStyle(activitySpinnerStyle),
	)
	return &teaModel{
		screen:  screen,
		picker:  picker,
		spinner: activitySpinner,
		actions: actions,
		ready:   ready,
		readyMu: &sync.Once{},
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
		return m, cmd
	case readyMsg:
		if m.ready != nil {
			m.readyMu.Do(func() { close(m.ready) })
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.screen.SetSize(msg.Width, msg.Height)
		m.resizePicker()
		return m, nil
	case sessionUpdate:
		wasLoading := m.loadingActive()
		m.screen.SetState(msg.state)
		if msg.phase != 0 {
			m.screen.SetPhase(msg.phase)
		}
		return m, m.startLoadingAnimation(wasLoading)
	case tea.KeyMsg:
		if m.screen.phase == PhaseSelect {
			return m.updatePicker(msg)
		}
		key := uiKey(msg)
		if action, ok := m.screen.HandleKey(key); ok {
			return m, m.dispatch(action)
		}
	default:
		// Bubbles list filtering returns its matches asynchronously. Forward
		// those messages to the list so the visible items and global selection
		// stay in sync with the search field.
		if m.screen.phase == PhaseSelect {
			updated, cmd := m.picker.Update(msg)
			m.picker = updated
			return m, cmd
		}
	}
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
	} else if m.screen.phase == PhaseSelect {
		content = m.pickerView()
	} else {
		content = renderPlayer(m.screen, m.spinner.View())
	}
	return content
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
	introText := "Choose a place to begin. Playback continues through the book."
	if width < 40 {
		introText = "Choose a starting chapter."
	}
	intro := mutedStyle.Width(width).Render(introText)
	return lipgloss.JoinVertical(lipgloss.Left,
		eyebrowStyle.Render("LISTEN  /  START READING"),
		bookTitle,
		lipgloss.NewStyle().Height(1).Render(""),
		intro,
	)
}

func (m *teaModel) pickerFooter(width int) string {
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
	lines := []string{mutedStyle.Render(clip("Resize to at least 32 columns × 10 rows", width))}
	if height >= 2 {
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
	eyebrowStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#79D7C1")).Bold(true)
	titleStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#F0F4FA")).Bold(true)
	mutedStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#AAB5C8"))
	footerStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("#B7C2D5"))
	separatorStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#344155"))
	rowStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD5E3"))
	selectedRowStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5FAFF")).Background(lipgloss.Color("#263A4E")).Bold(true).PaddingLeft(1)
	bufferActiveStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#79D7C1"))
	bufferTrackStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#344155"))
	activitySpinnerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#79D7C1")).Bold(true)
	timelineActiveStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#79D7C1")).Bold(true)
	timelineKnobStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#F0F4FA")).Bold(true)
	playerFrameStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#344155")).Padding(1, 2)
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
	helpLines := []string{footerStyle.Width(innerWidth).Render(clip("←/→ seek 5s   p/n chapter   Space pause   q stop", innerWidth))}
	if screen.phase == PhasePreparing {
		helpLines = []string{footerStyle.Width(innerWidth).Render(clip("q stop · waiting for first audio", innerWidth))}
	} else if innerWidth < 52 {
		helpLines = []string{
			footerStyle.Width(innerWidth).Render(clip("←/→ seek ±5s · p/n chapter", innerWidth)),
			footerStyle.Width(innerWidth).Render(clip("Space pause · q stop", innerWidth)),
		}
	}
	sections := []string{
		eyebrowStyle.Render("LISTEN  /  NOW PLAYING"),
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
		return mutedStyle.Render(clip("Starting playback clock…", width))
	}
	position := state.Position
	if position < 0 {
		position = 0
	}
	if position > state.Duration {
		position = state.Duration
	}
	left, right := clock(position), clock(state.Duration)
	barWidth := max(6, width-lipgloss.Width(left)-lipgloss.Width(right)-2)
	return lipgloss.JoinHorizontal(lipgloss.Left, mutedStyle.Render(left+" "), timelineBar(position, state.Duration, barWidth), mutedStyle.Render(" "+right))
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
