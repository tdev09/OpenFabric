<script lang="ts">
  import { onMount } from 'svelte';
  import ClusterRing from '$lib/components/ClusterRing.svelte';
  import NodeCard from '$lib/components/NodeCard.svelte';
  import TunnelStatusCard from '$lib/components/TunnelStatusCard.svelte';
  import { nodes, summary, totalRAMGB, usedRAMPercent, onlineNodes, formatBytes, appConfig } from '$lib/stores/cluster';

  interface Insight {
    id: string;
    title: string;
    body: string;
    priority: 'info' | 'warning' | 'action';
    actions?: { label: string; link: string }[];
    created_at: string;
  }

  let brainStatus: any = null;
  let insights: Insight[] = [];
  let gpuStatus: any = null;
  let showGpuWarning = false;
  let toasts: { id: string; title: string; body: string; priority: string }[] = [];

  let popoverEl: HTMLDivElement | undefined;
  let indicatorButtonEl: HTMLButtonElement | undefined;

  function handleWindowClick(event: MouseEvent) {
    if (!showGpuWarning) return;
    const target = event.target as Node;
    if (indicatorButtonEl && indicatorButtonEl.contains(target)) {
      return;
    }
    if (popoverEl && popoverEl.contains(target)) {
      return;
    }
    showGpuWarning = false;
  }

  async function loadGPUStatus() {
    try {
      const res = await fetch('/api/gpu/status');
      if (res.ok) gpuStatus = await res.json();
    } catch (e) { console.error('Failed to load GPU status', e); }
  }

  async function loadBrainStatus() {
    try {
      const res = await fetch('/api/brain/status');
      if (res.ok) brainStatus = await res.json();
    } catch (e) { console.error('Failed to load brain status', e); }
  }

  async function loadInsights() {
    try {
      const res = await fetch('/api/pulse/insights');
      if (res.ok) insights = await res.json();
    } catch (e) { console.error('Failed to load insights', e); }
  }

  async function dismissInsight(id: string) {
    try {
      const res = await fetch(`/api/pulse/insights/${encodeURIComponent(id)}/dismiss`, { method: 'POST' });
      if (res.ok) insights = insights.filter(i => i.id !== id);
    } catch (e) { console.error('Failed to dismiss insight', e); }
  }

  function showToast(ins: Insight) {
    if (ins.priority === 'info') return;
    const toast = { id: ins.id, title: ins.title, body: ins.body, priority: ins.priority };
    toasts = [...toasts, toast];
    setTimeout(() => { toasts = toasts.filter(t => t.id !== ins.id); }, 5000);
  }

  let telemetryHistory: any[] = [];

  async function loadTelemetryHistory() {
    try {
      const res = await fetch('/api/telemetry/history');
      if (res.ok) telemetryHistory = await res.json();
    } catch (e) { console.error('Failed to load telemetry history', e); }
  }

  function getLinePath(data: number[], width: number, height: number, maxVal: number): string {
    if (data.length < 2) return '';
    const points = data.map((val, i) => {
      const x = (i / (data.length - 1)) * width;
      const y = height - (val / (maxVal || 1)) * height;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    });
    return `M ${points.join(' L ')}`;
  }

  function getAreaPath(data: number[], width: number, height: number, maxVal: number): string {
    if (data.length < 2) return '';
    const points = data.map((val, i) => {
      const x = (i / (data.length - 1)) * width;
      const y = height - (val / (maxVal || 1)) * height;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    });
    return `M 0,${height} L ${points.join(' L ')} L ${width},${height} Z`;
  }

  onMount(() => {
    loadBrainStatus();
    loadInsights();
    loadGPUStatus();
    loadTelemetryHistory();

    const telemetryInterval = setInterval(loadTelemetryHistory, 5000);

    const es = new EventSource('/api/events');
    es.addEventListener('pulse_insight', (e: any) => {
      try {
        const ins = JSON.parse(e.data);
        if (!insights.some(i => i.id === ins.id)) { insights = [ins, ...insights]; showToast(ins); }
      } catch (err) { console.error('Failed to parse SSE pulse_insight', err); }
    });
    es.addEventListener('pulse_insight_dismissed', (e: any) => {
      try {
        const data = JSON.parse(e.data);
        insights = insights.filter(i => i.id !== data.id);
      } catch (err) { console.error('Failed to parse SSE pulse_insight_dismissed', err); }
    });
    return () => {
      es.close();
      clearInterval(telemetryInterval);
    };
  });
</script>

<svelte:head>
  <title>Dashboard - {$appConfig.project_name}</title>
  <meta name="description" content="{$appConfig.project_name} cluster dashboard - pooled compute across all your devices" />
</svelte:head>

<svelte:window on:click={handleWindowClick} />

