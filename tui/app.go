package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"img-util/drive"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// View represents the current screen
type View int

const (
	ViewLinks View = iota
	ViewSearch
	ViewFiles
	ViewDownloading
	ViewDone
)

// Model is the main application model
type Model struct {
	view          View
	width         int
	height        int
	err           error
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

	// Download progress
	fileProgress    map[string]drive.DownloadProgress
	downloading     bool
	downloadDone    bool
	completedCount  int
	totalToDownload int
	progressMu      sync.Mutex

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

// NewModel creates a new TUI model
func NewModel(linksFile, destDir string, maxConcurrent int) Model {
	ti := textarea.New()
	ti.Placeholder = "Paste Google Drive folder links (one per line)..."
	ti.Focus()
	ti.SetWidth(80)
	ti.SetHeight(10)

	si := textinput.New()
	si.Placeholder = "Search terms (comma-separated, e.g., 'abc, ddd')"
	si.Width = 60

	ctx, cancel := context.WithCancel(context.Background())

	return Model{
		view:          ViewLinks,
		linksInput:    ti,
		searchInput:   si,
		selectedFiles: make(map[string]bool),
		fileProgress:  make(map[string]drive.DownloadProgress),
		linksFile:     linksFile,
		destDir:       destDir,
		maxConcurrent: maxConcurrent,
		ctx:           ctx,
		cancel:        cancel,
	}
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		textarea.Blink,
		m.initClient(),
	}

	// Load links from file if provided
	if m.linksFile != "" {
		cmds = append(cmds, m.loadLinksFromFile())
	}

	return tea.Batch(cmds...)
}

func (m Model) initClient() tea.Cmd {
	return func() tea.Msg {
		client, err := drive.NewClient(m.ctx, "credentials.json")
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
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case "q":
			if m.view != ViewDownloading && m.view != ViewLinks && m.view != ViewSearch {
				m.cancel()
				return m, tea.Quit
			}
			// Allow 'q' to be typed in text inputs
			if m.view == ViewDone {
				m.cancel()
				return m, tea.Quit
			}
		case "esc":
			if m.view == ViewDownloading {
				m.cancel()
				return m, tea.Quit
			}
			// Go back
			switch m.view {
			case ViewSearch:
				m.view = ViewLinks
				m.linksInput.Focus()
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
		m.view = ViewSearch
		m.searchInput.Focus()
		return m, textinput.Blink

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
				// Alt+Enter to submit
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
	for _, f := range m.filteredFiles {
		m.selectedFiles[f.ID] = true
	}

	m.view = ViewFiles
	m.err = nil
	return m, nil
}

func (m Model) updateFiles(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.fileCursor > 0 {
				m.fileCursor--
			}
		case "down", "j":
			if m.fileCursor < len(m.filteredFiles)-1 {
				m.fileCursor++
			}
		case " ":
			// Toggle selection
			if m.fileCursor < len(m.filteredFiles) {
				f := m.filteredFiles[m.fileCursor]
				m.selectedFiles[f.ID] = !m.selectedFiles[f.ID]
			}
		case "a":
			// Toggle all
			m.selectAll = !m.selectAll
			for _, f := range m.filteredFiles {
				m.selectedFiles[f.ID] = m.selectAll
			}
		case "enter":
			return m.startDownload()
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
			destDir = "."
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

				// Track start
				m.progressMu.Lock()
				m.fileProgress[f.ID] = drive.DownloadProgress{
					FileID:      f.ID,
					FileName:    f.Name,
					TotalBytes:  f.Size,
					BytesLoaded: 0,
				}
				m.progressMu.Unlock()

				// Download
				err := m.driveClient.DownloadFile(m.ctx, f, destDir, nil)

				// Track completion
				m.progressMu.Lock()
				m.fileProgress[f.ID] = drive.DownloadProgress{
					FileID:      f.ID,
					FileName:    f.Name,
					TotalBytes:  f.Size,
					BytesLoaded: f.Size,
					Done:        true,
					Error:       err,
				}
				m.completedCount++
				m.progressMu.Unlock()

				if err != nil {
					errorsMu.Lock()
					errors = append(errors, fmt.Sprintf("%s: %v", f.Name, err))
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
	s.WriteString("\n\n")

	switch m.view {
	case ViewLinks:
		s.WriteString(m.viewLinks())
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
		s.WriteString(WarningStyle.Render("Authenticating with Google Drive..."))
	}

	return s.String()
}

func (m Model) viewSearch() string {
	var s strings.Builder

	s.WriteString(SubtitleStyle.Render(fmt.Sprintf("Found %d files. Enter search terms:", len(m.allFiles))))
	s.WriteString("\n")
	s.WriteString(m.searchInput.View())
	s.WriteString("\n")
	s.WriteString(HelpStyle.Render("Enter to search | Esc to go back | Ctrl+C to quit"))

	return s.String()
}

func (m Model) viewFiles() string {
	var s strings.Builder

	selectedCount := 0
	for _, selected := range m.selectedFiles {
		if selected {
			selectedCount++
		}
	}

	s.WriteString(SubtitleStyle.Render(fmt.Sprintf("Matching files (%d/%d selected):", selectedCount, len(m.filteredFiles))))
	s.WriteString("\n\n")

	// Show files with pagination
	visibleStart := 0
	visibleEnd := len(m.filteredFiles)
	maxVisible := m.height - 10
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

		size := formatSize(f.Size)
		line := fmt.Sprintf("%s%s %s (%s)", cursor, checkbox, f.Name, size)

		if i == m.fileCursor {
			s.WriteString(SelectedStyle.Render(line))
		} else {
			s.WriteString(NormalStyle.Render(line))
		}
		s.WriteString("\n")
	}

	s.WriteString("\n")
	s.WriteString(HelpStyle.Render("j/k or arrows to move | Space to toggle | a to toggle all | Enter to download | Esc to go back"))

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

	s.WriteString(SubtitleStyle.Render(fmt.Sprintf("Downloading files... (%d/%d)", completed, total)))
	s.WriteString("\n\n")

	for _, f := range m.filteredFiles {
		if !m.selectedFiles[f.ID] {
			continue
		}

		prog, hasProgress := progress[f.ID]

		var status string
		if hasProgress {
			if prog.Error != nil {
				status = ErrorStyle.Render("Failed")
			} else if prog.Done {
				status = SuccessStyle.Render("Done")
			} else {
				status = WarningStyle.Render("Downloading...")
			}
		} else {
			status = DimStyle.Render("Pending...")
		}

		s.WriteString(fmt.Sprintf("%-50s %s\n", truncate(f.Name, 50), status))
	}

	s.WriteString("\n")
	s.WriteString(HelpStyle.Render("Esc to cancel"))

	return s.String()
}

func (m Model) viewDone() string {
	var s strings.Builder

	s.WriteString(SuccessStyle.Render("Download complete!"))
	s.WriteString("\n\n")

	successCount := 0
	errorCount := 0
	var failedFiles []string

	m.progressMu.Lock()
	for _, f := range m.filteredFiles {
		if !m.selectedFiles[f.ID] {
			continue
		}
		if prog, ok := m.fileProgress[f.ID]; ok && prog.Error != nil {
			errorCount++
			failedFiles = append(failedFiles, fmt.Sprintf("  %s: %v", f.Name, prog.Error))
		} else if prog.Done {
			successCount++
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
