<script lang="ts">
  import type { NodeInfo } from '$lib/stores/cluster';

  export let nodes: NodeInfo[] = [];
  export let size: number = 320;

  // Layout nodes in a circle.
  $: radius = size / 2 - 44;
  $: center = size / 2;
  $: positions = nodes.map((_, i) => {
    const angle = (i / Math.max(nodes.length, 1)) * 2 * Math.PI - Math.PI / 2;
    return {
      x: center + radius * Math.cos(angle),
      y: center + radius * Math.sin(angle)
    };
  });

  const deviceIcons: Record<string, string> = {
    laptop: '💻', desktop: '🖥️', phone: '📱', pi: '🫐', unknown: '⚙️'
  };
</script>

<div class="cluster-ring-wrap">
  <svg
    width={size}
    height={size}
    viewBox="0 0 {size} {size}"
    class="cluster-ring"
    aria-label="Cluster topology ring"
    role="img"
  >
    <!-- Connection lines between all online nodes -->
    {#each nodes as nodeA, i}
      {#each nodes as nodeB, j}
        {#if j > i && nodeA.status === 'online' && nodeB.status === 'online'}
          <line
            x1={positions[i].x} y1={positions[i].y}
            x2={positions[j].x} y2={positions[j].y}
            stroke="rgba(0,201,167,0.15)"
            stroke-width="1"
          />
        {/if}
      {/each}
    {/each}

    <!-- Central hub glow -->
    {#if nodes.some(n => n.status === 'online')}
      <circle cx={center} cy={center} r="22" fill="var(--accent-dim)" class="hub-ring-outer" />
    {/if}
    <circle cx={center} cy={center} r="14" fill="var(--bg-card)" stroke="var(--border-accent)" stroke-width="1.5" />
    <text x={center} y={center + 5} text-anchor="middle" font-size="12" fill="var(--accent)">OF</text>

    <!-- Node circles -->
    {#each nodes as node, i}
      {@const pos = positions[i]}
      {@const online = node.status === 'online'}
      <g class="node-group" class:offline-node={!online}>
        <!-- Pulse ring for online nodes -->
        {#if online}
          <circle
            cx={pos.x} cy={pos.y} r="22"
            fill="none"
            stroke="var(--accent)"
            stroke-width="1"
            class="node-pulse"
            style="animation-delay: {i * 0.3}s"
          />
        {/if}
        <!-- Node circle -->
        <circle
          cx={pos.x} cy={pos.y} r="18"
          fill={online ? 'var(--bg-card)' : 'var(--bg-tertiary)'}
          stroke={online ? 'var(--accent)' : 'var(--border)'}
          stroke-width={online ? '1.5' : '1'}
        />
        <!-- Device emoji -->
        <text
          x={pos.x} y={pos.y + 5}
          text-anchor="middle"
          font-size="13"
          class="node-icon"
        >{deviceIcons[node.device_type] ?? '⚙️'}</text>
        <!-- Node name label -->
        <text
          x={pos.x}
          y={pos.y + 30}
          text-anchor="middle"
          font-size="9"
          fill={online ? 'var(--text-secondary)' : 'var(--text-muted)'}
          font-family="Inter, sans-serif"
        >{node.name.length > 12 ? node.name.slice(0, 11) + '…' : node.name}</text>
      </g>
    {/each}

    <!-- Empty state hint -->
    {#if nodes.length === 0}
      <text x={center} y={center + 40} text-anchor="middle" font-size="11" fill="var(--text-muted)" font-family="Inter, sans-serif">
        No devices yet
      </text>
    {/if}
  </svg>
</div>

<style>
  .cluster-ring-wrap {
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .cluster-ring {
    overflow: visible;
  }
  .hub-ring-outer {
    animation: hub-pulse 3s ease-in-out infinite;
  }
  @keyframes hub-pulse {
    0%, 100% { opacity: 0.6; r: 20; }
    50%       { opacity: 1;   r: 26; }
  }
  .node-pulse {
    animation: node-pulse 2s ease-out infinite;
    transform-origin: center;
  }
  @keyframes node-pulse {
    0%   { r: 18; opacity: 0.7; }
    100% { r: 32; opacity: 0; }
  }
  .offline-node { opacity: 0.4; }
  .node-icon { dominant-baseline: middle; }
</style>
