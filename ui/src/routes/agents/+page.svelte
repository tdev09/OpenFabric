<script lang="ts">
  import { onMount } from 'svelte';
  import { fade, slide } from 'svelte/transition';
  import { nodes, onlineNodes, appConfig } from '$lib/stores/cluster';

  interface Step {
    number: number;
    tool: string;
    args: Record<string, any>;
    result?: string;
    status: 'pending' | 'running' | 'completed' | 'failed';
    log?: string;
    elapsed_time_ms: number;
  }

  interface Agent {
    id: string;
    goal: string;
    tools: string[];
    status: 'pending' | 'running' | 'completed' | 'failed' | 'cancelled';
    steps: Step[];
    output?: string;
    error?: string;
    created_at: string;
    completed_at?: string;
  }

  interface AgentTemplate {
    id: string;
    name: string;
    description: string;
    goal: string;
    tools: string[];
  }

  interface McpServer {
    name: string;
    enabled: boolean;
    running: boolean;
    tool_count: number;
  }

  let agents: Agent[] = [];
  let templates: AgentTemplate[] = [];
  let mcpServers: McpServer[] = [];

  let goal = '';
  let selectedTools: string[] = ['web_search', 'search_brain', 'notify'];
  let selectedNode = '';
  let launching = false;
  let errorMsg: string | null = null;
  let activeTab: 'designer' | 'runs' = 'designer';

  let rawLog = '';
  let showLogModal = false;
  let logModalAgentId = '';

  let toasts: { id: number; message: string }[] = [];
  let toastId = 0;
  let es: EventSource;

  // Track expanded state of agent runs & individual steps
  let expandedAgentIds: Record<string, boolean> = {};
  let expandedSteps: Record<string, boolean> = {}; // key: "agentId-stepNumber"

  // Reactive stats for the dashboard header
  $: totalRuns = agents.length;
  $: runningCount = agents.filter(a => a.status === 'running').length;
  $: completedCount = agents.filter(a => a.status === 'completed').length;
  $: successRate = totalRuns > 0 ? Math.round((completedCount / (totalRuns - runningCount || 1)) * 100) : 100;

  const builtInToolsList = [
    { name: 'web_search', label: 'Web Search', desc: 'Queries search engine and returns text results' },
    { name: 'web_fetch', label: 'Web Fetch', desc: 'Retrieves raw page text from URLs' },
    { name: 'search_brain', label: 'Search Brain', desc: 'Queries semantic knowledge base (RAG)' },
    { name: 'read_file', label: 'Read File', desc: 'Reads files from Fabric Storage' },
    { name: 'write_file', label: 'Write File', desc: 'Writes content to Fabric Storage' },
    { name: 'list_storage', label: 'List Storage', desc: 'Lists files in storage paths' },
    { name: 'run_shell', label: 'Run Shell', desc: 'Runs host shell command tasks safely' },
    { name: 'remember', label: 'Memory Storage', desc: 'Saves key facts to Fabric Memory' },
    { name: 'notify', label: 'Dashboard Notify', desc: 'Triggers live dashboard toast notifications' },
  ];

  onMount(() => {
    async function init() {
      await loadTemplates();
      await loadAgents();
      await loadMcpServers();

      // Set default preferred node to self if available
      if ($onlineNodes.length > 0) {
        selectedNode = $onlineNodes[0].id;
      }
    }
    init();

    // Connect to global SSE endpoints
    es = new EventSource('/api/events');

    es.addEventListener('agent_updated', (e: any) => {
      try {
        const updated: Agent = JSON.parse(e.data);
        agents = [updated, ...agents.filter(a => a.id !== updated.id)];
        
        // Auto-expand newly updated agents if running
        if (updated.status === 'running') {
          expandedAgentIds[updated.id] = true;
        }

        // If completed or failed, trigger a status toast
        if (updated.status === 'completed') {
          showToast(`✅ Agent Task Completed: ${updated.id}`);
        } else if (updated.status === 'failed') {
          showToast(`❌ Agent Task Failed: ${updated.id}`);
        }
      } catch (err) {
        console.error('Failed to parse agent_updated event', err);
      }
    });

    es.addEventListener('agent_notification', (e: any) => {
      try {
        const data = JSON.parse(e.data);
        showToast(`⚡ Agent: ${data.message}`);
      } catch {}
    });

    return () => {
      if (es) es.close();
    };
  });

  async function loadTemplates() {
    try {
      const res = await fetch('/api/agents/templates');
      if (res.ok) templates = await res.json();
    } catch {}
  }

  async function loadAgents() {
    try {
      const res = await fetch('/api/agents');
      if (res.ok) agents = await res.json();
    } catch {}
  }

  async function loadMcpServers() {
    try {
      const res = await fetch('/api/mcp/servers');
      if (res.ok) {
        mcpServers = await res.json();
      }
    } catch {}
  }

  function showToast(message: string) {
    const id = toastId++;
    toasts = [{ id, message }];
    setTimeout(() => {
      toasts = toasts.filter(t => t.id !== id);
    }, 4000);
  }

  function selectTemplate(tmpl: AgentTemplate) {
    const toolsEqual = selectedTools.length === tmpl.tools.length &&
      selectedTools.every(t => tmpl.tools.includes(t));
    if (goal === tmpl.goal && toolsEqual) {
      return; // Already loaded and unmodified
    }
    goal = tmpl.goal;
    selectedTools = [...tmpl.tools];
    activeTab = 'designer';
    showToast(`Loaded Template: ${tmpl.name}`);
  }

  function isTemplateActive(tmpl: AgentTemplate): boolean {
    const toolsEqual = selectedTools.length === tmpl.tools.length &&
      selectedTools.every(t => tmpl.tools.includes(t));
    return goal === tmpl.goal && toolsEqual;
  }

  function toggleTool(toolName: string) {
    if (selectedTools.includes(toolName)) {
      selectedTools = selectedTools.filter(t => t !== toolName);
    } else {
      selectedTools = [...selectedTools, toolName];
    }
  }

  async function launchAgent() {
    if (!goal.trim()) {
      errorMsg = 'Please enter a goal for the agent';
      return;
    }
    launching = true;
    errorMsg = null;

    try {
      const res = await fetch('/api/agents', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          goal,
          tools: selectedTools
        })
      });

      if (!res.ok) {
        const data = await res.json();
        throw new Error(data.error || 'Failed to launch agent');
      }

      const newAgent: Agent = await res.json();
      agents = [newAgent, ...agents.filter(a => a.id !== newAgent.id)];
      expandedAgentIds[newAgent.id] = true;
      goal = '';
      activeTab = 'runs';
      showToast(`🚀 Agent launched: ${newAgent.id}`);
    } catch (err: any) {
      errorMsg = err.message;
    } finally {
      launching = false;
    }
  }

  async function cancelAgent(id: string) {
    try {
      const res = await fetch(`/api/agents/${id}/cancel`, { method: 'POST' });
      if (res.ok) {
        showToast(`Cancelled Agent: ${id}`);
        loadAgents();
      }
    } catch {}
  }

  async function openLogModal(id: string) {
    try {
      const res = await fetch(`/api/agents/${id}/log`);
      if (res.ok) {
        rawLog = await res.text();
        logModalAgentId = id;
        showLogModal = true;
      }
    } catch {
      showToast('Could not fetch agent logs');
    }
  }

  function copyLogToClipboard() {
    navigator.clipboard.writeText(rawLog);
    showToast('Copied log to clipboard');
  }

  function getStepIcon(status: string) {
    switch (status) {
      case 'completed': return '✓';
      case 'failed': return '✕';
      case 'running': return '⟳';
      default: return '○';
    }
  }

  function formatTime(dateStr: string) {
    if (!dateStr) return '';
    const date = new Date(dateStr);
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }

  function formatDuration(startStr: string, endStr?: string) {
    const start = new Date(startStr).getTime();
    const isCompleted = endStr && !endStr.startsWith('0001-01-01');
    const end = isCompleted ? new Date(endStr!).getTime() : Date.now();
    const diff = end - start;
    if (diff < 1000) return `${diff}ms`;
    const sec = Math.floor(diff / 1000);
    if (sec < 60) return `${sec}s`;
    const min = Math.floor(sec / 60);
    const remainSec = sec % 60;
    return `${min}m ${remainSec}s`;
  }
