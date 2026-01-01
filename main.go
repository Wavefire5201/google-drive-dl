package main

import (
	"flag"
	"fmt"
	"os"

	"img-util/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	linksFile := flag.String("f", "", "Path to file containing Google Drive links (one per line)")
	destDir := flag.String("o", ".", "Output directory for downloaded files")
	maxConcurrent := flag.Int("c", 4, "Maximum concurrent downloads")
	flag.Parse()

	// Verify credentials.json exists
	if _, err := os.Stat("credentials.json"); os.IsNotExist(err) {
		fmt.Println("Error: credentials.json not found in current directory")
		fmt.Println("Please place your Google Drive API credentials file here.")
		os.Exit(1)
	}

	// Create output directory if it doesn't exist
	if *destDir != "." {
		if err := os.MkdirAll(*destDir, 0755); err != nil {
			fmt.Printf("Error creating output directory: %v\n", err)
			os.Exit(1)
		}
	}

	model := tui.NewModel(*linksFile, *destDir, *maxConcurrent)
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running program: %v\n", err)
		os.Exit(1)
	}
}
