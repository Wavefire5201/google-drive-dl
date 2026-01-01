package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// DriveFile represents a file from Google Drive
type DriveFile struct {
	ID       string
	Name     string
	Size     int64
	FolderID string
}

// DownloadProgress tracks download progress
type DownloadProgress struct {
	FileID      string
	FileName    string
	BytesLoaded int64
	TotalBytes  int64
	Done        bool
	Error       error
}

// Client wraps the Google Drive API
type Client struct {
	service *drive.Service
}

// NewClient creates a new Drive client using credentials.json
func NewClient(ctx context.Context, credentialsPath string) (*Client, error) {
	b, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %v", err)
	}

	config, err := google.ConfigFromJSON(b, drive.DriveReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials: %v", err)
	}

	client, err := getClient(config)
	if err != nil {
		return nil, err
	}

	srv, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("unable to create Drive service: %v", err)
	}

	return &Client{service: srv}, nil
}

// getClient retrieves a token, saves it, and returns the generated client
func getClient(config *oauth2.Config) (*http.Client, error) {
	tokFile := "token.json"
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		tok, err = getTokenFromWeb(config)
		if err != nil {
			return nil, err
		}
		saveToken(tokFile, tok)
	}
	return config.Client(context.Background(), tok), nil
}

// getTokenFromWeb requests a token from the web, then returns the token
func getTokenFromWeb(config *oauth2.Config) (*oauth2.Token, error) {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser:\n%v\n\nEnter authorization code: ", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		return nil, fmt.Errorf("unable to read authorization code: %v", err)
	}

	tok, err := config.Exchange(context.TODO(), authCode)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve token from web: %v", err)
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
		return fmt.Errorf("unable to save token: %v", err)
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(token)
}

// ExtractFolderID extracts the folder ID from a Google Drive URL
func ExtractFolderID(url string) (string, error) {
	// Handle formats like:
	// https://drive.google.com/drive/folders/FOLDER_ID
	// https://drive.google.com/drive/folders/FOLDER_ID?usp=drive_link
	re := regexp.MustCompile(`/folders/([a-zA-Z0-9_-]+)`)
	matches := re.FindStringSubmatch(url)
	if len(matches) < 2 {
		return "", fmt.Errorf("could not extract folder ID from URL: %s", url)
	}
	return matches[1], nil
}

// ListFiles lists all files in a folder
func (c *Client) ListFiles(ctx context.Context, folderID string) ([]DriveFile, error) {
	var files []DriveFile
	pageToken := ""

	for {
		query := fmt.Sprintf("'%s' in parents and trashed = false", folderID)
		call := c.service.Files.List().
			Q(query).
			Fields("nextPageToken, files(id, name, size)").
			PageSize(1000)

		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		result, err := call.Context(ctx).Do()
		if err != nil {
			return nil, fmt.Errorf("unable to list files: %v", err)
		}

		for _, f := range result.Files {
			files = append(files, DriveFile{
				ID:       f.Id,
				Name:     f.Name,
				Size:     f.Size,
				FolderID: folderID,
			})
		}

		pageToken = result.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return files, nil
}

// ListFilesFromFolders lists files from multiple folder URLs
func (c *Client) ListFilesFromFolders(ctx context.Context, folderURLs []string) ([]DriveFile, error) {
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

			files, err := c.ListFiles(ctx, folderID)
			if err != nil {
				errChan <- fmt.Errorf("folder %s: %v", folderID, err)
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
	resp, err := c.service.Files.Get(file.ID).Context(ctx).Download()
	if err != nil {
		return fmt.Errorf("unable to download file: %v", err)
	}
	defer resp.Body.Close()

	destPath := fmt.Sprintf("%s/%s", destDir, file.Name)
	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("unable to create file: %v", err)
	}
	defer out.Close()

	// Create a progress reader
	pr := &progressReader{
		reader:       resp.Body,
		fileID:       file.ID,
		fileName:     file.Name,
		totalBytes:   file.Size,
		progressChan: progressChan,
	}

	_, err = io.Copy(out, pr)
	if err != nil {
		return fmt.Errorf("unable to save file: %v", err)
	}

	// Send final progress
	if progressChan != nil {
		progressChan <- DownloadProgress{
			FileID:      file.ID,
			FileName:    file.Name,
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
				errChan <- fmt.Errorf("%s: %v", f.Name, err)
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
