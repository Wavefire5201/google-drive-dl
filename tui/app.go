package tui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"img-util/drive"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

// AuthMethod represents the authentication method
type AuthMethod int

const (
	AuthOAuth AuthMethod = iota
	AuthAPIKey
)

// View represents the current screen
type View int

const (
	ViewLinks    View = iota
	ViewFileList      // New view to show all files with metadata
	ViewSearch
	ViewFiles
	ViewDownloading
	ViewDone
)

// SortField represents which field to sort by
type SortField int

const (
	SortByName SortField = iota
	SortBySize
	SortByDate
)

// Model is the main application model
type Model struct {
	view          View
	width         int
	height        int
	err           error
	authMethod    AuthMethod
	authValue     string // API key or credentials path
	linksFile     string
	destDir       string
	maxConcurrent int

	// Drive client
	driveClient *drive.Client

	// Links input
	linksInput textarea.Model
	links      []string

	// Search
	searchInput textinput.Model
	searchTerms []string

	// Files
	allFiles      []drive.DriveFile
	filteredFiles []drive.DriveFile
	selectedFiles map[string]bool
	fileCursor    int
	selectAll     bool
	sortField     SortField
	sortAsc       bool
	lastKeyG      bool // Track if last key was 'g' for gg command

	// Info popup
	showInfoPopup bool

	// Download progress
	fileProgress    map[string]drive.DownloadProgress
	downloading     bool
	downloadDone    bool
	completedCount  int
	totalToDownload int
	progressMu      sync.Mutex

	// Auto-download mode
	autoDownload    bool
	autoSearchTerms string

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc
}

// Messages
type errMsg struct{ err error }
type filesLoadedMsg struct{ files []drive.DriveFile }
type downloadProgressMsg drive.DownloadProgress
type downloadCompleteMsg struct{ errors []string }
type clientReadyMsg struct{ client *drive.Client }
type tickMsg struct{}

func (e errMsg) Error() string { return e.err.Error() }

// NewModel creates a new TUI model (deprecated, use NewModelWithClient)
func NewModel(authMethod AuthMethod, authValue, linksFile, destDir string, maxConcurrent int) Model {
	ti := textarea.New()
	ti.Placeholder = "Paste Google Drive folder links (one per line)..."
	ti.Focus()
	ti.SetWidth(80)
	ti.SetHeight(10)

	si := textinput.New()
	si.Placeholder = "Search terms (comma-separated, e.g., 'abc, ddd') - leave empty to see all"
	si.Width = 70

	ctx, cancel := context.WithCancel(context.Background())

	return Model{
		view:          ViewLinks,
		linksInput:    ti,
		searchInput:   si,
		selectedFiles: make(map[string]bool),
		fileProgress:  make(map[string]drive.DownloadProgress),
		authMethod:    authMethod,
		authValue:     authValue,
		linksFile:     linksFile,
		destDir:       destDir,
		maxConcurrent: maxConcurrent,
		ctx:           ctx,
		cancel:        cancel,
		sortField:     SortByName,
		sortAsc:       true,
	}
}

// NewModelWithClient creates a new TUI model with a pre-authenticated client
func NewModelWithClient(client *drive.Client, linksFile, destDir string, maxConcurrent int, autoDownload bool, searchTerms string) Model {
	ti := textarea.New()
	ti.Placeholder = "Paste Google Drive folder links (one per line)..."
	ti.Focus()
	ti.SetWidth(80)
	ti.SetHeight(10)

	si := textinput.New()
	si.Placeholder = "Search terms (comma-separated, e.g., 'abc, ddd') - leave empty to see all"
	si.Width = 70

	ctx, cancel := context.WithCancel(context.Background())

	return Model{
		view:            ViewLinks,
		linksInput:      ti,
		searchInput:     si,
		selectedFiles:   make(map[string]bool),
		fileProgress:    make(map[string]drive.DownloadProgress),
		driveClient:     client,
		linksFile:       linksFile,
		destDir:         destDir,
		maxConcurrent:   maxConcurrent,
		ctx:             ctx,
		cancel:          cancel,
		sortField:       SortByName,
		sortAsc:         true,
		autoDownload:    autoDownload,
		autoSearchTerms: searchTerms,
	}
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		textarea.Blink,
	}

	// Only init client if not already provided
	if m.driveClient == nil {
		cmds = append(cmds, m.initClient())
	}

	// Load links from file if provided
	if m.linksFile != "" {
		cmds = append(cmds, m.loadLinksFromFile())
	}

	return tea.Batch(cmds...)
}

