<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { connectSSE, connected, sseError, appConfig } from '$lib/stores/cluster';
  import GlobalDialog from '$lib/components/GlobalDialog.svelte';

  const navItems = [
    { href: '/',          label: 'Dashboard', icon: '⬡' },
    { href: '/devices',   label: 'Devices',   icon: '⬡' },
    { href: '/storage',   label: 'Storage',   icon: '⬡' },
    { href: '/tasks',     label: 'Tasks',     icon: '⬡' },
    { href: '/models',    label: 'Models',    icon: '⬡' },
    { href: '/flows',     label: 'Flows',     icon: '⬡' },
    { href: '/memory',    label: 'Memory',    icon: '⬡' },
    { href: '/integrations', label: 'Integrations', icon: '⬡' },
    { href: '/agents',    label: 'Agents',    icon: '⬡' },
    { href: '/social',    label: 'Social',    icon: '⬡' },
    { href: '/tunnel',    label: 'Tunnel',    icon: '⬡' },
    { href: '/shield',    label: 'Shield',    icon: '⬡' },
    { href: '/sdn',       label: 'SDN',       icon: '⬡' },
    { href: '/settings',  label: 'Settings',  icon: '⬡' },
  ];

  let menuOpen = false;

  onMount(() => {
    const cleanup = connectSSE();
    return cleanup;
  });
</script>

