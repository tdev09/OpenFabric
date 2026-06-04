<script lang="ts">
  import { onMount } from 'svelte';
  import {
    tunnelStatus,
    loadTunnelStatus,
    enableTunnel,
    disableTunnel,
    generatePIN,
    revokePIN,
    updateRelay
  } from '$lib/stores/tunnel';
  import { formatBytes, appConfig } from '$lib/stores/cluster';
  import { fade, slide } from 'svelte/transition';

  let customRelay = '';
  let generatedPin = '';
  let showPinModal = false;
  let enabling = false;
  let disabling = false;
  let updatingRelay = false;
  let errorMsg = '';

  onMount(() => {
    loadTunnelStatus();
    customRelay = $tunnelStatus.relay_url;
  });

  async function handleToggle() {
    errorMsg = '';
    if ($tunnelStatus.state === 'connected') {
      disabling = true;
      try {
        await disableTunnel();
      } catch (e: any) {
        errorMsg = e.message;
      } finally {
        disabling = false;
      }
    } else {
      enabling = true;
      try {
        await enableTunnel();
      } catch (e: any) {
        errorMsg = e.message;
      } finally {
        enabling = false;
      }
    }
  }

  async function handleGeneratePIN() {
    errorMsg = '';
    try {
      generatedPin = await generatePIN();
      showPinModal = true;
    } catch (e: any) {
      errorMsg = e.message;
    }
  }

  async function handleRevokePIN() {
    errorMsg = '';
    try {
      await revokePIN();
      generatedPin = '';
    } catch (e: any) {
      errorMsg = e.message;
    }
  }

  async function handleUpdateRelay() {
    errorMsg = '';
    updatingRelay = true;
    try {
      await updateRelay(customRelay);
    } catch (e: any) {
      errorMsg = e.message;
    } finally {
      updatingRelay = false;
    }
  }

  let copiedUrl = false;
  async function copyUrl() {
    await navigator.clipboard.writeText($tunnelStatus.tunnel_url);
    copiedUrl = true;
    setTimeout(() => copiedUrl = false, 2000);
  }

  let copiedPin = false;
  async function copyPin() {
    await navigator.clipboard.writeText(generatedPin);
    copiedPin = true;
    setTimeout(() => copiedPin = false, 2000);
  }
</script>

<svelte:head>
  <title>Tunnel - {$appConfig.project_name}</title>
  <meta name="description" content="Access your {$appConfig.project_name} cluster dashboard and LLM inference models securely from anywhere in the world." />
</svelte:head>

