package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// Constants for configuration
const (
	// DefaultPageSize is the number of files to fetch per API request
	DefaultPageSize = 1000
	// DefaultMaxDepth is the maximum recursion depth for folder traversal
	DefaultMaxDepth = 10
	// OAuthTimeout is the maximum time to wait for OAuth authorization
	OAuthTimeout = 5 * time.Minute
)

// Pre-compiled regex for extracting folder IDs from URLs
var folderIDRegex = regexp.MustCompile(`/folders/([a-zA-Z0-9_-]+)`)

// DriveFile represents a file from Google Drive with its metadata.
type DriveFile struct {
	// ID is the unique Google Drive file identifier
	ID string
	// Name is the file name
	Name string
	// Path is the parent folder path for nested folders
	Path string
	// Size is the file size in bytes
	Size int64
	// FolderID is the ID of the parent folder
	FolderID string
	// MimeType is the file's MIME type
	MimeType string
	// CreatedTime is when the file was created
	CreatedTime time.Time
	// ModifiedTime is when the file was last modified
	ModifiedTime time.Time
}

// DisplayName returns the name with path prefix if available
func (f DriveFile) DisplayName() string {
	if f.Path != "" {
		return f.Path + "/" + f.Name
	}
	return f.Name
}

// DownloadProgress tracks the progress of a file download.
type DownloadProgress struct {
	// FileID is the Google Drive file identifier
	FileID string
	// FileName is the display name of the file being downloaded
	FileName string
	// BytesLoaded is the number of bytes downloaded so far
	BytesLoaded int64
	// TotalBytes is the total file size in bytes
	TotalBytes int64
	// Done indicates whether the download is complete
	Done bool
	// Skipped indicates whether the file was skipped (already exists locally)
	Skipped bool
	// Error contains any error that occurred during download
	Error error
}

// Client wraps the Google Drive API and provides methods for listing and downloading files.
type Client struct {
	service *drive.Service
}

// NewClientWithAPIKey creates a new Drive client using an API key
func NewClientWithAPIKey(ctx context.Context, apiKey string) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required")
	}

	srv, err := drive.NewService(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("unable to create Drive service: %w", err)
	}

	return &Client{service: srv}, nil
}

// NewClientWithOAuth creates a new Drive client using OAuth credentials
func NewClientWithOAuth(ctx context.Context, credentialsPath string) (*Client, error) {
	b, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %w", err)
	}

	config, err := google.ConfigFromJSON(b, drive.DriveReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials: %w", err)
	}

	client, err := getOAuthClient(ctx, config)
	if err != nil {
		return nil, err
	}

	srv, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("unable to create Drive service: %w", err)
	}

	return &Client{service: srv}, nil
}

// getOAuthClient retrieves a token, saves it, and returns the generated client
func getOAuthClient(ctx context.Context, config *oauth2.Config) (*http.Client, error) {
	tokFile := "token.json"
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		tok, err = getTokenFromWeb(config)
		if err != nil {
			return nil, err
		}
		saveToken(tokFile, tok)
	}
	return config.Client(ctx, tok), nil
}

// getTokenFromWeb starts a local server to capture the OAuth callback
func getTokenFromWeb(config *oauth2.Config) (*oauth2.Token, error) {
	// Start listener on a random available port
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return nil, fmt.Errorf("failed to start OAuth callback server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	// Set redirect URL to the dynamically assigned port
	config.RedirectURL = fmt.Sprintf("http://localhost:%d", port)
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)

	// Channel to receive the auth code
	codeChan := make(chan string)
	errChan := make(chan error)

	// Create a new ServeMux to avoid conflicts with default mux
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errChan <- fmt.Errorf("no code in callback")
			fmt.Fprintf(w, "Error: No authorization code received. Please try again.")
			return
		}
		fmt.Fprintf(w, "<html><body><h1>Authorization successful!</h1><p>You can close this window and return to the terminal.</p></body></html>")
		codeChan <- code
	})

	go func() {
		if err := server.Serve(listener); err != http.ErrServerClosed {
			select {
			case errChan <- err:
			default:
			}
		}
	}()

	fmt.Printf("\n=== Google Drive Authorization ===\n")
	fmt.Printf("Open this link in your browser:\n\n")
	fmt.Printf("  %v\n\n", authURL)
	fmt.Printf("Waiting for authorization (5 minute timeout)...\n")

	// Wait for the code with timeout
	var authCode string
	select {
	case authCode = <-codeChan:
		// Got the code
	case err := <-errChan:
		server.Close()
		return nil, err
	case <-time.After(OAuthTimeout):
		server.Close()
		return nil, fmt.Errorf("OAuth authorization timed out after %v", OAuthTimeout)
	}

	// Shutdown the server
	server.Close()

	tok, err := config.Exchange(context.TODO(), authCode)
	if err != nil {
		return nil, fmt.Errorf("unable to exchange token: %w", err)
	}
	return tok, nil
}

