<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { chatStream, fmtRAM, models, runableModels, llmStatus,
    fetchModels, fetchLLMStatus, fetchMCPServers, type ChatMessage, type Shard
  } from '$lib/stores/llm';
  import { marked } from 'marked';
  import { appConfig } from '$lib/stores/cluster';

  function renderMarkdown(content: string, showCursor: boolean): string {
    try {
      const text = showCursor ? content + ' <span class="cursor">▋</span>' : content;
      return marked.parse(text, { breaks: true }) as string;
    } catch (e) {
      console.error(e);
      return content;
    }
  }

  // Active chat details
  let model = '';
  let activeSessionId = '';
  let activeSession: any = null;
  let chatSessions: any[] = [];

  interface DisplayMessage {
    role: 'user' | 'assistant';
    content: string;
    done?: boolean;
    tokSec?: number;
    shards?: Shard[];
    citations?: any[];
    toolCalls?: Array<{ server: string; tool: string; args: any; result?: string; completed?: boolean; duration?: number; startedAt?: number }>;
  }

  let messages: DisplayMessage[] = [];
  let useBrain = false;
  let brainTopK = 5;
  let brainStatus: any = null;

  async function loadBrainStatus() {
    try {
      const res = await fetch('/api/brain/status');
      if (res.ok) {
        brainStatus = await res.json();
      }
    } catch {}
  }
  let input = '';
  let generating = false;
  let currentTokSec = 0;
  let currentShards: Shard[] = [];
  let errorMsg = '';
  let inferenceInfoMsg = '';
  let controller: AbortController | null = null;
  let messagesEl: HTMLElement;
  let inputEl: HTMLTextAreaElement;

  // Real-time savings counter
  let lastSavings = 0;
  let isFocused = false;

  // Mobile sidebar toggle
  let showSidebar = false;

  // Custom Modals State
  let showDeleteModal = false;
  let sessionToDeleteId = '';

  let showRenameModal = false;
  let sessionToRenameId = '';
  let renameInputVal = '';

  async function loadSessionsList() {
    try {
      const res = await fetch('/api/llm/sessions');
      if (res.ok) {
        chatSessions = await res.json();
      }
    } catch {}
  }

  let mcpServers: any[] = [];
  let selectedMCPServers: Record<string, boolean> = {};
  let totalToolsCount = 0;

  async function loadMCPServers() {
    try {
      const statuses = await fetchMCPServers();
      mcpServers = statuses.filter(s => s.enabled);
      totalToolsCount = mcpServers.reduce((sum, s) => sum + s.tool_count, 0);
      mcpServers.forEach(s => {
        if (selectedMCPServers[s.name] === undefined) {
          selectedMCPServers[s.name] = true;
        }
      });
    } catch {}
  }

  onMount(async () => {
    await fetchLLMStatus();
    await fetchModels();
    await loadSessionsList();
    await loadBrainStatus();
    await loadMCPServers();

    const urlModel = $page.url.searchParams.get('model');
    if (urlModel) {
      model = urlModel;
      const existing = chatSessions.find(s => s.model === urlModel);
      if (existing) {
        await selectSession(existing.id);
      } else {
        await createNewChat();
      }
      // Clear the model query param from URL so refresh doesn't keep creating new sessions
      if (typeof window !== 'undefined') {
        const url = new URL(window.location.href);
        url.searchParams.delete('model');
        window.history.replaceState({}, '', url.toString());
      }
    } else if (chatSessions.length > 0) {
      await selectSession(chatSessions[0].id);
    } else {
      await createNewChat();
    }
    inputEl?.focus();
  });

  async function selectSession(sessionId: string) {
    if (generating) stopGeneration();
    activeSessionId = sessionId;
    inferenceInfoMsg = '';
    showSidebar = false; // close on mobile after selection
    try {
      const res = await fetch(`/api/llm/sessions/${sessionId}`);
      if (res.ok) {
        activeSession = await res.json();
        model = activeSession.model;
        messages = activeSession.messages.map((m: any) => ({
          role: m.role,
          content: m.content,
          done: true
        }));
        errorMsg = '';
        scrollBottom();
      }
    } catch (e) {
      errorMsg = 'Failed to load session';
    }
  }

  async function createNewChat() {
    if (generating) stopGeneration();
    inferenceInfoMsg = '';

    // Prevent spawning duplicate empty sessions if the current chat window is already empty
    if (activeSessionId && messages.length === 0 && (!model || model === activeSession?.model)) {
      inputEl?.focus();
      return;
    }

    if (!model) {
      if ($llmStatus && $llmStatus.local_models && $llmStatus.local_models.length > 0) {
        model = $llmStatus.local_models[0];
      } else if ($runableModels && $runableModels.length > 0) {
        model = $runableModels[0].model;
      } else {
        model = 'llama3.2:3b';
      }
    }

    try {
      const res = await fetch('/api/llm/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ model })
      });
      if (res.ok) {
        const newSess = await res.json();
        activeSessionId = newSess.id;
        activeSession = newSess;
        messages = [];
        errorMsg = '';
        await loadSessionsList();
        scrollBottom();
      }
    } catch (e) {
      errorMsg = 'Failed to create new chat';
    }
  }

  function scrollBottom() {
    tick().then(() => {
      if (messagesEl) messagesEl.scrollTop = messagesEl.scrollHeight;
    });
  }

  async function sendMessage() {
    const text = input.trim();
    if (!text || generating || !activeSessionId) return;

    input = '';
    errorMsg = '';
    inferenceInfoMsg = '';
    lastSavings = 0;
    messages = [...messages, { role: 'user', content: text }];

    // Append user message to server session
    try {
      await fetch(`/api/llm/sessions/${activeSessionId}/messages`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ role: 'user', content: text, sent_at: new Date().toISOString() })
      });
    } catch (e) {
      console.error('Failed to save user message', e);
    }

    // Add assistant placeholder
    const assistantIdx = messages.length;
    messages = [...messages, { role: 'assistant', content: '', done: false, toolCalls: [] }];
    scrollBottom();

    generating = true;
    currentTokSec = 0;
    currentShards = [];

    const context: ChatMessage[] = messages.slice(0, -1).map(m => ({
      role: m.role,
      content: m.content
    }));

    const activeMcpServers = mcpServers
      .filter(s => selectedMCPServers[s.name])
      .map(s => s.name);

    controller = chatStream(
      model,
      context,
      useBrain,
      brainTopK,
      activeMcpServers,
      // onBrainContext
      (retrieved) => {
        messages[assistantIdx] = {
          ...messages[assistantIdx],
          citations: retrieved
        };
        messages = messages;
      },
      // onToken
      (token, tokSec, shards) => {
        messages[assistantIdx] = {
          ...messages[assistantIdx],
          content: messages[assistantIdx].content + token
        };
        messages = messages;
        currentTokSec = tokSec;
        currentShards = shards;
        scrollBottom();
      },
      // onToolCall
      (server, tool, args) => {
        const currentMsg = messages[assistantIdx];
        const list = currentMsg.toolCalls || [];
        messages[assistantIdx] = {
          ...currentMsg,
          toolCalls: [...list, { server, tool, args, startedAt: Date.now(), completed: false }]
        };
        messages = messages;
        scrollBottom();
      },
      // onToolResult
      (tool, result) => {
        const currentMsg = messages[assistantIdx];
        if (currentMsg && currentMsg.toolCalls) {
          messages[assistantIdx] = {
            ...currentMsg,
            toolCalls: currentMsg.toolCalls.map(tc => {
              const fullToolName = tc.server + "__" + tc.tool;
              if (fullToolName === tool || tc.tool === tool) {
                const duration = Date.now() - (tc.startedAt || Date.now());
                let resultSummary = result;
                if (result.length > 100) {
                  resultSummary = result.substring(0, 100) + '...';
                }
                return { ...tc, result: resultSummary, completed: true, duration };
              }
              return tc;
            })
          };
          messages = messages;
          scrollBottom();
        }
      },
      // onDone
      async (tokSec) => {
        messages[assistantIdx] = { ...messages[assistantIdx], done: true, tokSec };
        messages = messages;
        generating = false;
        controller = null;

        const content = messages[assistantIdx]?.content || '';

        // Append assistant message to server session
        try {
          await fetch(`/api/llm/sessions/${activeSessionId}/messages`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ role: 'assistant', content, sent_at: new Date().toISOString() })
          });
        } catch (e) {
          console.error('Failed to save assistant message', e);
        }

        // Calculate savings
        const tokenCount = Math.round(content.length / 4);
        const savedUSD = (tokenCount / 1000) * 0.01;
        if (savedUSD > 0) {
          lastSavings = savedUSD;
          const existing = parseFloat(localStorage.getItem('openfabric_saved_costs') || '0') || 0;
          localStorage.setItem('openfabric_saved_costs', (existing + savedUSD).toString());
        }

        await loadSessionsList();
        inputEl?.focus();
      },
      // onError
      (err) => {
        errorMsg = err;
        messages[assistantIdx] = { ...messages[assistantIdx], done: true };
        messages = messages;
        generating = false;
        controller = null;
      },
      // onInferenceInfo
      (msg) => {
        inferenceInfoMsg = msg;
      }
    );
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      sendMessage();
    }
  }

  function stopGeneration() {
    controller?.abort();
    controller = null;
    generating = false;
  }

  function openDeleteModal(id: string, e: Event) {
    e.stopPropagation();
    sessionToDeleteId = id;
    showDeleteModal = true;
  }

  async function confirmDelete() {
    if (!sessionToDeleteId) return;
    const id = sessionToDeleteId;
    showDeleteModal = false;
    sessionToDeleteId = '';
    try {
      const res = await fetch(`/api/llm/sessions/${id}`, { method: 'DELETE' });
      if (res.ok) {
        if (activeSessionId === id) {
          activeSessionId = '';
          activeSession = null;
          messages = [];
        }
        await loadSessionsList();
        if (!activeSessionId && chatSessions.length > 0) {
          await selectSession(chatSessions[0].id);
        } else if (chatSessions.length === 0) {
          await createNewChat();
        }
      }
    } catch {
      errorMsg = 'Failed to delete chat';
    }
  }

  function openRenameModal(id: string, currentTitle: string, e: Event) {
    e.stopPropagation();
    sessionToRenameId = id;
    renameInputVal = currentTitle;
    showRenameModal = true;
  }

  async function confirmRename() {
    if (!sessionToRenameId || !renameInputVal.trim()) return;
    const id = sessionToRenameId;
    const newTitle = renameInputVal.trim();
    showRenameModal = false;
    sessionToRenameId = '';
    renameInputVal = '';

    try {
      const res = await fetch(`/api/llm/sessions/${id}/title`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: newTitle })
      });
      if (res.ok) {
        if (activeSessionId === id && activeSession) {
          activeSession.title = newTitle;
        }
        await loadSessionsList();
      }
    } catch {
      errorMsg = 'Failed to rename chat';
    }
  }

  let copiedIndex: number | null = null;
  let copyTimeout: any;

  function copyMessage(content: string, index: number) {
    navigator.clipboard.writeText(content).then(() => {
      copiedIndex = index;
      clearTimeout(copyTimeout);
      copyTimeout = setTimeout(() => {
        copiedIndex = null;
      }, 2000);
    }).catch(() => {});
  }

  interface SessionGroup {
    title: string;
    sessions: any[];
  }

  function groupSessionsByDate(sessList: any[]): SessionGroup[] {
    const today: any[] = [];
    const yesterday: any[] = [];
    const last7Days: any[] = [];
    const older: any[] = [];

    const now = new Date();
    const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    const startOfYesterday = new Date(startOfToday.getTime() - 24 * 60 * 60 * 1000);
    const startOf7DaysAgo = new Date(startOfToday.getTime() - 7 * 24 * 60 * 60 * 1000);

    for (const s of sessList) {
      const updated = new Date(s.updated_at);
      if (updated >= startOfToday) {
        today.push(s);
      } else if (updated >= startOfYesterday) {
        yesterday.push(s);
      } else if (updated >= startOf7DaysAgo) {
        last7Days.push(s);
      } else {
        older.push(s);
      }
    }

    const groups: SessionGroup[] = [];
    if (today.length > 0) groups.push({ title: 'Today', sessions: today });
    if (yesterday.length > 0) groups.push({ title: 'Yesterday', sessions: yesterday });
    if (last7Days.length > 0) groups.push({ title: 'Last 7 Days', sessions: last7Days });
    if (older.length > 0) groups.push({ title: 'Older', sessions: older });

    return groups;
  }

  $: groupedSessions = groupSessionsByDate(chatSessions);
  $: nodeNames = currentShards.length > 0
    ? currentShards.map(s => s.node_name).join(' → ')
    : null;
