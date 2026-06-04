package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
)

// InactivityThreshold defines how long a session must be idle before fact extraction runs.
// Exported to allow tests to override it for instant verification.
var InactivityThreshold = 5 * time.Minute

// ---- Fix 4.4: sensitive-data redaction patterns ----------------------------

// redactionRules maps a human-readable label to the compiled regex and its replacement.
type redactionRule struct {
	label       string
	re          *regexp.Regexp
	replacement string
}

var sensitivePatterns = []redactionRule{
	// API keys and bearer tokens
	{
		label:       "api_key_sk",
		re:          regexp.MustCompile(`(?i)(sk-[A-Za-z0-9]{16,})`),
		replacement: "[API_KEY_REDACTED]",
	},
	{
		label:       "bearer_token",
		re:          regexp.MustCompile(`(?i)(Bearer\s+[A-Za-z0-9._\-]{8,})`),
		replacement: "[BEARER_TOKEN_REDACTED]",
	},
	// Generic token/key/password assignments (e.g. token=abc123, password=secret)
	{
		label:       "kv_secret",
		re:          regexp.MustCompile(`(?i)((?:password|passwd|secret|token|api[_-]?key|access[_-]?key|auth[_-]?key)\s*[=:]\s*\S{4,})`),
		replacement: "[SECRET_REDACTED]",
	},
	// SSH / PEM private keys
	{
		label:       "ssh_key",
		re:          regexp.MustCompile(`-----BEGIN[A-Z ]+PRIVATE KEY-----`),
		replacement: "[SSH_KEY_REDACTED]",
	},
	// Private IPv4 ranges: 10.x.x.x, 172.16-31.x.x, 192.168.x.x
	{
		label: "private_ip",
		re: regexp.MustCompile(
			`\b(10\.\d{1,3}\.\d{1,3}\.\d{1,3}` +
				`|172\.(1[6-9]|2[0-9]|3[01])\.\d{1,3}\.\d{1,3}` +
				`|192\.168\.\d{1,3}\.\d{1,3})\b`,
		),
		replacement: "[PRIVATE_IP_REDACTED]",
	},
}

// redactSensitiveData removes patterns that should never be stored as memories or
// sent verbatim to an external LLM for fact extraction (Fix 4.4).
func redactSensitiveData(text string) string {
	for _, rule := range sensitivePatterns {
		text = rule.re.ReplaceAllString(text, rule.replacement)
	}
	return text
}

// ----------------------------------------------------------------------------

// RecordActivity records a user interaction on a chat session to update the inactivity timer.
func (m *Manager) RecordActivity(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.activities[sessionID] = &SessionActivity{
		SessionID:    sessionID,
		LastActivity: time.Now(),
		Extracted:    false,
	}
}

// StartInactivityMonitor starts a background loop checking for idle chat sessions.
func (m *Manager) StartInactivityMonitor(ctx context.Context, client LLMClient, log *zap.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkInactiveSessions(ctx, client, log)
		}
	}
}

// checkInactiveSessions scans tracked session activities and triggers extraction for idle ones.
func (m *Manager) checkInactiveSessions(ctx context.Context, client LLMClient, log *zap.Logger) {
	m.mu.Lock()
	var sessionsToProcess []string
	now := time.Now()

	for sessID, act := range m.activities {
		if !act.Extracted && now.Sub(act.LastActivity) >= InactivityThreshold {
			sessionsToProcess = append(sessionsToProcess, sessID)
			// Fix 4.6: delete the entry after scheduling extraction so the map
			// never accumulates stale sessions for the process lifetime.
			delete(m.activities, sessID)
		}
	}
	m.mu.Unlock()

	for _, sessID := range sessionsToProcess {
		go func(id string) {
			log.Info("session idle threshold reached, starting background fact extraction", zap.String("session_id", id))
			if err := m.ExtractMemoriesFromSession(ctx, client, id, log); err != nil {
				log.Warn("background fact extraction failed", zap.String("session_id", id), zap.Error(err))
			} else {
				log.Info("background fact extraction completed successfully", zap.String("session_id", id))
			}
		}(sessID)
	}
}

