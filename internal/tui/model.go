package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"audiobooks/internal/build"
	"audiobooks/internal/device"
	"audiobooks/internal/library"
)

type step int

const (
	stepLoading step = iota
	stepBooks
	stepDevices
	stepReview
	stepMetadata
	stepConfirm
	stepRunning
	stepDone
)

type Dependencies struct {
	Context    context.Context
	BooksDir   string
	Repository string
	DryRun     bool
	Devices    device.Runner
	Builder    build.Service
	ScanBooks  func(string) ([]library.Book, error)
	ProbeBook  func(context.Context, library.Book) library.Metadata
}

type Model struct {
	deps       Dependencies
	step       step
	width      int
	height     int
	books      []library.Book
	devices    []device.Device
	bookCursor int
	bookStatus string
	refreshing bool
	devCursor  int
	mode       build.Mode
	format     bool
	metadata   library.Metadata
	fields     []textinput.Model
	field      int
	confirmYes bool
	events     <-chan build.Event
	phase      string
	progress   float64
	logs       []string
	err        error
	cancel     context.CancelFunc
	started    time.Time
	completed  bool
}

type loadedMsg struct {
	books   []library.Book
	devices []device.Device
	err     error
}

type probedMsg struct {
	metadata library.Metadata
}

type devicesMsg struct {
	devices []device.Device
	err     error
}

type booksMsg struct {
	books        []library.Book
	selectedPath string
	previous     map[string]bool
	err          error
}

type eventMsg build.Event
type tickMsg time.Time
type devicePollMsg time.Time

func New(deps Dependencies) Model {
	ctx, cancel := context.WithCancel(deps.Context)
	deps.Context = ctx
	return Model{
		deps:   deps,
		step:   stepLoading,
		mode:   build.ModeDaisy,
		format: true,
		cancel: cancel,
	}
}

func (m Model) Init() tea.Cmd {
	return m.load
}