func (m Model) initClient() tea.Cmd {
	return func() tea.Msg {
		var client *drive.Client
		var err error

		if m.authMethod == AuthAPIKey {
			client, err = drive.NewClientWithAPIKey(m.ctx, m.authValue)
		} else {
			client, err = drive.NewClientWithOAuth(m.ctx, m.authValue)
		}

		if err != nil {
			return errMsg{err}
		}
		return clientReadyMsg{client}
	}
}

func (m Model) loadLinksFromFile() tea.Cmd {
	return func() tea.Msg {
		data, err := os.ReadFile(m.linksFile)
		if err != nil {
			return errMsg{fmt.Errorf("failed to read links file: %v", err)}
		}
		return linksFileLoadedMsg{content: string(data)}
	}
}

type linksFileLoadedMsg struct{ content string }

// Update handles messages
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Update input widths to use full terminal width
		m.linksInput.SetWidth(msg.Width - 4)
		m.searchInput.Width = msg.Width - 4
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case "q":
			if m.view == ViewDone || m.view == ViewFileList {
				m.cancel()
				return m, tea.Quit
			}
		case "esc":
			// If info popup is open, let the view handler close it
			if m.showInfoPopup {
				break
			}
			if m.view == ViewDownloading {
				m.cancel()
				return m, tea.Quit
			}
			// Go back
			switch m.view {
			case ViewFileList:
				m.view = ViewLinks
				m.linksInput.Focus()
			case ViewSearch:
				m.view = ViewFileList
			case ViewFiles:
				m.view = ViewSearch
				m.searchInput.Focus()
			}
			return m, nil
		}

	case clientReadyMsg:
		m.driveClient = msg.client
		return m, nil

	case linksFileLoadedMsg:
		m.linksInput.SetValue(msg.content)
		return m, nil

	case errMsg:
		m.err = msg.err
		return m, nil

	case filesLoadedMsg:
		m.allFiles = msg.files
		m.sortFiles()

		// If auto-download mode is enabled, filter and download immediately
		if m.autoDownload {
			// Apply search filter if provided
			if m.autoSearchTerms != "" {
				terms := strings.Split(m.autoSearchTerms, ",")
				var cleanTerms []string
				for _, t := range terms {
					t = strings.TrimSpace(t)
					if t != "" {
						cleanTerms = append(cleanTerms, t)
					}
				}
				m.searchTerms = cleanTerms
				m.filteredFiles = drive.FilterFiles(m.allFiles, cleanTerms)
			} else {
				m.filteredFiles = m.allFiles
			}

			if len(m.filteredFiles) == 0 {
				m.err = fmt.Errorf("no files match the search terms")
				m.view = ViewDone
				return m, nil
			}

			// Select all filtered files
			m.selectedFiles = make(map[string]bool)
			for _, f := range m.filteredFiles {
				m.selectedFiles[f.ID] = true
			}

			// Start download immediately
			return m.startDownload()
		}

		m.view = ViewFileList
		m.fileCursor = 0
		return m, nil

	case downloadProgressMsg:
		prog := drive.DownloadProgress(msg)
		m.progressMu.Lock()
		m.fileProgress[prog.FileID] = prog
		if prog.Done {
			m.completedCount++
		}
		m.progressMu.Unlock()
		return m, nil

	case downloadCompleteMsg:
		m.downloadDone = true
		m.view = ViewDone
		if len(msg.errors) > 0 {
			m.err = fmt.Errorf("%d downloads failed", len(msg.errors))
		}
		return m, nil

	case tickMsg:
		if m.view == ViewDownloading && !m.downloadDone {
			return m, tea.Tick(100*time.Millisecond, func(_ time.Time) tea.Msg { return tickMsg{} })
		}
		return m, nil
	}

	// Handle view-specific updates
	switch m.view {
	case ViewLinks:
		return m.updateLinks(msg)
	case ViewFileList:
		return m.updateFileList(msg)
	case ViewSearch:
		return m.updateSearch(msg)
	case ViewFiles:
		return m.updateFiles(msg)
	}

	return m, nil
}

func (m Model) updateLinks(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if msg.Alt {
				return m.submitLinks()
			}
		case "ctrl+s":
			return m.submitLinks()
		}
	}

	var cmd tea.Cmd
	m.linksInput, cmd = m.linksInput.Update(msg)
	return m, cmd
}