// ExtractMemoriesFromSession pulls recent messages from a chat session and uses an LLM to extract factual memories.
func (m *Manager) ExtractMemoriesFromSession(ctx context.Context, client LLMClient, sessionID string, log *zap.Logger) error {
	// 1. Retrieve session messages
	messages, modelName, lastExtractedIdx, err := client.GetChatSessionMessages(sessionID)
	if err != nil {
		return fmt.Errorf("retrieve chat session messages: %w", err)
	}

	if len(messages) == 0 {
		return nil
	}

	if lastExtractedIdx > len(messages) {
		lastExtractedIdx = len(messages)
	}

	newMessages := messages[lastExtractedIdx:]
	if len(newMessages) < 3 {
		log.Debug("not enough new content to extract from", zap.String("session_id", sessionID), zap.Int("new_count", len(newMessages)))
		return nil // not enough new content to extract from
	}

	// Only send the new messages (max 10) to avoid token waste
	toProcess := newMessages
	if len(toProcess) > 10 {
		toProcess = toProcess[len(toProcess)-10:]
	}

	// Fix 4.4: redact sensitive patterns from each message before sending to the LLM.
	// This prevents API keys, passwords, private IPs, and SSH keys typed into the chat
	// from being permanently extracted as user memories.
	var convBuilder strings.Builder
	for _, msg := range toProcess {
		safeContent := redactSensitiveData(msg.Content)
		convBuilder.WriteString(fmt.Sprintf("%s: %s\n", msg.Role, safeContent))
	}

	// 3. Setup prompt
	systemPrompt := `You are a background fact extractor for a personal assistant.
Analyze the conversation messages and extract factual statements about the user.
Focus on:
1. User preferences (e.g. "prefers concise answers", "prefers bullet points").
2. User projects (e.g. "working on a Go project called OpenFabric").
3. Facts they mentioned about their environment (e.g. "uses Ubuntu 22.04").
4. Corrections they made (e.g. "wants to avoid the word 'straightforward'").

Return a JSON array of strings, where each string is a single factual memory.
Example: ["User is building a distributed system in Go", "User prefers bullet points over prose"]
Return an empty array [] if no new notable facts are found.
Do not invent facts or include assistant responses as user facts unless it clarifies a user preference.
Do not store IP addresses, passwords, tokens, or credentials.`

	ollamaMsgs := []ChatMessage{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("Here is the conversation history:\n\n%s", convBuilder.String()),
		},
	}

	// 4. Call LLM
	resp, err := client.ChatNoStream(ctx, modelName, ollamaMsgs, true)
	if err != nil {
		return fmt.Errorf("ollama extraction call failed: %w", err)
	}

	// 5. Parse JSON output
	var facts []string
	if err := json.Unmarshal([]byte(resp), &facts); err != nil {
		log.Debug("failed to parse memory extraction JSON response", zap.String("response", resp), zap.Error(err))
		return fmt.Errorf("parse extracted facts JSON: %w", err)
	}

	// 6. Store extracted memories - apply a second redaction pass to LLM output
	// in case the model echoed back sensitive data despite the prompt instructions.
	count := 0
	for _, fact := range facts {
		fact = strings.TrimSpace(redactSensitiveData(fact))
		if fact == "" {
			continue
		}
		_, err := m.AddMemory(ctx, fact, "chat_session", sessionID, []string{"chat-extract"})
		if err != nil {
			log.Warn("failed to add extracted memory", zap.String("fact", fact), zap.Error(err))
			continue
		}
		count++
	}

	// Update last extracted index in the session
	newIdx := lastExtractedIdx + len(newMessages)
	if err := client.UpdateChatSessionLastExtractedIdx(sessionID, newIdx); err != nil {
		log.Warn("failed to update session last extracted index", zap.String("session_id", sessionID), zap.Error(err))
	}

	log.Info("successfully processed extracted facts", zap.Int("extracted", len(facts)), zap.Int("stored_or_updated", count))
	return nil
}
