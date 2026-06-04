<script lang="ts">
  import type { NodeInfo } from '$lib/stores/cluster';
  import { formatBytes, timeAgo, removeNode } from '$lib/stores/cluster';
  import { dialog } from '$lib/stores/dialog';
  import { wolDevices, wakingDevices, wakeWOLDevice } from '$lib/stores/wol';

  export let node: NodeInfo;

  $: wolDevice = $wolDevices.find(d => d.linked_node_id === node.id);
  $: isWaking = wolDevice ? ($wakingDevices[wolDevice.mac] || false) : false;

  const deviceIcons: Record<string, string> = {
    laptop: '💻', desktop: '🖥️', phone: '📱', pi: '🫐', unknown: '⚙️'
  };

  $: icon = deviceIcons[node.device_type] ?? '⚙️';
  $: ramPct = node.ram_total > 0 ? (node.ram_used / node.ram_total) * 100 : 0;
  $: storagePct = node.storage_total > 0 ? (node.storage_used / node.storage_total) * 100 : 0;
  $: isOnline = node.status === 'online';

  let removing = false;
  async function handleRemove() {
    const confirmed = await dialog.confirm(
      `Are you sure you want to remove ${node.name} from the cluster?`,
      'Remove Node',
      '⚙️',
      'Remove',
      'Cancel',
      true
    );
    if (!confirmed) return;
    removing = true;
    try { await removeNode(node.id); } finally { removing = false; }
  }
</script>