func (m Model) submitLinks() (tea.Model, tea.Cmd) {
	if m.driveClient == nil {
		m.err = fmt.Errorf("Drive client not ready yet, please wait...")
		return m, nil
	}

	content := m.linksInput.Value()
	lines := strings.Split(content, "\n")
	var links []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && strings.Contains(line, "drive.google.com") {
			links = append(links, line)
		}
	}

	if len(links) == 0 {
		m.err = fmt.Errorf("no valid Google Drive links found")
		return m, nil
	}

	m.links = links
	m.err = nil

	return m, m.loadFiles()
}

func (m Model) loadFiles() tea.Cmd {
	return func() tea.Msg {
		files, err := m.driveClient.ListFilesFromFolders(m.ctx, m.links)
		if err != nil {
			return errMsg{err}
		}
		return filesLoadedMsg{files}
	}
}

func (m *Model) sortFiles() {
	sort.Slice(m.allFiles, func(i, j int) bool {
		var less bool
		switch m.sortField {
		case SortByName:
			less = strings.ToLower(m.allFiles[i].Name) < strings.ToLower(m.allFiles[j].Name)
		case SortBySize:
			less = m.allFiles[i].Size < m.allFiles[j].Size
		case SortByDate:
			less = m.allFiles[i].ModifiedTime.Before(m.allFiles[j].ModifiedTime)
		}
		if m.sortAsc {
			return less
		}
		return !less
	})
}

func (m Model) updateFileList(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Handle popup close first
		if m.showInfoPopup {
			switch msg.String() {
			case "i", "esc":
				m.showInfoPopup = false
				return m, nil
			}
			return m, nil // Ignore other keys when popup is open
		}

		switch msg.String() {
		case "up", "k":
			m.lastKeyG = false
			if m.fileCursor > 0 {
				m.fileCursor--
			}
		case "down", "j":
			m.lastKeyG = false
			if m.fileCursor < len(m.allFiles)-1 {
				m.fileCursor++
			}
		case "g":
			if m.lastKeyG {
				// gg - go to top
				m.fileCursor = 0
				m.lastKeyG = false
			} else {
				m.lastKeyG = true
			}
		case "G":
			m.lastKeyG = false
			m.fileCursor = len(m.allFiles) - 1
		case "n":
			m.lastKeyG = false
			m.sortField = SortByName
			m.sortAsc = !m.sortAsc
			m.sortFiles()
		case "s":
			m.lastKeyG = false
			m.sortField = SortBySize
			m.sortAsc = !m.sortAsc
			m.sortFiles()
		case "d":
			m.lastKeyG = false
			m.sortField = SortByDate
			m.sortAsc = !m.sortAsc
			m.sortFiles()
		case " ":
			m.lastKeyG = false
			// Toggle selection for current file
			if m.fileCursor < len(m.allFiles) {
				f := m.allFiles[m.fileCursor]
				m.selectedFiles[f.ID] = !m.selectedFiles[f.ID]
			}
		case "a":
			m.lastKeyG = false
			// Toggle all files
			m.selectAll = !m.selectAll
			for _, f := range m.allFiles {
				m.selectedFiles[f.ID] = m.selectAll
			}
		case "i":
			m.lastKeyG = false
			// Toggle info popup
			m.showInfoPopup = !m.showInfoPopup
		case "enter":
			m.lastKeyG = false
			// Download selected files
			m.filteredFiles = m.allFiles
			return m.startDownload()
		case "/":
			m.lastKeyG = false
			m.view = ViewSearch
			m.searchInput.Focus()
			return m, textinput.Blink
		default:
			m.lastKeyG = false
		}
	}

	return m, nil
}

func (m Model) updateSearch(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			return m.submitSearch()
		}
	}

	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	return m, cmd
}

func (m Model) submitSearch() (tea.Model, tea.Cmd) {
	terms := strings.Split(m.searchInput.Value(), ",")
	var cleanTerms []string
	for _, t := range terms {
		t = strings.TrimSpace(t)
		if t != "" {
			cleanTerms = append(cleanTerms, t)
		}
	}

	m.searchTerms = cleanTerms
	m.filteredFiles = drive.FilterFiles(m.allFiles, cleanTerms)

	if len(m.filteredFiles) == 0 {
		m.err = fmt.Errorf("no files match the search terms")
		return m, nil
	}

	// Select all by default
	m.selectAll = true
	m.selectedFiles = make(map[string]bool)
	for _, f := range m.filteredFiles {
		m.selectedFiles[f.ID] = true
	}

	m.view = ViewFiles
	m.fileCursor = 0
	m.err = nil
	return m, nil
}

