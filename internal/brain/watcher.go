package brain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
)

// startWatcher registers fsnotify events on the storage directory and fires debounced indexing.
func (m *Manager) startWatcher(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		m.log.Error("failed to create fsnotify watcher", zap.Error(err))
		return
	}
	m.watcher = watcher

	m.registerWatcherPaths()

	pendingFiles := make(map[string]time.Time)
	var mu sync.Mutex

	// Debounce loop (runs every 500ms, indexes files quiet for 1.5s)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				now := time.Now()
				for path, lastSeen := range pendingFiles {
					if now.Sub(lastSeen) >= 1500*time.Millisecond {
						delete(pendingFiles, path)
						// Dispatch file indexing in a separate goroutine
						go m.IndexFile(path)
					}
				}
				mu.Unlock()
			}
		}
	}()

	// Event handling loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				watcher.Close()
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				// Focus on write, create, rename events
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) {
					if isSupportedExtension(event.Name) {
						mu.Lock()
						pendingFiles[event.Name] = time.Now()
						mu.Unlock()
					}
				} else if event.Has(fsnotify.Remove) {
					// File deleted, clear vectors from store
					filename := filepath.Base(event.Name)
					m.log.Info("file removed from storage, removing vectors", zap.String("filename", filename))
					if err := m.store.RemoveFile(filename); err != nil {
						m.log.Error("failed to remove deleted file from index", zap.String("filename", filename), zap.Error(err))
					}
					m.broadcastStorageUpdate(filename)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				m.log.Error("fsnotify watcher error", zap.Error(err))
			}
		}
	}()
}

// isSupportedExtension filters for indexable file formats.
func isSupportedExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf", ".docx", ".txt", ".md", ".csv",
		".go", ".py", ".js", ".ts", ".sh", ".json", ".xml", ".yaml", ".yml", ".html", ".css":
		return true
	}
	return false
}

// registerWatcherPaths updates fsnotify monitored paths for storage and local dirs.
func (m *Manager) registerWatcherPaths() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.watcher == nil {
		return
	}

	// Always watch the primary shared storage directory
	if err := m.watcher.Add(m.storageDir); err != nil {
		m.log.Warn("failed to add storage directory to watcher", zap.Error(err))
	}

	// Recursively watch local directories
	for _, dir := range m.localIndexDirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				if addErr := m.watcher.Add(path); addErr != nil {
					m.log.Debug("failed to add directory path to watcher", zap.String("path", path), zap.Error(addErr))
				}
			}
			return nil
		})
		if err != nil {
			m.log.Warn("failed walking local directory to watch", zap.String("dir", dir), zap.Error(err))
		}
	}
}