// tokenFromFile retrieves a token from a local file
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// saveToken saves a token to a file
func saveToken(path string, token *oauth2.Token) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("unable to save token: %w", err)
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(token)
}

// ExtractFolderID extracts the folder ID from a Google Drive URL
func ExtractFolderID(url string) (string, error) {
	// Handle formats like:
	// https://drive.google.com/drive/folders/FOLDER_ID
	// https://drive.google.com/drive/folders/FOLDER_ID?usp=drive_link
	matches := folderIDRegex.FindStringSubmatch(url)
	if len(matches) < 2 {
		return "", fmt.Errorf("could not extract folder ID from URL: %s", url)
	}
	return matches[1], nil
}

// ListFiles lists all files in a folder (non-recursive, for backward compatibility)
func (c *Client) ListFiles(ctx context.Context, folderID string) ([]DriveFile, error) {
	files, warnings, err := c.listFilesWithPath(ctx, folderID, "", 0, DefaultMaxDepth)
	if err != nil {
		return nil, err
	}
	if len(warnings) > 0 {
		return files, fmt.Errorf("completed with warnings: %s", strings.Join(warnings, "; "))
	}
	return files, nil
}

// ListFilesRecursive lists all files in a folder and its subfolders up to maxDepth
func (c *Client) ListFilesRecursive(ctx context.Context, folderID string, maxDepth int) ([]DriveFile, error) {
	files, warnings, err := c.listFilesWithPath(ctx, folderID, "", 0, maxDepth)
	if err != nil {
		return nil, err
	}
	if len(warnings) > 0 {
		return files, fmt.Errorf("completed with warnings: %s", strings.Join(warnings, "; "))
	}
	return files, nil
}

// listFilesWithPath is the internal recursive implementation
func (c *Client) listFilesWithPath(ctx context.Context, folderID, currentPath string, currentDepth, maxDepth int) ([]DriveFile, []string, error) {
	var files []DriveFile
	var warnings []string
	var subfolders []struct {
		id   string
		name string
	}
	pageToken := ""

	for {
		query := fmt.Sprintf("'%s' in parents and trashed = false", folderID)
		call := c.service.Files.List().
			Q(query).
			Fields("nextPageToken, files(id, name, size, mimeType, createdTime, modifiedTime)").
			PageSize(DefaultPageSize)

		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		result, err := call.Context(ctx).Do()
		if err != nil {
			return nil, nil, fmt.Errorf("unable to list files: %w", err)
		}

		for _, f := range result.Files {
			// Check if it's a folder
			if f.MimeType == "application/vnd.google-apps.folder" {
				// Store folder for recursive processing
				if currentDepth < maxDepth {
					subfolders = append(subfolders, struct {
						id   string
						name string
					}{id: f.Id, name: f.Name})
				}
				// Don't add folders to the file list
				continue
			}

			file := DriveFile{
				ID:       f.Id,
				Name:     f.Name,
				Path:     currentPath,
				Size:     f.Size,
				FolderID: folderID,
				MimeType: f.MimeType,
			}

			// Parse timestamps
			if f.CreatedTime != "" {
				if t, err := time.Parse(time.RFC3339, f.CreatedTime); err == nil {
					file.CreatedTime = t
				}
			}
			if f.ModifiedTime != "" {
				if t, err := time.Parse(time.RFC3339, f.ModifiedTime); err == nil {
					file.ModifiedTime = t
				}
			}

			files = append(files, file)
		}

		pageToken = result.NextPageToken
		if pageToken == "" {
			break
		}
	}

	// Recursively process subfolders
	for _, subfolder := range subfolders {
		subPath := subfolder.name
		if currentPath != "" {
			subPath = currentPath + "/" + subfolder.name
		}

		subFiles, subWarnings, err := c.listFilesWithPath(ctx, subfolder.id, subPath, currentDepth+1, maxDepth)
		if err != nil {
			// Collect warning but continue with other folders
			warnings = append(warnings, fmt.Sprintf("subfolder '%s': %v", subPath, err))
			continue
		}
		warnings = append(warnings, subWarnings...)
		files = append(files, subFiles...)
	}

	return files, warnings, nil
}

// ListFilesFromFolders lists files from multiple folder URLs (recursively)
func (c *Client) ListFilesFromFolders(ctx context.Context, folderURLs []string) ([]DriveFile, error) {
	return c.ListFilesFromFoldersWithDepth(ctx, folderURLs, 10)
}

