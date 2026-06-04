package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// ServerConfig is loaded from ~/.openfabric/mcp/<name>.json
type ServerConfig struct {
	Name           string            `json:"name"`
	Command        string            `json:"command"`
	Env            map[string]string `json:"env"`
	Enabled        bool              `json:"enabled"`
	CredentialKeys []string          `json:"credential_keys,omitempty"` // keys that are stored in the keychain
	Tools          []ToolDef         `json:"tools,omitempty"`           // cached tools
}

// ToolDef mirrors the MCP tool definition object.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

// ServerStatus is returned by the status endpoint.
type ServerStatus struct {
	Name      string            `json:"name"`
	Enabled   bool              `json:"enabled"`
	Running   bool              `json:"running"`
	ToolCount int               `json:"tool_count"`
	LastError string            `json:"last_error,omitempty"`
	StartedAt time.Time         `json:"started_at,omitempty"`
	Env       map[string]string `json:"env,omitempty"` // masked env vars for UI pre-population
}

// NamespacedTool represents a tool prefixed with the server name.
type NamespacedTool struct {
	ServerName string  `json:"server_name"`
	FullName   string  `json:"full_name"` // server__tool
	Tool       ToolDef `json:"tool"`
}

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int64 `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type mcpServerInstance struct {
	name      string
	command   string
	env       map[string]string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	pending   map[int64]chan *jsonRPCResponse
	mu        sync.Mutex
	nextID    int64
	done      chan struct{}
	lastError string
	startedAt time.Time
	log       *zap.Logger
}

func newInstance(name, command string, env map[string]string, log *zap.Logger) *mcpServerInstance {
	return &mcpServerInstance{
		name:    name,
		command: command,
		env:     env,
		pending: make(map[int64]chan *jsonRPCResponse),
		done:    make(chan struct{}),
		log:     log,
	}
}

func (inst *mcpServerInstance) start(ctx context.Context) error {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	parts := strings.Fields(inst.command)
	if len(parts) == 0 {
		return fmt.Errorf("empty command")
	}

	cmdName := parts[0]
	cmdArgs := parts[1:]

	cmd := exec.CommandContext(ctx, cmdName, cmdArgs...)
	
	// Prepare environment
	env := os.Environ()
	for k, v := range inst.env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	// We pipe stderr to logger
	stderr, err := cmd.StderrPipe()
	if err == nil {
		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				inst.log.Debug("MCP server stderr", zap.String("server", inst.name), zap.String("line", scanner.Text()))
			}
		}()
	}

	if err := cmd.Start(); err != nil {
		inst.lastError = err.Error()
		return fmt.Errorf("start: %w", err)
	}

	inst.cmd = cmd
	inst.stdin = stdin
	inst.stdout = stdout
	inst.startedAt = time.Now()
	inst.lastError = ""
	inst.done = make(chan struct{})

	go inst.readLoop()
	go inst.waitLoop()

	// Perform initialize handshake
	go func() {
		err := inst.handshake()
		if err != nil {
			inst.log.Error("handshake failed", zap.String("server", inst.name), zap.Error(err))
			inst.stop()
		}
	}()

	return nil
}

func (inst *mcpServerInstance) waitLoop() {
	err := inst.cmd.Wait()
	inst.mu.Lock()
	if err != nil {
		inst.lastError = err.Error()
		inst.log.Warn("MCP server process exited with error", zap.String("server", inst.name), zap.Error(err))
	} else {
		inst.log.Info("MCP server process exited normally", zap.String("server", inst.name))
	}
	close(inst.done)
	inst.mu.Unlock()
}

func (inst *mcpServerInstance) readLoop() {
	dec := json.NewDecoder(inst.stdout)
	for {
		var resp jsonRPCResponse
		if err := dec.Decode(&resp); err != nil {
			if !errors.Is(err, io.EOF) {
				inst.log.Debug("MCP readLoop decode error", zap.String("server", inst.name), zap.Error(err))
			}
			return
		}

		if resp.ID != nil {
			id := *resp.ID
			inst.mu.Lock()
			ch, ok := inst.pending[id]
			if ok {
				delete(inst.pending, id)
				ch <- &resp
			}
			inst.mu.Unlock()
		}
	}
}

func (inst *mcpServerInstance) handshake() error {
	// 1. Send initialize request
	initParams := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "openfabric",
			"version": "1.0",
		},
	}

	_, err := inst.call("initialize", initParams, 10*time.Second)
	if err != nil {
		return fmt.Errorf("initialize request: %w", err)
	}

	// 2. Send initialized notification (no response expected)
	notif := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	
	data, err := json.Marshal(notif)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.stdin == nil {
		return fmt.Errorf("stdin closed")
	}

	_, err = inst.stdin.Write(append(data, '\n'))
	if err != nil {
		return fmt.Errorf("write notification: %w", err)
	}

	inst.log.Info("MCP handshake completed successfully", zap.String("server", inst.name))
	return nil
}

func (inst *mcpServerInstance) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	id := atomic.AddInt64(&inst.nextID, 1)

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ch := make(chan *jsonRPCResponse, 1)

	inst.mu.Lock()
	if inst.stdin == nil {
		inst.mu.Unlock()
		return nil, fmt.Errorf("server not running")
	}
	inst.pending[id] = ch
	stdin := inst.stdin
	inst.mu.Unlock()

	// Write request
	_, err = stdin.Write(append(data, '\n'))
	if err != nil {
		inst.mu.Lock()
		delete(inst.pending, id)
		inst.mu.Unlock()
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Wait for response or timeout
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("JSON-RPC error (code: %d): %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-time.After(timeout):
		inst.mu.Lock()
		delete(inst.pending, id)
		inst.mu.Unlock()
		return nil, fmt.Errorf("timeout waiting for response after %s", timeout)
	case <-inst.done:
		return nil, fmt.Errorf("server terminated while waiting for response")
	}
}

func (inst *mcpServerInstance) stop() {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	if inst.stdin != nil {
		inst.stdin.Close()
		inst.stdin = nil
	}
	if inst.stdout != nil {
		inst.stdout.Close()
		inst.stdout = nil
	}
	if inst.cmd != nil && inst.cmd.Process != nil {
		_ = inst.cmd.Process.Kill()
	}
}

func (inst *mcpServerInstance) isRunning() bool {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.cmd == nil || inst.cmd.Process == nil {
		return false
	}
	select {
	case <-inst.done:
		return false
	default:
		return true
	}
}

// Gateway manages all MCP server instances.
type Gateway struct {
	dataDir   string
	configs   map[string]*ServerConfig
	instances map[string]*mcpServerInstance
	mu        sync.RWMutex
	log       *zap.Logger
}

// New creates a new Gateway instance and loads server configs.
func New(dataDir string, log *zap.Logger) (*Gateway, error) {
	mcpDir := filepath.Join(dataDir, "mcp")
	if err := os.MkdirAll(mcpDir, 0700); err != nil {
		return nil, fmt.Errorf("create mcp directory: %w", err)
	}

	g := &Gateway{
		dataDir:   mcpDir,
		configs:   make(map[string]*ServerConfig),
		instances: make(map[string]*mcpServerInstance),
		log:       log,
	}

	if err := g.loadConfigs(); err != nil {
		return nil, err
	}

	return g, nil
}

func (g *Gateway) loadConfigs() error {
	files, err := os.ReadDir(g.dataDir)
	if err != nil {
		return fmt.Errorf("read mcp configs: %w", err)
	}

	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}

		path := filepath.Join(g.dataDir, f.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			g.log.Error("read config file failed", zap.String("path", path), zap.Error(err))
			continue
		}

		var cfg ServerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			g.log.Error("unmarshal config failed", zap.String("path", path), zap.Error(err))
			continue
		}

		g.configs[cfg.Name] = &cfg
	}

	return nil
}

// Run starts enabled MCP servers and runs the watchdog.
func (g *Gateway) Run(ctx context.Context) {
	g.mu.Lock()
	for name, cfg := range g.configs {
		if cfg.Enabled {
			g.startInstanceLocked(ctx, name)
		}
	}
	g.mu.Unlock()

	// Watchdog loop
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			g.StopAll()
			return
		case <-ticker.C:
			g.watchdog(ctx)
		}
	}
}

func (g *Gateway) watchdog(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for name, cfg := range g.configs {
		if !cfg.Enabled {
			continue
		}

		inst, ok := g.instances[name]
		if !ok || !inst.isRunning() {
			g.log.Warn("MCP server not running or crashed, restarting...", zap.String("server", name))
			g.startInstanceLocked(ctx, name)
		}
	}
}

func (g *Gateway) startInstanceLocked(ctx context.Context, name string) {
	cfg, ok := g.configs[name]
	if !ok {
		return
	}

	// Clean up old instance if exists
	if old, exists := g.instances[name]; exists {
		old.stop()
		delete(g.instances, name)
	}

	// Load credentials from keychain/fallback and merge
	fullEnv := make(map[string]string)
	for k, v := range cfg.Env {
		fullEnv[k] = v
	}
	for _, key := range cfg.CredentialKeys {
		val, err := GetCredential(cfg.Name, key)
		if err != nil {
			g.log.Warn("failed to retrieve credential from keychain", zap.String("server", name), zap.String("key", key), zap.Error(err))
			continue
		}
		fullEnv[key] = val
	}

	inst := newInstance(name, cfg.Command, fullEnv, g.log)
	if err := inst.start(ctx); err != nil {
		g.log.Error("failed to start MCP server", zap.String("server", name), zap.Error(err))
	}
	g.instances[name] = inst
}

// StopAll stops all running MCP servers.
func (g *Gateway) StopAll() {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, inst := range g.instances {
		inst.stop()
	}
	g.instances = make(map[string]*mcpServerInstance)
}

// ListServers returns the status of all known servers.
func (g *Gateway) ListServers() []ServerStatus {
	g.mu.RLock()
	defer g.mu.RUnlock()

	statuses := make([]ServerStatus, 0, len(g.configs))

	// Include builtins that are not yet configured
	for _, builtin := range AllBuiltins() {
		cfg, configured := g.configs[builtin.Name]
		if !configured {
			statuses = append(statuses, ServerStatus{
				Name:    builtin.Name,
				Enabled: false,
				Running: false,
			})
			continue
		}

		inst, running := g.instances[builtin.Name]
		isRunning := running && inst.isRunning()
		
		toolCount := len(cfg.Tools)
		var lastErr string
		var startedAt time.Time
		if running {
			lastErr = inst.lastError
			startedAt = inst.startedAt
		}

		maskedEnv := make(map[string]string)
		for k, v := range cfg.Env {
			maskedEnv[k] = v
		}
		for _, key := range cfg.CredentialKeys {
			maskedEnv[key] = "********"
		}

		statuses = append(statuses, ServerStatus{
			Name:      cfg.Name,
			Enabled:   cfg.Enabled,
			Running:   isRunning,
			ToolCount: toolCount,
			LastError: lastErr,
			StartedAt: startedAt,
			Env:       maskedEnv,
		})
	}

	// Add custom configured servers
	for name, cfg := range g.configs {
		if _, isBuiltin := FindBuiltin(name); isBuiltin {
			continue
		}

		inst, running := g.instances[name]
		isRunning := running && inst.isRunning()
		
		toolCount := len(cfg.Tools)
		var lastErr string
		var startedAt time.Time
		if running {
			lastErr = inst.lastError
			startedAt = inst.startedAt
		}

		maskedEnv := make(map[string]string)
		for k, v := range cfg.Env {
			maskedEnv[k] = v
		}
		for _, key := range cfg.CredentialKeys {
			maskedEnv[key] = "********"
		}

		statuses = append(statuses, ServerStatus{
			Name:      cfg.Name,
			Enabled:   cfg.Enabled,
			Running:   isRunning,
			ToolCount: toolCount,
			LastError: lastErr,
			StartedAt: startedAt,
			Env:       maskedEnv,
		})
	}

	return statuses
}

// GetConfig returns the config for a server.
func (g *Gateway) GetConfig(name string) (*ServerConfig, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	cfg, ok := g.configs[name]
	return cfg, ok
}

// SaveConfig saves/updates a server config and restarts if enabled.
func (g *Gateway) SaveConfig(cfg ServerConfig) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 1. Identify which keys in cfg.Env are secrets.
	builtin, isBuiltin := FindBuiltin(cfg.Name)
	secretKeys := make(map[string]bool)
	if isBuiltin {
		for _, ev := range builtin.EnvVars {
			if ev.Secret {
				secretKeys[ev.Key] = true
			}
		}
	}

	// We also treat any existing CredentialKeys as secrets.
	for _, k := range cfg.CredentialKeys {
		secretKeys[k] = true
	}

	// 2. Extract secret values, store them in the keychain, and update CredentialKeys
	newCredKeys := make([]string, 0)
	diskEnv := make(map[string]string)

	for k, v := range cfg.Env {
		if secretKeys[k] {
			if v == "********" {
				// Keep existing credential, don't write anything to diskEnv
				newCredKeys = append(newCredKeys, k)
			} else if v != "" {
				// Store new credential in keychain
				if err := StoreCredential(cfg.Name, k, v); err != nil {
					g.log.Error("failed to store credential in keychain", zap.String("server", cfg.Name), zap.String("key", k), zap.Error(err))
				}
				newCredKeys = append(newCredKeys, k)
			}
		} else {
			diskEnv[k] = v
		}
	}
	cfg.CredentialKeys = newCredKeys

	// Create a copy of config to save on disk without secrets
	diskCfg := cfg
	diskCfg.Env = diskEnv

	g.configs[cfg.Name] = &cfg

	// Write to disk
	path := filepath.Join(g.dataDir, fmt.Sprintf("%s.json", cfg.Name))
	data, err := json.MarshalIndent(diskCfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	// Manage lifecycle
	if cfg.Enabled {
		g.startInstanceLocked(context.Background(), cfg.Name)
		// Fetch tools in background to populate config cache
		go func(name string) {
			_, _ = g.ListTools(context.Background(), name)
		}(cfg.Name)
	} else {
		if inst, exists := g.instances[cfg.Name]; exists {
			inst.stop()
			delete(g.instances, cfg.Name)
		}
	}

	return nil
}

// DeleteServer stops and deletes a server configuration.
func (g *Gateway) DeleteServer(name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	cfg, exists := g.configs[name]
	if exists {
		for _, key := range cfg.CredentialKeys {
			_ = DeleteCredential(name, key)
		}
	}

	if inst, exists := g.instances[name]; exists {
		inst.stop()
		delete(g.instances, name)
	}

	delete(g.configs, name)

	path := filepath.Join(g.dataDir, fmt.Sprintf("%s.json", name))
	_ = os.Remove(path)

	return nil
}

// ToggleServer enables or disables a server.
func (g *Gateway) ToggleServer(name string, enabled bool) error {
	g.mu.Lock()
	cfg, ok := g.configs[name]
	g.mu.Unlock()

	if !ok {
		// If not configured but is builtin, create a default enabled/disabled config
		builtin, isBuiltin := FindBuiltin(name)
		if !isBuiltin {
			return fmt.Errorf("server not found")
		}
		cfg = &ServerConfig{
			Name:    builtin.Name,
			Command: builtin.Command,
			Env:     make(map[string]string),
			Enabled: enabled,
		}
	} else {
		cfg.Enabled = enabled
	}

	return g.SaveConfig(*cfg)
}

// ListTools calls tools/list on the server and caches the result.
func (g *Gateway) ListTools(ctx context.Context, name string) ([]ToolDef, error) {
	g.mu.RLock()
	inst, running := g.instances[name]
	_, configured := g.configs[name]
	g.mu.RUnlock()

	if !configured {
		return nil, fmt.Errorf("server not configured")
	}

	if !running || !inst.isRunning() {
		return nil, fmt.Errorf("server not running")
	}

	// Call tools/list
	res, err := inst.call("tools/list", nil, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("list tools call: %w", err)
	}

	var listResp struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(res, &listResp); err != nil {
		return nil, fmt.Errorf("unmarshal tools response: %w", err)
	}

	// Cache tools in config
	g.mu.Lock()
	if currentCfg, exists := g.configs[name]; exists {
		currentCfg.Tools = listResp.Tools
		// Re-save config to cache
		path := filepath.Join(g.dataDir, fmt.Sprintf("%s.json", name))
		data, _ := json.MarshalIndent(*currentCfg, "", "  ")
		_ = os.WriteFile(path, data, 0600)
	}
	g.mu.Unlock()

	return listResp.Tools, nil
}

// CallTool calls a specific tool on a server.
func (g *Gateway) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (string, error) {
	g.mu.RLock()
	inst, running := g.instances[serverName]
	g.mu.RUnlock()

	if !running || !inst.isRunning() {
		return "", fmt.Errorf("server %s not running", serverName)
	}

	params := map[string]any{
		"name":      toolName,
		"arguments": args,
	}

	res, err := inst.call("tools/call", params, 30*time.Second)
	if err != nil {
		return "", fmt.Errorf("call tool %s on %s failed: %w", toolName, serverName, err)
	}

	// Unmarshal result per MCP spec
	// The result typically contains a "content" array: [{"type":"text", "text":"..."}]
	var callResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		} `json:"content"`
		IsError bool `json:"isError,omitempty"`
	}

	if err := json.Unmarshal(res, &callResult); err != nil {
		return "", fmt.Errorf("unmarshal call result: %w", err)
	}

	var texts []string
	for _, c := range callResult.Content {
		if c.Type == "text" {
			texts = append(texts, c.Text)
		}
	}

	resultText := strings.Join(texts, "\n")
	if callResult.IsError {
		return resultText, fmt.Errorf("tool execution error: %s", resultText)
	}

	return resultText, nil
}

// AllEnabledTools returns all namespaced tools across all running servers.
func (g *Gateway) AllEnabledTools() []NamespacedTool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var list []NamespacedTool
	for name, cfg := range g.configs {
		inst, running := g.instances[name]
		if !running || !inst.isRunning() {
			continue
		}

		for _, tool := range cfg.Tools {
			list = append(list, NamespacedTool{
				ServerName: name,
				FullName:   fmt.Sprintf("%s__%s", name, tool.Name),
				Tool:       tool,
			})
		}
	}

	return list
}

// TestServer does a quick start, list tools, and stop to verify configuration.
func (g *Gateway) TestServer(ctx context.Context, name string) ([]ToolDef, error) {
	g.mu.RLock()
	cfg, ok := g.configs[name]
	g.mu.RUnlock()

	if !ok {
		// If not configured, try to fetch builtin
		builtin, isBuiltin := FindBuiltin(name)
		if !isBuiltin {
			return nil, fmt.Errorf("server not found")
		}
		cfg = &ServerConfig{
			Name:    builtin.Name,
			Command: builtin.Command,
			Env:     make(map[string]string),
			Enabled: true,
		}
	}

	// Create temp instance
	testInst := newInstance(name, cfg.Command, cfg.Env, g.log)
	if err := testInst.start(ctx); err != nil {
		return nil, fmt.Errorf("start failed: %w", err)
	}
	defer testInst.stop()

	// Wait up to 5 seconds for it to start/handshake
	deadline := time.Now().Add(5 * time.Second)
	var err error
	var res json.RawMessage
	for time.Now().Before(deadline) {
		res, err = testInst.call("tools/list", nil, 2*time.Second)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if err != nil {
		return nil, fmt.Errorf("list tools check failed: %w", err)
	}

	var listResp struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(res, &listResp); err != nil {
		return nil, fmt.Errorf("invalid tools response: %w", err)
	}

	return listResp.Tools, nil
}
