package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"google-drive-dl/drive"
	"google-drive-dl/tui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env file (optional, won't error if not found)
	godotenv.Load()

	useOAuth := flag.Bool("oauth", false, "Force OAuth authentication (recommended, avoids quota issues)")
	apiKey := flag.String("k", "", "Google Drive API key (or set GOOGLE_API_KEY env var)")
	credentialsFile := flag.String("credentials", "credentials.json", "Path to OAuth credentials.json file")
	linksFile := flag.String("f", "", "Path to file containing Google Drive links (one per line)")
	destDir := flag.String("o", "./output", "Output directory for downloaded files")
	maxConcurrent := flag.Int("c", 4, "Maximum concurrent downloads")
	downloadAll := flag.Bool("a", false, "Download all matching files without selection prompt")
	searchTerms := flag.String("s", "", "Search terms (comma-separated) to filter files")
	flag.Parse()

	// Get API key from flag or environment
	key := *apiKey
	if key == "" {
		key = os.Getenv("GOOGLE_API_KEY")
	}

	// Determine auth method and create client BEFORE starting TUI
	var client *drive.Client
	var err error
	ctx := context.Background()

	// If --oauth flag is set, or no API key available, use OAuth
	if *useOAuth || key == "" {
		// Check if credentials file exists
		if _, err := os.Stat(*credentialsFile); os.IsNotExist(err) {
			if *useOAuth {
				fmt.Printf("Error: credentials file not found: %s\n", *credentialsFile)
				fmt.Println("Specify path with: ./gdrive-dl --oauth -credentials /path/to/credentials.json")
				os.Exit(1)
			}
			// No OAuth credentials and no API key
			fmt.Println("Error: No authentication method configured")
			fmt.Println()
			fmt.Println("Option 1 - OAuth (recommended, avoids quota issues):")
			fmt.Println("  Place credentials.json in the current directory")
			fmt.Println("  Or specify path: ./gdrive-dl --oauth -credentials /path/to/credentials.json")
			fmt.Println()
			fmt.Println("Option 2 - API Key (simpler but has quota limits):")
			fmt.Println("  ./gdrive-dl -k YOUR_API_KEY")
			fmt.Println("  export GOOGLE_API_KEY=YOUR_API_KEY")
			fmt.Println("  Or add to .env: GOOGLE_API_KEY=YOUR_API_KEY")
			os.Exit(1)
		}

		// Authenticate with OAuth BEFORE starting TUI
		fmt.Println("Authenticating with Google Drive (OAuth)...")
		client, err = drive.NewClientWithOAuth(ctx, *credentialsFile)
		if err != nil {
			fmt.Printf("Error authenticating: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Authentication successful!")
	} else {
		// Use API key
		fmt.Println("Authenticating with Google Drive (API Key)...")
		client, err = drive.NewClientWithAPIKey(ctx, key)
		if err != nil {
			fmt.Printf("Error authenticating: %v\n", err)
			os.Exit(1)
		}
	}

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(*destDir, 0o755); err != nil {
		fmt.Printf("Error creating output directory: %v\n", err)
		os.Exit(1)
	}

	model := tui.NewModelWithClient(client, *linksFile, *destDir, *maxConcurrent, *downloadAll, *searchTerms)
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running program: %v\n", err)
		os.Exit(1)
	}
}
