package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// CachedFile represents a cached file entry
type CachedFile struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	Size         int64     `json:"size"`
	FolderID     string    `json:"folder_id"`
	MimeType     string    `json:"mime_type"`
	CreatedTime  time.Time `json:"created_time"`
	ModifiedTime time.Time `json:"modified_time"`
}

// FolderCache represents cached data for a single folder
type FolderCache struct {
	FolderID   string       `json:"folder_id"`
	FolderName string       `json:"folder_name"`
	Files      []CachedFile `json:"files"`
	FetchedAt  time.Time    `json:"fetched_at"`
}

// Cache represents the full cache structure
type Cache struct {
	Folders map[string]*FolderCache `json:"folders"`
}

// Manager handles cache operations
type Manager struct {
	cacheDir  string
	cacheFile string
	cache     *Cache
}

// NewManager creates a new cache manager
func NewManager() (*Manager, error) {
	cacheDir, err := getCacheDir()
	if err != nil {
		return nil, err
	}

	m := &Manager{
		cacheDir:  cacheDir,
		cacheFile: filepath.Join(cacheDir, "folders.json"),
		cache: &Cache{
			Folders: make(map[string]*FolderCache),
		},
	}

	// Load existing cache
	if err := m.load(); err != nil {
		// If cache doesn't exist or is corrupted, start fresh
		m.cache = &Cache{
			Folders: make(map[string]*FolderCache),
		}
	}

	return m, nil
}

// getCacheDir returns the cache directory path
func getCacheDir() (string, error) {
	// Try XDG_CACHE_HOME first
	if cacheHome := os.Getenv("XDG_CACHE_HOME"); cacheHome != "" {
		return filepath.Join(cacheHome, "img-util"), nil
	}

	// Fall back to ~/.cache
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(homeDir, ".cache", "img-util"), nil
}

// load reads the cache from disk
func (m *Manager) load() error {
	data, err := os.ReadFile(m.cacheFile)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, m.cache)
}

// save writes the cache to disk
func (m *Manager) save() error {
	// Ensure cache directory exists
	if err := os.MkdirAll(m.cacheDir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(m.cache, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(m.cacheFile, data, 0644)
}

// GetFolder returns cached data for a folder, or nil if not cached
func (m *Manager) GetFolder(folderID string) *FolderCache {
	return m.cache.Folders[folderID]
}

// SetFolder stores folder data in cache
func (m *Manager) SetFolder(folderID string, folderName string, files []CachedFile) error {
	m.cache.Folders[folderID] = &FolderCache{
		FolderID:   folderID,
		FolderName: folderName,
		Files:      files,
		FetchedAt:  time.Now(),
	}

	return m.save()
}

// InvalidateFolder removes a folder from cache
func (m *Manager) InvalidateFolder(folderID string) error {
	delete(m.cache.Folders, folderID)
	return m.save()
}

// GetAllCachedFolderIDs returns all cached folder IDs
func (m *Manager) GetAllCachedFolderIDs() []string {
	ids := make([]string, 0, len(m.cache.Folders))
	for id := range m.cache.Folders {
		ids = append(ids, id)
	}
	return ids
}

// Clear removes all cached data
func (m *Manager) Clear() error {
	m.cache = &Cache{
		Folders: make(map[string]*FolderCache),
	}
	return m.save()
}