</script>

<svelte:head>
  <title>Autonomous Agents - {$appConfig.project_name}</title>
  <meta name="description" content="Orchestrate goal-oriented ReAct loops across your cluster nodes." />
</svelte:head>

<!-- Notification Toasts -->
<div class="toast-container">
  {#each toasts as t (t.id)}
    <div class="toast-item banner banner-info" transition:slide>
      <span>{t.message}</span>
    </div>
  {/each}
</div>

<div class="agents-container animate-fade-in">
  <!-- Page Header -->
  <header class="section-header">
    <div class="header-title">
      <h1 class="page-title">⚡ Autonomous Agents</h1>
      <p class="text-secondary">Orchestrate goal-oriented ReAct loops across your cluster nodes.</p>
    </div>
    <div class="tab-buttons">
      <button class="btn btn-secondary" class:btn-primary={activeTab === 'designer'} on:click={() => activeTab = 'designer'}>
        Goal Designer
      </button>
      <button class="btn btn-secondary" class:btn-primary={activeTab === 'runs'} on:click={() => activeTab = 'runs'}>
        Execution Runs ({agents.filter(a => a.status === 'running').length} running)
      </button>
    </div>
  </header>

  <!-- Stats Metrics Dashboard Row -->
  <div class="stats-row">
    <div class="stat-card total-agents-theme card">
      <div class="stat-icon-wrapper">
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="stat-svg"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
      </div>
      <div class="stat-details">
        <span class="stat-val">{totalRuns}</span>
        <span class="stat-lbl">Total Runs</span>
      </div>
    </div>
    <div class="stat-card active-agents-theme card">
      <div class="stat-icon-wrapper">
        {#if runningCount > 0}
          <span class="animate-spin" style="font-size: 20px; color: #00e0bc;">⟳</span>
        {:else}
          <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="stat-svg"><polygon points="5 3 19 12 5 21 5 3"/></svg>
        {/if}
      </div>
      <div class="stat-details">
        <span class="stat-val">{runningCount}</span>
        <span class="stat-lbl">Active Runs</span>
      </div>
    </div>
    <div class="stat-card completed-agents-theme card">
      <div class="stat-icon-wrapper">
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="stat-svg"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><polyline points="22 4 12 14.01 9 11.01"/></svg>
      </div>
      <div class="stat-details">
        <span class="stat-val">{completedCount}</span>
        <span class="stat-lbl">Completed Goal</span>
      </div>
    </div>
    <div class="stat-card rate-agents-theme card">
      <div class="stat-icon-wrapper">
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="stat-svg"><path d="M22 2L11 13M22 2l-7 20-4-9-9-4 20-7z"/></svg>
      </div>
      <div class="stat-details">
        <span class="stat-val">{successRate}%</span>
        <span class="stat-lbl">Success Rate</span>
      </div>
    </div>
  </div>

  {#if activeTab === 'designer'}
    <!-- Goal Designer Tab -->
    <div class="designer-layout">
      <!-- Templates column -->
      <section class="templates-section">
        <h3>Agent Templates</h3>
        <p class="text-secondary mb-4">Click a template to load recommended configurations.</p>
        <div class="templates-grid">
          {#each templates as t}
            <button class="card template-card" class:active={isTemplateActive(t)} on:click={() => selectTemplate(t)}>
              <span class="template-icon">🤖</span>
              <div class="template-meta">
                <span class="template-name">{t.name}</span>
                <span class="template-desc">{t.description}</span>
              </div>
            </button>
          {/each}
        </div>
      </section>

      <!-- Configuration form -->
      <section class="card config-card">
        <h3>Launch New Agent Run</h3>
        
        {#if errorMsg}
          <div class="banner banner-error mb-4" role="alert">
            ⚠️ {errorMsg}
          </div>
        {/if}

        <div class="form-group mb-6">
          <label for="goal-input">Define Agent Goal</label>
          <textarea
            id="goal-input"
            class="input textarea-goal"
            rows="4"
            bind:value={goal}
            placeholder={"e.g. Research distributed vector databases in 2026. Write a comprehensive report with code examples. Save it to storage and notify me when done."}
          ></textarea>
        </div>

        <div class="tools-selection mb-6">
          <label class="section-label">Select Available Tools</label>
          <div class="tools-grid">
            {#each builtInToolsList as tool}
              <button
                class="tool-checkbox-btn"
                class:active={selectedTools.includes(tool.name)}
                on:click={() => toggleTool(tool.name)}
              >
                <div class="tool-checkbox-header">
                  <span class="tool-indicator">{selectedTools.includes(tool.name) ? '✓' : '+'}</span>
                  <span class="tool-label">{tool.label}</span>
                </div>
                <span class="tool-desc">{tool.desc}</span>
              </button>
            {/each}
          </div>
        </div>

        <!-- MCP server integrations selection -->
        {#if mcpServers.filter(s => s.enabled).length > 0}
          <div class="tools-selection mb-6">
            <label class="section-label">Enabled MCP Integrations (Server Tools)</label>
            <p class="text-secondary mb-3">Checking these will expose all tools registered in the corresponding MCP gateway.</p>
            <div class="tools-grid">
              {#each mcpServers.filter(s => s.enabled) as server}
                <button
                  class="tool-checkbox-btn mcp-checkbox-btn"
                  class:active={selectedTools.includes(server.name)}
                  on:click={() => toggleTool(server.name)}
                >
                  <div class="tool-checkbox-header">
                    <span class="tool-indicator">{selectedTools.includes(server.name) ? '✓' : '+'}</span>
                    <span class="tool-label">📦 {server.name} integration</span>
                  </div>
                  <span class="tool-desc">{server.tool_count} active tool definitions</span>
                </button>
              {/each}
            </div>
          </div>
        {/if}

        <div class="form-row mb-6">
          <div class="form-group flex-1">
            <label for="node-select">Execution Node Coordinator</label>
            <select id="node-select" class="input select-node" bind:value={selectedNode}>
              {#each $onlineNodes as node}
                <option value={node.id}>
                  {node.name} ({node.id === selectedNode ? 'coordinator' : 'peer'})
                </option>
              {/each}
            </select>
          </div>
        </div>

        <button class="btn btn-primary btn-launch w-full" disabled={launching} on:click={launchAgent}>
          {#if launching}
            <span class="animate-spin mr-2">⟳</span> Launching Agent...
          {:else}
            🚀 Launch Agent Run
          {/if}
        </button>
      </section>
    </div>
  {:else}
    <!-- Active runs & history tab -->
    <div class="runs-list-layout">
      {#if agents.length === 0}
        <div class="empty-state card">
          <span class="empty-icon">🤖</span>
          <h3>No Agent Runs Found</h3>
          <p>Launch an agent in the Goal Designer to start executing tasks autonomously.</p>
        </div>
      {:else}
        <div class="runs-grid">
          {#each agents as a (a.id)}
            <div class="card run-card" class:running={a.status === 'running'}>
              <!-- Card Header -->
              <div class="run-header" on:click={() => expandedAgentIds[a.id] = !expandedAgentIds[a.id]}>
                <div class="run-header-left">
                  <span class="badge" class:badge-online={a.status === 'completed'} class:badge-warning={a.status === 'running' || a.status === 'pending'} class:badge-danger={a.status === 'failed'} class:badge-offline={a.status === 'cancelled'}>
                    {#if a.status === 'running'}
                      <span class="animate-spin mr-1">⟳</span>
                    {/if}
                    {a.status}
                  </span>
                  <span class="run-id mono">{a.id}</span>
                  <span class="run-time text-secondary">
                    {formatTime(a.created_at)} · {formatDuration(a.created_at, a.completed_at)}
                  </span>
                </div>
                <div class="run-header-right">
                  <button class="btn btn-ghost btn-sm" on:click|stopPropagation={() => openLogModal(a.id)}>
                    Console Log
                  </button>
                  {#if a.status === 'running' || a.status === 'pending'}
                    <button class="btn btn-danger btn-sm" on:click|stopPropagation={() => cancelAgent(a.id)}>
                      Cancel
                    </button>
                  {/if}
                  <span class="expand-chevron">{expandedAgentIds[a.id] ? '▲' : '▼'}</span>
                </div>
              </div>

              <!-- Card Body (Summary) -->
              <div class="run-goal-summary">
                <strong>Goal:</strong> {a.goal}
              </div>

              <!-- Expanded Details (Timeline of Steps) -->
              {#if expandedAgentIds[a.id]}
                <div class="run-expanded-content" transition:slide>
                  <div class="divider"></div>

                  <!-- Final Output Report Box -->
                  {#if a.output}
                    <div class="final-output-box mb-6">
                      <h4 class="text-accent mb-2">✦ Final Output Report</h4>
                      <div class="output-content pre-scrollable">{a.output}</div>
                    </div>
                  {/if}

                  {#if a.error}
                    <div class="banner banner-error mb-6">
                      <strong>Failure Error:</strong> {a.error}
                    </div>
                  {/if}

                  <!-- Timeline list -->
                  <h4 class="mb-4">Execution Steps Timeline ({a.steps.length} rounds)</h4>
                  <div class="steps-timeline">
                    {#each a.steps as step (step.number)}
                      <div class="timeline-item" class:step-running={step.status === 'running'}>
                        <!-- Timeline bullet line -->
                        <div class="timeline-bullet-wrapper">
                          <span class="timeline-bullet-icon" class:completed={step.status === 'completed'} class:failed={step.status === 'failed'} class:running={step.status === 'running'}>
                            {getStepIcon(step.status)}
                          </span>
                          <div class="timeline-line"></div>
                        </div>

                        <!-- Timeline details -->
                        <div class="timeline-step-detail">
                          <div class="step-summary-bar" on:click={() => expandedSteps[`${a.id}-${step.number}`] = !expandedSteps[`${a.id}-${step.number}`]}>
                            <div class="step-summary-left">
                              <span class="step-number mono">Step {step.number}</span>
                              <span class="step-tool-badge">{step.tool || 'Thinking'}</span>
                              <span class="step-log-text">{step.log}</span>
                            </div>
                            <div class="step-summary-right">
                              {#if step.elapsed_time_ms > 0}
                                <span class="step-duration text-secondary mono">{step.elapsed_time_ms}ms</span>
                              {/if}
                              <span class="step-chevron">{expandedSteps[`${a.id}-${step.number}`] ? '▲' : '▼'}</span>
                            </div>
                          </div>

                          {#if expandedSteps[`${a.id}-${step.number}`]}
                            <div class="step-expanded-details" transition:slide>
                              <!-- Tool Arguments -->
                              {#if step.args && Object.keys(step.args).length > 0}
                                <div class="step-sub-box">
                                  <strong>Arguments:</strong>
                                  <pre class="mono">{JSON.stringify(step.args, null, 2)}</pre>
                                </div>
                              {/if}

                              <!-- Tool Result Output -->
                              {#if step.result}
                                <div class="step-sub-box">
                                  <strong>Result Output:</strong>
                                  <pre class="mono pre-scrollable">{step.result}</pre>
                                </div>
                              {/if}
                            </div>
                          {/if}
                        </div>
                      </div>
                    {/each}
                  </div>
                </div>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
</div>

<!-- Raw Log Viewer Modal -->
{#if showLogModal}
  <div class="modal-backdrop" on:click={() => showLogModal = false}>
    <div class="modal log-modal" on:click|stopPropagation>
      <header class="modal-header">
        <h3>Console Execution Logs - {logModalAgentId}</h3>
        <button class="btn btn-ghost" on:click={() => showLogModal = false}>✕</button>
      </header>
      <div class="divider"></div>
      <div class="log-modal-body">
        <pre class="log-pre mono">{rawLog}</pre>
      </div>
      <div class="divider"></div>
      <footer class="modal-footer flex gap-3 justify-end">
        <button class="btn btn-secondary" on:click={copyLogToClipboard}>Copy to Clipboard</button>
        <button class="btn btn-primary" on:click={() => showLogModal = false}>Close</button>
      </footer>
    </div>
  </div>
{/if}

<style>
  .agents-container {
    width: 100%;
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
    margin-bottom: var(--space-1);
  }

  /* Stats Row styling */
  .stats-row {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: var(--space-4);
    margin-bottom: var(--space-6);
  }
  .stat-card {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    padding: var(--space-4);
    border-radius: var(--radius-lg);
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
  
  .stat-card.total-agents-theme:hover { border-color: rgba(0, 201, 167, 0.4); box-shadow: 0 4px 20px rgba(0, 201, 167, 0.08); }
  .stat-card.total-agents-theme::before { background: var(--accent); }
  .stat-card.total-agents-theme .stat-icon-wrapper { background: rgba(0, 201, 167, 0.15); color: var(--accent); }

  .stat-card.active-agents-theme:hover { border-color: rgba(0, 224, 188, 0.4); box-shadow: 0 4px 20px rgba(0, 224, 188, 0.08); }
  .stat-card.active-agents-theme::before { background: #00e0bc; }
  .stat-card.active-agents-theme .stat-icon-wrapper { background: rgba(0, 224, 188, 0.1); color: #00e0bc; }

  .stat-card.completed-agents-theme:hover { border-color: rgba(0, 201, 167, 0.4); box-shadow: 0 4px 20px rgba(0, 201, 167, 0.08); }
  .stat-card.completed-agents-theme::before { background: var(--accent); }
  .stat-card.completed-agents-theme .stat-icon-wrapper { background: rgba(0, 201, 167, 0.15); color: var(--accent); }

  .stat-card.rate-agents-theme:hover { border-color: rgba(0, 132, 255, 0.4); box-shadow: 0 4px 20px rgba(0, 132, 255, 0.08); }
  .stat-card.rate-agents-theme::before { background: #0084FF; }
  .stat-card.rate-agents-theme .stat-icon-wrapper { background: rgba(0, 132, 255, 0.1); color: #0084FF; }

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
    width: 22px;
    height: 22px;
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

  .tab-buttons {
    display: flex;
    gap: var(--space-2);
  }

  .toast-container {
    position: fixed;
    bottom: var(--space-6);
    right: var(--space-6);
    z-index: 2000;
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    pointer-events: none;
  }
  .toast-item {
    pointer-events: auto;
    box-shadow: var(--shadow-lg);
    margin: 0;
  }

  /* --- Designer Layout --- */
  .designer-layout {
    display: grid;
    grid-template-columns: 1fr 2fr;
    gap: var(--space-6);
    align-items: start;
  }

  @media (max-width: 900px) {
    .designer-layout {
      grid-template-columns: 1fr;
    }
  }

  .templates-section h3, .config-card h3 {
    margin-bottom: var(--space-2);
  }

  .templates-grid {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }

  .template-card {
    text-align: left;
    display: flex;
    align-items: flex-start;
    gap: var(--space-4);
    padding: var(--space-4);
    cursor: pointer;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    transition: all var(--transition);
  }
  .template-card:hover {
    border-color: var(--accent);
    background: var(--bg-card-hover);
  }
  .template-card.active {
    border-color: var(--accent);
    background: rgba(0, 201, 167, 0.08);
    box-shadow: 0 0 12px rgba(0, 201, 167, 0.15);
  }

  .template-icon {
    font-size: 24px;
  }

  .template-meta {
    display: flex;
    flex-direction: column;
  }
  .template-name {
    font-weight: 600;
    color: var(--text-primary);
  }
  .template-desc {
    font-size: var(--text-sm);
    color: var(--text-secondary);
    margin-top: 2px;
  }

  .textarea-goal {
    resize: vertical;
    min-height: 100px;
    font-size: var(--text-base);
  }

  .form-group label, .section-label {
    display: block;
    font-weight: 500;
    font-size: var(--text-sm);
    margin-bottom: var(--space-2);
    color: var(--text-primary);
  }

  .tools-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: var(--space-3);
  }

  .tool-checkbox-btn {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-3);
    text-align: left;
    cursor: pointer;
    display: flex;
    flex-direction: column;
    gap: 4px;
    transition: all var(--transition);
  }
  .tool-checkbox-btn:hover {
    border-color: var(--accent-glow);
    background: var(--bg-card-hover);
  }
  .tool-checkbox-btn.active {
    border-color: var(--accent);
    background: var(--accent-dim);
    box-shadow: 0 0 10px rgba(0, 201, 167, 0.1);
  }
  .tool-checkbox-header {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  .tool-indicator {
    font-size: var(--text-xs);
    font-weight: 700;
    color: var(--accent);
  }
  .tool-label {
    font-weight: 600;
    color: var(--text-primary);
    font-size: var(--text-sm);
  }
  .tool-desc {
    font-size: var(--text-xs);
    color: var(--text-secondary);
  }

  .mcp-checkbox-btn {
    border-style: dashed;
  }

  .form-row {
    display: flex;
    gap: var(--space-4);
  }
  .select-node {
    height: 42px;
  }

  .mb-3 { margin-bottom: var(--space-3); }
  .mb-4 { margin-bottom: var(--space-4); }
  .mb-6 { margin-bottom: var(--space-6); }
  .mr-2 { margin-right: var(--space-2); }
  .mr-1 { margin-right: var(--space-1); }
  .w-full { width: 100%; }
  .flex-1 { flex: 1; }
  .flex { display: flex; }
  .gap-3 { gap: var(--space-3); }
  .justify-end { justify-content: flex-end; }

  /* --- Runs Grid & Cards --- */
  .runs-grid {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }

  .run-card {
    padding: var(--space-5);
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    transition: all var(--transition);
  }
  .run-card.running {
    border-color: var(--accent);
    box-shadow: var(--shadow-accent);
  }

  .run-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    cursor: pointer;
    gap: var(--space-3);
  }
  .run-header-left {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    flex-wrap: wrap;
  }
  .run-header-right {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  .run-id {
    font-size: var(--text-sm);
    font-weight: 600;
  }
  .run-time {
    font-size: var(--text-xs);
  }
  .expand-chevron {
    font-size: var(--text-xs);
    color: var(--text-muted);
    margin-left: var(--space-1);
  }

  .run-goal-summary {
    margin-top: var(--space-3);
    font-size: var(--text-base);
    color: var(--text-primary);
  }

  .run-expanded-content {
    margin-top: var(--space-4);
  }

  /* --- Final Output Box --- */
  .final-output-box {
    background: var(--bg-tertiary);
    border: 1px solid var(--border-accent);
    border-radius: var(--radius-md);
    padding: var(--space-4);
    box-shadow: var(--shadow-sm);
  }
  .output-content {
    font-size: var(--text-sm);
    line-height: 1.6;
    white-space: pre-wrap;
    color: var(--text-primary);
  }
  .pre-scrollable {
    max-height: 250px;
    overflow-y: auto;
    padding-right: var(--space-2);
  }

  /* --- Steps Timeline --- */
  .steps-timeline {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    padding-left: var(--space-2);
  }

  .timeline-item {
    display: flex;
    gap: var(--space-4);
    position: relative;
  }
  .timeline-bullet-wrapper {
    display: flex;
    flex-direction: column;
    align-items: center;
    position: relative;
  }
  .timeline-bullet-icon {
    width: 24px;
    height: 24px;
    border-radius: 50%;
    background: var(--bg-tertiary);
    border: 1.5px solid var(--border);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: var(--text-xs);
    font-weight: 700;
    color: var(--text-secondary);
    z-index: 2;
    transition: all var(--transition);
  }
  .timeline-bullet-icon.completed {
    background: var(--accent-dim);
    border-color: var(--accent);
    color: var(--accent);
  }
  .timeline-bullet-icon.failed {
    background: rgba(255, 107, 107, 0.15);
    border-color: var(--danger);
    color: var(--danger);
  }
  .timeline-bullet-icon.running {
    background: var(--bg-secondary);
    border-color: var(--accent);
    color: var(--accent);
    box-shadow: 0 0 8px var(--accent-glow);
  }

  .timeline-line {
    position: absolute;
    top: 24px;
    bottom: -20px;
    width: 1px;
    background: var(--border);
    z-index: 1;
  }
  .timeline-item:last-child .timeline-line {
    display: none;
  }

  .timeline-step-detail {
    flex: 1;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    overflow: hidden;
  }
  .timeline-item.step-running .timeline-step-detail {
    border-color: var(--border-accent);
  }

  .step-summary-bar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: var(--space-3) var(--space-4);
    cursor: pointer;
    gap: var(--space-3);
  }
  .step-summary-left {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    flex: 1;
    min-width: 0;
  }
  .step-number {
    font-weight: 700;
    font-size: var(--text-xs);
    color: var(--text-muted);
    white-space: nowrap;
  }
  .step-tool-badge {
    background: var(--bg-card-hover);
    padding: 2px 8px;
    border-radius: var(--radius-sm);
    font-size: var(--text-xs);
    font-family: var(--font-mono);
    color: var(--text-secondary);
    border: 1px solid var(--border);
    white-space: nowrap;
  }
  .step-log-text {
    font-size: var(--text-sm);
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .step-summary-right {
    display: flex;
    align-items: center;
    gap: var(--space-3);
  }
  .step-duration {
    font-size: var(--text-xs);
  }
  .step-chevron {
    font-size: var(--text-xs);
    color: var(--text-muted);
  }

  .step-expanded-details {
    padding: var(--space-4);
    border-top: 1px solid var(--border);
    background: rgba(0,0,0,0.15);
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }
  .step-sub-box {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
  }
  .step-sub-box strong {
    font-size: var(--text-xs);
    color: var(--text-secondary);
  }
  .step-sub-box pre {
    background: var(--bg-primary);
    border: 1px solid var(--border);
    padding: var(--space-3);
    border-radius: var(--radius-sm);
    font-size: var(--text-xs);
    overflow-x: auto;
  }

  /* --- Log Modal --- */
  .log-modal {
    max-width: 800px;
    width: 100%;
    display: flex;
    flex-direction: column;
    max-height: 80vh;
  }
  .log-modal-body {
    flex: 1;
    overflow-y: auto;
    max-height: 55vh;
  }
  .log-pre {
    background: var(--bg-primary);
    padding: var(--space-4);
    border-radius: var(--radius-md);
    font-size: var(--text-xs);
    white-space: pre-wrap;
    line-height: 1.5;
  }

  .btn-sm {
    padding: var(--space-1) var(--space-3);
    font-size: var(--text-xs);
  }
</style>
