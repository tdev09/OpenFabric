<script lang="ts">
  export let value: number = 0;   // 0-100
  export let label: string = '';
  export let sublabel: string = '';
  export let color: string = 'var(--accent)';
  export let height: number = 5;
</script>

<div class="resource-bar">
  <div class="bar-header">
    <div class="bar-title">
      <span class="bar-label">{label}</span>
      <span class="bar-pct mono">{value.toFixed(0)}%</span>
    </div>
    {#if sublabel}
      <span class="bar-sublabel mono">{sublabel}</span>
    {/if}
  </div>
  <div class="bar-track" style="height: {height}px">
    <div
      class="bar-fill"
      style="width: {Math.min(value, 100)}%; background: {color}; height: {height}px"
      class:warn={value > 80}
      class:danger={value > 95}
    />
  </div>
</div>

<style>
  .resource-bar { display: flex; flex-direction: column; gap: 6px; }
  .bar-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    line-height: 1;
  }
  .bar-title {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  .bar-label {
    font-size: var(--text-xs);
    font-weight: 700;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.08em;
  }
  .bar-pct {
    font-size: var(--text-xs);
    font-weight: 700;
    color: var(--accent);
  }
  .bar-sublabel {
    font-size: var(--text-xs);
    color: var(--text-muted);
  }
  .bar-track {
    width: 100%;
    background: rgba(255, 255, 255, 0.04);
    border-radius: 99px;
    overflow: hidden;
  }
  .bar-fill {
    border-radius: 99px;
    transition: width 600ms cubic-bezier(0.4, 0, 0.2, 1);
    box-shadow: 0 0 10px rgba(0, 201, 167, 0.15);
  }
  .bar-fill.warn   { background: #ff9f1c !important; box-shadow: 0 0 10px rgba(255, 159, 28, 0.3) !important; }
  .bar-fill.danger { background: #ff4d6d !important; box-shadow: 0 0 10px rgba(255, 77, 109, 0.3) !important; }
</style>
