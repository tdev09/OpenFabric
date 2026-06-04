package llm

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Message represents a single message in a chat history.
type Message struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	SentAt  time.Time `json:"sent_at"`
}

// Session represents a persistent conversation history.
type Session struct {
	ID               string    `json:"id"`
	Model            string    `json:"model"`
	Title            string    `json:"title"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Messages         []Message `json:"messages"`
	LastExtractedIdx int       `json:"last_extracted_idx"`
}

// ensureChatsDir returns the absolute path to the chats directory and creates it if missing.
func ensureChatsDir(dataDir string) (string, error) {
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find user home dir: %w", err)
		}
		dataDir = filepath.Join(home, ".openfabric")
	}
	dir := filepath.Join(dataDir, "chats")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create chats dir: %w", err)
	}
	return dir, nil
}

// cleanSessionPath resolves and validates the path to a session JSON file to prevent path traversal.
func cleanSessionPath(dataDir, sessionID string) (string, error) {
	if sessionID == "" || strings.Contains(sessionID, "/") || strings.Contains(sessionID, "\\") || strings.Contains(sessionID, "..") {
		return "", errors.New("invalid or insecure session ID")
	}
	dir, err := ensureChatsDir(dataDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".json"), nil
}

// generateID generates a secure, random hex ID for a session.
func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "sess_" + hex.EncodeToString(b)
}

// generateTitle creates a short title from the first message content.
func generateTitle(content string) string {
	words := strings.Fields(content)
	if len(words) == 0 {
		return "New Chat"
	}
	limit := 6
	if len(words) < limit {
		limit = len(words)
	}
	return strings.Join(words[:limit], " ")
}

// saveSession writes a session atomically using a temp file.
func saveSession(path string, sess *Session) error {
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// CreateSession initializes a new chat session and saves it to disk.
func CreateSession(dataDir, model string) (*Session, error) {
	pathDir, err := ensureChatsDir(dataDir)
	if err != nil {
		return nil, err
	}

	sess := &Session{
		ID:        generateID(),
		Model:     model,
		Title:     "New Chat",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages:  []Message{},
	}

	path := filepath.Join(pathDir, sess.ID+".json")
	if err := saveSession(path, sess); err != nil {
		return nil, fmt.Errorf("save new session: %w", err)
	}
	return sess, nil
}

// AppendMessage appends a message to the session. Auto-generates title on first message.
func AppendMessage(dataDir, sessionID string, msg Message) error {
	path, err := cleanSessionPath(dataDir, sessionID)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}

	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return fmt.Errorf("unmarshal session: %w", err)
	}

	// Auto-title if it's the first message
	if (sess.Title == "" || sess.Title == "New Chat") && len(sess.Messages) == 0 && msg.Role == "user" {
		sess.Title = generateTitle(msg.Content)
	}

	sess.Messages = append(sess.Messages, msg)
	sess.UpdatedAt = time.Now()

	if err := saveSession(path, &sess); err != nil {
		return fmt.Errorf("append save session: %w", err)
	}
	return nil
}

// ListSessions returns a summary list of all chat sessions, sorted by UpdatedAt descending.
func ListSessions(dataDir string) ([]Session, error) {
	dir, err := ensureChatsDir(dataDir)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read chats dir: %w", err)
	}

	var sessions []Session
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue // skip corrupted or unreadable files
		}

		var sess Session
		if err := json.Unmarshal(data, &sess); err != nil {
			continue // skip malformed files
		}

		// Clear messages for list summaries to save bandwidth
		sess.Messages = nil
		sessions = append(sessions, sess)
	}

	// Sort by UpdatedAt descending
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})

	return sessions, nil
}

// GetSession retrieves the full details of a session.
func GetSession(dataDir, sessionID string) (*Session, error) {
	path, err := cleanSessionPath(dataDir, sessionID)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}

	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("decode session file: %w", err)
	}
	return &sess, nil
}

// DeleteSession deletes a session file from disk.
func DeleteSession(dataDir, sessionID string) error {
	path, err := cleanSessionPath(dataDir, sessionID)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// UpdateTitle updates the title of a session.
func UpdateTitle(dataDir, sessionID, title string) error {
	path, err := cleanSessionPath(dataDir, sessionID)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}

	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return fmt.Errorf("unmarshal session: %w", err)
	}

	sess.Title = strings.TrimSpace(title)
	sess.UpdatedAt = time.Now()

	if err := saveSession(path, &sess); err != nil {
		return fmt.Errorf("save title update: %w", err)
	}
	return nil
}

// UpdateLastExtractedIdx updates the last extracted index of a session.
func UpdateLastExtractedIdx(dataDir, sessionID string, idx int) error {
	path, err := cleanSessionPath(dataDir, sessionID)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}

	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return fmt.Errorf("unmarshal session: %w", err)
	}

	sess.LastExtractedIdx = idx
	sess.UpdatedAt = time.Now()

	if err := saveSession(path, &sess); err != nil {
		return fmt.Errorf("save session index update: %w", err)
	}
	return nil
}