<div class="page-container" in:fade={{ duration: 200 }}>
  <!-- HEADER -->
  <div class="page-header">
    <div class="title-area">
      <h1 class="page-title">Fabric Tunnel</h1>
      <p class="page-subtitle">Access your cluster dashboard and LLM inference models securely from anywhere in the world.</p>
    </div>
    
    <button 
      class="toggle-btn" 
      class:active={$tunnelStatus.state === 'connected'} 
      class:connecting={$tunnelStatus.state === 'connecting'} 
      on:click={handleToggle}
      disabled={enabling || disabling}
    >
      {#if enabling}
        Enabling…
      {:else}
        {$tunnelStatus.state === 'connected' ? 'Disable Tunnel' : 'Enable Tunnel'}
      {/if}
    </button>
  </div>

  {#if errorMsg}
    <div class="error-banner" transition:slide>
      <span>⚠️ Error: {errorMsg}</span>
    </div>
  {/if}

  <!-- TUNNEL METRICS GRID -->
  <div class="grid-layout">
    <!-- Tunnel Info Card -->
    <div class="glass-card main-info-card">
      <h2 class="card-title">Tunnel Connection</h2>
      
      <div class="info-grid">
        <div class="info-item">
          <span class="info-label">VPN IP Address</span>
          <span class="info-val">{$tunnelStatus.assigned_ip || '-'}</span>
        </div>
        <div class="info-item">
          <span class="info-label">WireGuard Public Key</span>
          <span class="info-val truncate">{$tunnelStatus.public_key || '-'}</span>
        </div>
        <div class="info-item">
          <span class="info-label">Relay Endpoint</span>
          <span class="info-val">{$tunnelStatus.relay_url || '-'}</span>
        </div>
        <div class="info-item">
          <span class="info-label">Session ID</span>
          <span class="info-val truncate">{$tunnelStatus.tunnel_id || '-'}</span>
        </div>
      </div>

      {#if $tunnelStatus.state === 'connected'}
        <div class="share-url-section" transition:slide>
          <span class="info-label">REMOTE ACCESS URL</span>
          <div class="url-copy-box">
            <input type="text" readonly value={$tunnelStatus.tunnel_url} />
            <button on:click={copyUrl}>{copiedUrl ? 'Copied' : 'Copy Link'}</button>
          </div>
          <span class="helper-txt">Share this URL to access your cluster dashboard from mobile or remote devices.</span>
        </div>
      {/if}
    </div>

    <!-- Security Control Card -->
    <div class="glass-card security-card">
      <h2 class="card-title">Security & Access Control</h2>
      <p class="card-desc">Protect your dashboard with a 6-digit session PIN. Remote browser connections will be challenged before accessing cluster resources.</p>
      
      <div class="pin-status-box">
        <div class="status-row">
          <span class="status-label">PIN Security status</span>
          <span class="status-badge" class:enabled={$tunnelStatus.pin_enabled}>
            {$tunnelStatus.pin_enabled ? 'Active' : 'Disabled'}
          </span>
        </div>

        <div class="action-row">
          <button class="btn btn-primary" on:click={handleGeneratePIN}>
            {$tunnelStatus.pin_enabled ? 'Regenerate PIN' : 'Generate PIN'}
          </button>
          {#if $tunnelStatus.pin_enabled}
            <button class="btn btn-secondary" on:click={handleRevokePIN}>Revoke PIN</button>
          {/if}
        </div>
      </div>

      <div class="glass-card nested-card">
        <h3 class="nested-title">Relay Configuration</h3>
        <p class="nested-desc">Configure a custom or self-hosted WireGuard connection broker relay.</p>
        
        <div class="relay-input-box">
          <input type="text" bind:value={customRelay} placeholder={"relay." + $appConfig.project_domain + ":51820"} />
          <button on:click={handleUpdateRelay} disabled={updatingRelay}>
            {updatingRelay ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  </div>

  <!-- PEERS SECTION -->
  <div class="peers-section">
    <h2 class="section-title">Connected Remote Clients</h2>
    
    {#if $tunnelStatus.state === 'connected'}
      {#if $tunnelStatus.peers.length > 0}
        <div class="peers-grid">
          {#each $tunnelStatus.peers as peer}
            <div class="peer-card">
              <div class="peer-header">
                <span class="peer-ip">{peer.tunnel_ip}</span>
                <span class="latency-badge" class:slow={peer.latency_ms > 150}>
                  {peer.latency_ms >= 0 ? `${peer.latency_ms} ms` : 'offline'}
                </span>
              </div>
              <div class="peer-stats">
                <div class="stat">
                  <span class="stat-label">Uploaded</span>
                  <span class="stat-val">{formatBytes(peer.bytes_tx)}</span>
                </div>
                <div class="stat">
                  <span class="stat-label">Downloaded</span>
                  <span class="stat-val">{formatBytes(peer.bytes_rx)}</span>
                </div>
              </div>
              <div class="peer-footer">
                <span class="key-lbl">Public Key</span>
                <span class="key-val truncate">{peer.public_key}</span>
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <div class="empty-peers">
          <span>💤 No active remote connections yet. Activate the VPN client config on your phone/laptop to connect.</span>
        </div>
      {/if}
    {:else}
      <div class="empty-peers">
        <span>🔒 Enable the Fabric Tunnel to establish client links.</span>
      </div>
    {/if}
  </div>
</div>

<!-- PIN DISPLAY MODAL -->
{#if showPinModal}
  <div class="modal-backdrop" transition:fade={{ duration: 150 }}>
    <div class="modal-content" transition:slide>
      <div class="modal-header">
        <h2>Your Browser PIN Generated</h2>
        <button class="close-btn" on:click={() => showPinModal = false}>×</button>
      </div>
      
      <div class="modal-body">
        <p class="modal-desc">Use this 6-digit code to log in when prompted on remote browsers. **It will only be shown once.**</p>
        
        <div class="pin-display">
          <span>{generatedPin}</span>
          <button class="copy-pin-btn" on:click={copyPin}>{copiedPin ? 'Copied' : 'Copy PIN'}</button>
        </div>
        
        <div class="modal-alert">
          <span>⚠️ Do not share this PIN. Anyone with this PIN can access your local model configurations and runs.</span>
        </div>
      </div>

      <div class="modal-footer">
        <button class="btn btn-primary" on:click={() => showPinModal = false}>Got It</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .page-container {
    max-width: 100%;
    padding: var(--space-4) 0;
    display: flex;
    flex-direction: column;
    gap: 30px;
  }

  .page-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-bottom: 1px solid var(--border);
    padding-bottom: 20px;
  }

  .page-title {
    font-size: 26px;
    font-weight: 700;
    color: var(--text-primary);
    margin: 0 0 8px;
    letter-spacing: -0.02em;
  }

  .page-subtitle {
    font-size: 14px;
    color: var(--text-secondary);
    margin: 0;
  }

  .toggle-btn {
    background: rgba(255,255,255,0.05);
    border: 1px solid rgba(255,255,255,0.1);
    color: var(--text-primary);
    padding: 12px 24px;
    border-radius: var(--radius-md);
    font-weight: 600;
    font-size: 14px;
    cursor: pointer;
    transition: all 0.2s;
  }
  .toggle-btn:hover {
    background: rgba(255,255,255,0.08);
  }
  .toggle-btn.active {
    background: #00c9a7;
    color: #0d1117;
    border-color: #00c9a7;
  }
  .toggle-btn.active:hover {
    background: #00b395;
  }

  .error-banner {
    background: rgba(255, 107, 107, 0.15);
    border: 1px solid #ff6b6b;
    border-radius: 8px;
    padding: 14px 20px;
    color: #ff6b6b;
    font-size: 14px;
  }

  .grid-layout {
    display: grid;
    grid-template-columns: 1.2fr 1fr;
    gap: 24px;
  }

  @media (max-width: 900px) {
    .grid-layout {
      grid-template-columns: 1fr;
    }
  }

  .glass-card {
    background: linear-gradient(145deg, rgba(18,26,44,0.7) 0%, rgba(12,18,32,0.55) 100%);
    border: 1px solid rgba(255,255,255,0.07);
    border-radius: 18px;
    padding: 24px;
    box-shadow: 0 4px 28px rgba(0,0,0,0.3);
  }

  .card-title {
    font-size: 18px;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0 0 16px;
  }

  .card-desc {
    font-size: 13px;
    line-height: 1.5;
    color: var(--text-secondary);
    margin: 0 0 20px;
  }

  .info-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 20px;
  }

  .info-item {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .info-label {
    font-size: 11px;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    font-weight: 600;
  }

  .info-val {
    font-size: 14px;
    font-weight: 600;
    color: var(--text-primary);
    font-family: var(--font-mono, monospace);
  }

  .truncate {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .share-url-section {
    margin-top: 24px;
    padding-top: 20px;
    border-top: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .url-copy-box {
    display: flex;
    background: rgba(13,17,23,0.6);
    border: 1px solid rgba(255,255,255,0.08);
    border-radius: 8px;
    padding: 4px;
    align-items: center;
  }

  .url-copy-box input {
    flex: 1;
    background: transparent;
    border: none;
    color: #e6edf3;
    padding: 8px 10px;
    font-size: 13px;
    outline: none;
    font-family: var(--font-mono, monospace);
  }

  .url-copy-box button {
    background: #00c9a7;
    border: none;
    color: #0d1117;
    padding: 8px 16px;
    font-size: 12px;
    font-weight: 600;
    border-radius: 6px;
    cursor: pointer;
    transition: background 0.2s;
  }
  .url-copy-box button:hover {
    background: #00b395;
  }

  .helper-txt {
    font-size: 11px;
    color: var(--text-muted);
    line-height: 1.4;
  }

  .pin-status-box {
    display: flex;
    flex-direction: column;
    gap: 16px;
    margin-bottom: 24px;
  }

  .status-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    background: rgba(255,255,255,0.02);
    padding: 12px 16px;
    border-radius: 8px;
    border: 1px solid var(--border);
  }

  .status-label {
    font-size: 13px;
    color: var(--text-secondary);
  }

  .status-badge {
    background: rgba(255, 107, 107, 0.1);
    color: #ff6b6b;
    border: 1px solid rgba(255, 107, 107, 0.2);
    font-size: 12px;
    padding: 4px 10px;
    border-radius: 12px;
    font-weight: 600;
  }
  .status-badge.enabled {
    background: rgba(0, 201, 167, 0.1);
    color: #00c9a7;
    border-color: rgba(0, 201, 167, 0.2);
  }

  .action-row {
    display: flex;
    gap: 12px;
  }

  .btn {
    flex: 1;
    padding: 10px 16px;
    border-radius: 8px;
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    transition: all 0.2s;
  }

  .btn-primary {
    background: #00c9a7;
    border: 1px solid #00c9a7;
    color: #0d1117;
  }
  .btn-primary:hover {
    background: #00b395;
  }

  .btn-secondary {
    background: transparent;
    border: 1px solid rgba(255,255,255,0.1);
    color: var(--text-primary);
  }
  .btn-secondary:hover {
    background: rgba(255,255,255,0.05);
  }

  .nested-card {
    background: rgba(255,255,255,0.015);
    border: 1px solid rgba(255,255,255,0.04);
    border-radius: 12px;
    padding: 16px;
  }

  .nested-title {
    font-size: 14px;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0 0 6px;
  }

  .nested-desc {
    font-size: 12px;
    color: var(--text-muted);
    line-height: 1.4;
    margin: 0 0 14px;
  }

  .relay-input-box {
    display: flex;
    background: rgba(0,0,0,0.3);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 4px;
  }

  .relay-input-box input {
    flex: 1;
    background: transparent;
    border: none;
    color: var(--text-primary);
    font-size: 13px;
    padding: 8px;
    outline: none;
  }

  .relay-input-box button {
    background: rgba(255,255,255,0.05);
    border: 1px solid rgba(255,255,255,0.1);
    color: var(--text-primary);
    padding: 6px 14px;
    font-size: 12px;
    font-weight: 600;
    border-radius: 6px;
    cursor: pointer;
    transition: all 0.2s;
  }
  .relay-input-box button:hover {
    background: rgba(255,255,255,0.1);
  }

  .peers-section {
    display: flex;
    flex-direction: column;
    gap: 16px;
    margin-top: 10px;
  }

  .section-title {
    font-size: 18px;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0;
  }

  .peers-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 20px;
  }

  .peer-card {
    background: linear-gradient(145deg, rgba(25,35,55,0.5) 0%, rgba(15,22,38,0.4) 100%);
    border: 1px solid rgba(255,255,255,0.06);
    border-radius: 12px;
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .peer-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  .peer-ip {
    font-size: 14px;
    font-weight: 700;
    color: var(--text-primary);
    font-family: var(--font-mono, monospace);
  }

  .latency-badge {
    background: rgba(0, 201, 167, 0.1);
    color: #00c9a7;
    border: 1px solid rgba(0, 201, 167, 0.2);
    font-size: 11px;
    padding: 2px 8px;
    border-radius: 10px;
    font-weight: 600;
  }
  .latency-badge.slow {
    background: rgba(255, 160, 0, 0.1);
    color: #ffa000;
    border-color: rgba(255, 160, 0, 0.2);
  }

  .peer-stats {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
    border-top: 1px solid rgba(255,255,255,0.04);
    border-bottom: 1px solid rgba(255,255,255,0.04);
    padding: 12px 0;
  }

  .stat {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .peer-footer {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .key-lbl {
    font-size: 10px;
    color: var(--text-muted);
  }

  .key-val {
    font-size: 12px;
    font-family: var(--font-mono, monospace);
    color: var(--text-secondary);
  }

  .empty-peers {
    background: rgba(255,255,255,0.02);
    border: 1px dashed var(--border);
    border-radius: 12px;
    padding: 30px;
    text-align: center;
    color: var(--text-muted);
    font-size: 14px;
  }

  /* MODAL */
  .modal-backdrop {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    background: rgba(0,0,0,0.75);
    backdrop-filter: blur(8px);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 2000;
  }

  .modal-content {
    background: #161b22;
    border: 1px solid #30363d;
    border-radius: 14px;
    width: 400px;
    max-width: 90%;
    overflow: hidden;
    box-shadow: 0 8px 32px rgba(0,0,0,0.8);
  }

  .modal-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 18px 24px;
    border-bottom: 1px solid var(--border);
  }

  .modal-header h2 {
    font-size: 16px;
    margin: 0;
    color: var(--text-primary);
  }

  .close-btn {
    background: transparent;
    border: none;
    color: var(--text-muted);
    font-size: 24px;
    cursor: pointer;
  }

  .modal-body {
    padding: 24px;
    display: flex;
    flex-direction: column;
    gap: 20px;
  }

  .modal-desc {
    font-size: 13px;
    color: var(--text-secondary);
    line-height: 1.5;
    margin: 0;
  }

  .pin-display {
    display: flex;
    align-items: center;
    justify-content: space-between;
    background: #0d1117;
    border: 1px solid #30363d;
    border-radius: 10px;
    padding: 14px 20px;
  }

  .pin-display span {
    font-size: 32px;
    font-weight: 700;
    color: #00c9a7;
    letter-spacing: 4px;
    font-family: var(--font-mono, monospace);
  }

  .copy-pin-btn {
    background: rgba(255,255,255,0.05);
    border: 1px solid rgba(255,255,255,0.1);
    color: var(--text-primary);
    padding: 6px 12px;
    font-size: 12px;
    border-radius: 6px;
    cursor: pointer;
    font-weight: 600;
  }

  .modal-alert {
    background: rgba(255, 160, 0, 0.1);
    border: 1px solid rgba(255, 160, 0, 0.2);
    border-radius: 8px;
    padding: 12px;
    color: #ffa000;
    font-size: 12px;
    line-height: 1.4;
  }

  .modal-footer {
    display: flex;
    justify-content: flex-end;
    padding: 16px 24px;
    border-top: 1px solid var(--border);
    background: rgba(0,0,0,0.1);
  }
</style>
