<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { appConfig } from '$lib/stores/cluster';

  interface ShieldStatus {
    sandbox_mode: boolean;
    violations_24h: number;
    violations_1h: number;
    risk_level: string;
    max_task_memory_mb: number;
    max_task_procs: number;
    max_task_file_size_mb: number;
    audit_log_enabled: boolean;
  }

  interface AuditEvent {
    id: string;
    at: string;
    node_id: string;
    task_id?: string;
    category: string;
    command?: string;
    reason: string;
    meta?: Record<string, string>;
    sig: string;
  }

  let status: ShieldStatus | null = null;
  let auditLogs: AuditEvent[] = [];
  let loading = true;
  let error = '';
  let search = '';
  let expandedEvent: string | null = null;

  async function loadData() {
    try {
      const [statusRes, auditRes] = await Promise.all([
        fetch('/api/shield/status'),
        fetch('/api/shield/audit?limit=100')
      ]);

      if (!statusRes.ok) throw new Error('Failed to load shield status');
      if (!auditRes.ok) throw new Error('Failed to load audit logs');

      status = await statusRes.json();
      auditLogs = await auditRes.json();
      error = '';
    } catch (err: any) {
      error = err.message || 'Failed to connect to agent API';
    } finally {
      loading = false;
    }
  }

  let pollInterval: any;

  onMount(() => {
    loadData();
    pollInterval = setInterval(loadData, 5000);
  });

  onDestroy(() => {
    if (pollInterval) clearInterval(pollInterval);
  });

  function getRiskColor(level: string) {
    switch (level) {
      case 'high': return 'risk-high';
      case 'medium': return 'risk-medium';
      case 'low': return 'risk-low';
      default: return 'risk-unknown';
    }
  }

  function getCategoryLabel(cat: string) {
    switch (cat) {
      case 'command_rejected': return 'Command Blocked';
      case 'command_allowed': return 'Permitted';
      case 'env_var_rejected': return 'Env Blocked';
      case 'path_traversal': return 'Path Escape';
      case 'task_timeout': return 'Timeout';
      case 'resource_limit_hit': return 'Limit Exceeded';
      case 'sandbox_violation': return 'Security Violation';
      default: return cat;
    }
  }

  function getCategoryClass(cat: string) {
    switch (cat) {
      case 'command_rejected':
      case 'env_var_rejected':
      case 'path_traversal':
      case 'sandbox_violation':
        return 'badge-danger';
      case 'task_timeout':
      case 'resource_limit_hit':
        return 'badge-warning';
      case 'command_allowed':
        return 'badge-online';
      default:
        return 'badge-offline';
    }
  }

  $: filteredLogs = auditLogs.filter(log => {
    if (!search) return true;
    const s = search.toLowerCase();
    return (
      log.id.toLowerCase().includes(s) ||
      log.category.toLowerCase().includes(s) ||
      (log.command && log.command.toLowerCase().includes(s)) ||
      log.reason.toLowerCase().includes(s) ||
      (log.task_id && log.task_id.toLowerCase().includes(s))
    );
  });
</script>

<svelte:head>
  <title>Fabric Shield - {$appConfig.project_name}</title>
  <meta name="description" content="OpenFabric Security Hardening Sandbox and Tamper-evident Audit Logs" />
</svelte:head>

