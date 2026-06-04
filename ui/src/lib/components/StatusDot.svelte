<script lang="ts">
  export let online: boolean = true;
  export let size: number = 10;
</script>

<span
  class="status-dot"
  class:online
  class:offline={!online}
  style="--size: {size}px"
  title={online ? 'Online' : 'Offline'}
>
  {#if online}
    <span class="pulse-ring" />
  {/if}
</span>

<style>
  .status-dot {
    position: relative;
    display: inline-block;
    width: var(--size);
    height: var(--size);
    border-radius: 50%;
    flex-shrink: 0;
  }
  .online  { background: var(--online); }
  .offline { background: var(--offline); }

  .pulse-ring {
    position: absolute;
    inset: -4px;
    border-radius: 50%;
    border: 2px solid var(--online);
    opacity: 0;
    animation: pulse-ring 2s ease-out infinite;
  }
  @keyframes pulse-ring {
    0%   { transform: scale(0.8); opacity: 0.6; }
    100% { transform: scale(1.8); opacity: 0; }
  }
</style>