<div class="app-shell">
  <!-- Sidebar -->
  <aside class="sidebar">
    <div class="logo-area">
      <div class="logo-mark">
        <svg width="28" height="28" viewBox="0 0 28 28" fill="none">
          <circle cx="14" cy="14" r="13" stroke="#00C9A7" stroke-width="1.5" fill="none"/>
          <circle cx="14" cy="7"  r="3" fill="#00C9A7"/>
          <circle cx="22" cy="19" r="3" fill="#00C9A7"/>
          <circle cx="6"  cy="19" r="3" fill="#00C9A7"/>
          <line x1="14" y1="7" x2="22" y2="19" stroke="#00C9A7" stroke-width="1.2" opacity="0.5"/>
          <line x1="14" y1="7" x2="6"  y2="19" stroke="#00C9A7" stroke-width="1.2" opacity="0.5"/>
          <line x1="22" y1="19" x2="6" y2="19" stroke="#00C9A7" stroke-width="1.2" opacity="0.5"/>
        </svg>
      </div>
      <div class="logo-text">
        <span class="logo-name">{$appConfig.project_name}</span>
        <span class="logo-tag">mesh compute</span>
      </div>
    </div>

    <!-- Mobile Hamburger Toggle -->
    <button class="menu-toggle" class:open={menuOpen} on:click={() => menuOpen = !menuOpen} aria-label="Toggle menu">
      {#if menuOpen}
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>
      {:else}
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="3" y1="12" x2="21" y2="12"></line><line x1="3" y1="6" x2="21" y2="6"></line><line x1="3" y1="18" x2="21" y2="18"></line></svg>
      {/if}
    </button>

    <nav class="nav" class:open={menuOpen}>
      {#each navItems as item}
        <a
          href={item.href}
          class="nav-item"
          class:active={item.href === '/'
            ? $page.url.pathname === '/'
            : $page.url.pathname.startsWith(item.href)}
          id="nav-{item.label.toLowerCase()}"
          on:click={() => menuOpen = false}
        >
          <span class="nav-label">{item.label}</span>
        </a>
      {/each}
      
      <!-- Mobile connection status -->
      <div class="connection-status mobile-status">
        <span class="dot" class:online={$connected} class:offline={!$connected}></span>
        <span class="status-text">{$connected ? 'Agent connected' : 'Connecting…'}</span>
      </div>
    </nav>

    <div class="sidebar-footer">
      <div class="connection-status">
        <span class="dot" class:online={$connected} class:offline={!$connected}></span>
        <span class="status-text">{$connected ? 'Agent connected' : 'Connecting…'}</span>
      </div>
    </div>
  </aside>

  <!-- Main content -->
  <main class="main-content">
    {#if $sseError}
      <div class="banner banner-error global-banner" role="alert">
        ⚠️ {$sseError}
      </div>
    {/if}
    <slot />
  </main>
  <GlobalDialog />
</div>

<style>
  .app-shell {
    display: flex;
    height: 100vh;
    overflow: hidden;
  }

  /* --- Sidebar --- */
  .sidebar {
    width: 220px;
    min-width: 220px;
    background: var(--bg-secondary);
    border-right: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    padding: var(--space-6) 0;
    height: 100vh;
    position: sticky;
    top: 0;
  }

  .logo-area {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: 0 var(--space-6) var(--space-6);
    border-bottom: 1px solid var(--border);
    margin-bottom: var(--space-4);
  }
  .logo-mark svg {
    filter: drop-shadow(0 0 6px rgba(0,201,167,0.4));
  }
  .logo-name {
    font-weight: 700;
    font-size: var(--text-base);
    letter-spacing: -0.02em;
    color: var(--text-primary);
    display: block;
  }
  .logo-tag {
    font-size: var(--text-xs);
    color: var(--accent);
    font-family: var(--font-mono);
    display: block;
  }

  .nav {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 0 var(--space-3);
  }
  .nav-item {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-3) var(--space-4);
    border-radius: var(--radius-md);
    font-size: var(--text-sm);
    font-weight: 500;
    color: var(--text-secondary);
    text-decoration: none;
    transition: all var(--transition);
    position: relative;
  }
  .nav-item:hover {
    color: var(--text-primary);
    background: var(--bg-tertiary);
    text-decoration: none;
  }
  .nav-item.active {
    color: var(--accent);
    background: var(--accent-dim);
  }
  .nav-item.active::before {
    content: '';
    position: absolute;
    left: 0;
    top: 20%;
    bottom: 20%;
    width: 3px;
    background: var(--accent);
    border-radius: 0 3px 3px 0;
  }

  .sidebar-footer {
    padding: var(--space-4) var(--space-6) 0;
    border-top: 1px solid var(--border);
    margin-top: var(--space-4);
  }
  .connection-status {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  .dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    flex-shrink: 0;
  }
  .dot.online  { background: var(--online); }
  .dot.offline { background: var(--offline); }
  .status-text { font-size: var(--text-xs); color: var(--text-muted); }

  /* --- Main --- */
  .main-content {
    flex: 1;
    overflow-y: scroll;
    padding: var(--space-8);
    display: flex;
    flex-direction: column;
    gap: var(--space-6);
    /* Do NOT use transform/will-change/filter here - they create a new
       containing block that breaks position:fixed in child components */
  }
  .global-banner { margin: 0 0 var(--space-4) 0; }

  /* Mobile Toggle */
  .menu-toggle {
    display: none;
    background: transparent;
    border: none;
    color: var(--text-primary);
    cursor: pointer;
    padding: var(--space-2);
    align-items: center;
    justify-content: center;
    border-radius: var(--radius-sm);
    transition: all var(--transition);
  }
  .menu-toggle:hover {
    background: var(--bg-card-hover);
    color: var(--accent);
  }

  .mobile-status {
    display: none;
    margin-top: auto;
    padding: var(--space-4) var(--space-2) 0;
    border-top: 1px solid var(--border);
  }

  @media (max-width: 768px) {
    .app-shell {
      flex-direction: column;
      height: 100dvh;
    }

    .sidebar {
      width: 100%;
      height: 60px;
      min-height: 60px;
      min-width: unset;
      border-right: none;
      border-bottom: 1px solid var(--border);
      flex-direction: row;
      align-items: center;
      justify-content: space-between;
      padding: var(--space-3) var(--space-4);
      position: relative;
      z-index: 1000;
    }

    .logo-area {
      border-bottom: none;
      margin-bottom: 0;
      padding: 0;
    }

    .logo-tag {
      display: none;
    }

    .menu-toggle {
      display: flex;
    }

    .nav {
      display: none;
      position: fixed;
      top: 60px;
      left: 0;
      right: 0;
      bottom: 0;
      background: var(--bg-secondary);
      flex-direction: column;
      padding: var(--space-6);
      gap: var(--space-3);
      z-index: 999;
    }

    .nav.open {
      display: flex;
      overflow-y: auto;
      animation: slide-down 200ms cubic-bezier(0.4, 0, 0.2, 1);
    }

    .nav-item {
      padding: var(--space-4) var(--space-5);
      font-size: var(--text-base);
    }
    .nav-item.active::before {
      display: none;
    }

    .sidebar-footer {
      display: none;
    }

    .mobile-status {
      display: flex;
    }

    .main-content {
      padding: var(--space-4);
      gap: var(--space-4);
    }

    @keyframes slide-down {
      from { opacity: 0; transform: translateY(-10px); }
      to   { opacity: 1; transform: translateY(0); }
    }
  }
</style>