func (m Model) updateFiles(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Handle popup close first
		if m.showInfoPopup {
			switch msg.String() {
			case "i", "esc":
				m.showInfoPopup = false
				return m, nil
			}
			return m, nil // Ignore other keys when popup is open
		}

		switch msg.String() {
		case "up", "k":
			m.lastKeyG = false
			if m.fileCursor > 0 {
				m.fileCursor--
			}
		case "down", "j":
			m.lastKeyG = false
			if m.fileCursor < len(m.filteredFiles)-1 {
				m.fileCursor++
			}
		case "g":
			if m.lastKeyG {
				// gg - go to top
				m.fileCursor = 0
				m.lastKeyG = false
			} else {
				m.lastKeyG = true
			}
		case "G":
			m.lastKeyG = false
			m.fileCursor = len(m.filteredFiles) - 1
		case " ":
			m.lastKeyG = false
			if m.fileCursor < len(m.filteredFiles) {
				f := m.filteredFiles[m.fileCursor]
				m.selectedFiles[f.ID] = !m.selectedFiles[f.ID]
			}
		case "a":
			m.lastKeyG = false
			m.selectAll = !m.selectAll
			for _, f := range m.filteredFiles {
				m.selectedFiles[f.ID] = m.selectAll
			}
		case "i":
			m.lastKeyG = false
			// Toggle info popup
			m.showInfoPopup = !m.showInfoPopup
		case "enter":
			m.lastKeyG = false
			return m.startDownload()
		default:
			m.lastKeyG = false
		}
	}

	return m, nil
}

func (m Model) startDownload() (tea.Model, tea.Cmd) {
	var toDownload []drive.DriveFile
	for _, f := range m.filteredFiles {
		if m.selectedFiles[f.ID] {
			toDownload = append(toDownload, f)
		}
	}

	if len(toDownload) == 0 {
		m.err = fmt.Errorf("no files selected")
		return m, nil
	}

	m.totalToDownload = len(toDownload)
	m.completedCount = 0
	m.view = ViewDownloading
	m.downloading = true

	return m, tea.Batch(
		m.downloadFiles(toDownload),
		tea.Tick(100*time.Millisecond, func(_ time.Time) tea.Msg { return tickMsg{} }),
	)
}

func (m *Model) downloadFiles(files []drive.DriveFile) tea.Cmd {
	return func() tea.Msg {
		destDir := m.destDir
		if destDir == "" {
			destDir = "./output"
		}

		var wg sync.WaitGroup
		sem := make(chan struct{}, m.maxConcurrent)
		var errorsMu sync.Mutex
		var errors []string

		for _, file := range files {
			wg.Add(1)
			go func(f drive.DriveFile) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				m.progressMu.Lock()
				m.fileProgress[f.ID] = drive.DownloadProgress{
					FileID:      f.ID,
					FileName:    f.DisplayName(),
					TotalBytes:  f.Size,
					BytesLoaded: 0,
				}
				m.progressMu.Unlock()

				// Create a progress channel for this file
				progressChan := make(chan drive.DownloadProgress, 10)

				// Goroutine to update progress
				done := make(chan struct{})
				go func() {
					for prog := range progressChan {
						m.progressMu.Lock()
						m.fileProgress[f.ID] = prog
						m.progressMu.Unlock()
					}
					close(done)
				}()

				err := m.driveClient.DownloadFile(m.ctx, f, destDir, progressChan)
				close(progressChan)
				<-done // Wait for progress updates to finish

				m.progressMu.Lock()
				m.fileProgress[f.ID] = drive.DownloadProgress{
					FileID:      f.ID,
					FileName:    f.DisplayName(),
					TotalBytes:  f.Size,
					BytesLoaded: f.Size,
					Done:        true,
					Error:       err,
				}
				m.completedCount++
				m.progressMu.Unlock()

				if err != nil {
					errorsMu.Lock()
					errors = append(errors, fmt.Sprintf("%s: %v", f.DisplayName(), err))
					errorsMu.Unlock()
				}
			}(file)
		}

		wg.Wait()
		return downloadCompleteMsg{errors: errors}
	}
}

