# google-drive-dl

TUI application for downloading files from Google Drive folders.

## Features

- Recursive folder traversal
- File search/filter
- Dedupe mode (shows smallest version of duplicate files)
- Skip existing files
- Concurrent downloads
- File list caching

## Installation

```bash
go build
```

## Usage

```bash
# With API key
./google-drive-dl -api-key YOUR_API_KEY

# With OAuth
./google-drive-dl -credentials path/to/credentials.json

# Auto-download with search terms
./google-drive-dl -api-key KEY -links links.txt -search "term1,term2" -dest ./output
```

## Keybindings

| Key   | Action                |
| ----- | --------------------- |
| j/k   | Navigate up/down      |
| gg/G  | Jump to top/bottom    |
| Space | Toggle selection      |
| a     | Select all            |
| /     | Search                |
| u     | Toggle dedupe mode    |
| i     | File info             |
| r     | Refresh (clear cache) |
| Enter | Confirm/Download      |
| q     | Quit                  |
