// llm.ts - reactive store and API helpers for the LLM inference feature.
import { writable, derived } from 'svelte/store';

// ---- Types ----------------------------------------------------------------

export interface Shard {
  node_id: string;
  node_name: string;
  layer_start: number;
  layer_end: number;
  assigned_ram: number;
}

export interface ShardPlan {
  coordinator: string;
  shards: Shard[];
  model: {
    name: string;
    total_layers: number;
    ram_per_layer: number;
    min_node_ram: number;
    quantization: string;
    ollama_tag: string;
    description: string;
    head_count?: number;
    embed_length?: number;
  };
}

export interface ModelFeasibility {
  model: string;
  description: string;
  quantization: string;
  required_ram: number;
  cluster_ram: number;
  can_run: boolean;
  shard_plan?: ShardPlan;
  fits_single_node: boolean;
  single_node_id?: string;
  is_downloaded: boolean;
  ollama_ready: boolean;
  downloaded_tags?: string[];
}

export interface LLMStatus {
  ollama_ready: boolean;
  local_models: string[];
  active_sessions: number;
}

export interface ChatMessage {
  role: 'user' | 'assistant' | 'system';
  content: string;
}

export interface PullProgressEvent {
  model: string;
  status: string;
  completed: number;
  total: number;
}

// ---- Stores ---------------------------------------------------------------

export const models = writable<ModelFeasibility[]>([]);
export const llmStatus = writable<LLMStatus | null>(null);
export const llmLoading = writable(false);
export const llmError = writable<string | null>(null);

// Derived helpers
export const runableModels = derived(models, $m => $m.filter(m => m.can_run));
export const unrunableModels = derived(models, $m => $m.filter(m => !m.can_run));
export const ollamaReady = derived(llmStatus, $s => $s?.ollama_ready ?? false);

// ---- API helpers ----------------------------------------------------------

const BASE = '/api/llm';

export async function fetchModels(): Promise<void> {
  llmLoading.set(true);
  llmError.set(null);
  try {
    const res = await fetch(`${BASE}/models`);
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data: ModelFeasibility[] = await res.json();
    models.set(data ?? []);
  } catch (e: any) {
    llmError.set(e.message ?? 'Failed to load models');
  } finally {
    llmLoading.set(false);
  }
}

export async function fetchLLMStatus(): Promise<void> {
  try {
    const res = await fetch(`${BASE}/status`);
    if (!res.ok) return;
    const data: LLMStatus = await res.json();
    llmStatus.set(data);
  } catch { }
}

/**
 * Pull a model to the local Ollama instance.
 * Calls onProgress with each SSE progress event; resolves when done.
 */
export async function pullModel(
  model: string,
  onProgress: (p: PullProgressEvent) => void
): Promise<void> {
  const res = await fetch(`${BASE}/pull`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model })
  });
  if (!res.ok || !res.body) throw new Error(`Pull failed: HTTP ${res.status}`);

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });

    // Parse SSE chunks
    const parts = buf.split('\n\n');
    buf = parts.pop() ?? '';
    for (const part of parts) {
      const dataLine = part.split('\n').find(l => l.startsWith('data: '));
      if (!dataLine) continue;
      try {
        const payload = JSON.parse(dataLine.slice(6));
        onProgress(payload as PullProgressEvent);
      } catch { }
    }
  }
}

/**
 * Delete a downloaded model from the local Ollama instance.
 * Model names (e.g. llama3.2:3b) are URL-encoded before embedding in the path.
 */
export async function deleteModel(model: string): Promise<void> {
  const res = await fetch(`${BASE}/models/${encodeURIComponent(model)}`, {
    method: 'DELETE'
  });
  if (!res.ok && res.status !== 204) {
    const body = await res.json().catch(() => ({ error: `HTTP ${res.status}` }));
    throw new Error(body.error ?? `Delete failed: HTTP ${res.status}`);
  }
}

export interface MCPServerStatus {
  name: string;
  enabled: boolean;
  running: boolean;
  tool_count: number;
  last_error?: string;
  started_at?: string;
}

export interface NamespacedTool {
  server_name: string;
  full_name: string;
  tool: {
    name: string;
    description: string;
    inputSchema: any;
  };
}

/**
 * Start a streaming chat session.
 * Calls onToken for each token; calls onDone when generation completes.
 * Returns a controller whose abort() cancels the session.
 */
export function chatStream(
  model: string,
  messages: ChatMessage[],
  useBrain: boolean,
  brainTopK: number,
  mcpServers: string[] | undefined,
  onBrainContext: (context: any[]) => void,
  onToken: (token: string, tokSec: number, shards: Shard[]) => void,
  onToolCall: (server: string, tool: string, args: any) => void,
  onToolResult: (tool: string, result: string) => void,
  onDone: (tokSec: number) => void,
  onError: (err: string) => void,
  onInferenceInfo?: (message: string) => void
): AbortController {
  const controller = new AbortController();

  (async () => {
    try {
      const res = await fetch(`${BASE}/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          model,
          messages,
          stream: true,
          use_brain: useBrain,
          brain_top_k: brainTopK,
          mcp_servers: mcpServers
        }),
        signal: controller.signal
      });
      if (!res.ok || !res.body) throw new Error(`HTTP ${res.status}`);

      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buf = '';
      let lastTokSec = 0;

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });

        const parts = buf.split('\n\n');
        buf = parts.pop() ?? '';

        for (const part of parts) {
          const lines = part.split('\n');
          const eventLine = lines.find(l => l.startsWith('event: '));
          const dataLine = lines.find(l => l.startsWith('data: '));
          if (!eventLine || !dataLine) continue;

          const event = eventLine.slice(7).trim();
          let payload: any;
          try { payload = JSON.parse(dataLine.slice(6)); } catch { continue; }

          if (event === 'llm_token') {
            lastTokSec = payload.tok_sec ?? 0;
            onToken(payload.token ?? '', lastTokSec, payload.shards ?? []);
          } else if (event === 'llm_brain_context') {
            onBrainContext(payload.context ?? []);
          } else if (event === 'llm_tool_call') {
            onToolCall(payload.server ?? '', payload.tool ?? '', payload.args ?? {});
          } else if (event === 'llm_tool_result') {
            onToolResult(payload.tool ?? '', payload.result ?? '');
          } else if (event === 'inference_info') {
            if (onInferenceInfo) onInferenceInfo(payload.message ?? '');
          } else if (event === 'llm_done') {
            onDone(lastTokSec);
          } else if (event === 'llm_error') {
            onError(payload.error ?? 'Inference error');
          }
        }
      }
    } catch (e: any) {
      if (e.name !== 'AbortError') {
        onError(e.message ?? 'Connection error');
      }
    }
  })();

  return controller;
}

export async function fetchMCPServers(): Promise<MCPServerStatus[]> {
  const res = await fetch('/api/mcp/servers');
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

export async function fetchAllMCPTools(): Promise<NamespacedTool[]> {
  const res = await fetch('/api/mcp/servers/tools/all');
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

export async function fetchMCPBuiltins(): Promise<any[]> {
  const res = await fetch('/api/mcp/builtins');
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

/** Format bytes → human-readable (shared helper). */
export function fmtRAM(bytes: number): string {
  const gb = 1024 ** 3;
  const mb = 1024 ** 2;
  if (bytes >= gb) return (bytes / gb).toFixed(1) + ' GB';
  return Math.round(bytes / mb) + ' MB';
}