// View renders the UI
func (m Model) View() string {
	var s strings.Builder

	s.WriteString(TitleStyle.Render("Google Drive Downloader"))
	s.WriteString("\n")

	switch m.view {
	case ViewLinks:
		s.WriteString(m.viewLinks())
	case ViewFileList:
		s.WriteString(m.viewFileList())
	case ViewSearch:
		s.WriteString(m.viewSearch())
	case ViewFiles:
		s.WriteString(m.viewFiles())
	case ViewDownloading:
		s.WriteString(m.viewDownloading())
	case ViewDone:
		s.WriteString(m.viewDone())
	}

	if m.err != nil {
		s.WriteString("\n")
		s.WriteString(ErrorStyle.Render(fmt.Sprintf("Error: %v", m.err)))
	}

	return s.String()
}

func (m Model) viewLinks() string {
	var s strings.Builder

	s.WriteString(SubtitleStyle.Render("Enter Google Drive folder links:"))
	s.WriteString("\n")
	s.WriteString(m.linksInput.View())
	s.WriteString("\n")
	s.WriteString(HelpStyle.Render("Ctrl+S to submit | Ctrl+C to quit"))

	if m.driveClient == nil {
		s.WriteString("\n")
		s.WriteString(WarningStyle.Render("Connecting to Google Drive..."))
	} else {
		s.WriteString("\n")
		if m.authMethod == AuthAPIKey {
			s.WriteString(DimStyle.Render("Auth: API Key"))
		} else {
			s.WriteString(DimStyle.Render("Auth: OAuth"))
		}
	}

	return s.String()
}

func (m Model) viewFileList() string {
	var s strings.Builder

	// Calculate total size and selected count
	var totalSize, selectedSize int64
	selectedCount := 0
	for _, f := range m.allFiles {
		totalSize += f.Size
		if m.selectedFiles[f.ID] {
			selectedCount++
			selectedSize += f.Size
		}
	}

	if selectedCount > 0 {
		s.WriteString(SubtitleStyle.Render(fmt.Sprintf("Found %d files | Selected: %d (%s)", len(m.allFiles), selectedCount, formatSize(selectedSize))))
	} else {
		s.WriteString(SubtitleStyle.Render(fmt.Sprintf("Found %d files (%s total)", len(m.allFiles), formatSize(totalSize))))
	}
	s.WriteString("\n")

	// Calculate dynamic widths based on terminal width
	width := m.width
	if width < 80 {
		width = 80
	}
	// Reserve space for: cursor(2) + checkbox(3) + space(1) + icon(2) + size(10) + space(1) + date(12) + padding(4)
	fixedWidth := 2 + 3 + 1 + 2 + 10 + 1 + 12 + 4
	nameWidth := width - fixedWidth
	if nameWidth < 20 {
		nameWidth = 20
	}

	// Header
	sortIndicator := func(field SortField) string {
		if m.sortField == field {
			if m.sortAsc {
				return " ^"
			}
			return " v"
		}
		return ""
	}

	header := fmt.Sprintf("       %s %10s %12s",
		padRight("Name"+sortIndicator(SortByName), nameWidth),
		"Size"+sortIndicator(SortBySize),
		"Modified"+sortIndicator(SortByDate))
	s.WriteString(DimStyle.Render(header))
	s.WriteString("\n")
	s.WriteString(DimStyle.Render(strings.Repeat("-", width-2)))
	s.WriteString("\n")

	// Pagination
	visibleStart := 0
	visibleEnd := len(m.allFiles)
	maxVisible := m.height - 12
	if maxVisible < 5 {
		maxVisible = 10
	}

	if len(m.allFiles) > maxVisible {
		visibleStart = m.fileCursor - maxVisible/2
		if visibleStart < 0 {
			visibleStart = 0
		}
		visibleEnd = visibleStart + maxVisible
		if visibleEnd > len(m.allFiles) {
			visibleEnd = len(m.allFiles)
			visibleStart = visibleEnd - maxVisible
			if visibleStart < 0 {
				visibleStart = 0
			}
		}
	}

	for i := visibleStart; i < visibleEnd; i++ {
		f := m.allFiles[i]
		cursor := "  "
		if i == m.fileCursor {
			cursor = "> "
		}

		checkbox := "[ ]"
		if m.selectedFiles[f.ID] {
			checkbox = "[x]"
		}

		// Show green square if file exists locally
		existsIcon := "  "
		if m.fileExistsLocally(f) {
			existsIcon = SuccessStyle.Render("■") + " "
		}

		dateStr := ""
		if !f.ModifiedTime.IsZero() {
			dateStr = f.ModifiedTime.Format("2006-01-02")
		}

		line := fmt.Sprintf("%s%s %s%s %10s %12s",
			cursor,
			checkbox,
			existsIcon,
			truncateAndPad(f.DisplayName(), nameWidth),
			formatSize(f.Size),
			dateStr)

		if i == m.fileCursor {
			s.WriteString(SelectedStyle.Render(line))
		} else {
			s.WriteString(NormalStyle.Render(line))
		}
		s.WriteString("\n")
	}

	s.WriteString("\n")
	s.WriteString(HelpStyle.Render("j/k:move | gg/G:top/bottom | Space:toggle | a:all | i:info | Enter:download | /:search | n/s/d:sort | q:quit"))

	// Show info popup if active
	if m.showInfoPopup && m.fileCursor < len(m.allFiles) {
		s.WriteString("\n\n")
		s.WriteString(m.renderInfoPopup(m.allFiles[m.fileCursor]))
	}

	return s.String()
}

