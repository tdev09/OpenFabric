<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import QRCode from 'qrcode';
  import { nodes } from '$lib/stores/cluster';

  let activeTab: 'code' | 'command' | 'link' | 'qr' = 'code';
  let token = '';
  let shortCode = '';
  let expiresAt = '';
  let joinUrl = '';
  let cliCommand = '';
  let countdownText = '00:00';
  let remainingSeconds = 0;
  let timerId: any;
  let isRefreshing = false;
  let qrCanvas: HTMLCanvasElement;

  // Join tracking
  let initialNodeIds = new Set<string>();
  let newlyJoinedNodeName = '';
  let joinSuccess = false;
  let nodeUnsubscribe: () => void;

  // Copy feedback
  let copiedCommand = false;
  let copiedLink = false;
  let copiedCode = false;
  let copiedP2PToken = false;

  function copyP2PToken() {
    navigator.clipboard.writeText(token).then(() => {
      copiedP2PToken = true;
      setTimeout(() => copiedP2PToken = false, 1500);
    });
  }

  async function fetchToken() {
    if (isRefreshing) return;
    isRefreshing = true;
    try {
      const res = await fetch('/api/cluster/join-token');
      if (!res.ok) throw new Error('Failed to fetch join token');
      const data = await res.json();
      token = data.token;
      shortCode = data.short_code || '';
      expiresAt = data.expires_at;
      joinUrl = data.join_url;
      cliCommand = data.cli_command;

      // Reset timer
      remainingSeconds = Math.max(0, Math.floor((new Date(expiresAt).getTime() - Date.now()) / 1000));
      updateCountdown();

      if (activeTab === 'qr') {
        setTimeout(drawQR, 50);
      }
    } catch (err) {
      console.error(err);
    } finally {
      isRefreshing = false;
    }
  }

  function updateCountdown() {
    if (remainingSeconds <= 0) {
      countdownText = 'Expired';
      fetchToken(); // Auto renew if expired
      return;
    }

    const minutes = Math.floor(remainingSeconds / 60);
    const seconds = remainingSeconds % 60;
    countdownText = `${minutes}:${seconds < 10 ? '0' : ''}${seconds}`;

    // Auto-refresh 60 seconds before expiry
    if (remainingSeconds === 60) {
      fetchToken();
    }
  }

  function startTimer() {
    clearInterval(timerId);
    timerId = setInterval(() => {
      if (remainingSeconds > 0) {
        remainingSeconds--;
        updateCountdown();
      }
    }, 1000);
  }

  function setTab(tab: 'code' | 'command' | 'link' | 'qr') {
    activeTab = tab;
    if (tab === 'qr') {
      setTimeout(drawQR, 50);
    }
  }

  function drawQR() {
    if (!qrCanvas || !joinUrl) return;
    QRCode.toCanvas(qrCanvas, joinUrl, {
      width: 140,
      margin: 1,
      color: {
        dark: '#1C2128', // dark blocks
        light: '#FFFFFF' // white background
      }
    }, (err) => {
      if (err) console.error('QR code generation failed:', err);
    });
  }

  function copyText(text: string, type: 'command' | 'link' | 'code') {
    navigator.clipboard.writeText(text).then(() => {
      if (type === 'command') {
        copiedCommand = true;
        setTimeout(() => copiedCommand = false, 1500);
      } else if (type === 'link') {
        copiedLink = true;
        setTimeout(() => copiedLink = false, 1500);
      } else if (type === 'code') {
        copiedCode = true;
        setTimeout(() => copiedCode = false, 1500);
      }
    });
  }

  onMount(() => {
    // Capture existing nodes
    initialNodeIds = new Set($nodes.map(n => n.id));

    // Listen to updates
    nodeUnsubscribe = nodes.subscribe(currentNodes => {
      const newJoined = currentNodes.find(n => !initialNodeIds.has(n.id));
      if (newJoined) {
        newlyJoinedNodeName = newJoined.name;
        joinSuccess = true;
        initialNodeIds.add(newJoined.id);
        
        // Reset success state after 4 seconds and refresh token
        setTimeout(() => {
          joinSuccess = false;
          newlyJoinedNodeName = '';
          fetchToken();
        }, 4000);
      }
    });

    fetchToken().then(startTimer);
  });

  onDestroy(() => {
    clearInterval(timerId);
    if (nodeUnsubscribe) nodeUnsubscribe();
  });
