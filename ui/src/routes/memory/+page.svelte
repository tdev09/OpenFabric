<script lang="ts">
  import { onMount } from 'svelte';
  import { settings, saveSettings, appConfig } from '$lib/stores/cluster';
  import { dialog } from '$lib/stores/dialog';

  interface MemoryEntry {
    id: string;
    content: string;
    source: string;
    source_id: string;
    created_at: string;
    last_used_at: string;
    use_count: number;
    tags?: string[];
  }

  let memories: MemoryEntry[] = [];
  let searchQuery = '';
  let newMemoryText = '';
  let loading = true;
  let adding = false;
  let clearing = false;
  let copiedId: string | null = null;

  // Active filter tab
  let activeTabFilter = 'all'; // 'all' | 'explicit' | 'chat' | 'popular'

  async function loadMemories() {
    loading = true;
    try {
      const res = await fetch('/api/memory');
      if (res.ok) {
        memories = await res.json();
      }
    } catch (err) {
      console.error('Failed to load memories:', err);
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    loadMemories();
  });

  async function handleAdd() {
    if (!newMemoryText.trim()) return;
    adding = true;
    try {
      const res = await fetch('/api/memory', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content: newMemoryText })
      });
      if (res.ok) {
        newMemoryText = '';
        await loadMemories();
      } else {
        const err = await res.json();
        dialog.alert(err.error || 'Failed to add memory', 'Add Memory Failed', '❌');
      }
    } catch (err: any) {
      dialog.alert(err.message || 'Failed to add memory', 'Add Memory Failed', '❌');
    } finally {
      adding = false;
    }
  }

  async function handleDelete(id: string) {
    const confirmed = await dialog.confirm(
      'Are you sure you want to delete this memory?',
      'Delete Memory',
      '🧠',
      'Delete',
      'Cancel',
      true
    );
    if (!confirmed) return;
    try {
      const res = await fetch(`/api/memory/${id}`, { method: 'DELETE' });
      if (res.ok) {
        memories = memories.filter(m => m.id !== id);
      }
    } catch (err) {
      console.error('Failed to delete memory:', err);
    }
  }

  async function handleClearAll() {
    const confirmed = await dialog.confirm(
      'Are you sure you want to delete all saved memories? This cannot be undone.',
      'Clear All Memories',
      '🚨',
      'Delete All',
      'Cancel',
      true
    );
    if (!confirmed) return;
    clearing = true;
    try {
      const res = await fetch('/api/memory', { method: 'DELETE' });
      if (res.ok) {
        memories = [];
      }
    } catch (err) {
      console.error('Failed to clear memories:', err);
    } finally {
      clearing = false;
    }
  }

  let searchResults: { memory: MemoryEntry; score: number }[] = [];
  let isSearching = false;

  async function triggerSearch() {
    if (!searchQuery.trim()) {
      isSearching = false;
      return;
    }
    isSearching = true;
    try {
      const res = await fetch(`/api/memory/search?q=${encodeURIComponent(searchQuery)}`);
      if (res.ok) {
        searchResults = await res.json();
      } else {
        const err = await res.json();
        dialog.alert(err.error || 'Failed to search memories', 'Search Failed', '❌');
      }
    } catch (err: any) {
      dialog.alert(err.message || 'Failed to search memories', 'Search Failed', '❌');
    }
  }

  function handleClearSearch() {
    searchQuery = '';
    isSearching = false;
  }

  function handleCopy(content: string, id: string) {
    navigator.clipboard.writeText(content);
    copiedId = id;
    setTimeout(() => {
      if (copiedId === id) copiedId = null;
    }, 2000);
  }

  // Derive stats
  $: totalMemories = memories.length;
  $: manualFacts = memories.filter(m => m.source === 'explicit').length;
  $: autoFacts = memories.filter(m => m.source !== 'explicit').length;
  $: totalUses = memories.reduce((acc, m) => acc + (m.use_count || 0), 0);

  // Apply tab + text search filter
  $: displayedMemories = (() => {
    let list = isSearching 
      ? searchResults.map(r => ({ memory: r.memory, score: r.score })) 
      : memories.map(m => ({ memory: m, score: undefined as number | undefined }));
    
    // Filter by tab
    if (activeTabFilter === 'explicit') {
      list = list.filter(item => item.memory.source === 'explicit');
    } else if (activeTabFilter === 'chat') {
      list = list.filter(item => item.memory.source !== 'explicit');
    } else if (activeTabFilter === 'popular') {
      list = list.filter(item => (item.memory.use_count || 0) > 0)
                 .sort((a, b) => (b.memory.use_count || 0) - (a.memory.use_count || 0));
    }

    // Filter by search query if semantic search didn't run (client-side text match fallback)
    if (!isSearching && searchQuery.trim()) {
      const q = searchQuery.toLowerCase();
      list = list.filter(item => item.memory.content.toLowerCase().includes(q));
    }

    return list;
  })();

  function formatDate(dStr: string) {
    if (!dStr) return '';
    const date = new Date(dStr);
    return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' });
  }