func (m Model) viewSearch() string {
	var s strings.Builder

	s.WriteString(SubtitleStyle.Render(fmt.Sprintf("Search in %d files:", len(m.allFiles))))
	s.WriteString("\n")
	s.WriteString(m.searchInput.View())
	s.WriteString("\n")
	s.WriteString(HelpStyle.Render("Enter to search (empty = all files) | Esc to go back"))

	return s.String()
}

func (m Model) viewFiles() string {
	var s strings.Builder

	selectedCount := 0
	var selectedSize int64
	for _, f := range m.filteredFiles {
		if m.selectedFiles[f.ID] {
			selectedCount++
			selectedSize += f.Size
		}
	}

	s.WriteString(SubtitleStyle.Render(fmt.Sprintf("Matching files: %d/%d selected (%s)",
		selectedCount, len(m.filteredFiles), formatSize(selectedSize))))
	s.WriteString("\n")

	// Calculate dynamic widths based on terminal width
	width := m.width
	if width < 80 {
		width = 80
	}
	// Reserve space for: cursor(2) + checkbox(3) + space(1) + icon(2) + size(10) + space(1) + date(12) + padding(4)
	fixedWidth := 2 + 3 + 1 + 2 + 10 + 1 + 12 + 4
	nameWidth := width - fixedWidth
	if nameWidth < 20 {
		nameWidth = 20
	}

	// Header
	header := fmt.Sprintf("       %s %10s %12s", padRight("Name", nameWidth), "Size", "Modified")
	s.WriteString(DimStyle.Render(header))
	s.WriteString("\n")
	s.WriteString(DimStyle.Render(strings.Repeat("-", width-2)))
	s.WriteString("\n")

	// Pagination
	visibleStart := 0
	visibleEnd := len(m.filteredFiles)
	maxVisible := m.height - 12
	if maxVisible < 5 {
		maxVisible = 10
	}

	if len(m.filteredFiles) > maxVisible {
		visibleStart = m.fileCursor - maxVisible/2
		if visibleStart < 0 {
			visibleStart = 0
		}
		visibleEnd = visibleStart + maxVisible
		if visibleEnd > len(m.filteredFiles) {
			visibleEnd = len(m.filteredFiles)
			visibleStart = visibleEnd - maxVisible
			if visibleStart < 0 {
				visibleStart = 0
			}
		}
	}

	for i := visibleStart; i < visibleEnd; i++ {
		f := m.filteredFiles[i]
		cursor := "  "
		if i == m.fileCursor {
			cursor = "> "
		}

		checkbox := "[ ]"
		if m.selectedFiles[f.ID] {
			checkbox = "[x]"
		}

		// Show green square if file exists locally
		existsIcon := "  "
		if m.fileExistsLocally(f) {
			existsIcon = SuccessStyle.Render("■") + " "
		}

		dateStr := ""
		if !f.ModifiedTime.IsZero() {
			dateStr = f.ModifiedTime.Format("2006-01-02")
		}

		line := fmt.Sprintf("%s%s %s%s %10s %12s",
			cursor,
			checkbox,
			existsIcon,
			truncateAndPad(f.DisplayName(), nameWidth),
			formatSize(f.Size),
			dateStr)

		if i == m.fileCursor {
			s.WriteString(SelectedStyle.Render(line))
		} else {
			s.WriteString(NormalStyle.Render(line))
		}
		s.WriteString("\n")
	}

	s.WriteString("\n")
	s.WriteString(HelpStyle.Render("j/k:move | gg/G:top/bottom | Space:toggle | a:all | i:info | Enter:download | Esc:back"))

	// Show info popup if active
	if m.showInfoPopup && m.fileCursor < len(m.filteredFiles) {
		s.WriteString("\n\n")
		s.WriteString(m.renderInfoPopup(m.filteredFiles[m.fileCursor]))
	}

	return s.String()
}