</script>

<svelte:head>
  <title>Chat - {activeSession?.title || model || 'New Chat'} - {$appConfig.project_name}</title>
  <meta name="description" content="Chat with local models distributed across your {$appConfig.project_name} cluster." />
</svelte:head>

<div class="chat-workspace">
  <!-- Sidebar -->
  <aside class="sidebar" class:open={showSidebar}>
    <div class="sidebar-header">
      <button class="btn btn-accent new-chat-btn" on:click={createNewChat}>
        <span>+</span> New Chat
      </button>
      <button class="mobile-close-btn" on:click={() => showSidebar = false}>✕</button>
    </div>

    <div class="history-list">
      {#each groupedSessions as group (group.title)}
        <div class="history-group">
          <h4 class="group-title">{group.title}</h4>
          {#each group.sessions as s (s.id)}
            <div 
              class="history-item" 
              class:active={activeSessionId === s.id}
              on:click={() => selectSession(s.id)}
            >
              <span class="item-icon">💬</span>
              <span class="item-title">{s.title || 'New Chat'}</span>
              <div class="item-actions">
                <button class="action-icon-btn" on:click={(e) => openRenameModal(s.id, s.title, e)} title="Rename">✏️</button>
                <button class="action-icon-btn delete" on:click={(e) => openDeleteModal(s.id, e)} title="Delete">🗑️</button>
              </div>
            </div>
          {/each}
        </div>
      {/each}

      {#if chatSessions.length === 0}
        <div class="no-history">No past conversations</div>
      {/if}
    </div>
  </aside>

  <!-- Mobile Overlay -->
  {#if showSidebar}
    <div class="sidebar-overlay" on:click={() => showSidebar = false}></div>
  {/if}

  <!-- Main Chat Window -->
  <main class="chat-main">
    <!-- Header -->
    <div class="chat-header">
      <div class="header-left">
        <button class="mobile-toggle-btn" on:click={() => showSidebar = true}>
          <span class="toggle-btn-text">☰ History</span>
          <span class="toggle-btn-text-mobile">☰</span>
        </button>
        <button class="btn btn-ghost btn-sm back-btn" on:click={() => goto('/models')}>
          <span class="back-btn-text">← Models</span>
          <span class="back-btn-text-mobile">←</span>
        </button>
        {#if activeSession}
          <div class="model-pill">
            <span class="model-name mono">{model}</span>
          </div>
        {/if}
      </div>
      <div class="header-title-display">
        {activeSession?.title || 'New Chat'}
      </div>
    </div>

    <!-- Error Banner -->
    {#if errorMsg}
      <div class="banner banner-error">{errorMsg}</div>
    {/if}

    <!-- Inference Info Banner -->
    {#if inferenceInfoMsg}
      <div class="banner banner-info inference-info-banner">
        <span class="info-icon">ℹ️</span>
        <div class="info-content">{inferenceInfoMsg}</div>
      </div>
    {/if}

    <!-- Messages List -->
    <div class="messages" bind:this={messagesEl}>
      {#if messages.length === 0}
        <div class="empty-state">
          <div class="empty-icon">🤖</div>
          <h2>Chat with <span class="mono">{model}</span></h2>
          <p class="empty-desc">Ask anything. Responses are generated locally and securely across your {$appConfig.project_name} cluster.</p>
        </div>
      {/if}

      {#each messages as msg, i (i)}
        <div class="message" class:user={msg.role === 'user'} class:assistant={msg.role === 'assistant'}>
          <div class="bubble">
            {#if msg.toolCalls && msg.toolCalls.length > 0}
              <div class="tool-calls-block">
                {#each msg.toolCalls as tc}
                  <div class="tool-call-item" class:completed={tc.completed}>
                    {#if !tc.completed}
                      <span class="spinner-sm" style="display:inline-block; vertical-align:middle; width: 10px; height: 10px; border-width: 1px; border-top-color: var(--accent);"></span>
                      <span class="tc-text">Executing {tc.server} → {tc.tool}({JSON.stringify(tc.args)})…</span>
                    {:else}
                      <span class="tc-status">✓</span>
                      <span class="tc-text">{tc.server}:{tc.tool} → {tc.result} ({tc.duration}ms)</span>
                    {/if}
                  </div>
                {/each}
              </div>
            {/if}
            <div class="markdown-content">
              {@html renderMarkdown(msg.content, msg.role === 'assistant' && !msg.done && generating && i === messages.length - 1)}
            </div>
            
            {#if msg.role === 'assistant' && msg.citations && msg.citations.length > 0}
              <div class="citations-panel">
                <span class="citations-label">Sources:</span>
                {#each msg.citations as cit, j}
                  <span class="citation-badge" title={cit.text}>
                    {cit.source}{#if cit.page && cit.page > 0} (p. {cit.page}){/if}
                  </span>
                  {#if j < msg.citations.length - 1}, {/if}
                {/each}
              </div>
            {/if}

            {#if msg.role === 'assistant' && msg.done && msg.content}
              <div class="message-footer">
                {#if msg.tokSec && msg.tokSec > 0}
                  <span class="tok-speed">{msg.tokSec.toFixed(1)} tok/s</span>
                {/if}
                <button 
                  class="copy-btn" 
                  class:copied={copiedIndex === i}
                  on:click={() => copyMessage(msg.content, i)} 
                  title="Copy message" 
                  aria-label="Copy message"
                >
                  {#if copiedIndex === i}
                    <!-- Check icon -->
                    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                      <polyline points="20 6 9 17 4 12"></polyline>
                    </svg>
                  {:else}
                    <!-- Copy icon -->
                    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                      <rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect>
                      <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path>
                    </svg>
                  {/if}
                </button>
              </div>
            {/if}
          </div>
        </div>
      {/each}
    </div>

    <!-- Real-time Cost Savings Floating Pill -->
    {#if !generating && lastSavings > 0}
      <div class="savings-floating-pill animate-fade-in">
        <span class="savings-pill-icon">💸</span>
        <div class="savings-pill-text">
          Saved <span class="savings-pill-amount">${lastSavings.toFixed(3)}</span> in cloud API costs
        </div>
        <button class="savings-pill-close" on:click={() => lastSavings = 0} aria-label="Dismiss">✕</button>
      </div>
    {/if}


    <!-- Input Area -->
    <div class="input-area">
      {#if mcpServers.length > 0}
        <div class="mcp-toolbar animate-fade-in">
          <span class="mcp-toolbar-label">{totalToolsCount} tools active from {mcpServers.length} integrations:</span>
          <div class="mcp-chips">
            {#each mcpServers as s}
              <label class="mcp-chip" class:selected={selectedMCPServers[s.name]}>
                <input type="checkbox" bind:checked={selectedMCPServers[s.name]} />
                <span class="chip-name">{s.name} ({s.tool_count})</span>
              </label>
            {/each}
          </div>
        </div>
      {/if}

      <div class="input-box-container" class:focused={isFocused} class:use-brain-active={useBrain}>
        <textarea
          bind:this={inputEl}
          bind:value={input}
          on:keydown={handleKeydown}
          on:focus={() => isFocused = true}
          on:blur={() => isFocused = false}
          placeholder="Message {model}… (Shift+Enter for newline)"
          rows="3"
          class="chat-input-textarea"
          disabled={generating}
          id="chat-input"
        ></textarea>
        
        <div class="input-box-bottom">
          <div class="input-bottom-left">
            <button
              type="button"
              class="btn use-brain-btn"
              class:active={useBrain}
              on:click={() => useBrain = !useBrain}
              id="use-brain-toggle"
            >
              <span class="brain-btn-text">🧠 Use my files: {useBrain ? 'ON' : 'OFF'}</span>
              <span class="brain-btn-text-mobile">🧠 Files: {useBrain ? 'ON' : 'OFF'}</span>
            </button>
          </div>
          
          <div class="input-bottom-right">
            <span class="hint">Enter ↵ to send</span>
            <button
              class="btn btn-accent send-btn-circle"
              class:stop-btn={generating}
              on:click={generating ? stopGeneration : sendMessage}
              disabled={!generating && (!input.trim() || !activeSessionId)}
              id="send-message"
              aria-label={generating ? "Stop generating" : "Send message"}
            >
              {#if generating}
                <svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor">
                  <rect x="4" y="4" width="16" height="16" rx="2" ry="2"></rect>
                </svg>
              {:else}
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                  <line x1="22" y1="2" x2="11" y2="13"></line>
                  <polygon points="22 2 15 22 11 13 2 9 22 2"></polygon>
                </svg>
              {/if}
            </button>
          </div>
        </div>
      </div>
    </div>
  </main>
  <!-- Custom Delete Modal -->
  {#if showDeleteModal}
    <div class="confirm-overlay" on:click|self={() => showDeleteModal = false} on:keydown={(e) => { if (e.key === 'Escape') showDeleteModal = false; }} role="button" tabindex="-1" aria-label="Close dialog">
      <div class="confirm-dialog">
        <div class="confirm-icon">🗑️</div>
        <h3 id="delete-modal-title">Delete chat session?</h3>
        <p>Are you sure you want to delete this chat session? This action cannot be undone.</p>
        <div class="confirm-actions">
          <button class="btn btn-ghost btn-sm" on:click={() => showDeleteModal = false}>Cancel</button>
          <button class="btn btn-danger btn-sm" on:click={confirmDelete}>Yes, delete</button>
        </div>
      </div>
    </div>
  {/if}

  <!-- Custom Rename Modal -->
  {#if showRenameModal}
    <div class="confirm-overlay" on:click|self={() => showRenameModal = false} on:keydown={(e) => { if (e.key === 'Escape') showRenameModal = false; }} role="button" tabindex="-1" aria-label="Close dialog">
      <div class="confirm-dialog">
        <div class="confirm-icon">✏️</div>
        <h3 id="rename-modal-title">Rename chat session</h3>
        <div class="rename-modal-input-wrapper">
          <input 
            type="text" 
            bind:value={renameInputVal} 
            class="rename-modal-input" 
            placeholder="Enter new title..." 
            on:keydown={(e) => {
              if (e.key === 'Enter') confirmRename();
            }}
          />
        </div>
        <div class="confirm-actions">
          <button class="btn btn-ghost btn-sm" on:click={() => showRenameModal = false}>Cancel</button>
          <button class="btn btn-accent btn-sm" on:click={confirmRename} disabled={!renameInputVal.trim()}>Save</button>
        </div>
      </div>
    </div>
  {/if}
</div>

<style>
  :global(.main-content:has(.chat-workspace)) {
    padding: 0 !important;
    overflow: hidden !important;
  }

  .chat-workspace {
    display: flex;
    height: 100%;
    width: 100%;
    background: var(--bg-primary);
    position: relative;
    overflow: hidden;
  }

  /* Sidebar styling */
  .sidebar {
    width: 260px;
    background: var(--bg-card);
    border-right: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    flex-shrink: 0;
    transition: transform 0.3s ease;
    z-index: 10;
  }
  .sidebar-header {
    padding: var(--space-4);
    border-bottom: 1px solid var(--border);
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .new-chat-btn {
    width: 100%;
    justify-content: center;
    gap: 8px;
    font-weight: 600;
  }
  .mobile-close-btn {
    display: none;
    background: none;
    border: none;
    color: var(--text-muted);
    font-size: 1.1rem;
    cursor: pointer;
  }
  .history-list {
    flex-grow: 1;
    overflow-y: auto;
    padding: var(--space-3) 0;
  }
  .history-group {
    margin-bottom: var(--space-4);
  }
  .group-title {
    font-size: 0.65rem;
    text-transform: uppercase;
    color: var(--text-muted);
    padding: 0 var(--space-4);
    margin: var(--space-2) 0;
    letter-spacing: 0.08em;
  }
  .history-item {
    display: flex;
    align-items: center;
    padding: var(--space-3) var(--space-4);
    cursor: pointer;
    font-size: var(--text-sm);
    color: var(--text-secondary);
    transition: all var(--transition);
    position: relative;
    gap: 8px;
  }
  .history-item:hover {
    background: rgba(255, 255, 255, 0.02);
    color: var(--text-primary);
  }
  .history-item.active {
    background: rgba(0, 201, 167, 0.05);
    border-left: 3px solid var(--accent);
    color: var(--text-primary);
  }
  .item-title {
    flex-grow: 1;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    padding-right: var(--space-2);
  }
  .item-actions {
    display: none;
    gap: 6px;
    align-items: center;
  }
  .history-item:hover .item-actions {
    display: flex;
  }
  .action-icon-btn {
    background: none;
    border: none;
    cursor: pointer;
    padding: 2px;
    font-size: 0.8rem;
    opacity: 0.6;
    transition: opacity 0.2s;
  }
  .action-icon-btn:hover { opacity: 1; }
  .no-history {
    font-size: var(--text-xs);
    color: var(--text-muted);
    text-align: center;
    padding: var(--space-6) 0;
  }

  /* Overlay on Mobile */
  .sidebar-overlay {
    display: none;
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    z-index: 9;
  }

  /* Main window styling */
  .chat-main {
    flex-grow: 1;
    display: flex;
    flex-direction: column;
    padding: var(--space-4);
    overflow: hidden;
    box-sizing: border-box;
    position: relative;
  }

  /* Header */
  .chat-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-shrink: 0;
    border-bottom: 1px solid var(--border);
    padding-bottom: var(--space-3);
    margin-bottom: var(--space-2);
  }
  .header-left { display: flex; align-items: center; gap: var(--space-3); }
  .mobile-toggle-btn {
    display: none;
    background: var(--bg-card);
    border: 1px solid var(--border);
    padding: var(--space-2) var(--space-3);
    border-radius: var(--radius-sm);
    font-size: var(--text-xs);
    color: var(--text-primary);
    cursor: pointer;
  }
  .model-pill {
    background: var(--bg-card);
    border: 1px solid var(--border-accent);
    border-radius: var(--radius-md);
    padding: var(--space-1) var(--space-3);
  }
  .model-name { font-size: var(--text-sm); color: var(--accent); }
  .header-title-display {
    font-size: var(--text-sm);
    font-weight: 600;
    color: var(--text-secondary);
    max-width: 50%;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  /* Error banner */
  .banner-error {
    background: rgba(239,68,68,.1);
    border: 1px solid rgba(239,68,68,.3);
    color: #ef4444;
    padding: var(--space-3) var(--space-4);
    border-radius: var(--radius-md);
    font-size: var(--text-sm);
    flex-shrink: 0;
  }

  /* Inference info banner */
  .inference-info-banner {
    margin: 0 var(--space-4) var(--space-2) var(--space-4);
    padding: var(--space-3) var(--space-4);
    font-size: var(--text-sm);
    background: rgba(0, 125, 255, 0.08);
    border: 1px solid rgba(0, 125, 255, 0.2);
    color: var(--text-primary);
    border-radius: var(--radius-md);
    display: flex;
    align-items: center;
    gap: var(--space-2);
    flex-shrink: 0;
    animation: fadeIn 0.3s ease;
  }
  .info-icon {
    font-size: var(--text-base);
    flex-shrink: 0;
  }
  .info-content {
    flex-grow: 1;
  }

  /* Messages */
  .messages {
    flex: 1;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
    padding: var(--space-2) 0 var(--space-4);
    scroll-behavior: smooth;
  }

  .empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-3);
    margin: auto;
    text-align: center;
    color: var(--text-muted);
    max-width: 500px;
  }
  .empty-icon { font-size: 3rem; }
  .empty-state h2 { font-size: var(--text-xl); color: var(--text-primary); margin: 0; }
  .empty-desc { font-size: var(--text-sm); margin: 0; line-height: 1.5; }

  .message { display: flex; }
  .message.user { justify-content: flex-end; }
  .message.assistant { justify-content: flex-start; }

  .bubble {
    max-width: 72%;
    padding: var(--space-4) var(--space-5);
    border-radius: var(--radius-lg);
    font-size: var(--text-sm);
    line-height: 1.65;
  }
  .message.user .bubble {
    background: rgba(0,201,167,.15);
    border: 1px solid rgba(0,201,167,.3);
    border-bottom-right-radius: 4px;
  }
  .message.assistant .bubble {
    background: var(--bg-card);
    border: 1px solid var(--border);
    border-bottom-left-radius: 4px;
  }

  .markdown-content {
    font-size: var(--text-sm);
    line-height: 1.65;
    color: var(--text-primary);
  }
  .markdown-content p {
    margin: 0 0 12px 0;
  }
  .markdown-content p:last-child {
    margin-bottom: 0;
  }
  .markdown-content strong {
    color: #fff;
    font-weight: 700;
  }
  .markdown-content ul, .markdown-content ol {
    margin: 0 0 12px 0;
    padding-left: 20px;
  }
  .markdown-content li {
    margin-bottom: 4px;
  }
  .markdown-content pre {
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-3);
    overflow-x: auto;
    margin: 12px 0;
  }
  .markdown-content code {
    font-family: var(--font-mono);
    font-size: 13px;
    background: rgba(255,255,255,0.04);
    padding: 2px 6px;
    border-radius: 4px;
    color: var(--accent);
  }
  .markdown-content pre code {
    background: transparent;
    padding: 0;
    border-radius: 0;
    color: var(--text-primary);
  }
  .markdown-content h1, .markdown-content h2, .markdown-content h3 {
    margin: 16px 0 8px 0;
    font-weight: 600;
    color: var(--text-primary);
  }
  .markdown-content h1 { font-size: 1.3rem; }
  .markdown-content h2 { font-size: 1.2rem; }
  .markdown-content h3 { font-size: 1.1rem; }
  .cursor {
    display: inline-block;
    color: var(--accent);
    animation: blink .8s step-end infinite;
  }
  @keyframes blink { 50% { opacity: 0; } }

  .message-footer {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: var(--space-3);
    margin-top: var(--space-2);
  }
  .tok-speed { font-size: var(--text-xs); color: var(--text-muted); font-family: var(--font-mono); }
  .copy-btn {
    background: none;
    border: none;
    cursor: pointer;
    color: var(--text-muted);
    padding: 6px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    transition: color var(--transition), background-color var(--transition), transform 0.1s ease;
    border-radius: var(--radius-sm);
  }
  .copy-btn:hover {
    color: var(--text-primary);
    background: rgba(255, 255, 255, 0.08);
  }
  .copy-btn:active {
    transform: scale(0.92);
  }
  .copy-btn.copied {
    color: var(--accent);
    background: rgba(0, 201, 167, 0.1);
  }


  /* Cost Savings Floating Pill */
  .savings-floating-pill {
    position: absolute;
    bottom: calc(var(--space-6) + 116px); /* Position it nicely above the input area */
    right: calc(var(--space-6) + 4px);
    background: rgba(16, 185, 129, 0.12);
    border: 1px solid rgba(16, 185, 129, 0.35);
    backdrop-filter: blur(12px);
    box-shadow: 0 4px 20px rgba(16, 185, 129, 0.15), 0 0 10px rgba(16, 185, 129, 0.05);
    padding: 8px 14px;
    border-radius: var(--radius-full);
    display: flex;
    align-items: center;
    gap: 8px;
    z-index: 50;
    color: #10b981;
    font-size: var(--text-xs);
    font-weight: 600;
    line-height: 1;
    animation: floating-pill-in 300ms cubic-bezier(0.16, 1, 0.3, 1) both;
  }
  @keyframes floating-pill-in {
    from { opacity: 0; transform: translateY(12px) scale(0.95); }
    to   { opacity: 1; transform: translateY(0) scale(1); }
  }
  .savings-pill-icon {
    font-size: 13px;
    filter: drop-shadow(0 0 4px rgba(16, 185, 129, 0.4));
  }
  .savings-pill-amount {
    color: #00ffcc;
    font-weight: 800;
    font-family: var(--font-mono);
    text-shadow: 0 0 8px rgba(0, 255, 204, 0.3);
  }
  .savings-pill-close {
    background: none;
    border: none;
    color: rgba(16, 185, 129, 0.6);
    cursor: pointer;
    font-size: 10px;
    padding: 2px 4px;
    transition: color var(--transition);
  }
  .savings-pill-close:hover {
    color: #10b981;
  }

  .btn-xs { padding: 3px 10px; font-size: var(--text-xs); }
  .text-muted { color: var(--text-muted) !important; }

  /* Input area */
  .input-area {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    flex-shrink: 0;
  }
  
  /* Unified Input Box Container (ChatGPT-like) */
  .input-box-container {
    width: 100%;
    background: var(--bg-card);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: 10px;
    display: flex;
    flex-direction: column;
    gap: 6px;
    transition: border-color var(--transition), box-shadow var(--transition);
    box-sizing: border-box;
  }
  .input-box-container.focused {
    border-color: var(--accent);
    box-shadow: 0 0 0 3px rgba(0, 201, 167, 0.12);
  }
  .input-box-container.use-brain-active {
    border-color: var(--accent);
  }

  .chat-input-textarea {
    width: 100%;
    background: transparent;
    border: none;
    outline: none;
    resize: none;
    color: var(--text-primary);
    font-family: inherit;
    font-size: var(--text-sm);
    line-height: 1.6;
    padding: 4px 6px;
    box-sizing: border-box;
  }
  .chat-input-textarea:disabled { opacity: 0.6; cursor: not-allowed; }

  .input-box-bottom {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding-top: 4px;
  }
  .input-bottom-left, .input-bottom-right {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .hint { font-size: var(--text-xs); color: var(--text-muted); }

  /* Round Send Button */
  .send-btn-circle {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border-radius: 50%;
    padding: 0;
    background: var(--accent);
    color: #000;
    border: none;
    cursor: pointer;
    transition: background var(--transition), opacity var(--transition);
  }
  .send-btn-circle:hover:not(:disabled) {
    background: #00b396;
  }
  .send-btn-circle:disabled {
    background: var(--bg-tertiary);
    color: var(--text-muted);
    border: 1px solid var(--border);
    cursor: not-allowed;
    opacity: 0.5;
  }
  .send-btn-circle.stop-btn {
    background: #fff;
    color: #000;
  }
  .send-btn-circle.stop-btn:hover {
    background: #e6e6e6;
  }

  .btn-accent {
    background: var(--accent);
    color: #000;
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  .btn-accent:hover:not(:disabled) { background: #00b396; }
  .btn-accent:disabled { opacity: .5; cursor: not-allowed; }

  .spinner-sm {
    width: 14px; height: 14px;
    border: 2px solid rgba(0,0,0,.2);
    border-top-color: #000;
    border-radius: 50%;
    animation: spin .7s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }

  .btn-sm { padding: 6px 14px; font-size: var(--text-sm); border-radius: var(--radius-sm); }
  .mono { font-family: var(--font-mono); }

  .toggle-btn-text-mobile { display: none; }
  .back-btn-text-mobile { display: none; }
  .brain-btn-text-mobile { display: none; }

  /* Responsive styling */
  @media (max-width: 768px) {
    .sidebar {
      position: absolute;
      left: 0;
      top: 0;
      bottom: 0;
      transform: translateX(-100%);
    }
    .sidebar.open {
      transform: translateX(0);
    }
    .sidebar-overlay {
      display: block;
    }
    .mobile-toggle-btn {
      display: block;
    }
    .mobile-close-btn {
      display: block;
    }
    .bubble {
      max-width: 92%;
      padding: var(--space-3) var(--space-4);
    }
    .mcp-chips {
      flex-wrap: nowrap;
      overflow-x: auto;
      padding-bottom: 4px;
      -webkit-overflow-scrolling: touch;
    }
    .mcp-chip {
      flex-shrink: 0;
    }
    .mcp-toolbar {
      padding: 8px 10px;
    }
    .input-box-container {
      padding: 8px;
    }
    .chat-input-textarea {
      padding: 6px 8px;
      font-size: 13px;
    }
    .savings-floating-pill {
      bottom: calc(var(--space-6) + 110px);
    }
  }

  @media (max-width: 576px) {
    .header-title-display {
      font-size: 12px;
      max-width: 40%;
    }
  }

  @media (max-width: 480px) {
    .toggle-btn-text { display: none; }
    .toggle-btn-text-mobile { display: inline; }
    .back-btn-text { display: none; }
    .back-btn-text-mobile { display: inline; }
    .back-btn { padding: 6px 10px; }
    
    .brain-btn-text { display: none; }
    .brain-btn-text-mobile { display: inline; }

    .model-pill {
      padding: var(--space-1) var(--space-2);
    }
    .model-name {
      font-size: 11px;
    }

    .bubble {
      max-width: 95%;
      padding: 8px 12px;
      font-size: 13px;
    }
    .chat-main {
      padding: var(--space-2) var(--space-3);
    }

    .input-box-bottom {
      flex-direction: row;
      align-items: center;
      justify-content: space-between;
      gap: 8px;
    }
    .hint {
      display: none;
    }
    .use-brain-btn {
      justify-content: center;
      font-size: 10px;
      padding: 5px 8px;
    }
    .send-btn-circle {
      width: 28px;
      height: 28px;
    }
    .input-box-container {
      padding: 6px;
    }
    .chat-input-textarea {
      padding: 4px 6px;
      font-size: 13px;
    }
    .savings-floating-pill {
      bottom: calc(var(--space-4) + 98px);
      right: calc(var(--space-4) + 2px);
      padding: 6px 12px;
      font-size: 11px;
    }
  }

  /* Confirm overlay */
  .confirm-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.55);
    backdrop-filter: blur(4px);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 100;
    animation: fadeIn .15s ease;
  }
  @keyframes fadeIn { from { opacity: 0; } to { opacity: 1; } }
  .confirm-dialog {
    background: var(--bg-card);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--space-8);
    max-width: 420px;
    width: 90%;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-4);
    text-align: center;
    box-shadow: 0 24px 64px rgba(0, 0, 0, 0.45);
    animation: slideUp .18s ease;
  }
  @keyframes slideUp { from { transform: translateY(16px); opacity: 0; } to { transform: translateY(0); opacity: 1; } }
  .confirm-icon { font-size: 2rem; }
  .confirm-dialog h3 { font-size: var(--text-lg); font-weight: 700; color: var(--text-primary); margin: 0; }
  .confirm-dialog p { font-size: var(--text-sm); color: var(--text-muted); margin: 0; line-height: 1.55; }
  .confirm-actions { display: flex; gap: var(--space-3); margin-top: var(--space-2); }

  /* Rename modal specific styling */
  .rename-modal-input-wrapper {
    width: 100%;
    margin: var(--space-2) 0;
  }
  .rename-modal-input {
    width: 100%;
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-3) var(--space-4);
    color: var(--text-primary);
    font-family: inherit;
    font-size: var(--text-sm);
    box-sizing: border-box;
    transition: border-color var(--transition), box-shadow var(--transition);
  }
  .rename-modal-input:focus {
    outline: none;
    border-color: var(--accent);
    box-shadow: 0 0 0 3px rgba(0,201,167,.12);
  }

  /* Brain RAG styling */
  .use-brain-btn {
    background: transparent;
    border: 1px solid var(--border);
    color: var(--text-muted);
    font-size: var(--text-xs);
    padding: 6px 12px;
    border-radius: var(--radius-md);
    cursor: pointer;
    transition: all var(--transition);
    display: flex;
    align-items: center;
    gap: var(--space-1);
  }
  .use-brain-btn:hover {
    border-color: var(--border-accent);
    color: var(--text-primary);
  }
  .use-brain-btn.active {
    background: rgba(0, 201, 167, 0.08);
    border-color: var(--accent);
    color: var(--accent);
  }
  .chat-input.use-brain-active {
    border-color: var(--accent);
    box-shadow: 0 0 0 3px rgba(0, 201, 167, 0.08);
  }
  .citations-panel {
    margin-top: var(--space-3);
    padding-top: var(--space-2);
    border-top: 1px dashed var(--border);
    font-size: var(--text-xs);
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
  }
  .citations-label {
    color: var(--text-muted);
    font-weight: 600;
  }
  .citation-badge {
    background: var(--bg-card-hover);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 2px 6px;
    color: var(--text-secondary);
    cursor: help;
    transition: all var(--transition);
  }
  .citation-badge:hover {
    border-color: var(--accent);
    color: var(--text-primary);
  }

  /* Tool execution blocks */
  .tool-calls-block {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-bottom: var(--space-3);
    padding: 8px 12px;
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .tool-call-item {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--text-muted);
  }
  .tool-call-item.completed {
    color: var(--text-secondary);
  }
  .tc-status {
    color: var(--accent);
    font-weight: bold;
  }
  .tc-text {
    word-break: break-all;
    white-space: pre-wrap;
  }

  /* MCP Toolbar */
  .mcp-toolbar {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: var(--space-2) var(--space-4);
    background: var(--bg-card);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    margin-bottom: 2px;
  }
  .mcp-toolbar-label {
    font-size: 11px;
    color: var(--text-muted);
    font-weight: 500;
  }
  .mcp-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .mcp-chip {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 3px 10px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-full);
    font-size: var(--text-xs);
    color: var(--text-secondary);
    cursor: pointer;
    user-select: none;
    transition: all var(--transition);
  }
  .mcp-chip input {
    display: none;
  }
  .mcp-chip.selected {
    background: var(--accent-dim);
    border-color: var(--accent);
    color: var(--accent);
    box-shadow: 0 0 6px rgba(0, 201, 167, 0.1);
  }
  .mcp-chip:hover {
    border-color: var(--text-muted);
  }
</style>