func (m Model) load() tea.Msg {
	books, err := m.deps.ScanBooks(m.deps.BooksDir)
	if err != nil {
		return loadedMsg{err: err}
	}
	devices, err := device.Discover(m.deps.Context, m.deps.Devices)
	return loadedMsg{books: books, devices: devices, err: err}
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case loadedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.step = stepDone
			return m, nil
		}
		m.books, m.devices = msg.books, msg.devices
		if len(m.books) == 0 {
			m.err = fmt.Errorf("no MP3, M4A, or M4B audiobooks found in %s", m.deps.BooksDir)
			m.step = stepDone
		} else {
			m.step = stepBooks
		}
	case probedMsg:
		m.metadata = msg.metadata
		m.makeFields()
		m.step = stepDevices
		if len(m.devices) == 0 {
			return m, pollDevices()
		}
	case devicesMsg:
		// Ignore stale rescan results if the user already moved on; entering
		// the device step again always triggers a fresh scan or poll.
		if m.step != stepLoading && m.step != stepDevices {
			return m, nil
		}
		if msg.err != nil {
			m.err = msg.err
			m.step = stepDone
			return m, nil
		}
		m.devices = msg.devices
		m.devCursor = 0
		m.step = stepDevices
		if len(m.devices) == 0 {
			return m, pollDevices()
		}
	case booksMsg:
		m.refreshing = false
		if msg.err != nil {
			m.bookStatus = "Refresh failed: " + msg.err.Error()
			return m, nil
		}
		m.books = msg.books
		m.bookCursor = 0
		added := 0
		for i, book := range m.books {
			if !msg.previous[book.Path] {
				if added == 0 {
					m.bookCursor = i
				}
				added++
			}
		}
		if added > 0 {
			m.bookStatus = fmt.Sprintf("Found %d new audiobook%s", added, plural(added))
			return m, nil
		}
		for i, book := range m.books {
			if book.Path == msg.selectedPath {
				m.bookCursor = i
				break
			}
		}
		if len(m.books) == 0 {
			m.bookStatus = "No audiobooks found; add one to books/ and refresh"
		} else {
			m.bookStatus = "Library is up to date"
		}
	case eventMsg:
		event := build.Event(msg)
		if event.Message != "" {
			m.logs = append(m.logs, event.Message)
			if len(m.logs) > 200 {
				m.logs = m.logs[len(m.logs)-200:]
			}
		}
		if event.Phase != "log" && event.Phase != "" {
			m.phase = event.Phase
		}
		if event.Percent > 0 {
			m.progress = event.Percent
		}
		if event.Err != nil {
			m.err = event.Err
			m.completed = false
			m.step = stepDone
			return m, nil
		}
		if event.Done {
			m.completed = true
			m.step = stepDone
			return m, nil
		}
		return m, waitEvent(m.events)
	case tickMsg:
		if m.step == stepRunning {
			return m, tick()
		}
	case devicePollMsg:
		if m.step == stepDevices && len(m.devices) == 0 {
			return m, m.rescanDevices
		}
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := key.String()
	if k == "ctrl+c" {
		m.cancel()
		return m, tea.Quit
	}
	if k == "q" && m.step != stepMetadata && m.step != stepConfirm && m.step != stepRunning {
		return m, tea.Quit
	}

	switch m.step {
	case stepBooks:
		switch k {
		case "up", "k":
			if len(m.books) > 0 {
				m.bookCursor = max(0, m.bookCursor-1)
			}
		case "down", "j":
			if len(m.books) > 0 {
				m.bookCursor = min(len(m.books)-1, m.bookCursor+1)
			}
		case "r":
			if !m.refreshing {
				m.refreshing = true
				m.bookStatus = ""
				return m, m.refreshBooks
			}
		case "enter":
			if len(m.books) > 0 {
				book := m.books[m.bookCursor]
				return m, func() tea.Msg {
					return probedMsg{metadata: m.deps.ProbeBook(m.deps.Context, book)}
				}
			}
		}
	case stepDevices:
		if len(m.devices) == 0 {
			if k == "r" {
				return m, m.rescanDevices
			}
			if k == "esc" {
				m.step = stepBooks
			}
			break
		}
		switch k {
		case "up", "k":
			m.devCursor = max(0, m.devCursor-1)
		case "down", "j":
			m.devCursor = min(len(m.devices)-1, m.devCursor+1)
		case "r":
			return m, m.rescanDevices
		case "esc":
			m.step = stepBooks
		case "enter":
			m.format = !compatibleDevice(m.devices[m.devCursor])
			m.step = stepReview
		}
	case stepReview:
		switch k {
		case "m":
			if m.mode == build.ModeDaisy {
				m.mode = build.ModeLibrary
			} else {
				m.mode = build.ModeDaisy
			}
		case "left", "up", "k":
			m.mode = build.ModeDaisy
		case "right", "down", "j":
			m.mode = build.ModeLibrary
		case "f":
			m.format = !m.format
		case "e":
			if m.mode == build.ModeDaisy {
				m.step = stepMetadata
				m.field = 0
				return m, m.focusField()
			}
		case "esc":
			m.step = stepDevices
		case "enter":
			m.step = stepConfirm
			m.confirmYes = false
		}
	case stepMetadata:
		switch k {
		case "esc":
			m.blurFields()
			m.step = stepReview
		case "tab", "down":
			m.field = (m.field + 1) % len(m.fields)
			return m, m.focusField()
		case "shift+tab", "up":
			m.field = (m.field - 1 + len(m.fields)) % len(m.fields)
			return m, m.focusField()
		case "enter":
			if m.field < len(m.fields)-1 {
				m.field++
				return m, m.focusField()
			}
			m.readFields()
			m.blurFields()
			m.step = stepReview
			return m, nil
		default:
			var cmd tea.Cmd
			m.fields[m.field], cmd = m.fields[m.field].Update(key)
			return m, cmd
		}
	case stepConfirm:
		switch k {
		case "esc":
			m.step = stepReview
			m.confirmYes = false
		case "left", "right", "tab", "shift+tab":
			m.confirmYes = !m.confirmYes
		case "enter":
			if m.confirmYes {
				return m.startBuild()
			}
			m.step = stepReview
		}
	case stepDone:
		if k == "n" && m.completed {
			m.err = nil
			m.completed = false
			m.step = stepLoading
			return m, m.rescanDevices
		}
		if k == "b" && m.completed {
			m.err = nil
			m.completed = false
			m.step = stepBooks
			return m, nil
		}
		if k == "enter" || k == "q" || k == "esc" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m Model) startBuild() (tea.Model, tea.Cmd) {
	m.err = nil
	m.completed = false
	m.logs = nil
	m.progress = 0
	m.phase = "starting"
	m.started = time.Now()
	m.step = stepRunning
	request := build.Request{
		Book:       m.books[m.bookCursor],
		Device:     m.devices[m.devCursor],
		Mode:       m.mode,
		Metadata:   m.metadata,
		Format:     m.format,
		DryRun:     m.deps.DryRun,
		Repository: m.deps.Repository,
	}
	m.events = m.deps.Builder.Run(m.deps.Context, request)
	return m, tea.Batch(waitEvent(m.events), tick())
}