func (m Model) viewDownloading() string {
	var s strings.Builder

	m.progressMu.Lock()
	completed := m.completedCount
	total := m.totalToDownload
	progress := make(map[string]drive.DownloadProgress)
	for k, v := range m.fileProgress {
		progress[k] = v
	}
	m.progressMu.Unlock()

	// Calculate overall progress
	var totalBytes, loadedBytes int64
	for _, f := range m.filteredFiles {
		if m.selectedFiles[f.ID] {
			totalBytes += f.Size
			if prog, ok := progress[f.ID]; ok {
				loadedBytes += prog.BytesLoaded
			}
		}
	}

	overallPct := 0.0
	if totalBytes > 0 {
		overallPct = float64(loadedBytes) / float64(totalBytes) * 100
	}

	s.WriteString(SubtitleStyle.Render(fmt.Sprintf("Downloading... %d/%d files (%.1f%%)", completed, total, overallPct)))
	s.WriteString("\n")

	// Calculate dynamic widths based on terminal width
	width := m.width
	if width < 80 {
		width = 80
	}
	progressBarWidth := width - 30
	if progressBarWidth < 20 {
		progressBarWidth = 20
	}

	// Overall progress bar
	s.WriteString(renderProgressBar(overallPct, progressBarWidth))
	s.WriteString(fmt.Sprintf(" %s / %s", formatSize(loadedBytes), formatSize(totalBytes)))
	s.WriteString("\n\n")

	// Calculate name width for file list
	// Reserve space for: name + space + status (progress bar or status text)
	statusWidth := 30 // "Done", "Failed", "Pending", or small progress bar
	nameWidth := width - statusWidth - 2
	if nameWidth < 20 {
		nameWidth = 20
	}

	// Individual file progress - show all files
	for _, f := range m.filteredFiles {
		if !m.selectedFiles[f.ID] {
			continue
		}

		prog, hasProgress := progress[f.ID]

		var status string
		if hasProgress {
			if prog.Error != nil {
				status = ErrorStyle.Render("Failed")
			} else if prog.Skipped {
				status = DimStyle.Render("Skipped")
			} else if prog.Done {
				status = SuccessStyle.Render("Done")
			} else {
				pct := 0.0
				if prog.TotalBytes > 0 {
					pct = float64(prog.BytesLoaded) / float64(prog.TotalBytes) * 100
				}
				status = fmt.Sprintf("%s %.0f%%", renderProgressBar(pct, 20), pct)
			}
		} else {
			status = DimStyle.Render("Pending")
		}

		s.WriteString(fmt.Sprintf("%s %s\n", truncateAndPad(f.DisplayName(), nameWidth), status))
	}

	s.WriteString("\n")
	s.WriteString(HelpStyle.Render("Esc to cancel"))

	return s.String()
}