<div class="dashboard animate-fade-in">

  <!-- ── Hero Stat Cards ── -->
  <div class="telemetry-deck">

    <!-- Pooled RAM -->
    <div class="stat-card accent-card">
      <div class="stat-icon-wrap accent-icon">
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <rect x="2" y="6" width="20" height="12" rx="2"/><path d="M6 6V4"/><path d="M10 6V4"/><path d="M14 6V4"/><path d="M18 6V4"/>
        </svg>
      </div>
      <div class="stat-content">
        <div class="stat-label">
          <span class="pulse-dot"></span>
          Pooled RAM
        </div>
        <div class="stat-value mono">{$totalRAMGB}<span class="stat-unit">GB</span></div>
        <div class="stat-sub">
          <span class="usage-bar-wrap">
            <span class="usage-bar" style="width:{$usedRAMPercent}%"></span>
          </span>
          {$usedRAMPercent}% in use
        </div>
      </div>
    </div>

    <!-- Online Devices -->
    <div class="stat-card">
      <div class="stat-icon-wrap blue-icon">
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <rect x="2" y="3" width="20" height="14" rx="2"/><path d="M8 21h8"/><path d="M12 17v4"/>
        </svg>
      </div>
      <div class="stat-content">
        <div class="stat-label">Online Devices</div>
        <div class="stat-value mono blue-val">{$summary.node_count}</div>
        <div class="stat-sub">
          {$summary.offline_count} peer{$summary.offline_count !== 1 ? 's' : ''} offline
        </div>
      </div>
    </div>

    <!-- Total Storage -->
    <div class="stat-card">
      <div class="stat-icon-wrap purple-icon">
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"/><path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"/>
        </svg>
      </div>
      <div class="stat-content">
        <div class="stat-label">Total Storage</div>
        <div class="stat-value mono">{formatBytes($summary.total_storage)}</div>
        <div class="stat-sub">{formatBytes($summary.used_storage)} synced</div>
      </div>
    </div>

    <!-- GPU VRAM -->
    <div class="stat-card gpu-card" class:has-warning={gpuStatus?.gpu_configuration_status === 'warning'}>
      <div class="stat-card-main">
        <div class="stat-icon-wrap pink-icon">
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <rect x="2" y="3" width="20" height="14" rx="2"/><path d="M6 21h12"/><path d="M12 17v4"/>
          </svg>
        </div>
        <div class="stat-content">
          <div class="stat-label">GPU VRAM</div>
          <div class="stat-value mono pink-val">
            {formatBytes($summary.total_vram)}
          </div>
          <div class="stat-sub">
            {#if $summary.gpu_node_count > 0}
              {$summary.gpu_node_count} GPU node{$summary.gpu_node_count !== 1 ? 's' : ''} active
            {:else}
              No GPUs active
            {/if}
          </div>
        </div>
      </div>
      {#if gpuStatus?.gpu_configuration_status === 'warning'}
        <button
          bind:this={indicatorButtonEl}
          class="gpu-alert-indicator"
          on:click={() => showGpuWarning = !showGpuWarning}
          aria-label="GPU acceleration limited warning"
          aria-expanded={showGpuWarning}
        >⚠️</button>
        {#if showGpuWarning}
          <!-- svelte-ignore a11y-click-events-have-key-events -->
          <!-- svelte-ignore a11y-no-static-element-interactions -->
          <div bind:this={popoverEl} class="gpu-warning-popover animate-fade-in">
            <div class="popover-arrow"></div>
            <div class="warn-header">
              <span class="warn-icon">⚠️</span>
              <span class="warn-title">GPU acceleration limited</span>
            </div>
            <div class="warn-desc">
              Ollama is running as a system service. {$appConfig.project_name} cannot configure its GPU settings automatically.
            </div>
            <div class="warn-actions">
              <a href="/settings" class="warn-link">Configure Settings →</a>
            </div>
          </div>
        {/if}
      {/if}
    </div>

    <!-- Brain Engine -->
    <div class="stat-card">
      <div class="stat-icon-wrap amber-icon">
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <path d="M9.5 2A2.5 2.5 0 0 1 12 4.5v15a2.5 2.5 0 0 1-4.96-.46 2.5 2.5 0 0 1-2.96-3.08 3 3 0 0 1-.34-5.58 2.5 2.5 0 0 1 1.32-4.24 2.5 2.5 0 0 1 1.98-3A2.5 2.5 0 0 1 9.5 2Z"/>
          <path d="M14.5 2A2.5 2.5 0 0 0 12 4.5v15a2.5 2.5 0 0 0 4.96-.46 2.5 2.5 0 0 0 2.96-3.08 3 3 0 0 0 .34-5.58 2.5 2.5 0 0 0-1.32-4.24 2.5 2.5 0 0 0-1.98-3A2.5 2.5 0 0 0 14.5 2Z"/>
        </svg>
      </div>
      <div class="stat-content">
        <div class="stat-label">Brain Engine</div>
        <div class="stat-value mono amber-val">
          {brainStatus ? brainStatus.total_chunks.toLocaleString() : '0'}
          <span class="stat-unit" style="font-size:10px">chunks</span>
        </div>
        <div class="stat-sub">{brainStatus ? brainStatus.indexed_files : 0} files indexed</div>
      </div>
    </div>

  </div>

  {#if telemetryHistory.length > 0}
    {@const cpuData = telemetryHistory.map(h => h.cpu_percent)}
    {@const ramData = telemetryHistory.map(h => h.ram_total > 0 ? (h.ram_used / h.ram_total) * 100 : 0)}
    {@const tasksData = telemetryHistory.map(h => h.tasks_running)}
    {@const throughputData = telemetryHistory.map(h => h.throughput)}
    {@const tokensSecData = telemetryHistory.map(h => h.tokens_sec)}

    {@const maxTasks = Math.max(5, ...tasksData)}
    {@const maxThroughput = Math.max(2.0, ...throughputData)}
    {@const maxTokens = Math.max(10.0, ...tokensSecData)}

    <div class="telemetry-charts animate-fade-in">
      <!-- Chart 1: Cluster Resources -->
      <div class="chart-card">
        <div class="chart-header">
          <div class="chart-title">Cluster Load</div>
          <div class="chart-legends">
            <span class="legend cpu"><span class="legend-color"></span>CPU ({cpuData[cpuData.length - 1].toFixed(0)}%)</span>
            <span class="legend ram"><span class="legend-color"></span>RAM ({ramData[ramData.length - 1].toFixed(0)}%)</span>
          </div>
        </div>
        <div class="chart-body">
          <svg viewBox="0 0 500 150" class="chart-svg">
            <defs>
              <linearGradient id="cpuGrad" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stop-color="#00c9a7" stop-opacity="0.2" />
                <stop offset="100%" stop-color="#00c9a7" stop-opacity="0.0" />
              </linearGradient>
              <linearGradient id="ramGrad" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stop-color="#38bdf8" stop-opacity="0.2" />
                <stop offset="100%" stop-color="#38bdf8" stop-opacity="0.0" />
              </linearGradient>
            </defs>
            <line x1="0" y1="37.5" x2="500" y2="37.5" class="grid-line" />
            <line x1="0" y1="75" x2="500" y2="75" class="grid-line" />
            <line x1="0" y1="112.5" x2="500" y2="112.5" class="grid-line" />
            <path d={getAreaPath(cpuData, 500, 150, 100)} fill="url(#cpuGrad)" />
            <path d={getAreaPath(ramData, 500, 150, 100)} fill="url(#ramGrad)" />
            <path d={getLinePath(cpuData, 500, 150, 100)} class="stroke-cpu" />
            <path d={getLinePath(ramData, 500, 150, 100)} class="stroke-ram" />
          </svg>
        </div>
      </div>

      <!-- Chart 2: Task Throughput & LLM Speed -->
      <div class="chart-card">
        <div class="chart-header">
          <div class="chart-title">Inference & Tasks</div>
          <div class="chart-legends">
            <span class="legend tokens"><span class="legend-color"></span>LLM Speed ({tokensSecData[tokensSecData.length - 1].toFixed(1)} t/s)</span>
            <span class="legend tasks"><span class="legend-color"></span>Active Tasks ({tasksData[tasksData.length - 1]})</span>
          </div>
        </div>
        <div class="chart-body">
          <svg viewBox="0 0 500 150" class="chart-svg">
            <defs>
              <linearGradient id="tokensGrad" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stop-color="#c084fc" stop-opacity="0.2" />
                <stop offset="100%" stop-color="#c084fc" stop-opacity="0.0" />
              </linearGradient>
              <linearGradient id="tasksGrad" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stop-color="#fbbf24" stop-opacity="0.15" />
                <stop offset="100%" stop-color="#fbbf24" stop-opacity="0.0" />
              </linearGradient>
            </defs>
            <line x1="0" y1="37.5" x2="500" y2="37.5" class="grid-line" />
            <line x1="0" y1="75" x2="500" y2="75" class="grid-line" />
            <line x1="0" y1="112.5" x2="500" y2="112.5" class="grid-line" />
            <path d={getAreaPath(tasksData, 500, 150, maxTasks)} fill="url(#tasksGrad)" />
            <path d={getAreaPath(tokensSecData, 500, 150, maxTokens)} fill="url(#tokensGrad)" />
            <path d={getLinePath(tasksData, 500, 150, maxTasks)} class="stroke-tasks" />
            <path d={getLinePath(tokensSecData, 500, 150, maxTokens)} class="stroke-tokens" />
          </svg>
        </div>
      </div>
    </div>
  {/if}

  <!-- ── Pulse Insights ── -->
  {#if insights.length > 0}
    <div class="insights-panel animate-fade-in" id="pulse-insights-panel">
      <div class="insights-header">
        <div class="insights-title-row">
          <span class="radar-dot"></span>
          <span class="insights-label">Pulse Insights</span>
          <span class="insights-badge">{insights.length}</span>
        </div>
        <span class="insights-subtitle">AI-powered cluster recommendations</span>
      </div>
      <div class="insights-list">
        {#each insights as ins (ins.id)}
          <div class="insight-item {ins.priority}">
            <div class="insight-accent-bar"></div>
            <div class="insight-content">
              <div class="insight-top">
                <span class="insight-title">{ins.title}</span>
                <button class="dismiss-btn" on:click={() => dismissInsight(ins.id)} aria-label="Dismiss">✕</button>
              </div>
              <p class="insight-body">{ins.body}</p>
              {#if ins.actions && ins.actions.length > 0}
                <div class="insight-actions">
                  {#each ins.actions as action}
                    <a href={action.link} class="insight-action-btn">{action.label} →</a>
                  {/each}
                </div>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    </div>
  {/if}

  <!-- ── Toast notifications ── -->
  <div class="toast-container">
    {#each toasts as t (t.id)}
      <div class="toast {t.priority} animate-fade-in">
        <div class="toast-stripe"></div>
        <div class="toast-body-wrap">
          <div class="toast-head">
            <span class="toast-icon">{t.priority === 'warning' ? '⚠️' : '⚡'}</span>
            <span class="toast-title">{t.title}</span>
            <button class="toast-close" on:click={() => toasts = toasts.filter(x => x.id !== t.id)}>✕</button>
          </div>
          <p class="toast-msg">{t.body}</p>
        </div>
      </div>
    {/each}
  </div>

  <!-- Tunnel Status Card -->
  <TunnelStatusCard />

  <!-- ── Cluster Section ── -->
  <div class="cluster-section">

    <!-- Topology -->
    <div class="topology-panel">
      <div class="topo-grid-bg"></div>
      <div class="topo-header">
        <span class="topo-label">Cluster Map</span>
        <span class="topo-count">{$nodes.length} node{$nodes.length !== 1 ? 's' : ''}</span>
      </div>
      {#if $nodes.length === 0}
        <div class="empty-state">
          <div class="empty-icon">🔗</div>
          <h3>No devices yet</h3>
          <p>Open {$appConfig.project_name} on another device on the same Wi-Fi - it will appear here automatically.</p>
        </div>
      {:else}
        <div class="ring-wrapper">
          <ClusterRing nodes={$nodes} size={240} />
        </div>
      {/if}
    </div>

    <!-- Peers -->
    <div class="devices-panel">
      <div class="section-header">
        <div class="section-title-group">
          <h2>Cluster Peers</h2>
          {#if $nodes.length > 0}
            <span class="peer-count-badge">{$nodes.length} node{$nodes.length !== 1 ? 's' : ''}</span>
          {/if}
        </div>
        <a href="/devices" class="btn btn-ghost btn-sm">All Peers →</a>
      </div>
      {#if $nodes.length === 0}
        <div class="no-devices-hint">
          <div class="hint-icon">🔍</div>
          <p>No peers discovered yet. Open {$appConfig.project_name} on another device on the same Wi-Fi.</p>
        </div>
      {:else}
        <div class="device-cards">
          {#each $nodes.slice(0, 3) as node (node.id)}
            <NodeCard {node} />
          {/each}
        </div>
        {#if $nodes.length > 3}
          <a href="/devices" class="more-peers-link">
            View {$nodes.length - 3} more peer{$nodes.length - 3 !== 1 ? 's' : ''} →
          </a>
        {/if}
      {/if}
    </div>

  </div>
</div>

<style>
  .dashboard {
    display: flex;
    flex-direction: column;
    gap: 24px;
  }

  /* ══════════════════════════════════════════
     STAT CARDS
  ══════════════════════════════════════════ */
  .telemetry-deck {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
    gap: 14px;
  }

  .stat-card {
    background: rgba(18, 26, 44, 0.5);
    border: 1px solid rgba(255, 255, 255, 0.06);
    backdrop-filter: blur(20px);
    border-radius: 18px;
    padding: 20px;
    display: flex;
    align-items: flex-start;
    gap: 16px;
    position: relative;
    overflow: hidden;
    transition: border-color 300ms ease, box-shadow 300ms ease, transform 300ms cubic-bezier(0.16,1,0.3,1);
    box-shadow: 0 2px 20px rgba(0,0,0,0.25), inset 0 1px 0 rgba(255,255,255,0.03);
  }
  .stat-card::before {
    content: '';
    position: absolute;
    top: 0; left: 0; right: 0;
    height: 1px;
    background: linear-gradient(90deg, transparent, rgba(255,255,255,0.08), transparent);
  }
  .stat-card:hover {
    border-color: rgba(255,255,255,0.12);
    transform: translateY(-2px);
    box-shadow: 0 8px 32px rgba(0,0,0,0.35), inset 0 1px 0 rgba(255,255,255,0.05);
  }
  .accent-card {
    background: linear-gradient(135deg, rgba(0,201,167,0.06) 0%, rgba(18,26,44,0.5) 60%);
    border-color: rgba(0,201,167,0.15);
  }
  .accent-card:hover { border-color: rgba(0,201,167,0.28); box-shadow: 0 8px 32px rgba(0,0,0,0.35), 0 0 20px rgba(0,201,167,0.06); }

  /* Icon avatars */
  .stat-icon-wrap {
    width: 40px;
    height: 40px;
    border-radius: 12px;
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    margin-top: 2px;
  }
  .accent-icon { background: rgba(0,201,167,0.12); color: #00c9a7; border: 1px solid rgba(0,201,167,0.2); }
  .blue-icon   { background: rgba(56,189,248,0.1);  color: #38bdf8; border: 1px solid rgba(56,189,248,0.18); }
  .purple-icon { background: rgba(192,132,252,0.1); color: #c084fc; border: 1px solid rgba(192,132,252,0.18); }
  .amber-icon  { background: rgba(251,191,36,0.1);  color: #fbbf24; border: 1px solid rgba(251,191,36,0.18); }
  .pink-icon   { background: rgba(244,63,94,0.1);   color: #f43f5e; border: 1px solid rgba(244,63,94,0.18); }

  .stat-content {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .stat-label {
    font-size: 10px;
    font-weight: 800;
    text-transform: uppercase;
    letter-spacing: 0.1em;
    color: rgba(255,255,255,0.35);
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .pulse-dot {
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: #00c9a7;
    box-shadow: 0 0 8px rgba(0,201,167,0.8);
    animation: pulse-glow 2s infinite ease-in-out;
    flex-shrink: 0;
  }
  .stat-value {
    font-size: 30px;
    font-weight: 800;
    color: var(--text-primary);
    line-height: 1.1;
    letter-spacing: -0.02em;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .blue-val   { color: #38bdf8; text-shadow: 0 0 20px rgba(56,189,248,0.2); }
  .amber-val  { color: #fbbf24; text-shadow: 0 0 20px rgba(251,191,36,0.15); }
  .pink-val   { color: #f43f5e; text-shadow: 0 0 20px rgba(244,63,94,0.15); }
  .stat-unit {
    font-size: 13px;
    font-weight: 600;
    color: rgba(255,255,255,0.35);
    margin-left: 3px;
  }
  .stat-sub {
    font-size: 11px;
    color: rgba(255,255,255,0.3);
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: 2px;
  }
  /* Mini usage bar in RAM card */
  .usage-bar-wrap {
    flex: 1;
    max-width: 48px;
    height: 3px;
    background: rgba(255,255,255,0.08);
    border-radius: 99px;
    overflow: hidden;
    flex-shrink: 0;
  }
  .usage-bar {
    display: block;
    height: 100%;
    background: #00c9a7;
    border-radius: 99px;
    box-shadow: 0 0 6px rgba(0,201,167,0.5);
  }

  /* ══════════════════════════════════════════
     PULSE INSIGHTS
  ══════════════════════════════════════════ */
  .insights-panel {
    background: rgba(14, 20, 35, 0.6);
    border: 1px solid rgba(0, 201, 167, 0.12);
    backdrop-filter: blur(20px);
    border-radius: 18px;
    overflow: hidden;
    box-shadow: 0 4px 28px rgba(0,0,0,0.2), inset 0 1px 0 rgba(255,255,255,0.03);
  }
  .insights-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 14px 20px;
    border-bottom: 1px solid rgba(255,255,255,0.04);
    background: rgba(0,201,167,0.02);
  }
  .insights-title-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .radar-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: #00c9a7;
    box-shadow: 0 0 10px rgba(0,201,167,0.8);
    animation: radar-ping 1.5s infinite ease-out;
    flex-shrink: 0;
  }
  .insights-label {
    font-size: 11px;
    font-weight: 800;
    text-transform: uppercase;
    letter-spacing: 0.1em;
    color: var(--text-primary);
  }
  .insights-badge {
    font-size: 9px;
    font-weight: 800;
    color: #00c9a7;
    background: rgba(0,201,167,0.1);
    border: 1px solid rgba(0,201,167,0.2);
    padding: 1px 7px;
    border-radius: 20px;
  }
  .insights-subtitle {
    font-size: 10px;
    color: rgba(255,255,255,0.25);
    font-style: italic;
  }
  .insights-list {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
    gap: 1px;
    background: rgba(255,255,255,0.03);
  }
  .insight-item {
    background: rgba(14, 20, 35, 0.8);
    display: flex;
    gap: 0;
    position: relative;
    overflow: hidden;
    transition: background 0.2s ease;
  }
  .insight-item:hover { background: rgba(22, 30, 50, 0.9); }
  .insight-accent-bar {
    width: 3px;
    flex-shrink: 0;
    background: rgba(0,201,167,0.5);
  }
  .insight-item.warning .insight-accent-bar { background: #fbbf24; }
  .insight-item.action  .insight-accent-bar { background: #ff4d6d; }
  .insight-content {
    flex: 1;
    padding: 14px 16px;
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 0;
  }
  .insight-top {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 8px;
  }
  .insight-title {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-primary);
    line-height: 1.3;
  }
  .dismiss-btn {
    background: none;
    border: none;
    color: rgba(255,255,255,0.2);
    cursor: pointer;
    font-size: 10px;
    padding: 2px 5px;
    border-radius: 4px;
    flex-shrink: 0;
    transition: all 0.15s;
  }
  .dismiss-btn:hover { color: var(--text-primary); background: rgba(255,255,255,0.06); }
  .insight-body {
    font-size: 11px;
    color: rgba(255,255,255,0.4);
    line-height: 1.5;
    margin: 0;
  }
  .insight-actions { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 2px; }
  .insight-action-btn {
    font-size: 10px;
    font-weight: 700;
    color: #00c9a7;
    background: rgba(0,201,167,0.06);
    border: 1px solid rgba(0,201,167,0.2);
    padding: 3px 10px;
    border-radius: 6px;
    text-decoration: none;
    transition: all 0.15s;
  }
  .insight-action-btn:hover {
    background: rgba(0,201,167,0.14);
    border-color: rgba(0,201,167,0.4);
    text-decoration: none;
  }

  /* ══════════════════════════════════════════
     TOASTS
  ══════════════════════════════════════════ */
  .toast-container {
    position: fixed;
    top: 24px;
    right: 24px;
    display: flex;
    flex-direction: column;
    gap: 10px;
    z-index: 1000;
    max-width: 340px;
  }
  .toast {
    background: rgba(13, 18, 30, 0.97);
    border: 1px solid rgba(255,255,255,0.08);
    border-radius: 14px;
    backdrop-filter: blur(16px);
    box-shadow: 0 16px 40px rgba(0,0,0,0.5);
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }
  .toast-stripe { height: 3px; background: #fbbf24; }
  .toast.action .toast-stripe { background: #ff4d6d; }
  .toast-body-wrap { padding: 12px 14px; display: flex; flex-direction: column; gap: 4px; }
  .toast-head { display: flex; align-items: center; gap: 6px; }
  .toast-icon { font-size: 13px; }
  .toast-title { font-size: 12px; font-weight: 700; color: var(--text-primary); flex: 1; }
  .toast-close { background: none; border: none; color: rgba(255,255,255,0.25); cursor: pointer; font-size: 10px; padding: 2px 4px; }
  .toast-msg { font-size: 11px; color: rgba(255,255,255,0.45); line-height: 1.45; margin: 0; }

  /* ══════════════════════════════════════════
     CLUSTER SECTION
  ══════════════════════════════════════════ */
  .cluster-section {
    display: grid;
    grid-template-columns: 280px 1fr;
    gap: 16px;
    align-items: stretch;
  }

  /* Topology panel */
  .topology-panel {
    background: rgba(14, 20, 35, 0.5);
    border: 1px solid rgba(255,255,255,0.05);
    backdrop-filter: blur(16px);
    border-radius: 18px;
    padding: 18px;
    position: relative;
    overflow: hidden;
    display: flex;
    flex-direction: column;
    align-items: center;
    box-shadow: 0 4px 24px rgba(0,0,0,0.2), inset 0 1px 0 rgba(255,255,255,0.03);
    min-height: 240px;
  }
  .topo-grid-bg {
    position: absolute;
    inset: 0;
    background-image: radial-gradient(rgba(255,255,255,0.015) 1px, transparent 0);
    background-size: 16px 16px;
    pointer-events: none;
  }
  .topo-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    margin-bottom: 16px;
    position: relative;
    z-index: 1;
  }
  .topo-label {
    font-size: 10px;
    font-weight: 800;
    text-transform: uppercase;
    letter-spacing: 0.1em;
    color: rgba(255,255,255,0.3);
  }
  .topo-count {
    font-size: 9px;
    font-weight: 700;
    color: #00c9a7;
    background: rgba(0,201,167,0.08);
    border: 1px solid rgba(0,201,167,0.18);
    padding: 2px 8px;
    border-radius: 20px;
  }
  .ring-wrapper {
    position: relative;
    z-index: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 100%;
    flex: 1;
  }

  /* Devices panel */
  .devices-panel {
    display: flex;
    flex-direction: column;
    gap: 12px;
    min-width: 0;
    background: rgba(14, 20, 35, 0.5);
    border: 1px solid rgba(255,255,255,0.05);
    backdrop-filter: blur(16px);
    border-radius: 18px;
    padding: 18px;
    box-shadow: 0 4px 24px rgba(0,0,0,0.2), inset 0 1px 0 rgba(255,255,255,0.03);
  }
  /* Cards: max 380px each, left-aligned - don't stretch a single card to full width */
  .device-cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 380px));
    gap: 14px;
    width: 100%;
  }

  /* Section header - matches topo-header height/style */
  .section-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 12px;
    padding-bottom: 12px;
    border-bottom: 1px solid rgba(255,255,255,0.04);
    margin-bottom: 4px;
  }
  .section-title-group {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
  }
  .section-header h2 {
    font-size: 10px;
    font-weight: 800;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: rgba(255,255,255,0.3);
    margin: 0;
    white-space: nowrap;
  }
  .peer-count-badge {
    font-size: 9px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: #00c9a7;
    background: rgba(0,201,167,0.08);
    border: 1px solid rgba(0,201,167,0.2);
    padding: 2px 8px;
    border-radius: 20px;
    white-space: nowrap;
  }

  /* Buttons */
  .btn-sm {
    padding: 5px 12px;
    font-size: 10px;
    font-weight: 700;
    border-radius: 8px;
    white-space: nowrap;
    flex-shrink: 0;
    letter-spacing: 0.02em;
  }
  .btn-ghost {
    background: transparent;
    border: 1px solid rgba(255,255,255,0.08);
    color: rgba(255,255,255,0.35);
    transition: all 0.2s ease;
    text-decoration: none;
  }
  .btn-ghost:hover {
    background: rgba(255,255,255,0.05);
    border-color: rgba(255,255,255,0.14);
    color: var(--text-primary);
    text-decoration: none;
  }

  /* No devices hint */
  .no-devices-hint {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    background: rgba(18, 26, 44, 0.3);
    border: 1px dashed rgba(255,255,255,0.06);
    border-radius: 14px;
    padding: 20px;
  }
  .hint-icon { font-size: 20px; flex-shrink: 0; opacity: 0.5; }
  .no-devices-hint p { font-size: 12px; color: rgba(255,255,255,0.3); line-height: 1.55; margin: 0; }

  /* More peers */
  .more-peers-link {
    display: block;
    text-align: center;
    font-size: 11px;
    font-weight: 600;
    color: rgba(255,255,255,0.3);
    padding: 10px;
    border: 1px dashed rgba(255,255,255,0.07);
    border-radius: 10px;
    text-decoration: none;
    transition: all 0.2s ease;
  }
  .more-peers-link:hover {
    color: #00c9a7;
    border-color: rgba(0,201,167,0.25);
    background: rgba(0,201,167,0.03);
    text-decoration: none;
  }

  /* GPU VRAM warning layout styles */
  .stat-card-main {
    display: flex;
    align-items: flex-start;
    gap: 16px;
    flex: 1;
    min-width: 0;
  }
  .stat-card.gpu-card {
    position: relative;
    overflow: visible; /* so popover floats outside card boundaries */
  }
  .stat-card.has-warning {
    border-color: rgba(251, 191, 36, 0.25);
    background: linear-gradient(135deg, rgba(251, 191, 36, 0.04) 0%, rgba(18, 26, 44, 0.5) 60%);
  }
  .stat-card.has-warning:hover {
    border-color: rgba(251, 191, 36, 0.4);
    box-shadow: 0 8px 32px rgba(0, 0, 0, 0.35), 0 0 20px rgba(251, 191, 36, 0.04);
  }
  .gpu-alert-indicator {
    position: absolute;
    top: 14px;
    right: 14px;
    font-size: 12px;
    width: 24px;
    height: 24px;
    background: rgba(251, 191, 36, 0.1);
    border: 1px solid rgba(251, 191, 36, 0.35);
    border-radius: 50%;
    display: flex;
    align-items: center;
    justify-content: center;
    cursor: pointer;
    transition: all 0.2s ease, transform 0.1s ease;
    z-index: 10;
    outline: none;
    padding: 0;
  }
  .gpu-alert-indicator:hover {
    background: rgba(251, 191, 36, 0.2);
    border-color: rgba(251, 191, 36, 0.6);
  }
  .gpu-alert-indicator::before {
    content: '';
    position: absolute;
    inset: -1px;
    border-radius: 50%;
    border: 1px solid rgba(251, 191, 36, 0.4);
    animation: indicator-ping 2s infinite ease-out;
  }
  @keyframes indicator-ping {
    0%   { transform: scale(1); opacity: 1; }
    100% { transform: scale(1.6); opacity: 0; }
  }

  .gpu-warning-popover {
    position: absolute;
    top: 10px;
    right: 44px;
    width: 260px;
    height: fit-content;
    background: rgba(18, 24, 38, 0.96);
    border: 1px solid rgba(251, 191, 36, 0.35);
    border-radius: 12px;
    padding: 14px;
    box-shadow: 0 12px 32px rgba(0, 0, 0, 0.5), 0 0 15px rgba(251, 191, 36, 0.05);
    z-index: 100;
    backdrop-filter: blur(20px);
  }
  .popover-arrow {
    position: absolute;
    top: 16px;
    left: 100%;
    width: 0;
    height: 0;
    border-top: 6px solid transparent;
    border-bottom: 6px solid transparent;
    border-left: 6px solid rgba(251, 191, 36, 0.35);
  }
  .popover-arrow::after {
    content: '';
    position: absolute;
    top: -5px;
    left: -7px;
    width: 0;
    height: 0;
    border-top: 5px solid transparent;
    border-bottom: 5px solid transparent;
    border-left: 5px solid rgba(18, 24, 38, 0.96);
  }

  .gpu-warning-popover .warn-header {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 6px;
  }
  .gpu-warning-popover .warn-icon {
    font-size: 13px;
  }
  .gpu-warning-popover .warn-title {
    font-size: 12.5px;
    font-weight: 700;
    color: #fbbf24;
  }
  .gpu-warning-popover .warn-desc {
    font-size: 11px;
    color: rgba(255, 255, 255, 0.65);
    line-height: 1.5;
  }
  .gpu-warning-popover .warn-actions {
    margin-top: 12px;
    display: flex;
    justify-content: flex-end;
  }
  .gpu-warning-popover .warn-link {
    font-size: 11px;
    font-weight: 700;
    color: #0b0e14;
    background: #fbbf24;
    padding: 6px 12px;
    border-radius: 6px;
    text-decoration: none;
    transition: background 0.15s, transform 0.1s;
    display: inline-block;
  }
  .gpu-warning-popover .warn-link:hover {
    background: #fcd34d;
    transform: translateY(-0.5px);
    text-decoration: none;
  }

  /* Empty state */
  .empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 32px 16px;
    text-align: center;
    gap: 10px;
    position: relative;
    z-index: 1;
  }
  .empty-state .empty-icon { font-size: 36px; opacity: 0.35; }
  .empty-state h3 { font-size: 13px; color: rgba(255,255,255,0.35); font-weight: 600; }
  .empty-state p { font-size: 11px; color: rgba(255,255,255,0.22); line-height: 1.5; max-width: 220px; }

  /* ══════════════════════════════════════════
     ANIMATIONS
  ══════════════════════════════════════════ */
  @keyframes pulse-glow {
    0%, 100% { box-shadow: 0 0 6px rgba(0,201,167,0.7); }
    50%       { box-shadow: 0 0 14px rgba(0,201,167,1); }
  }
  @keyframes radar-ping {
    0%   { box-shadow: 0 0 0 0 rgba(0,201,167,0.7); }
    100% { box-shadow: 0 0 0 10px rgba(0,201,167,0); }
  }

  /* ══════════════════════════════════════════
     TELEMETRY CHARTS
  ══════════════════════════════════════════ */
  .telemetry-charts {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    gap: 16px;
    width: 100%;
  }
  .chart-card {
    background: rgba(14, 20, 35, 0.5);
    border: 1px solid rgba(255, 255, 255, 0.05);
    backdrop-filter: blur(16px);
    border-radius: 18px;
    padding: 18px;
    display: flex;
    flex-direction: column;
    gap: 12px;
    box-shadow: 0 4px 24px rgba(0,0,0,0.2), inset 0 1px 0 rgba(255,255,255,0.03);
  }
  .chart-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .chart-title {
    font-size: 11px;
    font-weight: 800;
    text-transform: uppercase;
    letter-spacing: 0.1em;
    color: rgba(255, 255, 255, 0.3);
  }
  .chart-legends {
    display: flex;
    gap: 12px;
    align-items: center;
  }
  .legend {
    font-size: 10px;
    font-weight: 700;
    color: rgba(255, 255, 255, 0.5);
    display: flex;
    align-items: center;
    gap: 5px;
  }
  .legend-color {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    display: inline-block;
  }
  .legend.cpu .legend-color    { background: #00c9a7; box-shadow: 0 0 6px #00c9a7; }
  .legend.ram .legend-color    { background: #38bdf8; box-shadow: 0 0 6px #38bdf8; }
  .legend.tasks .legend-color  { background: #fbbf24; box-shadow: 0 0 6px #fbbf24; }
  .legend.tokens .legend-color { background: #c084fc; box-shadow: 0 0 6px #c084fc; }

  .chart-body {
    position: relative;
    width: 100%;
    height: 150px;
  }
  .chart-svg {
    width: 100%;
    height: 100%;
    overflow: visible;
  }
  .grid-line {
    stroke: rgba(255, 255, 255, 0.03);
    stroke-width: 1;
    stroke-dasharray: 4 4;
  }
  .stroke-cpu, .stroke-ram, .stroke-tasks, .stroke-tokens {
    fill: none;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
    transition: stroke-dasharray 300ms ease;
  }
  .stroke-cpu    { stroke: #00c9a7; filter: drop-shadow(0 0 4px rgba(0, 201, 167, 0.4)); }
  .stroke-ram    { stroke: #38bdf8; filter: drop-shadow(0 0 4px rgba(56, 189, 248, 0.4)); }
  .stroke-tasks  { stroke: #fbbf24; filter: drop-shadow(0 0 4px rgba(251, 191, 36, 0.4)); }
  .stroke-tokens { stroke: #c084fc; filter: drop-shadow(0 0 4px rgba(192, 132, 252, 0.4)); }

  /* ══════════════════════════════════════════
     RESPONSIVE
  ══════════════════════════════════════════ */
  @media (max-width: 960px) {
    .cluster-section { grid-template-columns: 1fr; }
    .topology-panel  { min-height: 220px; max-height: 280px; }
    .device-cards    { grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); }
  }
  @media (max-width: 700px) {
    .telemetry-deck  { grid-template-columns: repeat(2, 1fr); }
    .telemetry-deck .stat-card {
      flex-direction: column;
      align-items: flex-start;
      padding: 14px;
      gap: 10px;
    }
    .telemetry-deck .stat-card:last-child {
      grid-column: span 2;
      flex-direction: row;
      align-items: center;
    }
    .device-cards    { grid-template-columns: 1fr; }
    .insights-list   { grid-template-columns: 1fr; }
    .stat-value      { font-size: 22px; }
  }
  @media (max-width: 560px) {
    .telemetry-deck  { grid-template-columns: 1fr; }
    .telemetry-deck .stat-card {
      flex-direction: row;
      align-items: center;
      padding: 14px 16px;
      gap: 14px;
    }
    .telemetry-deck .stat-card:last-child {
      grid-column: auto;
    }
  }
  @media (max-width: 480px) {
    .telemetry-deck .stat-card {
      padding: 12px;
      gap: 10px;
    }
    .stat-icon-wrap {
      width: 36px;
      height: 36px;
      font-size: 16px;
    }
    .stat-value {
      font-size: 20px;
    }
    .stat-label {
      font-size: 9px;
      letter-spacing: 0.08em;
    }
    .stat-sub {
      font-size: 10px;
    }
  }

</style>
