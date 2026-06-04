package llm

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionCRUD(t *testing.T) {
	// Create a temporary data directory
	tmpDir, err := os.MkdirTemp("", "openfabric_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	model := "test-model:latest"

	// 1. Test CreateSession
	sess, err := CreateSession(tmpDir, model)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	if sess.ID == "" {
		t.Error("expected non-empty session ID")
	}
	if sess.Model != model {
		t.Errorf("expected model %q, got %q", model, sess.Model)
	}
	if sess.Title != "New Chat" {
		t.Errorf("expected default title %q, got %q", "New Chat", sess.Title)
	}

	// Verify file exists on disk
	filePath := filepath.Join(tmpDir, "chats", sess.ID+".json")
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatalf("expected session file to exist at %s, but it does not", filePath)
	}

	// 2. Test GetSession
	retrieved, err := GetSession(tmpDir, sess.ID)
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}
	if retrieved.ID != sess.ID || retrieved.Model != sess.Model {
		t.Errorf("retrieved session mismatch: %+v vs %+v", retrieved, sess)
	}

	// 3. Test AppendMessage (Auto-titling)
	userMsg := Message{
		Role:    "user",
		Content: "Hello world, this is a test prompt.",
		SentAt:  time.Now(),
	}

	err = AppendMessage(tmpDir, sess.ID, userMsg)
	if err != nil {
		t.Fatalf("failed to append message: %v", err)
	}

	retrieved, err = GetSession(tmpDir, sess.ID)
	if err != nil {
		t.Fatalf("failed to get session after append: %v", err)
	}

	if len(retrieved.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(retrieved.Messages))
	}
	if retrieved.Messages[0].Content != userMsg.Content {
		t.Errorf("expected message content %q, got %q", userMsg.Content, retrieved.Messages[0].Content)
	}
	// Title should be auto-generated to first 6 words of user message
	expectedTitle := "Hello world, this is a test"
	if retrieved.Title != expectedTitle {
		t.Errorf("expected auto-title %q, got %q", expectedTitle, retrieved.Title)
	}

	// Append assistant message
	assistantMsg := Message{
		Role:    "assistant",
		Content: "Sure, I can help you test.",
		SentAt:  time.Now(),
	}
	err = AppendMessage(tmpDir, sess.ID, assistantMsg)
	if err != nil {
		t.Fatalf("failed to append assistant message: %v", err)
	}

	retrieved, err = GetSession(tmpDir, sess.ID)
	if err != nil {
		t.Fatalf("failed to get session after assistant append: %v", err)
	}
	if len(retrieved.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(retrieved.Messages))
	}
	// Title should NOT change on subsequent messages
	if retrieved.Title != expectedTitle {
		t.Errorf("expected title to remain %q, but got %q", expectedTitle, retrieved.Title)
	}

	// 4. Test ListSessions
	// Create another session to test sorting and listing
	time.Sleep(10 * time.Millisecond) // ensure distinct UpdatedAt timestamps
	sess2, err := CreateSession(tmpDir, "model-2")
	if err != nil {
		t.Fatalf("failed to create second session: %v", err)
	}

	sessions, err := ListSessions(tmpDir)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}

	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
	// Sorter is descending: sess2 (more recent) should be first
	if sessions[0].ID != sess2.ID {
		t.Errorf("expected most recent session first (ID %s), got %s", sess2.ID, sessions[0].ID)
	}
	// Messages should be cleared/nil in ListSessions summaries to save bandwidth
	if sessions[0].Messages != nil || sessions[1].Messages != nil {
		t.Error("expected Messages field to be nil in session summaries")
	}

	// 5. Test UpdateTitle
	newTitle := "Manual Rename Title"
	err = UpdateTitle(tmpDir, sess.ID, newTitle)
	if err != nil {
		t.Fatalf("failed to update title: %v", err)
	}

	retrieved, err = GetSession(tmpDir, sess.ID)
	if err != nil {
		t.Fatalf("failed to get session after title update: %v", err)
	}
	if retrieved.Title != newTitle {
		t.Errorf("expected title %q, got %q", newTitle, retrieved.Title)
	}

	// 6. Test DeleteSession
	err = DeleteSession(tmpDir, sess.ID)
	if err != nil {
		t.Fatalf("failed to delete session: %v", err)
	}

	_, err = GetSession(tmpDir, sess.ID)
	if err == nil {
		t.Error("expected error retrieving deleted session, but got nil")
	}

	sessions, err = ListSessions(tmpDir)
	if err != nil {
		t.Fatalf("failed to list sessions after delete: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 session remaining, got %d", len(sessions))
	}
}

func TestCleanSessionPathPathTraversal(t *testing.T) {
	tmpDir := "/tmp/openfabric"

	// Secure checks
	badIDs := []string{
		"../relative",
		"sub/folder",
		"..\\winrelative",
		"",
	}

	for _, id := range badIDs {
		_, err := cleanSessionPath(tmpDir, id)
		if err == nil {
			t.Errorf("expected path traversal protection error for ID %q, but got nil", id)
		}
	}
}
