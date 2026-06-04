<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { tasks, nodes, submitTask, cancelTask, loadAll, appConfig, type Task } from '$lib/stores/cluster';

  // ── WASM file store ──────────────────────────────────────────────────────────
  interface WASMFile { name: string; path: string; size: number; }
  let wasmFiles: WASMFile[] = [];
  let wasmMode = false;
  let selectedWasm = '';
  let wasmArgs = '';
  let wasmDropdownOpen = false;

  async function loadWasmFiles() {
    try {
      const res = await fetch('/api/storage/wasm');
      if (res.ok) wasmFiles = await res.json();
    } catch { wasmFiles = []; }
  }

  // ── Terminal state ───────────────────────────────────────────────────────────
  let command = '';
  let preferredNode = '';
  let submitting = false;
  let submitError = '';
  let expandedTask: string | null = null;
  let terminalInput: HTMLTextAreaElement;
  let terminalFocused = false;
  let commandHistory: string[] = [];
  let historyIndex = -1;

  // Live timer tick + polling fallback
  let tick = 0;
  const tickInterval = setInterval(() => {
    tick++;
    if ($tasks.some(t => t.status === 'running' || t.status === 'pending')) loadAll();
  }, 1000);
  onDestroy(() => clearInterval(tickInterval));
  onMount(() => loadWasmFiles());

  // Auto-resize textarea
  function autoResize(node: HTMLTextAreaElement) {
    const resize = () => {
      node.style.height = 'auto';
      node.style.height = Math.min(node.scrollHeight, 200) + 'px';
    };
    node.addEventListener('input', resize);
    resize();
    return { destroy() { node.removeEventListener('input', resize); } };
  }

  function focusTerminal(e: MouseEvent) {
    const target = e.target as HTMLElement;
    if (target.tagName === 'SELECT' || target.tagName === 'OPTION') return;
    if (target.closest('.wasm-panel')) return;
    if (window.getSelection()?.toString()) return;
    terminalInput?.focus();
  }

  function navigateHistory(dir: 1 | -1) {
    if (dir === 1) {
      if (historyIndex < commandHistory.length - 1) {
        historyIndex++;
        command = commandHistory[historyIndex];
      }
    } else {
      if (historyIndex > 0) {
        historyIndex--;
        command = commandHistory[historyIndex];
      } else {
        historyIndex = -1;
        command = '';
      }
    }
    setTimeout(() => {
      if (terminalInput) {
        terminalInput.selectionStart = terminalInput.selectionEnd = terminalInput.value.length;
      }
    }, 0);
  }

  function handleKeyDown(e: KeyboardEvent) {
    if (wasmMode) return; // WASM mode uses its own submit
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSubmit();
      return;
    }
    if (e.key === 'ArrowUp') {
      const beforeCursor = command.slice(0, terminalInput?.selectionStart ?? 0);
      if (!beforeCursor.includes('\n')) { e.preventDefault(); navigateHistory(1); }
      return;
    }
    if (e.key === 'ArrowDown') {
      const afterCursor = command.slice(terminalInput?.selectionEnd ?? command.length);
      if (!afterCursor.includes('\n')) { e.preventDefault(); navigateHistory(-1); }
      return;
    }
  }

  async function handleSubmit() {
    if (submitting) return;
    let cmd: string;
    if (wasmMode) {
      if (!selectedWasm) { submitError = 'Select a .wasm module first'; return; }
      cmd = `wasm://${selectedWasm}${wasmArgs.trim() ? ' ' + wasmArgs.trim() : ''}`;
    } else {
      if (!command.trim()) return;
      cmd = command.trim();
    }
    submitting = true;
    submitError = '';
    try {
      await submitTask(cmd, preferredNode || undefined);
      if (!wasmMode) {
        if (commandHistory[0] !== cmd) commandHistory = [cmd, ...commandHistory].slice(0, 100);
        historyIndex = -1;
        command = '';
      }
    } catch (err: any) {
      submitError = err.message ?? 'Failed to submit task';
    } finally {
      submitting = false;
      if (!wasmMode) setTimeout(() => terminalInput?.focus(), 0);
    }
  }

  function toggleWasmMode() {
    wasmMode = !wasmMode;
    submitError = '';
    if (wasmMode) loadWasmFiles();
    else setTimeout(() => terminalInput?.focus(), 50);
  }

  function selectWasmFile(name: string) {
    selectedWasm = name;
    wasmDropdownOpen = false;
  }

  // ── Task card helpers ────────────────────────────────────────────────────────
  function isWasmTask(cmd: string) { return cmd?.startsWith('wasm://'); }

  function statusClass(status: Task['status']) {
    const map: Record<string, string> = {
      running: 'badge-warning', completed: 'badge-online',
      failed: 'badge-danger', pending: 'badge-offline', cancelled: 'badge-offline'
    };
    return map[status] ?? 'badge-offline';
  }

  function statusIcon(status: Task['status']) {
    const map: Record<string, string> = {
      running: '⏳', completed: '✅', failed: '❌', pending: '🕐', cancelled: '⊘'
    };
    return map[status] ?? '?';
  }

  function duration(task: Task, _tick: number): string {
    if (!task.started_at) return '-';
    const end = task.finished_at ? new Date(task.finished_at) : new Date();
    const ms = end.getTime() - new Date(task.started_at).getTime();
    if (ms < 1000) return `${ms}ms`;
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
    return `${Math.floor(ms / 60000)}m ${Math.floor((ms % 60000) / 1000)}s`;
  }

  function formatSize(bytes: number): string {
    if (bytes < 1024) return `${bytes}B`;
    if (bytes < 1048576) return `${(bytes / 1024).toFixed(0)}KB`;
    return `${(bytes / 1048576).toFixed(1)}MB`;
  }

  $: onlineNodes = $nodes.filter(n => n.status === 'online');