func (m Model) rescanDevices() tea.Msg {
	devices, err := device.Discover(m.deps.Context, m.deps.Devices)
	return devicesMsg{devices: devices, err: err}
}

func (m Model) refreshBooks() tea.Msg {
	previous := make(map[string]bool, len(m.books))
	for _, book := range m.books {
		previous[book.Path] = true
	}
	selectedPath := ""
	if len(m.books) > 0 {
		selectedPath = m.books[m.bookCursor].Path
	}
	books, err := m.deps.ScanBooks(m.deps.BooksDir)
	return booksMsg{
		books:        books,
		selectedPath: selectedPath,
		previous:     previous,
		err:          err,
	}
}

func waitEvent(events <-chan build.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return eventMsg(build.Event{Done: true})
		}
		return eventMsg(event)
	}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(now time.Time) tea.Msg {
		return tickMsg(now)
	})
}

func pollDevices() tea.Cmd {
	return tea.Tick(2*time.Second, func(now time.Time) tea.Msg {
		return devicePollMsg(now)
	})
}

func (m *Model) makeFields() {
	values := []struct {
		prompt string
		value  string
	}{
		{"Title", m.metadata.Title},
		{"Creator", m.metadata.Creator},
		{"Narrator", m.metadata.Narrator},
	}
	m.fields = make([]textinput.Model, len(values))
	for i, value := range values {
		input := textinput.New()
		input.Prompt = ""
		input.SetValue(value.value)
		input.SetWidth(60)
		input.Placeholder = value.prompt
		m.fields[i] = input
	}
}

func (m *Model) readFields() {
	m.metadata.Title = m.fields[0].Value()
	m.metadata.Creator = m.fields[1].Value()
	m.metadata.Narrator = m.fields[2].Value()
}

func (m *Model) blurFields() {
	for i := range m.fields {
		m.fields[i].Blur()
	}
}

func (m *Model) focusField() tea.Cmd {
	var commands []tea.Cmd
	for i := range m.fields {
		if i == m.field {
			commands = append(commands, m.fields[i].Focus())
		} else {
			m.fields[i].Blur()
		}
	}
	return tea.Batch(commands...)
}

func (m Model) View() tea.View {
	var content string
	switch m.step {
	case stepLoading:
		content = m.frame("Getting things ready", "Scanning books and USB cartridges", loadingPanel.Render("•••  Looking for media…"), "ctrl+c quit")
	case stepBooks:
		content = m.booksView()
	case stepDevices:
		content = m.devicesView()
	case stepReview:
		content = m.reviewView()
	case stepMetadata:
		content = m.metadataView()
	case stepConfirm:
		content = m.confirmView()
	case stepRunning:
		content = m.runningView()
	case stepDone:
		content = m.doneView()
	}
	view := tea.NewView(content)
	view.AltScreen = true
	return view
}

