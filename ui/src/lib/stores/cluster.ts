import { writable, derived } from 'svelte/store';
import { loadTunnelStatus, tunnelStatus } from './tunnel';
import { loadWOLDevices } from './wol';

export interface NodeInfo {
  id: string;
  name: string;
  status: 'online' | 'offline';
  device_type: 'laptop' | 'desktop' | 'phone' | 'pi' | 'unknown';
  os: string;
  arch: string;
  cpu_percent: number;
  ram_used: number;
  ram_total: number;
  storage_used: number;
  storage_total: number;
  addresses: string[];
  last_seen: string;
  joined_at: string;
  gpu?: {
    available: boolean;
    name: string;
    vram: number;
    vram_free: number;
    driver: string;
    backend: string;
    generator: string;
  };
}

export interface ClusterSummary {
  node_count: number;
  offline_count: number;
  total_ram: number;
  used_ram: number;
  total_storage: number;
  used_storage: number;
  total_vram: number;
  free_vram: number;
  gpu_node_count: number;
}

export interface FileInfo {
  name: string;
  path: string;
  size: number;
  node_id: string;
  sync_status: string;
  mod_time: string;
}

export interface Task {
  id: string;
  command: string;
  status: 'pending' | 'running' | 'completed' | 'failed' | 'cancelled';
  assigned_node: string;
  output: string;
  error?: string;
  submitted_at: string;
  started_at?: string;
  finished_at?: string;
}

// Core stores
export const nodes = writable<NodeInfo[]>([]);
export const summary = writable<ClusterSummary>({
  node_count: 0, offline_count: 0,
  total_ram: 0, used_ram: 0,
  total_storage: 0, used_storage: 0,
  total_vram: 0, free_vram: 0,
  gpu_node_count: 0
});
export const files = writable<FileInfo[]>([]);
export const tasks = writable<Task[]>([]);
export const connected = writable(false);
export const sseError = writable<string | null>(null);

export interface AppConfig {
  project_name: string;
  project_domain: string;
}

export const appConfig = writable<AppConfig>({
  project_name: 'OpenFabric',
  project_domain: 'openfabric.dev'
});

// Derived
export const onlineNodes = derived(nodes, $nodes => $nodes.filter(n => n.status === 'online'));
export const totalRAMGB = derived(summary, $s => ($s.total_ram / (1024 ** 3)).toFixed(1));
export const usedRAMPercent = derived(summary, $s =>
  $s.total_ram > 0 ? Math.round(($s.used_ram / $s.total_ram) * 100) : 0
);

// --- API helpers ---

const BASE = '/api';

async function fetchJSON<T>(url: string): Promise<T> {
  const res = await fetch(BASE + url);
  if (!res.ok) throw new Error(`HTTP ${res.status}: ${res.statusText}`);
  return res.json();
}

export async function loadConfig() {
  try {
    const cfg = await fetchJSON<AppConfig>('/config');
    appConfig.set(cfg);
  } catch (e) {
    console.error('loadConfig failed', e);
  }
}

export async function loadAll() {
  try {
    const [n, s, f, t] = await Promise.all([
      fetchJSON<NodeInfo[]>('/nodes'),
      fetchJSON<ClusterSummary>('/status'),
      fetchJSON<FileInfo[]>('/storage'),
      fetchJSON<Task[]>('/tasks'),
    ]);
    nodes.set(n ?? []);
    summary.set(s);
    files.set(f ?? []);
    tasks.set(t ?? []);
    loadConfig();
    loadWOLDevices();
  } catch (e) {
    console.error('loadAll failed', e);
  }
}

export async function removeNode(id: string) {
  await fetch(`${BASE}/nodes/${id}`, { method: 'DELETE' });
  nodes.update(ns => ns.filter(n => n.id !== id));
}

export async function uploadFile(file: File) {
  const form = new FormData();
  form.append('file', file);
  const res = await fetch(`${BASE}/storage/upload`, { method: 'POST', body: form });
  if (!res.ok) throw new Error('Upload failed');
  const info: FileInfo = await res.json();
  files.update(fs => [...fs, info]);
  return info;
}

export async function deleteFile(path: string) {
  const res = await fetch(`${BASE}/storage/${encodeURIComponent(path)}`, { method: 'DELETE' });
  if (!res.ok) {
    let errMsg = 'Failed to delete file';
    try {
      const errJson = await res.json();
      if (errJson && errJson.error) {
        errMsg = errJson.error;
      }
    } catch (_) {}
    throw new Error(errMsg);
  }
  files.update(fs => fs.filter(f => f.path !== path));
}

export async function submitTask(command: string, preferredNode?: string) {
  const res = await fetch(`${BASE}/tasks`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ command, preferred_node: preferredNode ?? '' })
  });
  if (!res.ok) {
    let errMsg = 'Task submission failed';
    try {
      const errJson = await res.json();
      if (errJson && errJson.error) {
        errMsg = errJson.error;
      }
    } catch (_) {}
    throw new Error(errMsg);
  }
  const task: Task = await res.json();
  // Only insert if the task isn't already in the store. For fast-completing
  // commands the SSE task_updated events can arrive before this HTTP
  // response resolves, so we must not downgrade a newer status back to
  // "pending".
  tasks.update(ts => {
    if (ts.some(t => t.id === task.id)) return ts;
    return [task, ...ts];
  });
  return task;
}