</script>

<svelte:head>
  <title>Fabric Memory - {$appConfig.project_name}</title>
</svelte:head>

<div class="memory-page animate-fade-in">
  <!-- Header -->
  <div class="section-header">
    <div>
      <h1 class="page-title">Fabric Memory</h1>
      <p class="section-desc">Private persistent context layer that learns facts and user preferences.</p>
    </div>
    {#if memories.length > 0}
      <button class="btn btn-danger btn-sm clear-all-btn" on:click={handleClearAll} disabled={clearing}>
        <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="btn-icon-svg"><path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/><line x1="10" x2="10" y1="11" y2="17"/><line x1="14" x2="14" y1="11" y2="17"/></svg>
        {clearing ? 'Clearing…' : 'Clear All Memories'}
      </button>
    {/if}
  </div>

  <!-- Stats Grid -->
  <div class="stats-row">
    <div class="stat-card memory-theme card">
      <div class="stat-icon-wrapper">
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="stat-svg"><path d="M12 22c5.523 0 10-4.477 10-10S17.523 2 12 2 2 6.477 2 12s4.477 10 10 10z"/><path d="M12 6v6l4 2"/></svg>
      </div>
      <div class="stat-details">
        <span class="stat-val">{totalMemories}</span>
        <span class="stat-lbl">Total Memories</span>
      </div>
    </div>
    <div class="stat-card manual-theme card">
      <div class="stat-icon-wrapper">
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="stat-svg"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>
      </div>
      <div class="stat-details">
        <span class="stat-val">{manualFacts}</span>
        <span class="stat-lbl">Manual Facts</span>
      </div>
    </div>
    <div class="stat-card chat-theme card">
      <div class="stat-icon-wrapper">
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="stat-svg"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>
      </div>
      <div class="stat-details">
        <span class="stat-val">{autoFacts}</span>
        <span class="stat-lbl">Chat Extracted</span>
      </div>
    </div>
    <div class="stat-card use-theme card">
      <div class="stat-icon-wrapper">
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="stat-svg"><path d="M13 2L3 14h9l-1 8 10-12h-9l1-8z"/></svg>
      </div>
      <div class="stat-details">
        <span class="stat-val">{totalUses}</span>
        <span class="stat-lbl">Total Lookups</span>
      </div>
    </div>
  </div>

  <div class="grid">
    <!-- Left panel: Settings, Add & Search -->
    <div class="sidebar-panel">
      <!-- Settings Card -->
      {#if $settings}
        <div class="card settings-card premium-sidebar-card">
          <h3 class="panel-title">
            <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="panel-icon"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
            Memory Engine
          </h3>
          
          <div class="toggle-row">
            <div class="toggle-info">
              <span class="toggle-title">Context Injection</span>
              <p class="hint">Injects remembered context into LLM system prompts.</p>
            </div>
            <label class="toggle">
              <input
                type="checkbox"
                checked={$settings.memory_enabled}
                on:change={async (e) => {
                  if ($settings) {
                    const copy = { ...$settings, memory_enabled: e.currentTarget.checked };
                    await saveSettings(copy);
                  }
                }}
              />
              <span class="toggle-slider"></span>
            </label>
          </div>

          <div class="toggle-row" class:disabled={!$settings.memory_enabled}>
            <div class="toggle-info">
              <span class="toggle-title">Autonomous Extraction</span>
              <p class="hint">Extract facts from finished chat sessions automatically.</p>
            </div>
            <label class="toggle">
              <input
                type="checkbox"
                checked={$settings.memory_auto_extract}
                disabled={!$settings.memory_enabled}
                on:change={async (e) => {
                  if ($settings) {
                    const copy = { ...$settings, memory_auto_extract: e.currentTarget.checked };
                    await saveSettings(copy);
                  }
                }}
              />
              <span class="toggle-slider"></span>
            </label>
          </div>
        </div>
      {/if}

      <!-- Add Memory Card -->
      <div class="card add-card premium-sidebar-card">
        <h3 class="panel-title">
          <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="panel-icon"><path d="M12 5v14"/><path d="M5 12h14"/></svg>
          Add Explicit Fact
        </h3>
        <p class="hint" style="margin-bottom: var(--space-3);">Manually program facts or preferences you want the AI to remember.</p>
        <form on:submit|preventDefault={handleAdd} class="add-form">
          <div class="textarea-wrapper">
            <textarea
              class="input textarea"
              placeholder="e.g., My primary local database is Postgres running on port 5432."
              bind:value={newMemoryText}
              rows={3}
              disabled={adding}
            ></textarea>
          </div>
          <button class="btn btn-primary remember-btn" type="submit" disabled={adding || !newMemoryText.trim()}>
            {#if adding}
              <span class="spinner-icon">⏳</span>
              Remembering…
            {:else}
              <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="btn-icon-svg"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/><polyline points="17 21 17 13 7 13 7 21"/><polyline points="7 3 7 8 15 8"/></svg>
              Remember Fact
            {/if}
          </button>
        </form>
      </div>

      <!-- Search Card -->
      <div class="card search-card premium-sidebar-card">
        <h3 class="panel-title">
          <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="panel-icon"><circle cx="11" cy="11" r="8"/><line x1="21" x2="16.65" y1="21" y2="16.65"/></svg>
          Search Context
        </h3>
        <p class="hint" style="margin-bottom: var(--space-3);">Filter memories by keyword or trigger a semantic database search.</p>
        <div class="search-box">
          <div class="search-input-container">
            <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="search-icon-svg"><circle cx="11" cy="11" r="8"/><line x1="21" x2="16.65" y1="21" y2="16.65"/></svg>
            <input
              type="text"
              class="input search-input"
              placeholder="Filter or search..."
              bind:value={searchQuery}
              on:keydown={(e) => e.key === 'Enter' && triggerSearch()}
            />
            {#if searchQuery}
              <button class="clear-search-btn" on:click={handleClearSearch} title="Clear Search">✕</button>
            {/if}
          </div>
          <button class="btn btn-secondary search-action-btn" on:click={triggerSearch}>Search</button>
        </div>
      </div>
    </div>

    <!-- Right panel: Memory Cards List -->
    <div class="content-panel">
      <!-- Tabs Selector -->
      <div class="tabs-header-wrapper">
        <div class="tabs-header">
          <button class="tab-btn" class:active={activeTabFilter === 'all'} on:click={() => activeTabFilter = 'all'}>
            All Memories <span class="tab-count">{totalMemories}</span>
          </button>
          <button class="tab-btn" class:active={activeTabFilter === 'explicit'} on:click={() => activeTabFilter = 'explicit'}>
            Manual <span class="tab-count">{manualFacts}</span>
          </button>
          <button class="tab-btn" class:active={activeTabFilter === 'chat'} on:click={() => activeTabFilter = 'chat'}>
            Chat Extracted <span class="tab-count">{autoFacts}</span>
          </button>
          <button class="tab-btn" class:active={activeTabFilter === 'popular'} on:click={() => activeTabFilter = 'popular'}>
            Frequently Used
          </button>
        </div>
      </div>

      {#if loading}
        <div class="card loader-state">
          <span class="animate-spin" style="font-size: 28px;">⚡</span>
          <p class="text-secondary" style="margin-top: 16px;">Loading memories from cluster database…</p>
        </div>
      {:else if displayedMemories.length === 0}
        <div class="card empty-state">
          <span class="empty-icon">🧠</span>
          <h3>No Stored Memories Found</h3>
          <p class="text-muted">
            {#if searchQuery}
              We couldn't find any context matching "{searchQuery}". Try a different search query or clear the filter.
            {:else}
              The memory engine has no stored items for this tab yet. Start by writing an explicit fact on the left column.
            {/if}
          </p>
        </div>
      {:else}
        <div class="memories-list">
          {#each displayedMemories as item (item.memory.id)}
            <div class="card memory-card animate-fade-in" class:semantic-result={item.score !== undefined}>
              <div class="memory-card-header">
                <div class="badge-row">
                  {#if item.memory.source === 'explicit'}
                    <span class="badge badge-manual">
                      <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="badge-svg-icon"><path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>
                      Manual Fact
                    </span>
                  {:else}
                    <span class="badge badge-chat">
                      <svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="badge-svg-icon"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>
                      Chat Extracted
                    </span>
                  {/if}

                  {#if item.score !== undefined}
                    <span class="badge badge-score" class:high-match={item.score >= 0.75} class:med-match={item.score < 0.75}>
                      {Math.round(item.score * 100)}% Match
                    </span>
                  {/if}
                </div>
                <span class="memory-date">{formatDate(item.memory.created_at)}</span>
              </div>
              
              <div class="memory-bubble">
                <span class="quote-mark open-quote">“</span>
                <p class="memory-content">{item.memory.content}</p>
                <span class="quote-mark close-quote">”</span>
              </div>

              <div class="memory-card-footer">
                <div class="memory-stats">
                  <span class="stat-pill">
                    <svg xmlns="http://www.w3.org/2000/svg" width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="stat-pill-svg"><path d="M2 12h20"/><path d="M20 12a8 8 0 1 0-16 0"/></svg>
                    Retrieved: <strong class="text-accent">{item.memory.use_count}</strong>
                  </span>
                  {#if item.memory.last_used_at}
                    <span class="stat-dot-divider">•</span>
                    <span class="last-retrieved-time">Last lookup: {formatDate(item.memory.last_used_at)}</span>
                  {/if}
                </div>

                <div class="action-buttons-group">
                  <button
                    class="btn-copy"
                    on:click={() => handleCopy(item.memory.content, item.memory.id)}
                    aria-label="Copy to Clipboard"
                    title="Copy memory content"
                  >
                    {#if copiedId === item.memory.id}
                      <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="#00C9A7" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="copied-check-icon"><polyline points="20 6 9 17 4 12"/></svg>
                      <span class="copied-text text-accent">Copied</span>
                    {:else}
                      <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
                    {/if}
                  </button>
                  <button
                    class="btn-delete"
                    on:click={() => handleDelete(item.memory.id)}
                    aria-label="Delete memory"
                    title="Delete memory"
                  >
                    <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>
                  </button>
                </div>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>
</div>

<style>
  .memory-page {
    display: flex;
    flex-direction: column;
    gap: var(--space-6);
  }
  .page-title {
    font-size: var(--text-3xl);
    font-weight: 700;
    letter-spacing: -0.02em;
    background: linear-gradient(135deg, var(--text-primary) 30%, var(--accent) 100%);
    -webkit-background-clip: text;
    -webkit-text-fill-color: transparent;
  }
  .section-desc {
    font-size: var(--text-sm);
    color: var(--text-secondary);
    margin-top: 4px;
  }
  .clear-all-btn {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  .btn-icon-svg {
    flex-shrink: 0;
  }

  .grid {
    display: grid;
    grid-template-columns: 320px 1fr;
    gap: var(--space-6);
    align-items: start;
  }
  @media (max-width: 992px) {
    .grid {
      grid-template-columns: 1fr;
    }
  }

  /* Stats Row */
  .stats-row {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: var(--space-4);
  }
  .stat-card {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    transition: all var(--transition);
    position: relative;
    overflow: hidden;
  }
  .stat-card::before {
    content: '';
    position: absolute;
    top: 0;
    left: 0;
    width: 4px;
    height: 100%;
    background: transparent;
    transition: background var(--transition);
  }
  
  /* Color themes for stats cards */
  .stat-card.memory-theme:hover { border-color: rgba(0, 201, 167, 0.4); box-shadow: 0 4px 20px rgba(0, 201, 167, 0.08); }
  .stat-card.memory-theme::before { background: var(--accent); }
  .stat-card.memory-theme .stat-icon-wrapper { background: rgba(0, 201, 167, 0.1); color: var(--accent); }

  .stat-card.manual-theme:hover { border-color: rgba(0, 132, 255, 0.4); box-shadow: 0 4px 20px rgba(0, 132, 255, 0.08); }
  .stat-card.manual-theme::before { background: #0084FF; }
  .stat-card.manual-theme .stat-icon-wrapper { background: rgba(0, 132, 255, 0.1); color: #0084FF; }

  .stat-card.chat-theme:hover { border-color: rgba(167, 0, 255, 0.4); box-shadow: 0 4px 20px rgba(167, 0, 255, 0.08); }
  .stat-card.chat-theme::before { background: #A700FF; }
  .stat-card.chat-theme .stat-icon-wrapper { background: rgba(167, 0, 255, 0.1); color: #A700FF; }

  .stat-card.use-theme:hover { border-color: rgba(255, 179, 0, 0.4); box-shadow: 0 4px 20px rgba(255, 179, 0, 0.08); }
  .stat-card.use-theme::before { background: #FFB300; }
  .stat-card.use-theme .stat-icon-wrapper { background: rgba(255, 179, 0, 0.1); color: #FFB300; }

  .stat-icon-wrapper {
    width: 48px;
    height: 48px;
    border-radius: var(--radius-md);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }
  .stat-svg {
    width: 24px;
    height: 24px;
  }
  .stat-details {
    display: flex;
    flex-direction: column;
  }
  .stat-val {
    font-size: var(--text-2xl);
    font-weight: 700;
    color: var(--text-primary);
    line-height: 1.1;
  }
  .stat-lbl {
    font-size: var(--text-xs);
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    margin-top: 2px;
  }

  /* Sidebar Panel */
  .sidebar-panel {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }
  .premium-sidebar-card {
    background: linear-gradient(145deg, var(--bg-card) 0%, rgba(22, 27, 34, 0.6) 100%);
    backdrop-filter: blur(8px);
    border: 1px solid var(--border);
  }
  .panel-title {
    font-size: var(--text-base);
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: var(--space-2);
    border-bottom: 1px solid var(--border);
    padding-bottom: var(--space-2);
    margin-bottom: var(--space-3);
    color: var(--text-primary);
  }
  .panel-icon {
    color: var(--accent);
  }
  
  /* Toggle Switch Panel styling */
  .toggle-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-4);
    padding: var(--space-3) 0;
    border-bottom: 1px dashed var(--border);
  }
  .toggle-row:last-child {
    border-bottom: none;
  }
  .toggle-row.disabled {
    opacity: 0.4;
  }
  .toggle-info {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .toggle-title {
    font-size: var(--text-sm);
    font-weight: 500;
    color: var(--text-primary);
  }
  .hint {
    font-size: var(--text-xs);
    color: var(--text-muted);
    line-height: 1.3;
  }

  /* Add Memory Form */
  .add-form {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }
  .textarea-wrapper {
    position: relative;
    border-radius: var(--radius-md);
  }
  .textarea {
    width: 100%;
    min-height: 90px;
    resize: none;
    line-height: 1.4;
  }
  .remember-btn {
    display: flex;
    justify-content: center;
    align-items: center;
    gap: var(--space-2);
    width: 100%;
    padding: 10px;
  }
  .spinner-icon {
    animation: spin 1.5s linear infinite;
  }

  /* Search Input Panel */
  .search-box {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .search-input-container {
    position: relative;
    display: flex;
    align-items: center;
  }
  .search-icon-svg {
    position: absolute;
    left: var(--space-3);
    color: var(--text-muted);
    pointer-events: none;
  }
  .search-input {
    padding-left: 36px;
    padding-right: 32px;
    height: 38px;
    font-size: var(--text-sm);
  }
  .clear-search-btn {
    position: absolute;
    right: var(--space-3);
    background: transparent;
    border: none;
    color: var(--text-muted);
    font-size: var(--text-sm);
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    transition: all var(--transition);
  }
  .clear-search-btn:hover {
    color: var(--text-primary);
    background: rgba(255, 255, 255, 0.1);
  }
  .search-action-btn {
    justify-content: center;
    height: 38px;
  }

  /* Content Panel */
  .content-panel {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  
  /* Tabs */
  .tabs-header-wrapper {
    position: relative;
    border-bottom: 1px solid var(--border);
    padding-bottom: 2px;
  }
  .tabs-header {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1);
  }
  .tab-btn {
    background: transparent;
    border: none;
    color: var(--text-secondary);
    padding: 8px 16px;
    border-radius: var(--radius-sm);
    font-size: var(--text-sm);
    font-weight: 500;
    cursor: pointer;
    transition: all var(--transition);
    display: flex;
    align-items: center;
    gap: 8px;
    position: relative;
  }
  .tab-btn::after {
    content: '';
    position: absolute;
    bottom: -3px;
    left: 0;
    width: 100%;
    height: 2px;
    background: transparent;
    transition: background var(--transition);
  }
  .tab-btn:hover {
    color: var(--text-primary);
  }
  .tab-btn.active {
    color: var(--accent);
  }
  .tab-btn.active::after {
    background: var(--accent);
  }
  .tab-count {
    font-size: var(--text-xs);
    background: rgba(255, 255, 255, 0.08);
    padding: 1px 6px;
    border-radius: 20px;
    color: var(--text-muted);
    font-weight: 500;
    transition: all var(--transition);
  }
  .tab-btn.active .tab-count {
    background: var(--accent-dim);
    color: var(--accent);
  }

  /* Memory Cards List */
  .memories-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .memory-card {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    transition: all var(--transition);
    position: relative;
  }
  .memory-card::before {
    content: '';
    position: absolute;
    left: 0;
    top: 0;
    bottom: 0;
    width: 3px;
    background: transparent;
    transition: background var(--transition);
    border-top-left-radius: var(--radius-lg);
    border-bottom-left-radius: var(--radius-lg);
  }
  .memory-card:hover {
    border-color: var(--border-accent);
    transform: translateY(-2px);
    box-shadow: 0 6px 20px rgba(0, 201, 167, 0.05);
  }
  
  /* Card source colors left stripe */
  .memory-card:has(.badge-manual)::before {
    background: var(--accent);
  }
  .memory-card:has(.badge-chat)::before {
    background: #A700FF;
  }
  
  .memory-card.semantic-result {
    border-color: rgba(0, 201, 167, 0.25);
    background: linear-gradient(160deg, var(--bg-secondary) 0%, rgba(0, 201, 167, 0.02) 100%);
  }

  .memory-card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .badge-row {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  .badge-manual {
    background: var(--accent-dim);
    color: var(--accent);
    border: 1px solid rgba(0, 201, 167, 0.2);
  }
  .badge-chat {
    background: rgba(167, 0, 255, 0.12);
    color: #b340ff;
    border: 1px solid rgba(167, 0, 255, 0.2);
  }
  .badge-score {
    background: rgba(255, 255, 255, 0.06);
    color: var(--text-secondary);
    border: 1px solid var(--border);
  }
  .badge-score.high-match {
    background: rgba(0, 201, 167, 0.1);
    color: var(--accent);
    border-color: rgba(0, 201, 167, 0.25);
  }
  .badge-score.med-match {
    background: rgba(246, 201, 14, 0.1);
    color: var(--warning);
    border-color: rgba(246, 201, 14, 0.25);
  }
  .badge-svg-icon {
    margin-right: 2px;
  }
  .memory-date {
    font-size: var(--text-xs);
    color: var(--text-muted);
  }
  
  /* Speech bubble context styling */
  .memory-bubble {
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-3) var(--space-4);
    display: flex;
    position: relative;
  }
  .quote-mark {
    font-family: Georgia, serif;
    font-size: var(--text-3xl);
    line-height: 1;
    color: var(--accent);
    opacity: 0.35;
    position: absolute;
    user-select: none;
  }
  .open-quote {
    top: 4px;
    left: 10px;
  }
  .close-quote {
    bottom: -10px;
    right: 10px;
  }
  .memory-content {
    font-size: var(--text-sm);
    line-height: 1.5;
    color: var(--text-primary);
    padding: 0 var(--space-5);
    font-style: italic;
    flex-grow: 1;
  }

  .memory-card-footer {
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-top: 1px solid var(--border);
    padding-top: var(--space-3);
    margin-top: var(--space-1);
  }
  .memory-stats {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    font-size: var(--text-xs);
    color: var(--text-muted);
  }
  .stat-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    background: rgba(255, 255, 255, 0.04);
    padding: 2px 8px;
    border-radius: 20px;
    border: 1px solid var(--border);
  }
  .stat-pill-svg {
    color: var(--text-muted);
  }
  .stat-dot-divider {
    opacity: 0.3;
  }
  .last-retrieved-time {
    font-size: var(--text-xs);
  }

  .action-buttons-group {
    display: flex;
    align-items: center;
    gap: var(--space-1);
  }
  
  .btn-copy, .btn-delete {
    background: transparent;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 6px;
    border-radius: var(--radius-sm);
    transition: all var(--transition);
  }
  .btn-copy:hover {
    color: var(--accent);
    background: var(--accent-dim);
  }
  .btn-delete:hover {
    color: var(--danger);
    background: rgba(255, 107, 107, 0.1);
  }
  .copied-text {
    font-size: var(--text-xs);
    font-weight: 500;
  }

  .loader-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: var(--space-12);
  }
</style>

