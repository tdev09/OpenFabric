<script lang="ts">
  import { dialogStore } from '$lib/stores/dialog';
  import { fade } from 'svelte/transition';
</script>

{#if $dialogStore}
  <div 
    class="confirm-overlay" 
    on:click|self={$dialogStore.onCancel || $dialogStore.onConfirm} 
    on:keydown={(e) => {
      if (e.key === 'Escape') {
        const handler = $dialogStore.onCancel || $dialogStore.onConfirm;
        if (handler) handler();
      }
    }}
    role="dialog" 
    aria-modal="true" 
    tabindex="-1"
    transition:fade={{ duration: 150 }}
  >
    <div class="confirm-dialog">
      {#if $dialogStore.icon}
        <div class="confirm-icon">{$dialogStore.icon}</div>
      {/if}
      {#if $dialogStore.title}
        <h3>{$dialogStore.title}</h3>
      {/if}
      <p>{$dialogStore.message}</p>
      
      <div class="confirm-actions">
        {#if $dialogStore.type === 'confirm'}
          <button class="btn btn-secondary" on:click={$dialogStore.onCancel}>
            {$dialogStore.cancelText || 'Cancel'}
          </button>
        {/if}
        <button 
          class="btn" 
          class:btn-accent={!$dialogStore.danger}
          class:btn-danger={$dialogStore.danger}
          on:click={$dialogStore.onConfirm}
        >
          {$dialogStore.confirmText || 'OK'}
        </button>
      </div>
    </div>
  </div>
{/if}