// renderProgressBar creates a text-based progress bar
func renderProgressBar(pct float64, width int) string {
	filled := int(pct / 100 * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	empty := width - filled

	bar := SuccessStyle.Render(strings.Repeat("█", filled)) +
		DimStyle.Render(strings.Repeat("░", empty))
	return bar
}

func (m Model) viewDone() string {
	var s strings.Builder

	s.WriteString(SuccessStyle.Render("Download complete!"))
	s.WriteString("\n\n")

	successCount := 0
	skippedCount := 0
	errorCount := 0
	var failedFiles []string

	m.progressMu.Lock()
	for _, f := range m.filteredFiles {
		if !m.selectedFiles[f.ID] {
			continue
		}
		if prog, ok := m.fileProgress[f.ID]; ok {
			if prog.Error != nil {
				errorCount++
				failedFiles = append(failedFiles, fmt.Sprintf("  %s: %v", f.DisplayName(), prog.Error))
			} else if prog.Skipped {
				skippedCount++
			} else if prog.Done {
				successCount++
			}
		}
	}
	m.progressMu.Unlock()

	if len(failedFiles) > 0 {
		s.WriteString(ErrorStyle.Render("Failed downloads:"))
		s.WriteString("\n")
		for _, line := range failedFiles {
			s.WriteString(ErrorStyle.Render(line))
			s.WriteString("\n")
		}
		s.WriteString("\n")
	}

	s.WriteString(fmt.Sprintf("Successfully downloaded: %d files\n", successCount))
	if skippedCount > 0 {
		s.WriteString(DimStyle.Render(fmt.Sprintf("Skipped (already exist): %d files\n", skippedCount)))
	}
	if errorCount > 0 {
		s.WriteString(ErrorStyle.Render(fmt.Sprintf("Failed: %d files\n", errorCount)))
	}

	destDir := m.destDir
	if destDir == "" {
		destDir = "."
	}
	s.WriteString(DimStyle.Render(fmt.Sprintf("\nFiles saved to: %s", destDir)))

	s.WriteString("\n\n")
	s.WriteString(HelpStyle.Render("Press q or Ctrl+C to quit"))

	return s.String()
}

// renderInfoPopup renders a popup with file metadata
func (m Model) renderInfoPopup(f drive.DriveFile) string {
	var s strings.Builder

	boxWidth := 60
	if m.width > 70 {
		boxWidth = m.width - 10
	}

	// Title
	s.WriteString(BoxStyle.Render(TitleStyle.Render("File Information")))
	s.WriteString("\n\n")

	// File details
	s.WriteString(fmt.Sprintf("  %s: %s\n", SelectedStyle.Render("Name"), f.Name))
	if f.Path != "" {
		s.WriteString(fmt.Sprintf("  %s: %s\n", SelectedStyle.Render("Path"), f.Path))
	}
	s.WriteString(fmt.Sprintf("  %s: %s\n", SelectedStyle.Render("File ID"), f.ID))
	s.WriteString(fmt.Sprintf("  %s: %s\n", SelectedStyle.Render("Folder ID"), f.FolderID))
	s.WriteString(fmt.Sprintf("  %s: %s\n", SelectedStyle.Render("Size"), formatSize(f.Size)))
	s.WriteString(fmt.Sprintf("  %s: %s\n", SelectedStyle.Render("MIME Type"), f.MimeType))

	if !f.CreatedTime.IsZero() {
		s.WriteString(fmt.Sprintf("  %s: %s\n", SelectedStyle.Render("Created"), f.CreatedTime.Format("2006-01-02 15:04:05")))
	}
	if !f.ModifiedTime.IsZero() {
		s.WriteString(fmt.Sprintf("  %s: %s\n", SelectedStyle.Render("Modified"), f.ModifiedTime.Format("2006-01-02 15:04:05")))
	}

	s.WriteString("\n")
	s.WriteString(DimStyle.Render(strings.Repeat("-", boxWidth)))
	s.WriteString("\n")
	s.WriteString(HelpStyle.Render("Press i or Esc to close"))

	return s.String()
}

func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// truncateWidth truncates a string to fit within a given display width,
// properly handling wide characters (CJK, etc.)
func truncateWidth(s string, maxWidth int) string {
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	// Need to truncate
	ellipsis := "..."
	ellipsisWidth := runewidth.StringWidth(ellipsis)
	targetWidth := maxWidth - ellipsisWidth
	if targetWidth < 0 {
		targetWidth = 0
	}

	var result strings.Builder
	currentWidth := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if currentWidth+rw > targetWidth {
			break
		}
		result.WriteRune(r)
		currentWidth += rw
	}
	result.WriteString(ellipsis)
	return result.String()
}

// padRight pads a string with spaces to reach the target display width,
// properly handling wide characters (CJK, etc.)
func padRight(s string, targetWidth int) string {
	currentWidth := runewidth.StringWidth(s)
	if currentWidth >= targetWidth {
		return s
	}
	return s + strings.Repeat(" ", targetWidth-currentWidth)
}

// truncateAndPad truncates if needed and then pads to exact width
func truncateAndPad(s string, width int) string {
	truncated := truncateWidth(s, width)
	return padRight(truncated, width)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// fileExistsLocally checks if a file already exists in the destination directory with the same size
func (m Model) fileExistsLocally(f drive.DriveFile) bool {
	destDir := m.destDir
	if destDir == "" {
		destDir = "./output"
	}

	fullPath := destDir
	if f.Path != "" {
		fullPath = fmt.Sprintf("%s/%s", destDir, f.Path)
	}
	filePath := fmt.Sprintf("%s/%s", fullPath, f.Name)

	info, err := os.Stat(filePath)
	if err != nil {
		return false
	}
	return info.Size() == f.Size
}
