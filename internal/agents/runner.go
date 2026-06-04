package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

var builtinTools = []OllamaTool{
	{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        "web_search",
			Description: "Search the web for real-time information and articles.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "The search query",
					},
				},
				"required": []string{"query"},
			},
		},
	},
	{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        "web_fetch",
			Description: "Fetch the text content of a given web page URL.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "The URL of the webpage to fetch",
					},
				},
				"required": []string{"url"},
			},
		},
	},
	{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        "read_file",
			Description: "Read the file contents from the shared Fabric Storage path.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Relative file path in storage",
					},
				},
				"required": []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        "write_file",
			Description: "Write content to a file in the shared Fabric Storage path.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Relative file path in storage",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Content text to write",
					},
				},
				"required": []string{"path", "content"},
			},
		},
	},
	{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        "list_storage",
			Description: "List files and directories in the shared Fabric Storage.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Optional relative subdirectory path to list",
					},
				},
			},
		},
	},
	{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        "run_shell",
			Description: "Execute a shell command on the host node.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Shell command line to execute",
					},
				},
				"required": []string{"command"},
			},
		},
	},
	{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        "search_brain",
			Description: "Query the Brain semantic RAG index for matching document chunks.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Natural language query",
					},
				},
				"required": []string{"query"},
			},
		},
	},
	{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        "remember",
			Description: "Save a key fact to the Fabric Memory system for subsequent recalls.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"fact": map[string]any{
						"type":        "string",
						"description": "The fact text to remember",
					},
				},
				"required": []string{"fact"},
			},
		},
	},
	{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        "notify",
			Description: "Send a notification/alert message to the dashboard UI.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{
						"type":        "string",
						"description": "Notification message text",
					},
				},
				"required": []string{"message"},
			},
		},
	},
	{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        "list_cluster_nodes",
			Description: "List all online nodes in the cluster, including their hardware specs (CPU, RAM, GPU) and status, to decide where to spawn sub-agents.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	},
	{
		Type: "function",
		Function: OllamaToolFunc{
			Name:        "spawn_sub_agent",
			Description: "Spawn a sub-agent actor on a specific cluster node to perform a sub-task. Use this for complex goals that can be executed in parallel or need to run on a specific node (e.g. where files or GPUs are located).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"node_id": map[string]any{
						"type":        "string",
						"description": "Optional specific Node ID where the sub-agent should run. If empty, the orchestrator selects the best node.",
					},
					"goal": map[string]any{
						"type":        "string",
						"description": "The sub-task goal description for the sub-agent to accomplish.",
					},
					"tools": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "string",
						},
						"description": "List of tool names the sub-agent is allowed to use (e.g., ['read_file', 'write_file', 'run_shell']).",
					},
				},
				"required": []string{"goal", "tools"},
			},
		},
	},
}

// Ollama API structures
type OllamaTool struct {
	Type     string         `json:"type"` // "function"
	Function OllamaToolFunc `json:"function"`
}

type OllamaToolFunc struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type ChatMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Name      string     `json:"name,omitempty"`
}

type ToolCall struct {
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type OllamaChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Tools    []OllamaTool  `json:"tools,omitempty"`
}

type OllamaChatResponse struct {
	Model     string      `json:"model"`
	Message   ChatMessage `json:"message"`
	Done      bool        `json:"done"`
	Error     string      `json:"error,omitempty"`
}

