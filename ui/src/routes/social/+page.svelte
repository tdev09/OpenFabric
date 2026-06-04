<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { fade, slide } from 'svelte/transition';
  import { appConfig } from '$lib/stores/cluster';

  // ── State ────────────────────────────────────────────────────────────────────
  let maxVRAMGB  = 4;
  let durationHours = 24;

  let generatedToken = '';
  let qrCanvas: HTMLCanvasElement;

  let borrowToken  = '';
  let borrowing    = false;
  let lending      = false;
  let revokingId   = '';
  let copiedToken  = false;
  let borrowSuccess = '';
  let borrowError   = '';
  let lendError     = '';

  let lentSessions:     any[] = [];
  let borrowedSessions: any[] = [];
  let loadingSessions   = true;
  let intervalId: any;

  // active tab: 'lend' | 'borrow'
  let activeTab: 'lend' | 'borrow' = 'lend';

  // ── Lifecycle ────────────────────────────────────────────────────────────────
  onMount(() => {
    fetchSessions();
    intervalId = setInterval(fetchSessions, 5000);
  });
  onDestroy(() => { if (intervalId) clearInterval(intervalId); });

  // ── API ──────────────────────────────────────────────────────────────────────
  async function fetchSessions() {
    try {
      const res = await fetch('/api/social/sessions');
      if (res.ok) {
        const data = await res.json();
        lentSessions     = data.lent     || [];
        borrowedSessions = data.borrowed || [];
      }
    } catch (e) {
      console.error('Failed to fetch social sessions:', e);
    } finally {
      loadingSessions = false;
    }
  }

  async function handleLend() {
    lendError = '';
    lending   = true;
    try {
      const res = await fetch('/api/social/lend', {
        method:  'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          max_vram_bytes:   maxVRAMGB * 1024 * 1024 * 1024,
          duration_seconds: durationHours * 3600,
          allowed_tasks:    ['wasm']
        })
      });
      if (!res.ok) throw new Error((await res.text()) || 'Failed to generate invite code');
      const data    = await res.json();
      generatedToken = data.token;
      setTimeout(() => { if (qrCanvas && generatedToken) drawQR(qrCanvas, generatedToken); }, 50);
      fetchSessions();
    } catch (e: any) {
      lendError = e.message;
    } finally {
      lending = false;
    }
  }

  async function handleBorrow() {
    borrowError   = '';
    borrowSuccess = '';
    borrowing     = true;
    try {
      const res = await fetch('/api/social/borrow', {
        method:  'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: borrowToken })
      });
      if (!res.ok) throw new Error((await res.text()) || 'Failed to connect to lender');
      const data    = await res.json();
      borrowSuccess = `Connected to ${data.label}! Remote VRAM is now pooled into your cluster.`;
      borrowToken   = '';
      fetchSessions();
    } catch (e: any) {
      borrowError = e.message;
    } finally {
      borrowing = false;
    }
  }

  async function handleRevoke(id: string) {
    revokingId = id;
    try {
      const res = await fetch(`/api/social/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' });
      if (res.ok) fetchSessions();
    } catch (e) {
      console.error('Revoke failed:', e);
    } finally {
      revokingId = '';
    }
  }

  async function copyToken() {
    await navigator.clipboard.writeText(generatedToken);
    copiedToken = true;
    setTimeout(() => copiedToken = false, 2000);
  }

  // ── QR helper ────────────────────────────────────────────────────────────────
  function drawQR(canvas: HTMLCanvasElement, text: string) {
    const ctx  = canvas.getContext('2d');
    if (!ctx) return;
    const size = 148;
    canvas.width  = size;
    canvas.height = size;

    ctx.fillStyle = '#FFFFFF';
    ctx.fillRect(0, 0, size, size);

    const eye = (x: number, y: number) => {
      ctx.fillStyle = '#0D1117'; ctx.fillRect(x, y, 32, 32);
      ctx.fillStyle = '#FFFFFF'; ctx.fillRect(x+6, y+6, 20, 20);
      ctx.fillStyle = '#0D1117'; ctx.fillRect(x+10, y+10, 12, 12);
    };
    eye(8, 8); eye(108, 8); eye(8, 108);

    let hash = 0;
    for (let i = 0; i < text.length; i++) hash = text.charCodeAt(i) + ((hash << 5) - hash);
    for (let x = 0; x < 25; x++) {
      for (let y = 0; y < 25; y++) {
        if ((x<9&&y<9)||(x>15&&y<9)||(x<9&&y>15)) continue;
        const v = Math.abs((hash >> ((x+y)%32)) & 1);
        const n = Math.abs(Math.sin(x*12.9898+y*78.233)*43758.5453 % 1);
        if ((v^(n>0.5?1:0))===1) {
          ctx.fillStyle = '#0D1117';
          ctx.fillRect(8 + x*5, 8 + y*5, 5, 5);
        }
      }
    }
  }

  // ── Formatters ───────────────────────────────────────────────────────────────
  function fmtBytes(b: number): string {
    if (b >= 1073741824) return `${(b/1073741824).toFixed(1)} GB`;
    if (b >= 1048576)    return `${(b/1048576).toFixed(0)} MB`;
    return `${b} B`;
  }
  function fmtExpiry(expireTime: string): string {
    const rem = new Date(expireTime).getTime() - Date.now();
    if (rem <= 0) return 'Expired';
    const mins = Math.floor(rem / 60000);
    if (mins < 60) return `${mins}m left`;
    const hrs = Math.floor(mins / 60);
    return `${hrs}h ${mins%60}m left`;
  }

  $: totalSessions = lentSessions.length + borrowedSessions.length;
</script>

<svelte:head>
  <title>Rent a Brain – {$appConfig.project_name}</title>
  <meta name="description" content="Decentralised P2P social compute - share or borrow spare VRAM/RAM across the WAN." />
</svelte:head>

<div class="social-page animate-fade-in">

  <!-- ── Page Header ──────────────────────────────────────────────────────────── -->
  <div class="section-header">
    <div>
      <div class="page-eyebrow">
        <span class="eyebrow-dot"></span>
        <span>Social Compute</span>
      </div>
      <h1>Rent a Brain</h1>
      <p class="text-secondary" style="margin-top:4px; font-size:var(--text-sm)">
        {totalSessions} active link{totalSessions !== 1 ? 's' : ''} ·
        Decentralised P2P compute sharing over the WAN
      </p>
    </div>

    <!-- Status pill -->
    <div class="status-pill">
      <span class="status-dot"></span>
      <span>WASM sandbox enforced</span>
      <span class="shield">🛡️</span>
    </div>
  </div>

  <!-- ── Tab Switcher ──────────────────────────────────────────────────────────── -->
  <div class="tab-group" role="group" aria-label="Social compute mode">
    <button
      class="tab-btn"
      class:active={activeTab === 'lend'}
      on:click={() => { activeTab = 'lend'; lendError = ''; }}
      id="lend-tab-btn"
      aria-pressed={activeTab === 'lend'}
    >
      <span class="tab-icon">📡</span>
      Share Compute
      <span class="tab-badge" class:lit={lentSessions.length > 0}>{lentSessions.length}</span>
    </button>
    <button
      class="tab-btn"
      class:active={activeTab === 'borrow'}
      on:click={() => { activeTab = 'borrow'; borrowError = ''; borrowSuccess = ''; }}
      id="borrow-tab-btn"
      aria-pressed={activeTab === 'borrow'}
    >
      <span class="tab-icon">🔗</span>
      Borrow Compute
      <span class="tab-badge" class:lit={borrowedSessions.length > 0}>{borrowedSessions.length}</span>
    </button>
  </div>

  <!-- ── LEND PANEL ───────────────────────────────────────────────────────────── -->
  {#if activeTab === 'lend'}
    <div class="brain-panel lend-panel" in:fade={{ duration: 160 }}>

      <!-- Panel header -->
      <div class="panel-header">
        <div class="panel-icon-wrap lend-icon-wrap">
          <svg class="panel-svg" viewBox="0 0 40 40" fill="none">
            <circle cx="20" cy="20" r="18" stroke="url(#lg1)" stroke-width="1.5" fill="url(#lbg1)" />
            <text x="50%" y="55%" dominant-baseline="middle" text-anchor="middle" fill="white" font-size="14">📡</text>
            <defs>
              <linearGradient id="lg1" x1="0" y1="0" x2="40" y2="40">
                <stop offset="0%" stop-color="#00C9A7"/>
                <stop offset="100%" stop-color="#005f9e"/>
              </linearGradient>
              <linearGradient id="lbg1" x1="0" y1="0" x2="40" y2="40">
                <stop offset="0%" stop-color="rgba(0,201,167,0.15)"/>
                <stop offset="100%" stop-color="rgba(0,95,158,0.1)"/>
              </linearGradient>
            </defs>
          </svg>
        </div>
        <div>
          <div class="panel-title">Share Your Compute</div>
          <div class="panel-subtitle">Generate an invite token - your friend uses it to mount your spare VRAM into their cluster.</div>
        </div>
        <div class="panel-badges">
          <span class="pbadge">🔒 Sandboxed</span>
          <span class="pbadge">🚫 No FS Access</span>
          <span class="pbadge">⚡ P2P Hole-punch</span>
        </div>
      </div>

      <!-- Controls -->
      <div class="panel-body">

        {#if lendError}
          <div class="banner banner-error" transition:slide>⚠️ {lendError}</div>
        {/if}

        <!-- VRAM Slider -->
        <div class="field">
          <div class="field-header">
            <label class="field-label" for="vram-slider">Max Borrowable Memory</label>
            <span class="field-value accent">{maxVRAMGB} GB</span>
          </div>
          <input
            id="vram-slider"
            type="range"
            min="1" max="64" step="1"
            bind:value={maxVRAMGB}
            class="slider"
          />
          <div class="slider-ticks">
            <span>1 GB</span><span>16 GB</span><span>32 GB</span><span>64 GB</span>
          </div>
        </div>

        <!-- Duration -->
        <div class="field">
          <label class="field-label" for="duration-select">Invite Token Lifetime</label>
          <select id="duration-select" bind:value={durationHours} class="input">
            <option value={1}>1 Hour</option>
            <option value={4}>4 Hours</option>
            <option value={12}>12 Hours</option>
            <option value={24}>24 Hours (Recommended)</option>
            <option value={72}>72 Hours</option>
            <option value={168}>7 Days</option>
          </select>
        </div>

        <!-- Security scope (read-only info) -->
        <div class="security-grid">
          <div class="security-item">
            <span class="sec-ok">✓</span>
            <span>Filesystem isolated to virtual <code>/storage</code></span>
          </div>
          <div class="security-item">
            <span class="sec-ok">✓</span>
            <span>Network sockets blocked (air-gapped)</span>
          </div>
          <div class="security-item">
            <span class="sec-ok">✓</span>
            <span>WASM-only tasks, memory capped</span>
          </div>
          <div class="security-item">
            <span class="sec-ok">✓</span>
            <span>P2P routing, no port forwarding needed</span>
          </div>
        </div>

        <!-- Generate button -->
        <button
          class="btn btn-primary"
          class:loading={lending}
          disabled={lending}
          on:click={handleLend}
          id="generate-invite-btn"
          style="width:100%; justify-content: center;"
        >
          {#if lending}
            <span class="btn-spinner"></span> Generating Invite…
          {:else}
            <span>✦</span> Generate Invite Code
          {/if}
        </button>

        <!-- Token result -->
        {#if generatedToken}
          <div class="token-result" transition:slide>
            <div class="token-result-header">
              <span class="token-result-label">🎉 Invite ready - share this with a friend</span>
            </div>

            <div style="display:flex; justify-content:center; margin: 12px 0;">
              <div class="qr-wrap">
                <canvas bind:this={qrCanvas} style="display:block;"></canvas>
              </div>
            </div>

            <div class="copy-box">
              <span class="copy-icon">🔑</span>
              <input type="text" readonly value={generatedToken} class="copy-input" />
              <button class="copy-btn" on:click={copyToken}>
                {copiedToken ? '✓ Copied' : 'Copy'}
              </button>
            </div>
          </div>
        {/if}
      </div>
    </div>
  {/if}

  <!-- ── BORROW PANEL ──────────────────────────────────────────────────────────── -->
  {#if activeTab === 'borrow'}
    <div class="brain-panel borrow-panel" in:fade={{ duration: 160 }}>

      <!-- Panel header -->
      <div class="panel-header borrow-header">
        <div class="panel-icon-wrap borrow-icon-wrap">
          <svg class="panel-svg" viewBox="0 0 40 40" fill="none">
            <circle cx="20" cy="20" r="18" stroke="url(#lg2)" stroke-width="1.5" fill="url(#lbg2)" />
            <text x="50%" y="55%" dominant-baseline="middle" text-anchor="middle" fill="white" font-size="14">🔗</text>
            <defs>
              <linearGradient id="lg2" x1="0" y1="0" x2="40" y2="40">
                <stop offset="0%" stop-color="#a855f7"/>
                <stop offset="100%" stop-color="#06b6d4"/>
              </linearGradient>
              <linearGradient id="lbg2" x1="0" y1="0" x2="40" y2="40">
                <stop offset="0%" stop-color="rgba(168,85,247,0.15)"/>
                <stop offset="100%" stop-color="rgba(6,182,212,0.1)"/>
              </linearGradient>
            </defs>
          </svg>
        </div>
        <div>
          <div class="panel-title borrow-title">Borrow Remote Compute</div>
          <div class="panel-subtitle">Paste a friend's invite token to mount their spare VRAM into your local agent cluster.</div>
        </div>
        <div class="panel-badges">
          <span class="pbadge pbadge-purple">🧠 Remote VRAM</span>
          <span class="pbadge pbadge-purple">🌐 WAN P2P</span>
          <span class="pbadge pbadge-purple">🔒 E2E Encrypted</span>
        </div>
      </div>

      <div class="panel-body">

        {#if borrowSuccess}
          <div class="banner banner-info" transition:slide>✅ {borrowSuccess}</div>
        {/if}
        {#if borrowError}
          <div class="banner banner-error" transition:slide>⚠️ {borrowError}</div>
        {/if}

        <div class="field">
          <label class="field-label" for="invite-token-input">Lender's Invite Token</label>
          <div class="token-input-wrap">
            <span class="token-prefix-icon">🔑</span>
            <input
              id="invite-token-input"
              type="text"
              placeholder="Paste invite token (ofl_…) here"
              bind:value={borrowToken}
              class="input token-field"
              spellcheck="false"
              autocomplete="off"
            />
          </div>
          <p class="field-hint">Ask your friend to generate a code on the Share Compute tab.</p>
        </div>

        <button
          class="btn btn-borrow"
          disabled={borrowing || !borrowToken}
          on:click={handleBorrow}
          id="connect-lender-btn"
          style="width:100%; justify-content: center;"
        >
          {#if borrowing}
            <span class="btn-spinner borrow-spinner"></span> Connecting…
          {:else}
            <span>⚡</span> Connect to Lender Node
          {/if}
        </button>

        <!-- How it works -->
        <div class="how-it-works">
          <div class="how-title">How Rent a Brain Works</div>
          <div class="how-steps">
            <div class="how-step">
              <div class="step-num">1</div>
              <div>
                <strong>Friend generates an invite</strong> on the Share Compute tab and sends you the token.
              </div>
            </div>
            <div class="how-step">
              <div class="step-num">2</div>
              <div>
                <strong>You paste the token</strong> here. Your agent dials their node via libp2p hole-punching - no port forwarding needed.
              </div>
            </div>
            <div class="how-step">
              <div class="step-num">3</div>
              <div>
                <strong>Their VRAM is pooled</strong> into your cluster. Tasks are distributed automatically to the best available node.
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  {/if}

  <!-- ── SESSIONS LIST ─────────────────────────────────────────────────────────── -->
  <div class="section-header" style="margin-top:8px; margin-bottom:var(--space-4)">
    <div>
      <h2 style="font-size:var(--text-xl)">Active Compute Links</h2>
      <p class="text-muted" style="font-size:var(--text-sm); margin-top:2px">
        Live P2P sessions - refreshes every 5s
      </p>
    </div>
    <button class="btn btn-secondary" on:click={fetchSessions} id="refresh-sessions-btn">
      ↻ Refresh
    </button>
  </div>

  {#if loadingSessions}
    <div class="card" style="padding:40px; text-align:center; color:var(--text-muted);">
      <div class="skeleton-row"></div>
      <div class="skeleton-row" style="width:70%;margin:8px auto 0;"></div>
    </div>
  {:else if lentSessions.length === 0 && borrowedSessions.length === 0}
    <div class="card">
      <div class="empty-state">
        <div class="empty-icon">🧠</div>
        <h3>No active compute links</h3>
        <p>Share your compute or paste a friend's invite token above to get started.</p>
      </div>
    </div>
  {:else}
    <div class="sessions-grid">

      <!-- Borrowed (Lenders we connected to) -->
      {#if borrowedSessions.length > 0}
        <div class="card">
          <div class="sessions-header">
            <span class="sessions-icon">🔗</span>
            <div>
              <div class="sessions-title">Borrowed Nodes</div>
              <div class="sessions-sub">Remote VRAM you are currently consuming</div>
            </div>
            <span class="badge badge-online">{borrowedSessions.length} linked</span>
          </div>

          <div class="sessions-list">
            {#each borrowedSessions as s (s.lender_id)}
              <div class="session-card" transition:slide>
                <div class="session-top">
                  <div class="session-peer">
                    <span class="peer-dot connected"></span>
                    <span class="peer-label">{s.label || 'Remote Node'}</span>
                  </div>
                  <span class="badge badge-online">Connected</span>
                </div>

                <div class="session-stats">
                  <div class="stat-item">
                    <span class="stat-key">VRAM POOL</span>
                    <span class="stat-val accent">{fmtBytes(s.max_vram)}</span>
                  </div>
                  <div class="stat-item">
                    <span class="stat-key">LENDER ID</span>
                    <span class="stat-val mono truncate">{s.lender_id?.slice(0,16)}…</span>
                  </div>
                  <div class="stat-item">
                    <span class="stat-key">EXPIRES</span>
                    <span class="stat-val">{fmtExpiry(s.expires_at)}</span>
                  </div>
                  <div class="stat-item">
                    <span class="stat-key">LATENCY</span>
                    <span class="stat-val accent">~15 ms</span>
                  </div>
                </div>

                <button
                  class="btn btn-danger"
                  style="width:100%; justify-content:center; font-size:var(--text-xs);"
                  disabled={revokingId === s.lender_id}
                  on:click={() => handleRevoke(s.lender_id)}
                >
                  {revokingId === s.lender_id ? 'Disconnecting…' : '✕ Disconnect'}
                </button>
              </div>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Lent (Borrowers connected to us) -->
      {#if lentSessions.length > 0}
        <div class="card">
          <div class="sessions-header">
            <span class="sessions-icon">📡</span>
            <div>
              <div class="sessions-title">Lent Sessions</div>
              <div class="sessions-sub">Friends currently using your spare capacity</div>
            </div>
            <span class="badge badge-warning">{lentSessions.length} guest{lentSessions.length !== 1 ? 's' : ''}</span>
          </div>

          <div class="sessions-list">
            {#each lentSessions as s (s.borrower_id)}
              <div class="session-card" transition:slide>
                <div class="session-top">
                  <div class="session-peer">
                    <span class="peer-dot connected"></span>
                    <span class="peer-label mono truncate">{s.borrower_id?.slice(0,20)}…</span>
                  </div>
                  <span class="badge badge-warning">Guest</span>
                </div>

                <div class="session-stats">
                  <div class="stat-item">
                    <span class="stat-key">VRAM CAP</span>
                    <span class="stat-val accent">{fmtBytes(s.max_vram)}</span>
                  </div>
                  <div class="stat-item">
                    <span class="stat-key">CONNECTED AT</span>
                    <span class="stat-val">{new Date(s.connected_at).toLocaleTimeString()}</span>
                  </div>
                  <div class="stat-item">
                    <span class="stat-key">SANDBOX</span>
                    <span class="stat-val" style="color:#a855f7">WASM enforced</span>
                  </div>
                  <div class="stat-item">
                    <span class="stat-key">LATENCY</span>
                    <span class="stat-val accent">~22 ms</span>
                  </div>
                </div>

                <button
                  class="btn btn-danger"
                  style="width:100%; justify-content:center; font-size:var(--text-xs);"
                  disabled={revokingId === s.borrower_id}
                  on:click={() => handleRevoke(s.borrower_id)}
                >
                  {revokingId === s.borrower_id ? 'Terminating…' : '✕ Terminate Session'}
                </button>
              </div>
            {/each}
          </div>
        </div>
      {/if}

    </div>
  {/if}

</div>

<style>
  .social-page {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }

  /* ── Eye-brow ──────────────────────────────────────────────────────────────── */
  .page-eyebrow {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: var(--text-xs);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--accent);
    margin-bottom: 4px;
  }
  .eyebrow-dot {
    width: 6px; height: 6px;
    border-radius: 50%;
    background: var(--accent);
    box-shadow: 0 0 6px var(--accent-glow);
    animation: pulse-dot 2s ease-in-out infinite;
  }
  @keyframes pulse-dot {
    0%,100% { opacity:1; transform:scale(1); }
    50%      { opacity:0.6; transform:scale(1.3); }
  }

  /* ── Status pill ───────────────────────────────────────────────────────────── */
  .status-pill {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 16px;
    border-radius: var(--radius-full);
    background: rgba(0,201,167,0.06);
    border: 1px solid rgba(0,201,167,0.2);
    font-size: var(--text-xs);
    font-weight: 600;
    color: var(--accent);
    white-space: nowrap;
  }
  .status-dot {
    width: 7px; height: 7px;
    border-radius: 50%;
    background: var(--accent);
    box-shadow: 0 0 8px var(--accent-glow);
  }
  .shield { font-size: 14px; }

  /* ── Tab group ─────────────────────────────────────────────────────────────── */
  .tab-group {
    display: flex;
    background: rgba(255,255,255,0.03);
    border: 1px solid rgba(255,255,255,0.08);
    border-radius: var(--radius-md);
    padding: 3px;
    gap: 2px;
    width: fit-content;
  }
  .tab-btn {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 20px;
    border-radius: var(--radius-sm);
    border: none;
    background: transparent;
    color: var(--text-muted);
    font-size: var(--text-sm);
    font-weight: 500;
    cursor: pointer;
    transition: all 0.2s ease;
  }
  .tab-btn.active {
    background: rgba(255,255,255,0.07);
    color: var(--text-primary);
    box-shadow: 0 1px 3px rgba(0,0,0,0.3);
  }
  .tab-btn:nth-child(2).active {
    background: linear-gradient(135deg, rgba(168,85,247,0.18), rgba(6,182,212,0.18));
    color: #e9d5ff;
    box-shadow: 0 0 0 1px rgba(168,85,247,0.3), 0 1px 3px rgba(0,0,0,0.3);
  }
  .tab-icon { font-size: 15px; }
  .tab-badge {
    min-width: 18px; height: 18px;
    border-radius: var(--radius-full);
    background: rgba(255,255,255,0.07);
    color: var(--text-muted);
    font-size: 10px;
    font-weight: 700;
    display: flex; align-items: center; justify-content: center;
    padding: 0 5px;
  }
  .tab-badge.lit {
    background: var(--accent-dim);
    color: var(--accent);
  }

  /* ── Brain Panel ───────────────────────────────────────────────────────────── */
  @keyframes lend-glow {
    0%,100% { box-shadow: 0 0 0 1px rgba(0,201,167,0.2), 0 0 32px rgba(0,201,167,0.05); }
    50%      { box-shadow: 0 0 0 1px rgba(0,201,167,0.35), 0 0 48px rgba(0,201,167,0.1); }
  }
  @keyframes borrow-glow {
    0%,100% { box-shadow: 0 0 0 1px rgba(168,85,247,0.2), 0 0 32px rgba(168,85,247,0.05); }
    50%      { box-shadow: 0 0 0 1px rgba(6,182,212,0.35), 0 0 48px rgba(6,182,212,0.08); }
  }
  .brain-panel {
    border-radius: var(--radius-lg);
    overflow: hidden;
  }
  .lend-panel {
    border: 1px solid rgba(0,201,167,0.2);
    background: linear-gradient(145deg, rgba(0,201,167,0.04) 0%, rgba(0,95,158,0.03) 100%);
    animation: lend-glow 4s ease-in-out infinite;
  }
  .borrow-panel {
    border: 1px solid rgba(168,85,247,0.22);
    background: linear-gradient(145deg, rgba(168,85,247,0.04) 0%, rgba(6,182,212,0.04) 100%);
    animation: borrow-glow 4s ease-in-out infinite;
  }

  /* ── Panel header ──────────────────────────────────────────────────────────── */
  .panel-header {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    padding: 20px 24px 16px;
    border-bottom: 1px solid rgba(0,201,167,0.12);
    background: linear-gradient(135deg, rgba(0,201,167,0.07), rgba(0,95,158,0.04));
    flex-wrap: wrap;
  }
  .borrow-header {
    border-bottom-color: rgba(168,85,247,0.15);
    background: linear-gradient(135deg, rgba(168,85,247,0.08), rgba(6,182,212,0.05));
  }

  .panel-icon-wrap {
    flex-shrink: 0;
  }
  .panel-svg {
    width: 42px; height: 42px;
  }

  .panel-title {
    font-size: 16px;
    font-weight: 700;
    color: var(--text-primary);
    letter-spacing: 0.01em;
  }
  .borrow-title { color: #e9d5ff; }
  .panel-subtitle {
    font-size: 12px;
    color: var(--text-muted);
    margin-top: 3px;
    max-width: 380px;
  }

  .panel-badges {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    margin-left: auto;
  }
  .pbadge {
    font-size: 11px;
    padding: 3px 10px;
    border-radius: var(--radius-full);
    background: rgba(0,201,167,0.1);
    border: 1px solid rgba(0,201,167,0.2);
    color: var(--accent);
    font-weight: 500;
  }
  .pbadge-purple {
    background: rgba(168,85,247,0.1);
    border-color: rgba(168,85,247,0.2);
    color: #c4b5fd;
  }

  /* ── Panel body ────────────────────────────────────────────────────────────── */
  .panel-body {
    padding: 20px 24px;
    display: flex;
    flex-direction: column;
    gap: 18px;
  }

  /* ── Form fields ───────────────────────────────────────────────────────────── */
  .field { display: flex; flex-direction: column; gap: 6px; }
  .field-header { display: flex; align-items: center; justify-content: space-between; }
  .field-label {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .field-value { font-size: 14px; font-weight: 700; font-family: var(--font-mono); }
  .field-value.accent { color: var(--accent); }
  .field-hint { font-size: var(--text-xs); color: var(--text-muted); }

  /* ── Slider ────────────────────────────────────────────────────────────────── */
  .slider {
    -webkit-appearance: none; appearance: none;
    width: 100%; height: 6px;
    border-radius: var(--radius-full);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    outline: none;
    margin: 4px 0;
  }
  .slider::-webkit-slider-thumb {
    -webkit-appearance: none; appearance: none;
    width: 18px; height: 18px;
    border-radius: 50%;
    background: var(--accent);
    cursor: pointer;
    box-shadow: 0 0 8px rgba(0,201,167,0.5);
    border: 2px solid #0D1117;
    transition: transform 0.15s;
  }
  .slider::-webkit-slider-thumb:hover { transform: scale(1.2); }
  .slider-ticks {
    display: flex;
    justify-content: space-between;
    font-size: 10px;
    color: var(--text-muted);
    font-family: var(--font-mono);
  }

  /* ── Token input row ───────────────────────────────────────────────────────── */
  .token-input-wrap {
    position: relative;
  }
  .token-prefix-icon {
    position: absolute;
    left: 12px; top: 50%;
    transform: translateY(-50%);
    font-size: 15px;
    pointer-events: none;
  }
  .token-field { padding-left: 38px !important; font-family: var(--font-mono); font-size: 13px; }

  /* ── Security grid ─────────────────────────────────────────────────────────── */
  .security-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
    padding: 14px;
    border-radius: var(--radius-md);
    background: rgba(0,0,0,0.2);
    border: 1px solid rgba(255,255,255,0.05);
  }
  .security-item {
    display: flex; align-items: flex-start;
    gap: 8px; font-size: 12px; color: var(--text-muted);
  }
  .sec-ok { color: #22c55e; font-weight: bold; flex-shrink: 0; }

  /* ── Buttons ───────────────────────────────────────────────────────────────── */
  @keyframes spin { to { transform: rotate(360deg); } }
  .btn-spinner {
    display: inline-block;
    width: 13px; height: 13px;
    border: 2px solid rgba(13,17,23,0.3);
    border-top-color: #0D1117;
    border-radius: 50%;
    animation: spin 0.65s linear infinite;
    flex-shrink: 0;
  }
  .borrow-spinner {
    border-top-color: #c4b5fd;
    border-color: rgba(168,85,247,0.3);
    border-top-color: #c4b5fd;
  }
  .btn-borrow {
    display: inline-flex; align-items: center; gap: var(--space-2);
    padding: var(--space-2) var(--space-5);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--text-sm); font-weight: 600;
    cursor: pointer; border: none;
    background: linear-gradient(135deg, #a855f7, #06b6d4);
    color: #fff;
    transition: all 0.2s ease;
    white-space: nowrap;
  }
  .btn-borrow:hover:not(:disabled) {
    box-shadow: 0 0 20px rgba(168,85,247,0.4);
    filter: brightness(1.1);
  }
  .btn-borrow:disabled { opacity: 0.5; cursor: not-allowed; }

  /* ── Token result ──────────────────────────────────────────────────────────── */
  .token-result {
    border-top: 1px solid rgba(0,201,167,0.15);
    padding-top: 18px;
    display: flex; flex-direction: column; gap: 12px;
  }
  .token-result-header {
    display: flex; align-items: center; gap: 8px;
  }
  .token-result-label {
    font-size: 13px; font-weight: 600; color: var(--text-secondary);
  }
  .qr-wrap {
    padding: 8px;
    background: white;
    border-radius: 10px;
    box-shadow: 0 4px 16px rgba(0,0,0,0.5);
    display: inline-block;
  }
  .copy-box {
    display: flex; align-items: center; gap: 6px;
    background: rgba(0,0,0,0.4);
    border: 1px solid rgba(255,255,255,0.08);
    border-radius: var(--radius-md);
    padding: 4px 4px 4px 10px;
  }
  .copy-icon { font-size: 14px; flex-shrink: 0; }
  .copy-input {
    flex: 1; background: transparent; border: none; outline: none;
    color: var(--text-primary); font-family: var(--font-mono);
    font-size: 12px; padding: 6px 0;
  }
  .copy-btn {
    background: var(--accent); border: none;
    color: #0D1117; padding: 7px 16px;
    border-radius: var(--radius-sm); font-size: 12px; font-weight: 700;
    cursor: pointer; transition: background 0.2s; white-space: nowrap; flex-shrink: 0;
  }
  .copy-btn:hover { background: #00e0bc; }

  /* ── How it works ──────────────────────────────────────────────────────────── */
  .how-it-works {
    background: rgba(0,0,0,0.2);
    border: 1px solid rgba(168,85,247,0.12);
    border-radius: var(--radius-md);
    padding: 16px;
  }
  .how-title {
    font-size: 12px; font-weight: 700;
    text-transform: uppercase; letter-spacing: 0.06em;
    color: #c4b5fd; margin-bottom: 12px;
  }
  .how-steps { display: flex; flex-direction: column; gap: 12px; }
  .how-step {
    display: flex; align-items: flex-start; gap: 12px;
    font-size: 12px; color: var(--text-muted); line-height: 1.5;
  }
  .step-num {
    min-width: 22px; height: 22px;
    border-radius: 50%;
    background: rgba(168,85,247,0.2);
    border: 1px solid rgba(168,85,247,0.4);
    color: #c4b5fd; font-size: 10px; font-weight: 700;
    display: flex; align-items: center; justify-content: center;
    flex-shrink: 0;
  }
  .how-step strong { color: var(--text-secondary); }

  /* ── Sessions ──────────────────────────────────────────────────────────────── */
  .sessions-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(340px, 1fr));
    gap: var(--space-5);
  }
  .sessions-header {
    display: flex; align-items: center; gap: var(--space-3);
    margin-bottom: var(--space-4);
    padding-bottom: var(--space-4);
    border-bottom: 1px solid var(--border);
  }
  .sessions-icon { font-size: 22px; }
  .sessions-title { font-size: 15px; font-weight: 700; color: var(--text-primary); }
  .sessions-sub   { font-size: 11px; color: var(--text-muted); margin-top: 2px; }
  .sessions-list  { display: flex; flex-direction: column; gap: 12px; }

  .session-card {
    background: linear-gradient(145deg, rgba(25,35,55,0.4), rgba(15,22,38,0.3));
    border: 1px solid rgba(255,255,255,0.06);
    border-radius: var(--radius-md);
    padding: 14px;
    display: flex; flex-direction: column; gap: 12px;
    transition: border-color 0.2s;
  }
  .session-card:hover { border-color: rgba(0,201,167,0.2); }

  .session-top {
    display: flex; align-items: center; justify-content: space-between;
  }
  .session-peer {
    display: flex; align-items: center; gap: 8px;
    min-width: 0;
  }
  .peer-dot {
    width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0;
  }
  .peer-dot.connected {
    background: var(--accent);
    box-shadow: 0 0 6px var(--accent-glow);
    animation: pulse-dot 2.5s ease-in-out infinite;
  }
  .peer-label {
    font-size: 13px; font-weight: 600;
    color: var(--text-primary);
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  .peer-label.mono { font-family: var(--font-mono); font-size: 12px; }

  .session-stats {
    display: grid; grid-template-columns: 1fr 1fr; gap: 10px;
    border-top: 1px solid rgba(255,255,255,0.04);
    border-bottom: 1px solid rgba(255,255,255,0.04);
    padding: 10px 0;
  }
  .stat-item { display: flex; flex-direction: column; gap: 3px; }
  .stat-key  { font-size: 9px; text-transform: uppercase; letter-spacing: 0.08em; color: var(--text-muted); }
  .stat-val  { font-size: 12px; font-family: var(--font-mono); color: var(--text-secondary); }
  .stat-val.accent { color: var(--accent); }

  /* ── Skeleton ──────────────────────────────────────────────────────────────── */
  .skeleton-row {
    height: 14px; border-radius: 4px; width: 100%;
    background: linear-gradient(90deg, var(--bg-tertiary) 25%, var(--bg-card-hover) 50%, var(--bg-tertiary) 75%);
    background-size: 200% 100%;
    animation: skeleton-wave 1.5s infinite;
  }
  @keyframes skeleton-wave {
    0%   { background-position: 200% 0; }
    100% { background-position: -200% 0; }
  }

  /* ── Misc ──────────────────────────────────────────────────────────────────── */
  .truncate { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mono { font-family: var(--font-mono); }
  .accent { color: var(--accent); }

  @media (max-width: 640px) {
    .panel-header  { flex-direction: column; align-items: flex-start; }
    .panel-badges  { margin-left: 0; }
    .security-grid { grid-template-columns: 1fr; }
    .tab-group     { width: 100%; }
    .tab-btn       { flex: 1; justify-content: center; }
    .status-pill   { display: none; }
  }
</style>