<div class="node-card" class:offline={!isOnline}>

  <!-- TOP BAR: icon · name · status badge -->
  <div class="card-top">
    <div class="icon-wrap">
      <span class="device-icon">{icon}</span>
      {#if isOnline}<span class="online-pip"></span>{/if}
    </div>

    <div class="name-block">
      <span class="node-name">{node.name}</span>
      <div class="badges">
        <span class="badge">{node.os}</span>
        <span class="badge">{node.arch}</span>
      </div>
    </div>

    <span class="status-pill" class:active={isOnline}>
      {isOnline ? 'Online' : 'Offline'}
    </span>
  </div>

  <!-- METRICS or OFFLINE hint -->
  {#if isOnline}
    <div class="metrics">
      <div class="metric">
        <div class="m-row">
          <span class="m-label">CPU</span>
          <span class="m-pct">{node.cpu_percent.toFixed(1)}%</span>
        </div>
        <div class="bar-track">
          <div class="bar-fill" style="width:{Math.min(node.cpu_percent,100)}%; --bar-color:#00c9a7;"></div>
        </div>
      </div>

      <div class="metric">
        <div class="m-row">
          <span class="m-label">RAM <span class="m-detail">{formatBytes(node.ram_used)} / {formatBytes(node.ram_total)}</span></span>
          <span class="m-pct">{ramPct.toFixed(1)}%</span>
        </div>
        <div class="bar-track">
          <div class="bar-fill" style="width:{Math.min(ramPct,100)}%; --bar-color:#38bdf8;"></div>
        </div>
      </div>

      <div class="metric">
        <div class="m-row">
          <span class="m-label">Disk <span class="m-detail">{formatBytes(node.storage_used)} / {formatBytes(node.storage_total)}</span></span>
          <span class="m-pct">{storagePct.toFixed(1)}%</span>
        </div>
        <div class="bar-track">
          <div class="bar-fill" style="width:{Math.min(storagePct,100)}%; --bar-color:#c084fc;"></div>
        </div>
      </div>
    </div>
  {:else}
    <div class="offline-row">
      <span class="offline-ico">⏸</span>
      <span class="offline-lbl">Last seen {timeAgo(node.last_seen)}</span>
    </div>
  {/if}

  <!-- FOOTER -->
  <div class="card-foot">
    <div class="peer-block">
      <span class="peer-key">PEER ID</span>
      <span class="peer-val">{node.id.slice(0, 16)}…</span>
    </div>
    {#if !isOnline && wolDevice}
      <button 
        class="btn-wake" 
        on:click={() => wakeWOLDevice(wolDevice.mac)} 
        disabled={isWaking}
      >
        {isWaking ? '⚡ Waking...' : '⚡ Wake'}
      </button>
    {/if}
    <button class="btn-rm" on:click={handleRemove} disabled={removing}>
      {removing ? '…' : 'Remove'}
    </button>
  </div>
</div>

<style>
  /* ── Card shell ─────────────────────────────── */
  .node-card {
    background: linear-gradient(145deg, rgba(18,26,44,0.7) 0%, rgba(12,18,32,0.55) 100%);
    border: 1px solid rgba(255,255,255,0.07);
    backdrop-filter: blur(20px) saturate(160%);
    border-radius: 18px;
    padding: 22px;
    display: flex;
    flex-direction: column;
    gap: 18px;
    box-shadow: 0 4px 28px rgba(0,0,0,0.3), inset 0 1px 0 rgba(255,255,255,0.04);
    transition: border-color 300ms ease, box-shadow 300ms ease, transform 300ms cubic-bezier(0.16,1,0.3,1);
    animation: fade-up 280ms ease both;
    width: 100%;
    box-sizing: border-box;
  }
  .node-card:hover {
    border-color: rgba(0,201,167,0.3);
    box-shadow: 0 14px 36px rgba(0,0,0,0.4), 0 0 22px rgba(0,201,167,0.07);
    transform: translateY(-3px);
  }
  .node-card.offline { opacity: 0.55; filter: grayscale(0.4); }
  .node-card.offline:hover { transform: none; border-color: rgba(255,255,255,0.07); box-shadow: 0 4px 28px rgba(0,0,0,0.3); }

  /* ── Top bar ─────────────────────────────────── */
  .card-top {
    display: flex;
    align-items: flex-start;
    gap: 12px;
  }

  /* Icon */
  .icon-wrap {
    position: relative;
    width: 50px;
    height: 50px;
    border-radius: 14px;
    background: rgba(255,255,255,0.03);
    border: 1px solid rgba(255,255,255,0.07);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 22px;
    flex-shrink: 0;
  }
  .device-icon { line-height: 1; }
  .online-pip {
    position: absolute;
    bottom: -2px;
    right: -2px;
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: #00c9a7;
    border: 2px solid rgba(12,18,32,0.95);
    box-shadow: 0 0 8px rgba(0,201,167,0.7);
  }

  /* Name block - takes all available horizontal space */
  .name-block {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 5px;
  }
  .node-name {
    display: block;
    font-weight: 700;
    font-size: 16px;
    color: var(--text-primary);
    letter-spacing: -0.01em;
    line-height: 1.25;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .badges {
    display: flex;
    gap: 4px;
    flex-wrap: nowrap;
  }
  .badge {
    font-size: 9px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: rgba(255,255,255,0.35);
    background: rgba(255,255,255,0.04);
    border: 1px solid rgba(255,255,255,0.06);
    padding: 2px 6px;
    border-radius: 4px;
    white-space: nowrap;
    line-height: 1.5;
  }

  /* Status pill - always right-aligned, never wraps */
  .status-pill {
    flex-shrink: 0;
    margin-top: 1px;
    font-size: 9px;
    font-weight: 800;
    text-transform: uppercase;
    letter-spacing: 0.09em;
    padding: 3px 9px;
    border-radius: 20px;
    white-space: nowrap;
    color: rgba(255,255,255,0.3);
    background: rgba(255,255,255,0.04);
    border: 1px solid rgba(255,255,255,0.06);
  }
  .status-pill.active {
    color: #00c9a7;
    background: rgba(0,201,167,0.08);
    border-color: rgba(0,201,167,0.25);
    box-shadow: 0 0 10px rgba(0,201,167,0.12);
  }

  /* ── Metrics block ───────────────────────────── */
  .metrics {
    display: flex;
    flex-direction: column;
    gap: 14px;
    background: rgba(255,255,255,0.015);
    border: 1px solid rgba(255,255,255,0.04);
    border-radius: 13px;
    padding: 16px;
  }
  .metric {
    display: flex;
    flex-direction: column;
    gap: 7px;
  }
  .m-row {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 6px;
  }
  .m-label {
    font-size: 10px;
    font-weight: 800;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: rgba(255,255,255,0.45);
    white-space: nowrap;
    display: flex;
    align-items: baseline;
    gap: 6px;
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .m-detail {
    font-size: 9px;
    font-weight: 400;
    text-transform: none;
    letter-spacing: 0;
    color: rgba(255,255,255,0.22);
    white-space: nowrap;
  }
  .m-pct {
    font-size: 11px;
    font-weight: 800;
    color: var(--text-primary);
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
    flex-shrink: 0;
  }

  /* Smooth gradient progress bar */
  .bar-track {
    width: 100%;
    height: 5px;
    background: rgba(255,255,255,0.05);
    border-radius: 99px;
    overflow: hidden;
  }
  .bar-fill {
    height: 100%;
    border-radius: 99px;
    background: var(--bar-color);
    box-shadow: 0 0 8px var(--bar-color);
    transition: width 700ms cubic-bezier(0.4,0,0.2,1);
  }

  /* ── Offline state ───────────────────────────── */
  .offline-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 0;
  }
  .offline-ico { font-size: 14px; opacity: 0.4; }
  .offline-lbl { font-size: 11px; color: rgba(255,255,255,0.25); }

  /* ── Footer ──────────────────────────────────── */
  .card-foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding-top: 16px;
    border-top: 1px solid rgba(255,255,255,0.05);
  }
  .peer-block {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
    flex: 1;
  }
  .peer-key {
    font-size: 8px;
    font-weight: 800;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: rgba(255,255,255,0.18);
  }
  .peer-val {
    font-size: 10px;
    font-family: 'JetBrains Mono', 'Fira Code', ui-monospace, monospace;
    color: rgba(255,255,255,0.28);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .btn-wake {
    flex-shrink: 0;
    background: rgba(0, 201, 167, 0.12);
    border: 1px solid rgba(0, 201, 167, 0.25);
    color: #00c9a7;
    padding: 5px 13px;
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.03em;
    border-radius: 7px;
    cursor: pointer;
    transition: all 0.2s ease;
    white-space: nowrap;
    margin-right: 8px;
    box-shadow: 0 0 10px rgba(0, 201, 167, 0.05);
  }
  .btn-wake:hover:not(:disabled) {
    background: rgba(0, 201, 167, 0.22);
    border-color: #00c9a7;
    box-shadow: 0 0 14px rgba(0, 201, 167, 0.25);
  }
  .btn-wake:disabled {
    opacity: 0.45;
    cursor: not-allowed;
    background: rgba(255, 255, 255, 0.03);
    border-color: rgba(255, 255, 255, 0.08);
    color: rgba(255, 255, 255, 0.25);
    box-shadow: none;
  }

  .btn-rm {
    flex-shrink: 0;
    background: transparent;
    border: 1px solid rgba(255,77,109,0.15);
    color: rgba(255,77,109,0.55);
    padding: 5px 13px;
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.03em;
    border-radius: 7px;
    cursor: pointer;
    transition: all 0.2s ease;
    white-space: nowrap;
  }
  .btn-rm:hover:not(:disabled) {
    background: rgba(255,77,109,0.1);
    border-color: rgba(255,77,109,0.4);
    color: #ff4d6d;
    box-shadow: 0 0 14px rgba(255,77,109,0.2);
  }
  .btn-rm:disabled { opacity: 0.35; cursor: not-allowed; }

  @keyframes fade-up {
    from { opacity: 0; transform: translateY(6px); }
    to   { opacity: 1; transform: translateY(0); }
  }

  @media (max-width: 480px) {
    .node-card {
      padding: 12px;
      gap: 12px;
    }
    .metrics {
      padding: 10px;
      gap: 8px;
    }
    .card-top {
      gap: 8px;
    }
    .icon-wrap {
      width: 38px;
      height: 38px;
      font-size: 16px;
    }
    .node-name {
      font-size: 13px;
    }
    .badges {
      gap: 3px;
    }
    .badge {
      font-size: 8px;
      padding: 1px 4px;
    }
    .status-pill {
      font-size: 8px;
      padding: 2px 6px;
    }
    .m-row {
      gap: 4px;
    }
    .m-label {
      font-size: 9px;
      letter-spacing: 0.06em;
      gap: 4px;
    }
    .m-detail {
      font-size: 8px;
    }
    .m-pct {
      font-size: 10px;
    }
    .bar-track {
      height: 4px;
    }
    .card-foot {
      padding-top: 10px;
    }
    .peer-key {
      font-size: 7px;
    }
    .peer-val {
      font-size: 9px;
    }
    .btn-rm {
      padding: 4px 10px;
      font-size: 9px;
    }
  }
</style>