// StartAgent launches the ReAct execution loop in a background goroutine.
func (m *Manager) StartAgent(id string) error {
	m.mu.Lock()
	a, ok := m.agents[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("agent not found")
	}

	if a.Status == "running" {
		m.mu.Unlock()
		return fmt.Errorf("agent is already running")
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancels[id] = cancel
	a.Status = "running"
	a.Steps = GenerateInitialSteps(a.Goal)
	m.saveAgent(a)
	m.mu.Unlock()

	m.emitEvent("agent_updated", a)

	go m.runLoop(ctx, id)

	return nil
}

func (m *Manager) runLoop(ctx context.Context, id string) {
	defer func() {
		m.mu.Lock()
		delete(m.cancels, id)
		m.mu.Unlock()
	}()

	m.mu.RLock()
	a := m.agents[id]
	m.mu.RUnlock()

	// 1. Determine model
	model := "llama3:8b"
	if m.llmMgr != nil {
		status := m.llmMgr.Status(ctx)
		for _, lm := range status.LocalModels {
			if !strings.Contains(strings.ToLower(lm), "embed") {
				model = lm
				break
			}
		}
	}
	m.log.Info("agent running with model", zap.String("agent_id", id), zap.String("model", model))

	// 2. Build system and user prompt history
	history := []ChatMessage{
		{
			Role: "system",
			Content: `You are an autonomous AI Agent executing a goal-oriented ReAct (Reasoning + Acting) loop.
Your objective is to complete the user's goal step-by-step.
For each step, you must choose one of the available functions/tools to gather information or make progress.
Check the outputs of your tool calls to reason about the next step.
If a tool fails, try a different parameter or approach.
When you have fully accomplished the goal, present the final output/report to the user without calling any more tools.
Be rigorous, structured, and complete.`,
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("Goal: %s", a.Goal),
		},
	}

	// 3. Collect active tools
	var tools []OllamaTool
	// Built-in tools
	for _, bt := range builtinTools {
		if contains(a.Tools, bt.Function.Name) {
			tools = append(tools, bt)
		}
	}
	// MCP tools
	if m.mcpGateway != nil {
		mcpTools := m.mcpGateway.AllEnabledTools()
		for _, nt := range mcpTools {
			if contains(a.Tools, nt.ServerName) {
				tools = append(tools, OllamaTool{
					Type: "function",
					Function: OllamaToolFunc{
						Name:        nt.FullName,
						Description: nt.Tool.Description,
						Parameters:  nt.Tool.InputSchema,
					},
				})
			}
		}
	}

	maxSteps := 50
	stepCount := 0

	for {
		stepCount++
		if stepCount > maxSteps {
			m.mu.Lock()
			a.Status = "failed"
			a.Error = fmt.Sprintf("exceeded maximum ReAct steps (%d)", maxSteps)
			a.CompletedAt = time.Now()
			m.saveAgent(a)
			m.mu.Unlock()
			m.emitEvent("agent_updated", a)
			return
		}

		// Check for cancellation
		select {
		case <-ctx.Done():
			m.mu.Lock()
			a.Status = "cancelled"
			a.CompletedAt = time.Now()
			m.saveAgent(a)
			m.mu.Unlock()
			m.emitEvent("agent_updated", a)
			return
		default:
		}

		// Prepare active step in UI
		m.mu.Lock()
		// If the step number exists, update it, else create new
		var curStep *Step
		for i := range a.Steps {
			if a.Steps[i].Number == stepCount {
				curStep = &a.Steps[i]
				break
			}
		}
		if curStep == nil {
			a.Steps = append(a.Steps, Step{
				Number: stepCount,
				Status: "running",
				Log:    "Thinking about the next action...",
			})
			curStep = &a.Steps[len(a.Steps)-1]
		} else {
			curStep.Status = "running"
			curStep.Log = "Thinking about the next action..."
		}
		m.saveAgent(a)
		m.mu.Unlock()
		m.emitEvent("agent_updated", a)

		// 4. Request Ollama
		respMessage, err := m.queryOllama(ctx, model, history, tools)
		if err != nil {
			m.mu.Lock()
			if ctx.Err() != nil {
				a.Status = "cancelled"
				if curStep != nil {
					curStep.Status = "failed"
					curStep.Log = "Execution cancelled"
				}
			} else {
				a.Status = "failed"
				a.Error = fmt.Sprintf("Ollama chat failed: %v", err)
				if curStep != nil {
					curStep.Status = "failed"
					curStep.Log = err.Error()
				}
			}
			a.CompletedAt = time.Now()
			m.saveAgent(a)
			m.mu.Unlock()
			m.emitEvent("agent_updated", a)
			return
		}

		// Check if Ollama returned any tool calls
		if len(respMessage.ToolCalls) > 0 {
			// Ollama wants to call tools. Executing them.
			history = append(history, respMessage)

			m.mu.Lock()
			// Update step with chosen tool details
			firstCall := respMessage.ToolCalls[0]
			curStep.Tool = firstCall.Function.Name
			curStep.Args = firstCall.Function.Arguments
			curStep.Log = fmt.Sprintf("Invoking tool %s...", firstCall.Function.Name)
			m.saveAgent(a)
			m.mu.Unlock()
			m.emitEvent("agent_updated", a)

			startTime := time.Now()
			toolResult, executeErr := m.ExecuteTool(ctx, id, firstCall.Function.Name, firstCall.Function.Arguments)
			elapsed := time.Since(startTime).Milliseconds()

			m.mu.Lock()
			if ctx.Err() != nil {
				a.Status = "cancelled"
				a.CompletedAt = time.Now()
				if curStep != nil {
					curStep.Status = "failed"
					curStep.Log = "Execution cancelled"
				}
				m.saveAgent(a)
				m.mu.Unlock()
				m.emitEvent("agent_updated", a)
				return
			}

			if executeErr != nil {
				curStep.Status = "failed"
				curStep.Result = fmt.Sprintf("Error: %v", executeErr)
				curStep.Log = fmt.Sprintf("Tool execution failed: %v", executeErr)
				toolResult = fmt.Sprintf("Tool execution failed: %v", executeErr)
			} else {
				curStep.Status = "completed"
				curStep.Result = toolResult
				// Truncate summary for step log
				summary := toolResult
				if len(summary) > 200 {
					summary = summary[:200] + "..."
				}
				curStep.Log = fmt.Sprintf("Tool executed successfully. Output summary: %s", summary)
			}
			curStep.ElapsedTimeMs = elapsed
			m.saveAgent(a)
			m.mu.Unlock()
			m.emitEvent("agent_updated", a)

			// Append tool outcome to history
			history = append(history, ChatMessage{
				Role:    "tool",
				Name:    firstCall.Function.Name,
				Content: toolResult,
			})

		} else {
			// No tool calls means the agent is done!
			m.mu.Lock()
			a.Status = "completed"
			a.Output = respMessage.Content
			a.CompletedAt = time.Now()
			curStep.Status = "completed"
			curStep.Tool = "final_answer"
			curStep.Log = "Goal accomplished. Formulated final response."
			m.saveAgent(a)
			m.mu.Unlock()
			m.emitEvent("agent_updated", a)
			return
		}
	}
}

func (m *Manager) queryOllama(ctx context.Context, model string, messages []ChatMessage, tools []OllamaTool) (ChatMessage, error) {
	reqBody := OllamaChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   false,
		Tools:    tools,
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return ChatMessage{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.OllamaURL, bytes.NewReader(data))
	if err != nil {
		return ChatMessage{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ChatMessage{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return ChatMessage{}, fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, string(body))
	}

	var chatResp OllamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return ChatMessage{}, err
	}

	if chatResp.Error != "" {
		return ChatMessage{}, fmt.Errorf("ollama error: %s", chatResp.Error)
	}

	return chatResp.Message, nil
}

func contains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}