export async function cancelTask(id: string) {
  await fetch(`${BASE}/tasks/${id}`, { method: 'DELETE' });
  tasks.update(ts => ts.map(t => t.id === id ? { ...t, status: 'cancelled' as const } : t));
}

export interface PolicyRule {
  metric: 'cpu_percent' | 'ram_used_percent' | 'gpu_used_percent' | 'tasks_running' | 'throughput' | 'tokens_sec';
  scope: 'cluster' | 'node';
  operator: 'gt' | 'lt' | 'gte' | 'lte';
  value: number;
}

export interface Policy {
  id: string;
  name: string;
  enabled: boolean;
  rules: PolicyRule[];
  action: 'block' | 'backpressure';
  target_class: string;
  message: string;
}

export interface Settings {
  cluster_name: string;
  device_name: string;
  api_port: number;
  auto_start: boolean;
  storage_sync_enabled: boolean;
  accept_tasks: boolean;
  network_access: string;
  memory_enabled: boolean;
  memory_auto_extract: boolean;
  sandbox_mode: boolean;
  allowed_commands: string[];
  task_timeout: number;
  image_gen_url: string;
  wol_memory_threshold: number;
  policies: Policy[];
}

export const settings = writable<Settings | null>(null);

export async function loadSettings() {
  const s = await fetchJSON<Settings>('/settings');
  settings.set(s);
}

export async function saveSettings(s: Settings) {
  const res = await fetch(`${BASE}/settings`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(s)
  });
  const updated = await res.json();
  settings.set(updated);
}

// --- SSE connection ---

export function connectSSE() {
  const es = new EventSource('/api/events');

  es.addEventListener('connected', () => {
    connected.set(true);
    sseError.set(null);
    loadAll();
    loadTunnelStatus();
  });

  es.addEventListener('tunnel_state_changed', (e) => {
    try {
      const data = JSON.parse((e as MessageEvent).data);
      if (data.state) {
        tunnelStatus.update(s => ({ ...s, state: data.state, error: data.error }));
      }
    } catch (err) {
      console.error('failed to parse tunnel_state_changed event', err);
    }
    loadTunnelStatus();
  });

  es.addEventListener('node_joined', (e) => {
    const node: NodeInfo = JSON.parse((e as MessageEvent).data);
    nodes.update(ns => {
      const idx = ns.findIndex(n => n.id === node.id);
      if (idx >= 0) { const copy = [...ns]; copy[idx] = node; return copy; }
      return [...ns, node];
    });
    refreshSummary();
  });

  es.addEventListener('node_updated', (e) => {
    const node: NodeInfo = JSON.parse((e as MessageEvent).data);
    nodes.update(ns => ns.map(n => n.id === node.id ? node : n));
    refreshSummary();
  });

  es.addEventListener('node_offline', (e) => {
    const node: NodeInfo = JSON.parse((e as MessageEvent).data);
    nodes.update(ns => ns.map(n => n.id === node.id ? { ...n, status: 'offline' } : n));
    refreshSummary();
  });

  es.addEventListener('node_left', (e) => {
    const node: NodeInfo = JSON.parse((e as MessageEvent).data);
    nodes.update(ns => ns.filter(n => n.id !== node.id));
    refreshSummary();
  });

  es.addEventListener('task_submitted', (e) => {
    const task: Task = JSON.parse((e as MessageEvent).data);
    // Guard: task_updated events for fast commands can arrive before
    // task_submitted. If the task is already in the store (with a newer
    // status), don't overwrite it.
    tasks.update(ts => {
      if (ts.some(t => t.id === task.id)) return ts;
      return [task, ...ts];
    });
  });

  es.addEventListener('task_updated', (e) => {
    const task: Task = JSON.parse((e as MessageEvent).data);
    tasks.update(ts => {
      const idx = ts.findIndex(t => t.id === task.id);
      if (idx >= 0) {
        const copy = [...ts];
        copy[idx] = task;
        return copy;
      }
      return [task, ...ts];
    });
  });

  es.addEventListener('storage_updated', () => {
    fetchJSON<FileInfo[]>('/storage').then(f => files.set(f ?? [])).catch(() => {});
  });

  es.addEventListener('wol_device_registered', () => {
    loadWOLDevices();
  });

  es.addEventListener('wol_device_unregistered', () => {
    loadWOLDevices();
  });

  es.addEventListener('wol_device_woken', () => {
    loadWOLDevices();
  });

  es.onerror = () => {
    connected.set(false);
    sseError.set('Lost connection to agent. Reconnecting…');
  };

  return () => es.close();
}

async function refreshSummary() {
  try {
    const s = await fetchJSON<ClusterSummary>('/status');
    summary.set(s);
  } catch {}
}

// Byte formatting helper (exported for components)
export function formatBytes(bytes: number, decimals = 1): string {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(decimals)) + ' ' + sizes[i];
}

export function timeAgo(dateStr: string): string {
  const diff = Date.now() - new Date(dateStr).getTime();
  const s = Math.floor(diff / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  return `${h}h ago`;
}