</script>

<svelte:head>
  <title>Tasks - {$appConfig.project_name}</title>
  <meta name="description" content="Distributed tasks running across your {$appConfig.project_name} cluster" />
</svelte:head>

<div class="tasks-page animate-fade-in">

  <!-- Page header -->
  <div class="section-header">
    <div>
      <h1>Tasks</h1>
      <p class="text-secondary" style="margin-top: 4px; font-size: var(--text-sm)">
        {$tasks.filter(t => t.status === 'running').length} running ·
        {$tasks.length} total
      </p>
    </div>
    <!-- Mode toggle pill -->
    <div class="mode-toggle-group" role="group" aria-label="Task execution mode">
      <button
        class="mode-btn"
        class:active={!wasmMode}
        on:click={() => wasmMode && toggleWasmMode()}
        id="shell-mode-btn"
        aria-pressed={!wasmMode}
      >
        <span class="mode-icon">⬛</span> Shell
      </button>
      <button
        class="mode-btn wasm-mode-btn"
        class:active={wasmMode}
        on:click={() => !wasmMode && toggleWasmMode()}
        id="wasm-mode-btn"
        aria-pressed={wasmMode}
      >
        <span class="mode-icon wasm-hex">⬡</span> WASM Sandbox
      </button>
    </div>
  </div>

  <!-- ── Terminal (shell mode) ──────────────────────────────────────────────── -->
  {#if !wasmMode}
    <!-- svelte-ignore a11y-click-events-have-key-events -->
    <!-- svelte-ignore a11y-no-static-element-interactions -->
    <div
      class="terminal"
      class:terminal-focused={terminalFocused}
      on:click={focusTerminal}
    >
      <div class="terminal-titlebar">
        <div class="terminal-dots">
          <span class="dot dot-red"></span>
          <span class="dot dot-yellow"></span>
          <span class="dot dot-green"></span>
        </div>
        <span class="terminal-title">bash - {$appConfig.project_name}</span>
        <div class="terminal-titlebar-right">
          {#if submitting}<span class="terminal-spinner" aria-label="Submitting…"></span>{/if}
        </div>
      </div>

      <div class="terminal-body">
        {#if submitError}
          <div class="terminal-error" role="alert">✗ {submitError}</div>
        {/if}

        <div class="terminal-input-row">
          <span class="terminal-prompt" aria-hidden="true">
            <span class="p-host">{$appConfig.project_name.toLowerCase()}</span><span
              class="p-sep">:</span><span
              class="p-path">~</span><span
              class="p-dollar"> $</span>
          </span>

          <textarea
            bind:this={terminalInput}
            bind:value={command}
            class="terminal-textarea"
            placeholder="type a command and press Enter…"
            rows="1"
            spellcheck="false"
            autocapitalize="off"
            id="terminal-input"
            on:keydown={handleKeyDown}
            on:focus={() => { terminalFocused = true; submitError = ''; }}
            on:blur={() => terminalFocused = false}
            use:autoResize
          ></textarea>
        </div>

        <div class="terminal-footer">
          <span class="terminal-hint">Enter to run · Shift+Enter for newline · ↑↓ history</span>
          {#if onlineNodes.length > 1}
            <div class="terminal-node-row">
              <span class="terminal-hint">node:</span>
              <select class="terminal-select" bind:value={preferredNode} id="terminal-node-select">
                <option value="">auto (best)</option>
                {#each onlineNodes as node}
                  <option value={node.id}>
                    {node.name} - {((node.ram_total - node.ram_used) / 1024 ** 3).toFixed(1)}GB free
                  </option>
                {/each}
              </select>
            </div>
          {/if}
        </div>
      </div>
    </div>
  {/if}

  <!-- ── WASM Sandbox Panel ─────────────────────────────────────────────────── -->
  {#if wasmMode}
    <div class="wasm-panel">
      <!-- Animated header -->
      <div class="wasm-header">
        <div class="wasm-logo-wrap">
          <svg class="wasm-logo" viewBox="0 0 40 40" fill="none" xmlns="http://www.w3.org/2000/svg">
            <polygon points="20,2 38,11 38,29 20,38 2,29 2,11" stroke="url(#wg)" stroke-width="2" fill="url(#wbg)" />
            <text x="50%" y="55%" dominant-baseline="middle" text-anchor="middle" fill="white" font-size="11" font-weight="bold" font-family="monospace">W</text>
            <defs>
              <linearGradient id="wg" x1="0" y1="0" x2="40" y2="40">
                <stop offset="0%" stop-color="#a855f7"/>
                <stop offset="100%" stop-color="#06b6d4"/>
              </linearGradient>
              <linearGradient id="wbg" x1="0" y1="0" x2="40" y2="40">
                <stop offset="0%" stop-color="rgba(168,85,247,0.15)"/>
                <stop offset="100%" stop-color="rgba(6,182,212,0.15)"/>
              </linearGradient>
            </defs>
          </svg>
        </div>
        <div>
          <div class="wasm-title">WebAssembly Sandbox</div>
          <div class="wasm-subtitle">Cross-platform · Air-gapped · Memory-isolated</div>
        </div>
        <div class="wasm-badges">
          <span class="wasm-badge">🔒 No Network</span>
          <span class="wasm-badge">📁 Virtual FS</span>
          <span class="wasm-badge">🧠 128MB cap</span>
        </div>
      </div>

      <!-- WASM controls -->
      <div class="wasm-controls">
        <!-- Module selector -->
        <div class="wasm-field">
          <label class="wasm-label" for="wasm-module-selector">Module</label>
          <div class="wasm-select-wrap" id="wasm-module-selector">
            <!-- svelte-ignore a11y-click-events-have-key-events -->
            <!-- svelte-ignore a11y-no-static-element-interactions -->
            <div
              class="wasm-select-trigger"
              class:has-value={!!selectedWasm}
              on:click={() => { wasmDropdownOpen = !wasmDropdownOpen; loadWasmFiles(); }}
            >
              <span class="wasm-hex-sm">⬡</span>
              <span class="wasm-select-text">
                {selectedWasm || 'Select a .wasm module…'}
              </span>
              <span class="wasm-chevron" class:open={wasmDropdownOpen}>▾</span>
            </div>
            {#if wasmDropdownOpen}
              <div class="wasm-dropdown">
                {#if wasmFiles.length === 0}
                  <div class="wasm-empty">
                    <div class="wasm-empty-icon">📭</div>
                    <div>No .wasm files in shared storage</div>
                    <div class="wasm-empty-hint">Upload a .wasm file via the Storage page</div>
                  </div>
                {:else}
                  {#each wasmFiles as f}
                    <!-- svelte-ignore a11y-click-events-have-key-events -->
                    <!-- svelte-ignore a11y-no-static-element-interactions -->
                    <div
                      class="wasm-option"
                      class:selected={selectedWasm === f.name}
                      on:click={() => selectWasmFile(f.name)}
                    >
                      <span class="wasm-option-icon">⬡</span>
                      <div>
                        <div class="wasm-option-name">{f.name}</div>
                        <div class="wasm-option-size">{formatSize(f.size)}</div>
                      </div>
                      {#if selectedWasm === f.name}
                        <span class="wasm-check">✓</span>
                      {/if}
                    </div>
                  {/each}
                {/if}
              </div>
            {/if}
          </div>
        </div>

        <!-- Arguments -->
        <div class="wasm-field">
          <label class="wasm-label" for="wasm-args">Arguments <span class="wasm-label-hint">(optional)</span></label>
          <input
            id="wasm-args"
            class="wasm-args-input"
            type="text"
            bind:value={wasmArgs}
            placeholder="--flag value1 value2"
            spellcheck="false"
          />
        </div>

        <!-- Node selector -->
        {#if onlineNodes.length > 1}
          <div class="wasm-field">
            <label class="wasm-label" for="wasm-node-select">Target Node</label>
            <select id="wasm-node-select" class="wasm-node-select" bind:value={preferredNode}>
              <option value="">auto (best)</option>
              {#each onlineNodes as node}
                <option value={node.id}>{node.name}</option>
              {/each}
            </select>
          </div>
        {/if}

        <!-- Command preview -->
        {#if selectedWasm}
          <div class="wasm-preview">
            <span class="wasm-preview-label">Command</span>
            <code class="wasm-preview-code">wasm://{selectedWasm}{wasmArgs.trim() ? ' ' + wasmArgs.trim() : ''}</code>
          </div>
        {/if}

        <!-- Security info panel -->
        <div class="wasm-security">
          <div class="wasm-security-title">🛡️ Sandbox Guarantees</div>
          <div class="wasm-security-grid">
            <div class="wasm-security-item">
              <span class="wasm-security-icon ok">✓</span>
              <span>Filesystem isolated to virtual <code>/storage</code></span>
            </div>
            <div class="wasm-security-item">
              <span class="wasm-security-icon ok">✓</span>
              <span>Network sockets blocked (air-gapped)</span>
            </div>
            <div class="wasm-security-item">
              <span class="wasm-security-icon ok">✓</span>
              <span>Max 128 MiB linear memory enforced</span>
            </div>
            <div class="wasm-security-item">
              <span class="wasm-security-icon ok">✓</span>
              <span>Runs on any node regardless of OS/CPU</span>
            </div>
          </div>
        </div>

        <!-- Error -->
        {#if submitError}
          <div class="wasm-error" role="alert">✗ {submitError}</div>
        {/if}

        <!-- Run button -->
        <button
          class="wasm-run-btn"
          class:loading={submitting}
          disabled={submitting || !selectedWasm}
          on:click={handleSubmit}
          id="wasm-run-btn"
        >
          {#if submitting}
            <span class="wasm-run-spinner"></span> Running…
          {:else}
            <span>▶</span> Run WASM Module
          {/if}
        </button>
      </div>
    </div>
  {/if}

  <!-- ── Task history ───────────────────────────────────────────────────────── -->
  {#if $tasks.length > 0}
    <div class="tasks-list">
      {#each $tasks as task (task.id)}
        <div
          class="task-card card"
          class:running={task.status === 'running'}
          class:wasm-task={isWasmTask(task.command)}
        >
          <div
            class="task-header"
            on:click={() => expandedTask = expandedTask === task.id ? null : task.id}
            role="button"
            tabindex="0"
            on:keydown={(e) => e.key === 'Enter' && (expandedTask = expandedTask === task.id ? null : task.id)}
          >
            <div class="task-left">
              <span class="status-icon">{statusIcon(task.status)}</span>
              <div style="min-width:0">
                <div class="task-command-wrap">
                  {#if isWasmTask(task.command)}
                    <span class="wasm-task-badge" title="WebAssembly Sandbox">⬡ WASM</span>
                  {/if}
                  <span class="task-command mono">{task.command}</span>
                </div>
                <div class="task-meta">
                  <span class="text-muted">Node: {task.assigned_node?.slice(0, 12) ?? '-'}…</span>
                  <span class="text-muted">·</span>
                  <span class="text-muted">{duration(task, tick)}</span>
                </div>
              </div>
            </div>
            <div class="task-right">
              <span class="badge {statusClass(task.status)}">{task.status}</span>
              {#if task.status === 'running' || task.status === 'pending'}
                <button
                  class="btn btn-danger btn-xs"
                  on:click|stopPropagation={() => cancelTask(task.id)}
                >Cancel</button>
              {/if}
              {#if isWasmTask(task.command)}
                <span class="wasm-shield-icon" title="Executed in WASM sandbox">🛡️</span>
              {/if}
              <span class="expand-icon" class:rotated={expandedTask === task.id}>▾</span>
            </div>
          </div>

          {#if expandedTask === task.id}
            <div class="task-output">
              {#if isWasmTask(task.command)}
                <div class="wasm-output-banner">
                  ⬡ WebAssembly Sandbox · Air-gapped · Read-only /storage mount
                </div>
              {/if}
              {#if task.error}
                <div class="output-error">Error: {task.error}</div>
              {/if}
              {#if task.output}
                <pre class="output-pre">{task.output}</pre>
              {:else if task.status === 'running'}
                <p class="text-muted" style="font-size: var(--text-sm); padding: var(--space-3)">Task is running…</p>
              {:else}
                <p class="text-muted" style="font-size: var(--text-sm); padding: var(--space-3)">No output</p>
              {/if}
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}

</div>

<style>
  .tasks-page { display: flex; flex-direction: column; gap: var(--space-5); }

  /* ── Mode Toggle ─────────────────────────────────────────────────────────── */
  .section-header { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: var(--space-3); }
  .mode-toggle-group {
    display: flex;
    background: rgba(255,255,255,0.03);
    border: 1px solid rgba(255,255,255,0.08);
    border-radius: 10px;
    padding: 3px;
    gap: 2px;
  }
  .mode-btn {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 7px 16px;
    border-radius: 7px;
    border: none;
    background: transparent;
    color: var(--text-muted);
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.2s ease;
  }
  .mode-btn.active {
    background: rgba(255,255,255,0.07);
    color: var(--text-primary);
    box-shadow: 0 1px 3px rgba(0,0,0,0.3);
  }
  .wasm-mode-btn.active {
    background: linear-gradient(135deg, rgba(168,85,247,0.2), rgba(6,182,212,0.2));
    color: #e9d5ff;
    box-shadow: 0 0 0 1px rgba(168,85,247,0.3), 0 1px 3px rgba(0,0,0,0.3);
  }
  .mode-icon { font-size: 14px; }
  .wasm-hex { background: linear-gradient(135deg, #a855f7, #06b6d4); -webkit-background-clip: text; -webkit-text-fill-color: transparent; }

  /* ── Terminal ─────────────────────────────────────────────────────────────── */
  .terminal {
    background: #0d1117;
    border: 1px solid #30363d;
    border-radius: 12px;
    overflow: hidden;
    cursor: text;
    transition: border-color 0.2s ease, box-shadow 0.2s ease;
  }
  .terminal-focused {
    border-color: #3fb950;
    box-shadow: 0 0 0 3px rgba(63, 185, 80, 0.12);
  }
  .terminal-titlebar {
    background: #161b22;
    border-bottom: 1px solid #21262d;
    padding: 10px 16px;
    display: flex;
    align-items: center;
    gap: var(--space-3);
    user-select: none;
  }
  .terminal-dots { display: flex; gap: 6px; }
  .dot { width: 12px; height: 12px; border-radius: 50%; display: block; flex-shrink: 0; }
  .dot-red    { background: #ff5f57; }
  .dot-yellow { background: #febc2e; }
  .dot-green  { background: #28c840; }
  .terminal-title {
    flex: 1;
    text-align: center;
    font-size: 12px;
    color: #6e7681;
    font-family: var(--font-mono);
    letter-spacing: 0.02em;
  }
  .terminal-titlebar-right { width: 54px; display: flex; justify-content: flex-end; align-items: center; }
  @keyframes spin { to { transform: rotate(360deg); } }
  .terminal-spinner {
    display: inline-block; width: 13px; height: 13px;
    border: 2px solid rgba(63, 185, 80, 0.3);
    border-top-color: #3fb950;
    border-radius: 50%;
    animation: spin 0.65s linear infinite;
  }
  .terminal-body { padding: 14px 18px 12px; font-family: 'JetBrains Mono', 'Fira Code', 'Menlo', monospace; }
  .terminal-error { color: #f85149; font-size: 13px; margin-bottom: 10px; font-family: var(--font-mono); }
  .terminal-input-row { display: flex; align-items: flex-start; gap: 10px; }
  .terminal-prompt { font-size: 14px; line-height: 1.5; white-space: nowrap; flex-shrink: 0; padding-top: 1px; user-select: none; }
  .p-host   { color: #3fb950; font-weight: 600; }
  .p-sep    { color: #6e7681; }
  .p-path   { color: #58a6ff; }
  .p-dollar { color: #6e7681; }
  .terminal-textarea {
    flex: 1; background: transparent; border: none; outline: none;
    color: #e6edf3; font-family: inherit; font-size: 14px; line-height: 1.5;
    resize: none; min-height: 21px; caret-color: #3fb950; padding: 0; overflow: hidden;
  }
  .terminal-textarea::placeholder { color: #3d444d; }
  .terminal-footer {
    display: flex; align-items: center; justify-content: space-between;
    margin-top: 10px; padding-top: 8px; border-top: 1px solid #21262d;
    gap: var(--space-3); flex-wrap: wrap;
  }
  .terminal-hint { font-size: 11px; color: #3d444d; user-select: none; font-family: var(--font-mono); }
  .terminal-node-row { display: flex; align-items: center; gap: 6px; }
  .terminal-select {
    background: #161b22; border: 1px solid #30363d; border-radius: 4px;
    color: #6e7681; font-size: 11px; font-family: var(--font-mono);
    padding: 2px 6px; outline: none; cursor: pointer; transition: border-color 0.15s;
  }
  .terminal-select:focus { border-color: #3fb950; color: #e6edf3; }

  /* ── WASM Panel ──────────────────────────────────────────────────────────── */
  @keyframes wasm-glow {
    0%, 100% { box-shadow: 0 0 0 1px rgba(168,85,247,0.3), 0 0 30px rgba(168,85,247,0.08); }
    50%       { box-shadow: 0 0 0 1px rgba(6,182,212,0.4), 0 0 40px rgba(6,182,212,0.12); }
  }
  @keyframes hexspin {
    from { transform: rotate(0deg); }
    to   { transform: rotate(360deg); }
  }
  .wasm-panel {
    border-radius: 16px;
    overflow: hidden;
    border: 1px solid rgba(168,85,247,0.25);
    background: linear-gradient(145deg, rgba(168,85,247,0.04) 0%, rgba(6,182,212,0.04) 100%);
    animation: wasm-glow 4s ease-in-out infinite;
  }
  .wasm-header {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    padding: 20px 24px 16px;
    border-bottom: 1px solid rgba(168,85,247,0.15);
    background: linear-gradient(135deg, rgba(168,85,247,0.08), rgba(6,182,212,0.06));
    flex-wrap: wrap;
  }
  .wasm-logo-wrap { flex-shrink: 0; }
  .wasm-logo { width: 40px; height: 40px; animation: hexspin 12s linear infinite; }
  .wasm-title { font-size: 16px; font-weight: 700; color: #e9d5ff; letter-spacing: 0.01em; }
  .wasm-subtitle { font-size: 12px; color: rgba(168,85,247,0.7); margin-top: 2px; }
  .wasm-badges { display: flex; gap: 6px; flex-wrap: wrap; margin-left: auto; }
  .wasm-badge {
    font-size: 11px;
    padding: 3px 10px;
    border-radius: 20px;
    background: rgba(168,85,247,0.1);
    border: 1px solid rgba(168,85,247,0.2);
    color: #c4b5fd;
    font-weight: 500;
  }

  .wasm-controls { padding: 20px 24px; display: flex; flex-direction: column; gap: 16px; }
  .wasm-field { display: flex; flex-direction: column; gap: 6px; }
  .wasm-label {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .wasm-label-hint { font-weight: 400; text-transform: none; color: var(--text-muted); }

  /* WASM dropdown selector */
  .wasm-select-wrap { position: relative; }
  .wasm-select-trigger {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px 14px;
    border-radius: 10px;
    border: 1px solid rgba(168,85,247,0.25);
    background: rgba(168,85,247,0.05);
    cursor: pointer;
    transition: all 0.2s ease;
    user-select: none;
  }
  .wasm-select-trigger:hover {
    border-color: rgba(168,85,247,0.45);
    background: rgba(168,85,247,0.08);
  }
  .wasm-select-trigger.has-value { border-color: rgba(168,85,247,0.5); }
  .wasm-hex-sm { font-size: 16px; background: linear-gradient(135deg, #a855f7, #06b6d4); -webkit-background-clip: text; -webkit-text-fill-color: transparent; }
  .wasm-select-text { flex: 1; font-size: 14px; color: var(--text-secondary); font-family: var(--font-mono); }
  .wasm-select-trigger.has-value .wasm-select-text { color: #e9d5ff; }
  .wasm-chevron { color: var(--text-muted); transition: transform 0.2s; font-size: 12px; }
  .wasm-chevron.open { transform: rotate(180deg); }

  .wasm-dropdown {
    position: absolute;
    top: calc(100% + 4px);
    left: 0; right: 0;
    z-index: 100;
    border-radius: 10px;
    border: 1px solid rgba(168,85,247,0.3);
    background: #0d1117;
    box-shadow: 0 8px 32px rgba(0,0,0,0.5), 0 0 0 1px rgba(168,85,247,0.1);
    overflow: hidden;
    max-height: 280px;
    overflow-y: auto;
  }
  .wasm-option {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 11px 14px;
    cursor: pointer;
    transition: background 0.15s;
    border-bottom: 1px solid rgba(255,255,255,0.04);
  }
  .wasm-option:last-child { border-bottom: none; }
  .wasm-option:hover { background: rgba(168,85,247,0.1); }
  .wasm-option.selected { background: rgba(168,85,247,0.15); }
  .wasm-option-icon { font-size: 14px; background: linear-gradient(135deg, #a855f7, #06b6d4); -webkit-background-clip: text; -webkit-text-fill-color: transparent; }
  .wasm-option-name { font-size: 13px; color: #e9d5ff; font-family: var(--font-mono); }
  .wasm-option-size { font-size: 11px; color: var(--text-muted); }
  .wasm-check { margin-left: auto; color: #a855f7; font-weight: bold; }
  .wasm-empty { padding: 24px; text-align: center; color: var(--text-muted); font-size: 13px; }
  .wasm-empty-icon { font-size: 28px; margin-bottom: 8px; }
  .wasm-empty-hint { font-size: 11px; margin-top: 4px; color: rgba(255,255,255,0.25); }

  /* Args input */
  .wasm-args-input {
    padding: 11px 14px;
    border-radius: 10px;
    border: 1px solid rgba(255,255,255,0.1);
    background: rgba(255,255,255,0.03);
    color: var(--text-primary);
    font-size: 13px;
    font-family: var(--font-mono);
    outline: none;
    transition: border-color 0.2s;
    width: 100%;
    box-sizing: border-box;
  }
  .wasm-args-input:focus { border-color: rgba(168,85,247,0.5); box-shadow: 0 0 0 3px rgba(168,85,247,0.1); }
  .wasm-args-input::placeholder { color: var(--text-muted); }

  .wasm-node-select {
    padding: 10px 14px;
    border-radius: 10px;
    border: 1px solid rgba(255,255,255,0.1);
    background: rgba(255,255,255,0.03);
    color: var(--text-secondary);
    font-size: 13px;
    outline: none;
    cursor: pointer;
    width: 100%;
    box-sizing: border-box;
  }

  /* Command preview */
  .wasm-preview {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 14px;
    border-radius: 8px;
    background: rgba(0,0,0,0.3);
    border: 1px solid rgba(255,255,255,0.06);
  }
  .wasm-preview-label { font-size: 11px; color: var(--text-muted); white-space: nowrap; }
  .wasm-preview-code {
    font-family: var(--font-mono);
    font-size: 12px;
    color: #c4b5fd;
    word-break: break-all;
  }

  /* Security grid */
  .wasm-security {
    padding: 14px;
    border-radius: 10px;
    background: rgba(0,0,0,0.2);
    border: 1px solid rgba(255,255,255,0.05);
  }
  .wasm-security-title { font-size: 12px; font-weight: 600; color: var(--text-secondary); margin-bottom: 10px; }
  .wasm-security-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
  .wasm-security-item { display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--text-muted); }
  .wasm-security-icon.ok { color: #22c55e; font-weight: bold; flex-shrink: 0; }

  .wasm-error { color: #f87171; font-size: 13px; padding: 10px 14px; background: rgba(248,113,113,0.08); border-radius: 8px; border: 1px solid rgba(248,113,113,0.2); }

  /* Run button */
  @keyframes wasm-pulse {
    0%, 100% { box-shadow: 0 0 0 0 rgba(168,85,247,0.4); }
    50%       { box-shadow: 0 0 0 8px rgba(168,85,247,0); }
  }
  .wasm-run-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 10px;
    padding: 14px 24px;
    border-radius: 12px;
    border: none;
    background: linear-gradient(135deg, #a855f7, #7c3aed);
    color: white;
    font-size: 15px;
    font-weight: 600;
    cursor: pointer;
    transition: all 0.2s ease;
    animation: wasm-pulse 2.5s ease-in-out infinite;
    letter-spacing: 0.01em;
  }
  .wasm-run-btn:hover:not(:disabled) {
    background: linear-gradient(135deg, #9333ea, #6d28d9);
    transform: translateY(-1px);
    box-shadow: 0 4px 20px rgba(168,85,247,0.4);
  }
  .wasm-run-btn:disabled { opacity: 0.5; cursor: not-allowed; animation: none; }
  .wasm-run-btn.loading { animation: none; }
  .wasm-run-spinner {
    width: 14px; height: 14px;
    border: 2px solid rgba(255,255,255,0.3);
    border-top-color: white;
    border-radius: 50%;
    animation: spin 0.65s linear infinite;
    flex-shrink: 0;
  }

  /* ── Task list cards ─────────────────────────────────────────────────────── */
  .tasks-list { display: flex; flex-direction: column; gap: var(--space-3); }
  .task-card { padding: 0; overflow: hidden; }
  .task-card.running { border-color: var(--warning); }

  /* WASM task card gets a special purple border glow */
  .task-card.wasm-task {
    border-color: rgba(168,85,247,0.3);
    background: linear-gradient(145deg, rgba(168,85,247,0.03), transparent);
  }
  .task-card.wasm-task.running {
    border-color: rgba(168,85,247,0.5);
    box-shadow: 0 0 12px rgba(168,85,247,0.12);
  }

  .task-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: var(--space-4) var(--space-5);
    cursor: pointer;
    gap: var(--space-3);
  }
  .task-header:hover { background: var(--bg-card-hover); }
  .task-left { display: flex; align-items: flex-start; gap: var(--space-3); min-width: 0; flex: 1; }
  .status-icon { font-size: 18px; flex-shrink: 0; margin-top: 1px; }
  .task-command-wrap { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .task-command {
    font-size: var(--text-sm);
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 460px;
  }
  .task-meta { display: flex; gap: var(--space-2); font-size: var(--text-xs); margin-top: 3px; }
  .task-right { display: flex; align-items: center; gap: var(--space-2); flex-shrink: 0; }
  .expand-icon { color: var(--text-muted); transition: transform var(--transition); font-size: var(--text-sm); }
  .expand-icon.rotated { transform: rotate(180deg); }
  .wasm-task-badge {
    font-size: 10px;
    padding: 2px 7px;
    border-radius: 4px;
    background: rgba(168,85,247,0.15);
    border: 1px solid rgba(168,85,247,0.3);
    color: #c4b5fd;
    font-weight: 600;
    white-space: nowrap;
    flex-shrink: 0;
    font-family: var(--font-mono);
  }
  .wasm-shield-icon { font-size: 14px; }

  .task-output { border-top: 1px solid var(--border); background: var(--bg-primary); }
  .wasm-output-banner {
    font-size: 11px;
    color: rgba(168,85,247,0.8);
    background: rgba(168,85,247,0.06);
    border-bottom: 1px solid rgba(168,85,247,0.1);
    padding: 7px 16px;
    font-family: var(--font-mono);
  }
  .output-pre {
    font-family: var(--font-mono);
    font-size: var(--text-xs);
    color: var(--text-secondary);
    padding: var(--space-4);
    overflow-x: auto;
    white-space: pre-wrap;
    word-break: break-all;
    max-height: 300px;
    overflow-y: auto;
  }
  .output-error { color: var(--danger); font-size: var(--text-sm); padding: var(--space-3) var(--space-5); background: rgba(255,107,107,0.05); }
  .btn-xs { padding: 2px 8px; font-size: var(--text-xs); border-radius: var(--radius-sm); }

  @media (max-width: 640px) {
    .wasm-security-grid { grid-template-columns: 1fr; }
    .wasm-badges { display: none; }
    .task-header { flex-direction: column; align-items: flex-start; padding: var(--space-3) var(--space-4); }
    .task-left { width: 100%; }
    .task-command { max-width: 100%; white-space: pre-wrap; word-break: break-all; }
    .task-right { width: 100%; justify-content: flex-end; border-top: 1px solid var(--border); padding-top: var(--space-2); }
  }
</style>