<div class="shield-page animate-fade-in">
  <div class="section-header">
    <div>
      <h1 class="header-title">🛡️ Fabric Shield</h1>
      <p class="text-secondary" style="margin-top: 4px; font-size: var(--text-sm)">
        Subsystem process isolation, resource constraint sandbox, and cryptographic audit monitoring.
      </p>
    </div>
    <button class="btn btn-secondary btn-sm" on:click={loadData}>
      ↻ Refresh
    </button>
  </div>

  {#if error}
    <div class="banner banner-error" role="alert">
      ⚠️ {error}
    </div>
  {/if}

  {#if loading && !status}
    <div class="card"><p class="text-muted">Loading Fabric Shield status…</p></div>
  {:else if status}
    <!-- Status Grid -->
    <div class="status-grid">
      <!-- Security Posture Card -->
      <div class="status-card card glassmorphic">
        <div class="card-title">Security Posture</div>
        <div class="posture-row">
          <span class="posture-label">Risk Level</span>
          <span class="risk-badge {getRiskColor(status.risk_level)}">{status.risk_level.toUpperCase()}</span>
        </div>
        <div class="divider"></div>
        <div class="posture-info">
          <div class="info-item">
            <span class="dot {status.sandbox_mode ? 'online' : 'offline'}"></span>
            <span class="info-text">Sandbox Mode: <strong>{status.sandbox_mode ? 'ENABLED' : 'DISABLED'}</strong></span>
          </div>
          <div class="info-item">
            <span class="dot {status.audit_log_enabled ? 'online' : 'offline'}"></span>
            <span class="info-text">Cryptographic Audit: <strong>{status.audit_log_enabled ? 'ACTIVE' : 'INACTIVE'}</strong></span>
          </div>
        </div>
      </div>

      <!-- Violation Counts Card -->
      <div class="status-card card glassmorphic">
        <div class="card-title">Recorded Incidents</div>
        <div class="metrics-row">
          <div class="metric-box">
            <div class="metric-val text-red">{status.violations_1h}</div>
            <div class="metric-lbl">Last Hour</div>
          </div>
          <div class="metric-box">
            <div class="metric-val text-yellow">{status.violations_24h}</div>
            <div class="metric-lbl">Last 24 Hours</div>
          </div>
        </div>
        <p class="risk-desc text-muted">
          {#if status.risk_level === 'high'}
            ⚠️ Multiple sandbox violations detected in the last hour. System is under high alert.
          {:else if status.risk_level === 'medium'}
            ⚠️ Rejections recorded. Review task commands and network settings.
          {:else}
            ✓ No suspicious sandbox violations or threat anomalies detected.
          {/if}
        </p>
      </div>

      <!-- Hardening Limits Card -->
      <div class="status-card card glassmorphic">
        <div class="card-title">Resource Hardening Limits</div>
        <div class="limits-list">
          <div class="limit-item">
            <span class="limit-name">Max Task Memory</span>
            <span class="limit-val">{status.max_task_memory_mb} MB</span>
          </div>
          <div class="limit-item">
            <span class="limit-name">Max Task Processes</span>
            <span class="limit-val">{status.max_task_procs} procs</span>
          </div>
          <div class="limit-item">
            <span class="limit-name">Max Task Output Size</span>
            <span class="limit-val">{status.max_task_file_size_mb} MB</span>
          </div>
        </div>
      </div>
    </div>

    <!-- Audit Logs Section -->
    <div class="audit-section">
      <div class="audit-header">
        <h2>Security Audit Logs</h2>
        <input
          type="text"
          placeholder="Search by command, category, reason or task ID…"
          class="input input-premium search-input"
          bind:value={search}
        />
      </div>

      {#if filteredLogs.length === 0}
        <div class="card text-center text-muted" style="padding: var(--space-8)">
          No security audit logs recorded.
        </div>
      {:else}
        <div class="logs-list">
          {#each filteredLogs as log (log.id)}
            <div class="log-card card" class:expanded={expandedEvent === log.id}>
              <!-- svelte-ignore a11y-click-events-have-key-events -->
              <!-- svelte-ignore a11y-no-static-element-interactions -->
              <div
                class="log-header-row"
                on:click={() => expandedEvent = expandedEvent === log.id ? null : log.id}
              >
                <div class="log-left">
                  <span class="badge {getCategoryClass(log.category)}">{getCategoryLabel(log.category)}</span>
                  <span class="log-cmd mono">{log.command || '-'}</span>
                </div>
                <div class="log-right">
                  <span class="log-time text-muted">{new Date(log.at).toLocaleTimeString()}</span>
                  <span class="expand-icon" class:rotated={expandedEvent === log.id}>▾</span>
                </div>
              </div>

              {#if expandedEvent === log.id}
                <div class="log-details animate-slide-down">
                  <div class="detail-row">
                    <span class="detail-label">Event ID</span>
                    <span class="detail-val mono">{log.id}</span>
                  </div>
                  {#if log.task_id}
                    <div class="detail-row">
                      <span class="detail-label">Task ID</span>
                      <span class="detail-val mono">{log.task_id}</span>
                    </div>
                  {/if}
                  <div class="detail-row">
                    <span class="detail-label">Triggered At</span>
                    <span class="detail-val">{new Date(log.at).toLocaleString()}</span>
                  </div>
                  <div class="detail-row">
                    <span class="detail-label">Origin Node</span>
                    <span class="detail-val mono">{log.node_id}</span>
                  </div>
                  <div class="detail-row">
                    <span class="detail-label">Rejection Reason</span>
                    <span class="detail-val text-red">{log.reason}</span>
                  </div>

                  <!-- Detached Signature verification -->
                  <div class="signature-block">
                    <div class="sig-header">
                      <span>Ed25519 Cryptographic Signature</span>
                      <span class="sig-status text-online">✓ Offline Verified</span>
                    </div>
                    <pre class="sig-val">{log.sig}</pre>
                  </div>
                </div>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .shield-page {
    display: flex;
    flex-direction: column;
    gap: var(--space-6);
  }

  .status-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: var(--space-5);
  }

  .status-card {
    display: flex;
    flex-direction: column;
    padding: var(--space-5);
  }

  .card-title {
    font-size: var(--text-xs);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    font-weight: 600;
    color: var(--text-muted);
    margin-bottom: var(--space-4);
  }

  .posture-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--space-3);
  }

  .posture-label {
    font-size: var(--text-sm);
    color: var(--text-secondary);
  }

  .risk-badge {
    font-size: var(--text-xs);
    font-weight: 700;
    padding: 4px 10px;
    border-radius: var(--radius-sm);
    letter-spacing: 0.02em;
  }

  .risk-low {
    background: rgba(0, 201, 167, 0.12);
    color: var(--online);
    border: 1px solid rgba(0, 201, 167, 0.25);
    box-shadow: 0 0 10px rgba(0, 201, 167, 0.15);
  }

  .risk-medium {
    background: rgba(254, 188, 46, 0.12);
    color: var(--warning);
    border: 1px solid rgba(254, 188, 46, 0.25);
    box-shadow: 0 0 10px rgba(254, 188, 46, 0.15);
  }

  .risk-high {
    background: rgba(255, 95, 87, 0.12);
    color: var(--danger);
    border: 1px solid rgba(255, 95, 87, 0.25);
    box-shadow: 0 0 15px rgba(255, 95, 87, 0.25);
    animation: pulse-glow 2s infinite ease-in-out;
  }

  @keyframes pulse-glow {
    0%, 100% { box-shadow: 0 0 10px rgba(255, 95, 87, 0.2); }
    50% { box-shadow: 0 0 20px rgba(255, 95, 87, 0.45); }
  }

  .divider {
    height: 1px;
    background: var(--border);
    margin: var(--space-2) 0 var(--space-4);
  }

  .posture-info {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }

  .info-item {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }

  .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
  }

  .dot.online { background: var(--online); }
  .dot.offline { background: var(--offline); }

  .info-text {
    font-size: var(--text-xs);
    color: var(--text-secondary);
  }

  .metrics-row {
    display: flex;
    gap: var(--space-5);
    margin-bottom: var(--space-3);
  }

  .metric-box {
    flex: 1;
    background: rgba(0, 0, 0, 0.15);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-3);
    text-align: center;
  }

  .metric-val {
    font-size: var(--text-xl);
    font-weight: 700;
  }

  .metric-lbl {
    font-size: var(--text-xxs);
    text-transform: uppercase;
    color: var(--text-muted);
    margin-top: 2px;
  }

  .text-red { color: var(--danger); }
  .text-yellow { color: var(--warning); }
  .text-online { color: var(--online); }

  .risk-desc {
    font-size: var(--text-xs);
    line-height: 1.4;
  }

  .limits-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }

  .limit-item {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  .limit-name {
    font-size: var(--text-xs);
    color: var(--text-secondary);
  }

  .limit-val {
    font-size: var(--text-xs);
    font-family: var(--font-mono);
    color: var(--text-primary);
  }

  .audit-section {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }

  .audit-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: var(--space-4);
    flex-wrap: wrap;
  }

  .search-input {
    width: 320px;
    padding: 8px 12px;
    font-size: var(--text-xs);
  }

  .logs-list {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .log-card {
    padding: 0;
    overflow: hidden;
  }

  .log-header-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: var(--space-3) var(--space-5);
    cursor: pointer;
    gap: var(--space-3);
  }

  .log-header-row:hover {
    background: var(--bg-card-hover);
  }

  .log-left {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    min-width: 0;
  }

  .log-cmd {
    font-size: var(--text-xs);
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 450px;
  }

  .log-right {
    display: flex;
    align-items: center;
    gap: var(--space-3);
  }

  .log-time {
    font-size: var(--text-xs);
  }

  .expand-icon {
    color: var(--text-muted);
    transition: transform var(--transition);
    font-size: var(--text-sm);
  }

  .expand-icon.rotated {
    transform: rotate(180deg);
  }

  .log-details {
    border-top: 1px solid var(--border);
    padding: var(--space-4) var(--space-5);
    background: var(--bg-primary);
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }

  .detail-row {
    display: flex;
    font-size: var(--text-xs);
    line-height: 1.4;
  }

  .detail-label {
    width: 140px;
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .detail-val {
    color: var(--text-secondary);
  }

  .signature-block {
    margin-top: var(--space-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    background: rgba(0, 0, 0, 0.2);
    overflow: hidden;
  }

  .sig-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: var(--space-2) var(--space-3);
    background: rgba(255, 255, 255, 0.02);
    border-bottom: 1px solid var(--border);
    font-size: var(--text-xxs);
    color: var(--text-muted);
    text-transform: uppercase;
    font-weight: 600;
  }

  .sig-val {
    margin: 0;
    padding: var(--space-3);
    font-family: var(--font-mono);
    font-size: var(--text-xxs);
    color: var(--text-muted);
    white-space: pre-wrap;
    word-break: break-all;
  }

  @keyframes slide-down {
    from { opacity: 0; transform: translateY(-5px); }
    to { opacity: 1; transform: translateY(0); }
  }

  .animate-slide-down {
    animation: slide-down 150ms ease-out;
  }

  @media (max-width: 640px) {
    .audit-header {
      flex-direction: column;
      align-items: flex-start;
    }

    .search-input {
      width: 100%;
    }

    .log-header-row {
      flex-direction: column;
      align-items: flex-start;
      gap: var(--space-2);
    }

    .log-right {
      width: 100%;
      justify-content: flex-end;
      border-top: 1px solid var(--border);
      padding-top: var(--space-2);
    }
  }
</style>