</script>

<div class="card add-device-card">
  <h3>Add a device to your cluster</h3>
  <p class="desc text-secondary">Connect computers, servers, or mobile screens to the cluster.</p>

  <div class="tabs">
    <button class="tab-btn" class:active={activeTab === 'code'} on:click={() => setTab('code')}>
      Short Code
    </button>
    <button class="tab-btn" class:active={activeTab === 'command'} on:click={() => setTab('command')}>
      Command
    </button>
    <button class="tab-btn" class:active={activeTab === 'link'} on:click={() => setTab('link')}>
      Link
    </button>
    <button class="tab-btn" class:active={activeTab === 'qr'} on:click={() => setTab('qr')}>
      QR Code
    </button>
  </div>

  <div class="tab-content">
    {#if activeTab === 'code'}
      <div class="code-tab animate-fade-in">
        <p class="tab-instruction">On the other device, open OpenFabric and enter:</p>
        
        <div class="code-display-container">
          <div class="display-code" class:copied={copiedCode} title="Click to copy join code" on:click={() => copyText('fabric-' + shortCode, 'code')}>
            {#if shortCode}
              fabric-{shortCode}
            {:else}
              fabric-......
            {/if}
          </div>
          <button class="btn btn-ghost copy-inline" on:click={() => copyText('fabric-' + shortCode, 'code')}>
            {copiedCode ? 'Copied!' : 'Copy'}
          </button>
        </div>

        <div class="code-meta">
          <span class="expiry-timer text-secondary">
            Expires in <strong class="mono text-accent">{countdownText}</strong>
          </span>
          <div class="code-actions">
            <button class="btn btn-secondary btn-xs" on:click={copyP2PToken} disabled={!token}>
              {copiedP2PToken ? 'Copied P2P Token!' : 'Copy P2P Token'}
            </button>
            <button class="btn btn-secondary btn-xs refresh-btn" on:click={fetchToken} disabled={isRefreshing}>
              {isRefreshing ? 'Generating...' : 'Generate new code'}
            </button>
          </div>
        </div>
      </div>
    {:else if activeTab === 'command'}
      <div class="command-tab animate-fade-in">
        <p class="tab-instruction">Install OpenFabric and run this command in terminal:</p>
        <div class="copy-box">
          <code class="mono">{cliCommand}</code>
          <button class="btn btn-primary btn-sm copy-btn" on:click={() => copyText(cliCommand, 'command')}>
            {copiedCommand ? 'Copied!' : 'Copy'}
          </button>
        </div>
        <p class="tab-hint">Perfect for headlessly onboarding Raspberry Pis or Linux servers.</p>
      </div>
    {:else if activeTab === 'link'}
      <div class="link-tab animate-fade-in">
        <p class="tab-instruction">Send this link to the device you want to add:</p>
        <div class="copy-box">
          <code class="mono">{joinUrl}</code>
          <button class="btn btn-primary btn-sm copy-btn" on:click={() => copyText(joinUrl, 'link')}>
            {copiedLink ? 'Copied!' : 'Copy'}
          </button>
        </div>
        <p class="tab-hint">Opens a secure web page showing OS download links and automatic join instructions.</p>
      </div>
    {:else if activeTab === 'qr'}
      <div class="qr-tab animate-fade-in">
        <p class="tab-instruction">Scan with a camera to download the app or view dashboard:</p>
        <div class="qr-container">
          <canvas bind:this={qrCanvas}></canvas>
        </div>
        <p class="tab-hint">Scan with any smartphone to open the dashboard immediately.</p>
      </div>
    {/if}
  </div>

  <div class="join-status">
    {#if joinSuccess}
      <div class="status-alert success animate-fade-in">
        <span class="status-icon">✓</span>
        <span class="status-msg"><strong>{newlyJoinedNodeName}</strong> successfully joined the cluster!</span>
      </div>
    {:else}
      <div class="status-alert pending">
        <span class="status-spinner animate-spin">⟳</span>
        <span class="status-msg text-muted">Waiting for device to join...</span>
      </div>
    {/if}
  </div>
</div>

<style>
  .add-device-card {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 380px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
  }

  h3 {
    font-size: var(--text-lg);
    font-weight: 600;
  }

  .desc {
    font-size: var(--text-sm);
    margin-bottom: var(--space-4);
  }

  .tabs {
    display: flex;
    gap: var(--space-1);
    border-bottom: 1px solid var(--border);
    margin-bottom: var(--space-4);
    padding-bottom: var(--space-1);
    flex-wrap: wrap;
  }

  .tab-btn {
    background: transparent;
    border: none;
    color: var(--text-secondary);
    padding: var(--space-2) var(--space-3);
    font-size: var(--text-sm);
    font-weight: 500;
    cursor: pointer;
    border-bottom: 2px solid transparent;
    transition: all var(--transition);
  }

  .tab-btn:hover {
    color: var(--accent);
  }

  .tab-btn.active {
    color: var(--accent);
    border-bottom-color: var(--accent);
  }

  .tab-content {
    flex-grow: 1;
    display: flex;
    flex-direction: column;
    justify-content: center;
  }

  .tab-instruction {
    font-size: var(--text-sm);
    color: var(--text-secondary);
    margin-bottom: var(--space-3);
  }

  .tab-hint {
    font-size: var(--text-xs);
    color: var(--text-muted);
    margin-top: var(--space-3);
  }

  /* Short Code Tab */
  .code-display-container {
    display: flex;
    align-items: center;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-3) var(--space-4);
    margin-bottom: var(--space-4);
    justify-content: space-between;
    gap: var(--space-2);
    overflow: hidden;
  }

  .display-code {
    font-family: var(--font-mono);
    font-size: var(--text-lg);
    font-weight: 600;
    color: var(--accent);
    letter-spacing: 0.05em;
    cursor: pointer;
    user-select: all;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    flex-grow: 1;
    min-width: 0;
  }

  .display-code:hover {
    opacity: 0.8;
  }

  .copy-inline {
    flex-shrink: 0;
  }

  .code-meta {
    display: flex;
    align-items: center;
    justify-content: space-between;
    font-size: var(--text-sm);
    flex-wrap: wrap;
    gap: var(--space-2);
  }

  .code-actions {
    display: flex;
    gap: var(--space-2);
    align-items: center;
  }

  .btn-xs {
    padding: var(--space-1) var(--space-3);
    font-size: var(--text-xs);
  }

  /* Command & Link Copy Boxes */
  .copy-box {
    display: flex;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-3);
    align-items: center;
    gap: var(--space-3);
    justify-content: space-between;
    max-width: 100%;
    overflow: hidden;
  }

  .copy-box code {
    white-space: nowrap;
    overflow-x: auto;
    flex-grow: 1;
    color: var(--text-primary);
    padding-right: var(--space-2);
  }

  .copy-btn {
    flex-shrink: 0;
  }

  /* QR Code Tab */
  .qr-tab {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
  }

  .qr-container {
    background: #1C2128;
    padding: var(--space-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    margin: var(--space-2) 0;
  }

  /* Join Status Section */
  .join-status {
    margin-top: var(--space-6);
    border-top: 1px solid var(--border);
    padding-top: var(--space-4);
  }

  .status-alert {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    font-size: var(--text-sm);
  }

  .status-spinner {
    font-size: var(--text-lg);
    display: inline-block;
    color: var(--accent);
  }

  .status-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    background: var(--accent-dim);
    color: var(--accent);
    border-radius: 50%;
    font-weight: bold;
    flex-shrink: 0;
  }

  .status-alert.success {
    color: var(--accent);
  }

  @media (max-width: 480px) {
    .tab-btn {
      padding: var(--space-2) var(--space-2);
      font-size: var(--text-xs);
    }
    .display-code {
      font-size: var(--text-lg);
    }
    .code-display-container {
      padding: var(--space-2) var(--space-3);
    }
  }
</style>