// ListFilesFromFoldersWithDepth lists files from multiple folder URLs with specified max depth
func (c *Client) ListFilesFromFoldersWithDepth(ctx context.Context, folderURLs []string, maxDepth int) ([]DriveFile, error) {
	var allFiles []DriveFile
	var mu sync.Mutex
	var wg sync.WaitGroup
	errChan := make(chan error, len(folderURLs))

	for _, url := range folderURLs {
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}

		wg.Add(1)
		go func(u string) {
			defer wg.Done()

			folderID, err := ExtractFolderID(u)
			if err != nil {
				errChan <- err
				return
			}

			files, err := c.ListFilesRecursive(ctx, folderID, maxDepth)
			if err != nil {
				errChan <- fmt.Errorf("folder %s: %w", folderID, err)
				return
			}

			mu.Lock()
			allFiles = append(allFiles, files...)
			mu.Unlock()
		}(url)
	}

	wg.Wait()
	close(errChan)

	// Collect any errors
	var errs []string
	for err := range errChan {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return allFiles, fmt.Errorf("some folders failed: %s", strings.Join(errs, "; "))
	}

	return allFiles, nil
}

// FilterFiles filters files by search terms (OR logic - matches any term)
func FilterFiles(files []DriveFile, searchTerms []string) []DriveFile {
	if len(searchTerms) == 0 {
		return files
	}

	var filtered []DriveFile
	for _, f := range files {
		nameLower := strings.ToLower(f.Name)
		for _, term := range searchTerms {
			if strings.Contains(nameLower, strings.ToLower(strings.TrimSpace(term))) {
				filtered = append(filtered, f)
				break
			}
		}
	}
	return filtered
}

// DownloadFile downloads a file to the specified directory
func (c *Client) DownloadFile(ctx context.Context, file DriveFile, destDir string, progressChan chan<- DownloadProgress) error {
	// Build the full destination path including subfolder structure
	fullDestDir := destDir
	if file.Path != "" {
		fullDestDir = fmt.Sprintf("%s/%s", destDir, file.Path)
	}

	destPath := fmt.Sprintf("%s/%s", fullDestDir, file.Name)

	// Check if file already exists with same size
	if info, err := os.Stat(destPath); err == nil {
		if info.Size() == file.Size {
			// File exists and has same size, skip download
			if progressChan != nil {
				progressChan <- DownloadProgress{
					FileID:      file.ID,
					FileName:    file.DisplayName(),
					BytesLoaded: file.Size,
					TotalBytes:  file.Size,
					Done:        true,
					Skipped:     true,
				}
			}
			return nil
		}
	}

	resp, err := c.service.Files.Get(file.ID).Context(ctx).Download()
	if err != nil {
		return fmt.Errorf("unable to download file: %w", err)
	}
	defer resp.Body.Close()

	// Create subdirectories if they don't exist
	if file.Path != "" {
		if err := os.MkdirAll(fullDestDir, 0755); err != nil {
			return fmt.Errorf("unable to create directory %s: %w", fullDestDir, err)
		}
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("unable to create file: %w", err)
	}
	defer out.Close()

	// Create a progress reader if channel provided
	var reader io.Reader = resp.Body
	if progressChan != nil {
		reader = &progressReader{
			reader:       resp.Body,
			fileID:       file.ID,
			fileName:     file.DisplayName(),
			totalBytes:   file.Size,
			progressChan: progressChan,
		}
	}

	_, err = io.Copy(out, reader)
	if err != nil {
		return fmt.Errorf("unable to save file: %w", err)
	}

	// Send final progress
	if progressChan != nil {
		progressChan <- DownloadProgress{
			FileID:      file.ID,
			FileName:    file.DisplayName(),
			BytesLoaded: file.Size,
			TotalBytes:  file.Size,
			Done:        true,
		}
	}

	return nil
}

// DownloadFiles downloads multiple files in parallel
func (c *Client) DownloadFiles(ctx context.Context, files []DriveFile, destDir string, maxConcurrent int, progressChan chan<- DownloadProgress) error {
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}

	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	errChan := make(chan error, len(files))

	for _, file := range files {
		wg.Add(1)
		go func(f DriveFile) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := c.DownloadFile(ctx, f, destDir, progressChan); err != nil {
				if progressChan != nil {
					progressChan <- DownloadProgress{
						FileID:   f.ID,
						FileName: f.Name,
						Done:     true,
						Error:    err,
					}
				}
				errChan <- fmt.Errorf("%s: %w", f.Name, err)
			}
		}(file)
	}

	wg.Wait()
	close(errChan)

	var errs []string
	for err := range errChan {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("some downloads failed: %s", strings.Join(errs, "; "))
	}

	return nil
}

// progressReader wraps an io.Reader to report progress
type progressReader struct {
	reader       io.Reader
	fileID       string
	fileName     string
	bytesRead    int64
	totalBytes   int64
	progressChan chan<- DownloadProgress
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.bytesRead += int64(n)

	if pr.progressChan != nil && n > 0 {
		pr.progressChan <- DownloadProgress{
			FileID:      pr.fileID,
			FileName:    pr.fileName,
			BytesLoaded: pr.bytesRead,
			TotalBytes:  pr.totalBytes,
			Done:        false,
		}
	}

	return n, err
}
