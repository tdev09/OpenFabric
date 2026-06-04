<script lang="ts">
  import { tunnelStatus } from '$lib/stores/tunnel';
  import { slide } from 'svelte/transition';

  let copied = false;
  async function copyUrl() {
    try {
      await navigator.clipboard.writeText($tunnelStatus.tunnel_url);
      copied = true;
      setTimeout(() => copied = false, 2000);
    } catch (e) {
      console.error(e);
    }
  }

  $: stateText = {
    disconnected: 'Inactive',
    connecting: 'Connecting…',
    connected: 'Active Tunnel',
    error: 'Config Error'
  }[$tunnelStatus.state] ?? 'Unknown';
</script>

<div class="tunnel-card" class:active={$tunnelStatus.state === 'connected'} class:error={$tunnelStatus.state === 'error'}>
  <div class="card-header">
    <div class="status-indicator">
      <span class="pulse-dot"></span>
      <span class="status-label">{stateText}</span>
    </div>
    <a class="view-btn" href="/tunnel">Configure ↗</a>
  </div>

  <div class="card-body">
    {#if $tunnelStatus.state === 'connected'}
      <div class="info-section" transition:slide={{ duration: 200 }}>
        <span class="section-title">REMOTE DASHBOARD URL</span>
        <div class="url-copy-box">
          <input type="text" readonly value={$tunnelStatus.tunnel_url} class="url-input" />
          <button on:click={copyUrl} class="copy-btn" aria-label="Copy URL">
            {copied ? '✅' : '📋'}
          </button>
        </div>
        <div class="stats-row">
          <div class="stat-item">
            <span class="stat-label">VPN IP</span>
            <span class="stat-val">{$tunnelStatus.assigned_ip.split('/')[0]}</span>
          </div>
          <div class="stat-item">
            <span class="stat-label">Active Clients</span>
            <span class="stat-val">{$tunnelStatus.peers.length}</span>
          </div>
        </div>
      </div>
    {:else if $tunnelStatus.state === 'connecting'}
      <div class="connecting-loader" transition:slide={{ duration: 200 }}>
        <div class="spinner"></div>
        <span class="loader-txt">Establishing secure WireGuard tunnel...</span>
      </div>
    {:else if $tunnelStatus.state === 'error'}
      <div class="error-msg" transition:slide={{ duration: 200 }}>
        <span class="alert-icon">⚠️</span>
        <span class="alert-txt">{$tunnelStatus.error || 'Failed to bring up interface. Check permissions.'}</span>
      </div>
    {:else}
      <div class="inactive-state" transition:slide={{ duration: 200 }}>
        <span class="state-icon">🔒</span>
        <p class="state-desc">Secure remote access is disabled. Enable to access models and stats from anywhere.</p>
      </div>
    {/if}
  </div>
</div>

<style>
  .tunnel-card {
    background: linear-gradient(145deg, rgba(18,26,44,0.7) 0%, rgba(12,18,32,0.55) 100%);
    border: 1px solid rgba(255,255,255,0.07);
    backdrop-filter: blur(20px) saturate(160%);
    border-radius: 18px;
    padding: 22px;
    display: flex;
    flex-direction: column;
    gap: 16px;
    box-shadow: 0 4px 28px rgba(0,0,0,0.3), inset 0 1px 0 rgba(255,255,255,0.04);
    transition: all 300ms ease;
  }
  .tunnel-card:hover {
    border-color: rgba(255,255,255,0.12);
    box-shadow: 0 8px 36px rgba(0,0,0,0.4);
  }

  .card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  .status-indicator {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .pulse-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: #8b949e;
  }

  .active .pulse-dot {
    background: #00c9a7;
    box-shadow: 0 0 10px #00c9a7;
    animation: pulse 2s infinite;
  }
  .error .pulse-dot {
    background: #ff6b6b;
    box-shadow: 0 0 10px #ff6b6b;
  }

  @keyframes pulse {
    0% { transform: scale(0.95); box-shadow: 0 0 0 0 rgba(0,201,167,0.7); }
    70% { transform: scale(1); box-shadow: 0 0 0 8px rgba(0,201,167,0); }
    100% { transform: scale(0.95); box-shadow: 0 0 0 0 rgba(0,201,167,0); }
  }

  .status-label {
    font-size: 14px;
    font-weight: 600;
    color: #e6edf3;
  }

  .view-btn {
    font-size: 13px;
    color: #00c9a7;
    text-decoration: none;
    font-weight: 500;
    transition: opacity 0.2s;
  }
  .view-btn:hover {
    opacity: 0.8;
  }

  .info-section {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .section-title {
    font-size: 11px;
    letter-spacing: 0.05em;
    color: #8b949e;
    font-weight: 600;
  }

  .url-copy-box {
    display: flex;
    background: rgba(13,17,23,0.6);
    border: 1px solid rgba(255,255,255,0.08);
    border-radius: 8px;
    padding: 4px;
    align-items: center;
  }

  .url-input {
    flex: 1;
    background: transparent;
    border: none;
    color: #e6edf3;
    padding: 6px 8px;
    font-size: 13px;
    outline: none;
    font-family: var(--font-mono, monospace);
  }

  .copy-btn {
    background: transparent;
    border: none;
    color: #e6edf3;
    cursor: pointer;
    padding: 6px 10px;
    font-size: 14px;
    border-radius: 6px;
    transition: background 0.2s;
  }
  .copy-btn:hover {
    background: rgba(255,255,255,0.05);
  }

  .stats-row {
    display: flex;
    gap: 24px;
    margin-top: 4px;
  }

  .stat-item {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .stat-label {
    font-size: 11px;
    color: #8b949e;
  }

  .stat-val {
    font-size: 14px;
    font-weight: 600;
    color: #e6edf3;
  }

  .inactive-state {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 6px 0;
  }

  .state-icon {
    font-size: 24px;
  }

  .state-desc {
    font-size: 13px;
    line-height: 1.5;
    color: #8b949e;
    margin: 0;
  }

  .connecting-loader {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 0;
  }

  .spinner {
    width: 16px;
    height: 16px;
    border: 2px solid rgba(0,201,167,0.2);
    border-top-color: #00c9a7;
    border-radius: 50%;
    animation: spin 1s linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  .loader-txt {
    font-size: 13px;
    color: #8b949e;
  }

  .error-msg {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 4px 0;
  }

  .alert-icon {
    font-size: 16px;
  }

  .alert-txt {
    font-size: 13px;
    color: #ff6b6b;
    line-height: 1.4;
  }
</style>