func (m Model) booksView() string {
	var b strings.Builder
	start, end := visibleRange(m.bookCursor, len(m.books), max(5, m.height-12))
	lineWidth := min(64, max(34, m.width-26))
	nameWidth := max(18, lineWidth-24)
	for i := start; i < end; i++ {
		book := m.books[i]
		parts := "single file"
		if book.MultiPart() {
			parts = fmt.Sprintf("%d parts", len(book.Files))
		}
		line := fmt.Sprintf("%-*s  %-10s  %8s", nameWidth, trim(book.Name, nameWidth), parts, library.FormatBytes(book.SizeBytes))
		if i == m.bookCursor {
			b.WriteString(selectedRow.Render("  "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	body := panelStyle.Render(b.String())
	if len(m.books) == 0 {
		body = warningPanel.Render("No audiobooks found in books/\n\nAdd a book, then press r to refresh.")
	}
	subtitle := fmt.Sprintf("%d audiobooks ready", len(m.books))
	if m.refreshing {
		subtitle = "Refreshing library…"
	} else if m.bookStatus != "" {
		subtitle = m.bookStatus
	}
	return m.frame(
		"Choose a book",
		subtitle,
		body,
		"↑/↓ navigate   enter select   r refresh   q quit",
	)
}

func (m Model) devicesView() string {
	var b strings.Builder
	if len(m.devices) == 0 {
		body := warningPanel.Render(
			emphasis.Render("No USB cartridge detected") +
				"\n\nInsert a 2–4 GB USB stick. Detection is automatic.",
		)
		return m.frame("Choose a USB", "Waiting for a cartridge", body, "r rescan now   esc back   q quit")
	}
	for i, disk := range m.devices {
		name := firstNonEmpty(disk.Name, "Unnamed")
		fs := firstNonEmpty(disk.Filesystem, "unknown")
		card := fmt.Sprintf("%s\n%s   %s   /dev/%s",
			emphasis.Render(name),
			library.FormatBytes(disk.SizeBytes), schemeName(disk.Scheme)+" / "+fs, disk.Identifier)
		style := panelStyle
		if i == m.devCursor {
			style = selectedPanel
		}
		b.WriteString(style.Render(card) + "\n")
	}
	body := b.String() + "\n" + safetyStyle.Render("✓ External physical USB devices only")
	return m.frame("Choose a USB", "Select the cartridge to write", body, "↑/↓ navigate   enter select   r rescan   esc back")
}

func (m Model) reviewView() string {
	book := m.books[m.bookCursor]
	disk := m.devices[m.devCursor]
	optionWidth := min(58, max(38, m.width-26))
	daisy := modeOption(
		m.mode == build.ModeDaisy,
		"DAISY 2.02",
		"Recommended • time + resume • 56 kb/s mono",
		optionWidth,
	)
	libraryMode := modeOption(
		m.mode == build.ModeLibrary,
		"Library Mode",
		"Fast fallback • 64 kb/s • two-hour segments",
		optionWidth,
	)
	modes := compactPanel.Width(optionWidth).Render(
		sectionLabel.Render("LISTENING MODE") + "\n" +
			daisy + "\n" + libraryMode,
	)

	formatLabel := safetyStyle.Render("FORMAT + ERASE")
	formatText := "Fresh MBR + FAT32"
	if !m.format {
		formatLabel = badgeMuted.Render("KEEP FORMAT")
		formatText = "Clear book files, keep current FAT32"
	}
	meta := fmt.Sprintf("%s — %s",
		emphasis.Render(firstNonEmpty(m.metadata.Title, book.Name)),
		firstNonEmpty(m.metadata.Creator, "Unknown author"),
	)
	summary := compactPanel.Render(fmt.Sprintf(
		"%s  %s\n%s  %s\n%s  %s",
		sectionLabel.Render("BOOK"), meta,
		sectionLabel.Render("USB"),
		fmt.Sprintf("%s • %s • %s • /dev/%s", firstNonEmpty(disk.Name, "Unnamed"), library.FormatBytes(disk.SizeBytes), schemeName(disk.Scheme), disk.Identifier),
		formatLabel, formatText,
	))
	body := modes + "\n" + summary
	hint := "↑/↓ mode   f format   e metadata   enter continue   esc back"
	if m.mode != build.ModeDaisy {
		hint = "↑/↓ mode   f format   enter continue   esc back"
	}
	return m.frame("Ready to write", "Review the cartridge recipe", body, hint)
}

func modeOption(selected bool, title, description string, width int) string {
	marker := dim.Render("○")
	titleText := emphasis.Render(title)
	style := modeRow.Width(width - 4)
	if selected {
		marker = accent.Render("●")
		titleText = selectedModeTitle.Render(title)
		style = selectedModeRow.Width(width - 4)
	}
	return style.Render(marker + "  " + titleText + "\n   " + description)
}

func (m Model) metadataView() string {
	labels := []string{"Title", "Creator (author)", "Reader (narrator)"}
	var b strings.Builder
	for i, field := range m.fields {
		label := sectionLabel.Render(labels[i])
		value := field.View()
		style := fieldPanel
		if i == m.field {
			style = selectedField
		}
		fmt.Fprintf(&b, "%s\n", style.Render(label+"\n"+value))
	}
	return m.frame("Edit metadata", "Used in the DAISY navigation package", b.String(), "tab next field   enter save   esc cancel")
}

func (m Model) confirmView() string {
	book := m.books[m.bookCursor]
	disk := m.devices[m.devCursor]
	action := "WRITE CARTRIDGE"
	detail := "Existing audiobook files on the volume will be replaced."
	if m.format {
		action = "ERASE + WRITE"
		detail = "The entire USB disk will be erased and rebuilt as MBR + FAT32."
	}
	if m.deps.DryRun {
		action = "RUN PREVIEW"
		detail = "Dry run is enabled. No files or devices will be changed."
	}
	cancel := buttonActive.Render("  Cancel  ")
	proceed := button.Render("  " + action + "  ")
	if m.confirmYes {
		cancel = button.Render("  Cancel  ")
		proceed = dangerButton.Render("  " + action + "  ")
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Center, cancel, "   ", proceed)
	target := fmt.Sprintf("%s • %s / %s • /dev/%s",
		library.FormatBytes(disk.SizeBytes), schemeName(disk.Scheme),
		firstNonEmpty(disk.Filesystem, "unknown"), disk.Identifier)
	confirmWidth := min(60, max(34, m.width-24))
	body := warningCompact.Width(confirmWidth).Render(
		dangerTitle.Render(action)+"\n"+
			emphasis.Render(book.Name)+"  →  "+emphasis.Render(firstNonEmpty(disk.Name, "Unnamed"))+
			"\n"+dim.Render(m.mode.String()+" • "+target)+
			"\n"+detail,
	) + "\n" + buttons
	return m.frame("Confirm write", "Cancel is selected by default", body, "←/→ choose   enter confirm   esc back")
}

func (m Model) runningView() string {
	var b strings.Builder
	elapsed := time.Since(m.started).Round(time.Second)
	activity := []string{"·", "••", "•••"}[int(elapsed.Seconds())%3]
	width := min(58, max(24, m.width-26))
	fmt.Fprintf(&b, "%s\n%s  %3.0f%%\n%s  %s  •  %s elapsed\n\n",
		phaseTimeline(m.phase),
		progressBar(m.progress, width), m.progress*100,
		accent.Render(activity), phaseName(m.phase), elapsed)
	start := max(0, len(m.logs)-4)
	var activityLog strings.Builder
	for _, line := range m.logs[start:] {
		fmt.Fprintf(&activityLog, "%s\n", dim.Render("• "+trim(line, max(20, m.width-24))))
	}
	b.WriteString(compactPanel.Render(sectionLabel.Render("ACTIVITY") + "\n" + activityLog.String()))
	return m.frame("Writing cartridge", m.books[m.bookCursor].Name, b.String(), "ctrl+c cancel")
}

func (m Model) doneView() string {
	title := "Cartridge ready"
	message := successTitle.Render("✓ USB ejected — safe to unplug")
	if m.deps.DryRun {
		message = "Dry run completed; no files or devices were changed."
	}
	if m.err != nil {
		title = "Build Failed"
		message = errorStyle.Render(m.err.Error())
	}
	var b strings.Builder
	b.WriteString(successCompact.Render(message))
	if len(m.logs) > 0 {
		b.WriteString("\n")
		start := max(0, len(m.logs)-3)
		var log strings.Builder
		for _, line := range m.logs[start:] {
			fmt.Fprintf(&log, "%s\n", dim.Render("• "+trim(line, max(20, m.width-24))))
		}
		b.WriteString(compactPanel.Render(sectionLabel.Render("SUMMARY") + "\n" + log.String()))
	}
	footer := "enter exit"
	if m.completed {
		footer = "n next USB, same book   b choose another book   enter exit"
	}
	return m.frame(title, completionSubtitle(m), b.String(), footer)
}

func (m Model) frame(title, subtitle, body, footer string) string {
	width := min(84, max(44, m.width-12))
	brand := titleStyle.Render("AUDIOBOOK CARTRIDGE")
	steps := m.stepIndicator()
	header := brand + "\n" + steps
	if m.width >= 100 {
		header = lipgloss.JoinHorizontal(lipgloss.Top, brand, strings.Repeat(" ", max(2, width-lipgloss.Width(brand)-lipgloss.Width(steps))), steps)
	}
	heading := headingStyle.Render(title) + "\n" + subtitleStyle.Render(subtitle)
	content := header + "\n" + heading + "\n\n" + body
	if m.height > 0 {
		// Two footer newlines, one footer row, and vertical app padding.
		padding := m.height - lipgloss.Height(content) - 5
		if padding > 0 {
			content += strings.Repeat("\n", padding)
		}
	}
	return appStyle.Width(width).Render(content + "\n\n" + footerStyle.Render(footer))
}

func (m Model) stepIndicator() string {
	current := 1
	switch m.step {
	case stepDevices:
		current = 2
	case stepReview, stepMetadata, stepConfirm:
		current = 3
	case stepRunning, stepDone:
		current = 4
	}
	labels := []string{"BOOK", "USB", "REVIEW", "WRITE"}
	var parts []string
	for i, label := range labels {
		style := stepMuted
		if i+1 < current {
			style = stepDoneStyle
		} else if i+1 == current {
			style = stepCurrent
		}
		parts = append(parts, style.Render(label))
	}
	return strings.Join(parts, dim.Render("  ·  "))
}

func visibleRange(cursor, length, available int) (int, int) {
	if length <= available {
		return 0, length
	}
	start := max(0, cursor-available/2)
	if start+available > length {
		start = length - available
	}
	return start, start + available
}

func progressBar(percent float64, width int) string {
	filled := int(percent * float64(width))
	filled = min(width, max(0, filled))
	return accent.Render(strings.Repeat("━", filled)) + dim.Render(strings.Repeat("─", width-filled))
}

func phaseName(phase string) string {
	names := map[string]string{
		"starting":  "Starting",
		"preflight": "Checking tools and space",
		"format":    "Formatting USB",
		"prepare":   "Preparing cartridge",
		"source":    "Preparing audio",
		"convert":   "Converting audio",
		"daisy":     "Building DAISY package",
		"copy":      "Copying to USB",
		"validate":  "Validating cartridge",
		"finalize":  "Cleaning metadata",
		"eject":     "Syncing and ejecting",
	}
	if name := names[phase]; name != "" {
		return name
	}
	return strings.Title(phase)
}

func phaseTimeline(current string) string {
	phases := []struct {
		key   string
		label string
	}{
		{"preflight", "CHECK"},
		{"prepare", "PREP"},
		{"daisy", "BUILD"},
		{"copy", "COPY"},
		{"validate", "VERIFY"},
		{"eject", "EJECT"},
	}
	order := map[string]int{
		"starting": 0, "preflight": 0, "format": 1, "prepare": 1, "source": 1,
		"convert": 2, "daisy": 2, "copy": 3, "validate": 4, "finalize": 4,
		"eject": 5, "complete": 6,
	}
	active := order[current]
	var parts []string
	for i, phase := range phases {
		style := stepMuted
		if i < active {
			style = stepDoneStyle
		} else if i == active {
			style = stepCurrent
		}
		parts = append(parts, style.Render(phase.label))
	}
	return strings.Join(parts, dim.Render(" ━ "))
}

func completionSubtitle(m Model) string {
	if m.err != nil {
		return "The USB remains mounted when possible so you can retry"
	}
	if m.deps.DryRun {
		return "Preview finished without changing the USB"
	}
	return "Make another copy without reselecting the audiobook"
}

func trim(value string, width int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	return string(runes[:max(0, width-1)]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func compatibleDevice(disk device.Device) bool {
	filesystem := strings.ToUpper(strings.ReplaceAll(disk.Filesystem, "-", "_"))
	fat32 := strings.Contains(filesystem, "FAT32") || strings.Contains(filesystem, "DOS_FAT_32")
	return fat32 && strings.EqualFold(disk.Scheme, "FDisk_partition_scheme")
}

func schemeName(scheme string) string {
	switch strings.ToLower(scheme) {
	case "fdisk_partition_scheme":
		return "MBR"
	case "gpt_partition_scheme":
		return "GPT"
	default:
		return firstNonEmpty(scheme, "unknown scheme")
	}
}

var (
	purple = lipgloss.Color("#9B87F5")
	pink   = lipgloss.Color("#F472B6")
	mint   = lipgloss.Color("#5EEAD4")
	red    = lipgloss.Color("#FB7185")
	amber  = lipgloss.Color("#FBBF24")
	slate  = lipgloss.Color("#64748B")
	ink    = lipgloss.Color("#E2E8F0")

	appStyle      = lipgloss.NewStyle().Padding(1, 2)
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(purple)
	headingStyle  = lipgloss.NewStyle().Bold(true).Foreground(ink)
	subtitleStyle = lipgloss.NewStyle().Foreground(slate)
	emphasis      = lipgloss.NewStyle().Bold(true).Foreground(ink)
	accent        = lipgloss.NewStyle().Foreground(mint)
	dim           = lipgloss.NewStyle().Foreground(slate)
	errorStyle    = lipgloss.NewStyle().Bold(true).Foreground(red)
	footerStyle   = lipgloss.NewStyle().Foreground(slate)
	sectionLabel  = lipgloss.NewStyle().Bold(true).Foreground(purple)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#334155")).
			Padding(1, 2)
	selectedPanel = panelStyle.
			BorderForeground(purple).
			Background(lipgloss.Color("#17152B"))
	compactPanel = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#334155")).
			Padding(0, 1)
	selectedCompact = compactPanel.
			BorderForeground(purple).
			Background(lipgloss.Color("#17152B"))
	modeRow = lipgloss.NewStyle().
		Foreground(slate).
		Padding(0, 1)
	selectedModeRow = modeRow.Copy().
			Foreground(ink).
			Background(lipgloss.Color("#27224A"))
	selectedModeTitle = lipgloss.NewStyle().
				Bold(true).
				Foreground(pink)
	warningPanel   = panelStyle.BorderForeground(amber)
	warningCompact = compactPanel.BorderForeground(amber)
	successPanel   = panelStyle.BorderForeground(mint).Padding(1, 3)
	successCompact = compactPanel.
			BorderForeground(mint).
			Padding(0, 2)
	logPanel      = panelStyle.BorderForeground(lipgloss.Color("#334155"))
	loadingPanel  = panelStyle.BorderForeground(purple)
	fieldPanel    = panelStyle.MarginBottom(1)
	selectedField = fieldPanel.
			BorderForeground(pink).
			Background(lipgloss.Color("#211629"))

	selectedRow = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#312E81")).
			Padding(0, 1)
	badgeStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#052E2B")).
			Background(mint).
			Padding(0, 1)
	badgeMuted = lipgloss.NewStyle().
			Bold(true).
			Foreground(ink).
			Background(lipgloss.Color("#475569")).
			Padding(0, 1)
	safetyStyle  = lipgloss.NewStyle().Bold(true).Foreground(mint)
	dangerTitle  = lipgloss.NewStyle().Bold(true).Foreground(red)
	successTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(mint)

	button = lipgloss.NewStyle().
		Foreground(ink).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#475569")).
		Padding(0, 1)
	buttonActive = button.Copy().
			Bold(true).
			Foreground(lipgloss.Color("#0F172A")).
			Background(lipgloss.Color("#CBD5E1")).
			BorderForeground(lipgloss.Color("#CBD5E1"))
	dangerButton = button.Copy().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(red).
			BorderForeground(red)

	stepMuted = lipgloss.NewStyle().
			Foreground(slate)
	stepCurrent = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#0F172A")).
			Background(purple).
			Padding(0, 1)
	stepDoneStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(mint)
)
