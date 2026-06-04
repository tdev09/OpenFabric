<script lang="ts">
  import { onMount } from 'svelte';
  import { fetchMCPServers, fetchMCPBuiltins, type MCPServerStatus } from '$lib/stores/llm';
  import { dialog } from '$lib/stores/dialog';
  import { appConfig } from '$lib/stores/cluster';

  let servers: MCPServerStatus[] = [];
  let builtins: any[] = [];
  let loading = true;
  let errorMsg: string | null = null;

  // Drawer / Modal State
  let showModal = false;
  let currentServer: any = null;
  let isCustom = false;
  let formEnv: Record<string, string> = {};
  
  // Custom server specific config
  let customServerName = '';
  let customServerCommand = '';
  let customEnvList: { key: string; value: string }[] = [];

  // Saving state
  let saving = false;

  // Selected tool explorer state
  let selectedServerTools: any[] = [];
  let selectedServerName = '';
  let loadingTools = false;

  // Custom inline SVG icons for each service
  const serviceIcons: Record<string, string> = {
    github: `
      <svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor">
        <path fill-rule="evenodd" clip-rule="evenodd" d="M12 2C6.477 2 2 6.477 2 12c0 4.42 2.87 8.17 6.84 9.5.5.08.66-.23.66-.5v-1.69c-2.77.6-3.36-1.34-3.36-1.34-.46-1.16-1.11-1.47-1.11-1.47-.9-.62.07-.6.07-.6 1 .07 1.53 1.03 1.53 1.03.9 1.52 2.34 1.07 2.91.83.09-.65.35-1.09.63-1.34-2.22-.25-4.55-1.11-4.55-4.92 0-1.11.38-2 1.03-2.71-.1-.25-.45-1.29.1-2.64 0 0 .84-.27 2.75 1.02.79-.22 1.65-.33 2.5-.33.85 0 1.71.11 2.5.33 1.91-1.29 2.75-1.02 2.75-1.02.55 1.35.2 2.39.1 2.64.65.71 1.03 1.6 1.03 2.71 0 3.82-2.34 4.66-4.57 4.91.36.31.69.92.69 1.85V21c0 .27.16.59.67.5C19.14 20.16 22 16.42 22 12A10 10 0 0012 2z"/>
      </svg>
    `,
    notion: `
      <svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor">
        <path d="M4 2h16a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2zm2.93 4.29L6.5 6.78v10.44l.43.49c.14.16.47.29 1 .29.47 0 .81-.13.97-.29l.43-.49V8.89l5.88 8.78c.3.47.6.62.92.62.46 0 .82-.19 1-.5l.37-.5v-10.5l-.43-.49c-.14-.16-.47-.29-1-.29-.47 0-.81.13-.97.29l-.43.49V15.1L8.85 6.29c-.3-.47-.6-.62-.92-.62-.46 0-.82.19-1 .5l-.37.5z"/>
      </svg>
    `,
    'google-calendar': `
      <svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor">
        <path d="M19 4h-1V2h-2v2H8V2H6v2H5c-1.11 0-1.99.9-1.99 2L3 20a2 2 0 0 0 2 2h14c1.1 0 2-.9 2-2V6c0-1.1-.9-2-2-2zm0 16H5V10h14v10zm-5-7h-4v4h4v-4z"/>
      </svg>
    `,
    slack: `
      <svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor">
        <path d="M5.042 15.165a2.528 2.528 0 0 1-2.52 2.523 2.528 2.528 0 0 1-2.522-2.523 2.528 2.528 0 0 1 2.522-2.52h2.52v2.52zm1.261 0a2.528 2.528 0 0 1 2.52-2.52h5.043a2.528 2.528 0 0 1 2.522 2.52v5.042a2.528 2.528 0 0 1-2.522 2.52H8.823a2.528 2.528 0 0 1-2.52-2.52v-5.042zM8.823 5.043a2.528 2.528 0 0 1 2.52-2.52 2.528 2.528 0 0 1 2.522 2.52v2.522h-2.522a2.528 2.528 0 0 1-2.52-2.522zm0 1.261a2.528 2.528 0 0 1 2.52 2.52v5.043a2.528 2.528 0 0 1-2.52 2.522H3.78a2.528 2.528 0 0 1-2.522-2.522V8.824a2.528 2.528 0 0 1 2.522-2.52h5.043zm10.135 10.135a2.528 2.528 0 0 1 2.52-2.522 2.528 2.528 0 0 1 2.522 2.522 2.528 2.528 0 0 1-2.522 2.52h-2.52v-2.52zm-1.262 0a2.528 2.528 0 0 1-2.52 2.52h-5.043a2.528 2.528 0 0 1-2.522-2.52v-5.043a2.528 2.528 0 0 1 2.522-2.52h5.043a2.528 2.528 0 0 1 2.52 2.52v5.043zm-3.781-10.135a2.528 2.528 0 0 1-2.52 2.522 2.528 2.528 0 0 1-2.522-2.522 2.528 2.528 0 0 1 2.522-2.52h2.52v2.52zm0-1.262a2.528 2.528 0 0 1-2.52-2.52H15.18a2.528 2.528 0 0 1 2.522 2.52v5.043a2.528 2.528 0 0 1-2.522 2.52h-5.043a2.528 2.528 0 0 1-2.52-2.52v-5.043z"/>
      </svg>
    `,
    postgres: `
      <svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor">
        <path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z"/>
      </svg>
    `,
    linear: `
      <svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor">
        <path d="M12 2L2 22h20L12 2zm0 4.8L18.6 18H5.4L12 6.8z"/>
      </svg>
    `,
    obsidian: `
      <svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor">
        <path d="M12 2L2 12l10 10 10-10L12 2zm0 3.8L18.2 12 12 18.2 5.8 12 12 5.8z"/>
      </svg>
    `,
    gmail: `
      <svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor">
        <path d="M20 4H4c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V6c0-1.1-.9-2-2-2zm0 4l-8 5-8-5V6l8 5 8-5v2z"/>
      </svg>
    `,
    jira: `
      <svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor">
        <path d="M11.53 2C6.81 2 3 5.81 3 10.53V22h11.47c4.72 0 8.53-3.81 8.53-8.53V2H11.53zm8.53 8.53c0 3.6-2.93 6.53-6.53 6.53H5V10.53C5 6.93 7.93 4 11.53 4H20v6.53z"/>
      </svg>
    `,
    filesystem: `
      <svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor">
        <path d="M20 6h-8l-2-2H4c-1.1 0-1.99.9-1.99 2L2 18c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2zm0 12H4V8h16v10z"/>
      </svg>
    `
  };

  const serviceColors: Record<string, string> = {
    github: '#24292e',
    notion: '#000000',
    'google-calendar': '#4285F4',
    slack: '#4A154B',
    postgres: '#336791',
    linear: '#5E6AD2',
    obsidian: '#483699',
    gmail: '#EA4335',
    jira: '#0052CC',
    filesystem: '#10B981'
  };

  const oneLineDescriptions: Record<string, string> = {
    github: 'Access issues, PRs, and repositories',
    notion: 'Read and write pages and databases',
    'google-calendar': 'View and manage your calendar events',
    slack: 'Read channels and send messages',
    postgres: 'Query your databases directly',
    linear: 'Access issues and projects',
    obsidian: 'Search and read your vault notes',
    gmail: 'Read and send emails',
    jira: 'Access tickets and sprints',
    filesystem: 'Read files from any local directory'
  };

  onMount(async () => {
    await loadServers();
  });

  async function loadServers() {
    loading = true;
    errorMsg = null;
    try {
      builtins = await fetchMCPBuiltins();
      servers = await fetchMCPServers();
    } catch (err: any) {
      errorMsg = err.message ?? 'Failed to load integrations';
    } finally {
      loading = false;
    }
  }

  function getStatus(name: string) {
    const s = servers.find(x => x.name === name);
    if (!s) return { enabled: false, running: false, toolCount: 0, lastError: '' };
    return {
      enabled: s.enabled,
      running: s.running,
      toolCount: s.tool_count,
      lastError: s.last_error || ''
    };
  }

  function openConfigure(builtinName: string) {
    const builtin = builtins.find(x => x.name === builtinName);
    if (!builtin) return;

    isCustom = false;
    currentServer = builtin;
    formEnv = {};

    // Initialize fields
    for (const v of builtin.env_vars) {
      formEnv[v.key] = '';
    }

    // Try to pre-populate from existing configs if available
    fetch(`/api/mcp/servers`)
      .then(res => res.json())
      .then((data: any[]) => {
        const configured = data.find(s => s.name === builtinName);
        if (configured && configured.env) {
          for (const key of Object.keys(formEnv)) {
            if (configured.env[key] !== undefined) {
              formEnv[key] = configured.env[key];
            }
          }
        }
      })
      .catch(() => {});

    showModal = true;
  }

  function openCustomConfigure() {
    isCustom = true;
    currentServer = {
      name: '',
      label: 'Custom Integration',
      description: 'Configure a custom MCP server using an executable command.',
      env_vars: []
    };
    customServerName = '';
    customServerCommand = '';
    customEnvList = [];
    showModal = true;
  }

  function addCustomEnv() {
    customEnvList = [...customEnvList, { key: '', value: '' }];
  }

  function removeCustomEnv(index: number) {
    customEnvList = customEnvList.filter((_, i) => i !== index);
  }

  async function saveAndConnect() {
    saving = true;
    errorMsg = null;

    let payload: any = {};
    if (isCustom) {
      const envMap: Record<string, string> = {};
      for (const item of customEnvList) {
        if (item.key) envMap[item.key] = item.value;
      }
      payload = {
        name: customServerName.trim().toLowerCase(),
        command: customServerCommand.trim(),
        env: envMap,
        enabled: true
      };
    } else {
      payload = {
        name: currentServer.name,
        command: currentServer.command,
        env: { ...formEnv },
        enabled: true
      };
    }

    if (!payload.name || !payload.command) {
      errorMsg = 'Name and Command are required.';
      saving = false;
      return;
    }

    try {
      const res = await fetch('/api/mcp/servers', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
      });
      if (!res.ok) {
        const errData = await res.json();
        throw new Error(errData.error || 'Failed to save integration');
      }
      showModal = false;
      await loadServers();
    } catch (err: any) {
      errorMsg = err.message || 'Failed to save integration';
    } finally {
      saving = false;
    }
  }

  async function disconnect(name: string) {
    const confirmed = await dialog.confirm(
      `Are you sure you want to disconnect ${name}?`,
      'Disconnect Integration',
      '🔌',
      'Disconnect',
      'Cancel',
      true
    );
    if (!confirmed) return;
    try {
      const res = await fetch(`/api/mcp/servers/${name}`, {
        method: 'DELETE'
      });
      if (!res.ok) {
        const errData = await res.json();
        throw new Error(errData.error || 'Failed to disconnect integration');
      }
      await loadServers();
      if (selectedServerName === name) {
        selectedServerTools = [];
        selectedServerName = '';
      }
    } catch (err: any) {
      errorMsg = err.message || 'Failed to disconnect integration';
    }
  }

  async function exploreTools(name: string) {
    selectedServerName = name;
    selectedServerTools = [];
    loadingTools = true;
    // Scroll to top so the drawer is visible
    window.scrollTo({ top: 0, behavior: 'smooth' });
    try {
      const res = await fetch(`/api/mcp/servers/${name}/tools`);
      if (!res.ok) throw new Error('Failed to fetch tools');
      selectedServerTools = await res.json();
    } catch (err: any) {
      errorMsg = 'Could not load tools for ' + name;
    } finally {
      loadingTools = false;
    }
  }

  function copyToClipboard(text: string) {
    navigator.clipboard.writeText(text);
    dialog.alert(`Copied namespaced tool: ${text}`, 'Copied to Clipboard', '📋');
  }

  function getFirstLetter(label: string): string {
    return label ? label.charAt(0).toUpperCase() : 'M';
  }
</script>

<svelte:head>
  <title>Integrations (MCP) - {$appConfig.project_name}</title>
  <meta name="description" content="Manage external integrations and Model Context Protocol servers." />
</svelte:head>

<div class="integrations-page animate-fade-in">
  <div class="section-header">
    <div>
      <h1>Integrations</h1>
      <p class="subtitle text-secondary">Connect live tools and external databases to your cluster via Model Context Protocol (MCP).</p>
    </div>
    <button class="btn btn-primary" on:click={openCustomConfigure}>
      <span>+</span> Add Custom Server
    </button>
  </div>

  {#if errorMsg}
    <div class="banner banner-error animate-fade-in" role="alert">
      {errorMsg}
      <button class="btn-ghost" style="margin-left: auto; padding: 2px 8px;" on:click={() => errorMsg = null}>✕</button>
    </div>
  {/if}

  {#if loading}
    <div class="integrations-grid">
      {#each Array(6) as _}
        <div class="card skeleton-card">
          <div class="skeleton" style="height: 48px; width: 48px; border-radius: 8px; margin-bottom: 16px;"></div>
          <div class="skeleton" style="height: 20px; width: 140px; margin-bottom: 8px;"></div>
          <div class="skeleton" style="height: 14px; width: 200px; margin-bottom: 16px;"></div>
          <div class="skeleton" style="height: 36px; width: 100%;"></div>
        </div>
      {/each}
    </div>
  {:else}
    <!-- Grid of Integrations -->
    <div class="integrations-grid">
      {#each builtins as builtin}
        {@const status = getStatus(builtin.name)}
        {@const icon = serviceIcons[builtin.name]}
        {@const color = serviceColors[builtin.name] || '#1C2128'}
        {@const description = oneLineDescriptions[builtin.name] || builtin.description}
        
        <div class="card integration-card" class:connected={status.enabled}>
          <div class="card-top">
            <div class="icon-wrapper" style="background-color: {color}; color: #ffffff;">
              {#if icon}
                {@html icon}
              {:else}
                <span class="fallback-letter">{getFirstLetter(builtin.label)}</span>
              {/if}
            </div>
            
            <div class="status-indicator">
              <span 
                class="status-dot" 
                class:running={status.enabled && status.running}
                class:stopped={status.enabled && !status.running}
                class:disabled={!status.enabled}
              ></span>
              <span class="status-text text-xs">
                {status.enabled ? 'Connected' : 'Disconnected'}
              </span>
            </div>
          </div>
          
          <div class="card-info">
            <h3 class="integration-title">{builtin.label}</h3>
            <p class="integration-desc text-muted text-sm">{description}</p>
          </div>

          {#if status.enabled}
            <div class="connected-info">
              <div class="tool-count-badge badge badge-online">
                {status.toolCount} tools available
              </div>
            </div>
          {/if}

          {#if status.lastError}
            <p class="error-text text-danger text-xs">{status.lastError}</p>
          {/if}

          <div class="card-actions">
            {#if status.enabled}
              <button class="card-btn card-btn-explore" on:click={() => exploreTools(builtin.name)}>
                <span class="card-btn-icon">🔍</span> Explore Tools
              </button>
              <div class="card-btn-row">
                <button class="card-btn card-btn-configure" on:click={() => openConfigure(builtin.name)}>
                  ⚙ Reconfigure
                </button>
                <button class="card-btn-disconnect" on:click={() => disconnect(builtin.name)} title="Disconnect">
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M18 6L6 18M6 6l12 12"/></svg>
                </button>
              </div>
            {:else}
              <button class="btn btn-primary btn-sm" style="width: 100%; justify-content: center;" on:click={() => openConfigure(builtin.name)}>
                Connect
              </button>
            {/if}
          </div>
        </div>
      {/each}

      <!-- Custom configured servers card listing -->
      {#each servers.filter(s => !builtins.some(b => b.name === s.name)) as customSrv}
        <div class="card integration-card custom-server-card connected">
          <div class="card-top">
            <div class="icon-wrapper" style="background-color: var(--accent); color: #000;">
              <span>⚙️</span>
            </div>
            
            <div class="status-indicator">
              <span 
                class="status-dot" 
                class:running={customSrv.enabled && customSrv.running}
                class:stopped={customSrv.enabled && !customSrv.running}
                class:disabled={!customSrv.enabled}
              ></span>
              <span class="status-text text-xs">
                {customSrv.enabled ? 'Connected' : 'Disconnected'}
              </span>
            </div>
          </div>

          <div class="card-info">
            <h3 class="integration-title">{customSrv.name}</h3>
            <p class="integration-desc text-muted text-sm">Custom command-line integration server</p>
          </div>

          <div class="connected-info">
            <div class="tool-count-badge badge badge-online">
              {customSrv.tool_count} tools available
            </div>
          </div>

          {#if customSrv.last_error}
            <p class="error-text text-danger text-xs">{customSrv.last_error}</p>
          {/if}

          <div class="card-actions">
            <button class="card-btn card-btn-explore" on:click={() => exploreTools(customSrv.name)}>
              <span class="card-btn-icon">🔍</span> Explore Tools
            </button>
            <div class="card-btn-row">
              <button class="card-btn-disconnect" on:click={() => disconnect(customSrv.name)} title="Disconnect">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M18 6L6 18M6 6l12 12"/></svg>
                Disconnect
              </button>
            </div>
          </div>
        </div>
      {/each}
    </div>

  {/if}
</div>

<!-- Tool Explorer Drawer & Config Modal - siblings outside scroll container -->
{#if selectedServerName}
    <!-- Backdrop -->
    <div class="drawer-backdrop" on:click={() => { selectedServerName = ''; selectedServerTools = []; }}></div>
    <!-- Drawer -->
    <div class="tool-drawer animate-drawer-in">
      <div class="drawer-header">
        <div class="drawer-header-info">
          <div class="drawer-icon-wrap">
            {#if serviceIcons[selectedServerName]}
              {@html serviceIcons[selectedServerName]}
            {:else}
              <span>🔍</span>
            {/if}
          </div>
          <div>
            <h3>{selectedServerName} <span class="text-muted" style="font-weight:400">tools</span></h3>
            <p class="text-muted text-xs">Click any tool name to copy its ID</p>
          </div>
        </div>
        <button class="drawer-close-btn" on:click={() => { selectedServerName = ''; selectedServerTools = []; }} title="Close">
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M18 6L6 18M6 6l12 12"/></svg>
        </button>
      </div>

      <div class="drawer-body">
        {#if loadingTools}
          <div class="drawer-loading">
            <div class="drawer-spinner"></div>
            <p class="text-muted text-sm">Loading tools…</p>
          </div>
        {:else if selectedServerTools.length === 0}
          <div class="empty-state">
            <span class="empty-icon">🛠️</span>
            <h3>No tools found</h3>
            <p>This integration hasn't exposed any tools yet.</p>
          </div>
        {:else}
          <div class="drawer-tools-list">
            {#each selectedServerTools as tool}
              <button class="drawer-tool-item" on:click={() => copyToClipboard(`${selectedServerName}__${tool.name}`)}
                title="Click to copy: {selectedServerName}__{tool.name}">
                <div class="drawer-tool-header">
                  <code class="drawer-tool-name">{tool.name}</code>
                  <span class="drawer-copy-icon">
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
                  </span>
                </div>
                <p class="drawer-tool-desc">{tool.description}</p>
                {#if tool.inputSchema && tool.inputSchema.properties}
                  <div class="drawer-tool-params">
                    {#each Object.keys(tool.inputSchema.properties) as param}
                      <span class="param-chip">{param}</span>
                    {/each}
                  </div>
                {/if}
              </button>
            {/each}
          </div>
        {/if}
      </div>
    </div>
  {/if}

  <!-- Configuration Modal -->
  {#if showModal}
    <div class="modal-backdrop" on:click|self={() => showModal = false}>
      <div class="modal config-modal">
        <div class="modal-header">
          <div>
            <h2>Configure {currentServer ? currentServer.label : 'Custom Integration'}</h2>
            <p class="text-muted text-xs modal-desc-sub">
              {currentServer ? (oneLineDescriptions[currentServer.name] || currentServer.description) : 'Enter command line command'}
            </p>
          </div>
          <button class="btn-ghost" on:click={() => showModal = false}>✕</button>
        </div>

        <div class="modal-body">
          {#if !isCustom && (currentServer.name === 'google-calendar' || currentServer.name === 'gmail')}
            <div class="coming-soon-banner">
              <span class="coming-soon-icon">⏳</span>
              <h3>Coming soon</h3>
              <p>OAuth-based integrations for {currentServer.label} are currently in development and will be available in an upcoming release.</p>
            </div>
          {:else}
            <div class="form-fields">
              {#if isCustom}
                <div class="field">
                  <label for="custom-name" class="field-label">Server Name</label>
                  <input id="custom-name" class="input" bind:value={customServerName} placeholder="e.g. filesystem" />
                </div>
                <div class="field">
                  <label for="custom-cmd" class="field-label">Command Line</label>
                  <input id="custom-cmd" class="input" bind:value={customServerCommand} placeholder="e.g. npx -y @modelcontextprotocol/server-filesystem /path/to/dir" />
                </div>
                
                <div class="env-section">
                  <div class="env-header">
                    <span class="field-label">Environment Variables</span>
                    <button class="btn btn-secondary btn-sm" on:click={addCustomEnv}>+ Add</button>
                  </div>
                  <div class="env-rows">
                    {#each customEnvList as env, i}
                      <div class="env-row">
                        <input class="input" placeholder="KEY" bind:value={env.key} />
                        <input class="input" placeholder="VALUE" bind:value={env.value} type="password" />
                        <button class="btn btn-danger btn-sm" on:click={() => removeCustomEnv(i)}>✕</button>
                      </div>
                    {/each}
                  </div>
                </div>
              {:else}
                <!-- Builtin integration fields -->
                {#each currentServer.env_vars as env}
                  <div class="field">
                    <label for="env-{env.key}" class="field-label">
                      {env.label}
                      {#if env.required}<span class="text-accent">*</span>{/if}
                    </label>
                    
                    {#if currentServer.name === 'filesystem' && env.key === 'ALLOWED_DIRECTORIES'}
                      <input 
                        id="env-{env.key}" 
                        class="input" 
                        type="text" 
                        bind:value={formEnv[env.key]} 
                        placeholder="e.g. /Users/username/Documents"
                      />
                    {:else if currentServer.name === 'obsidian' && env.key === 'OBSIDIAN_VAULT_PATH'}
                      <input 
                        id="env-{env.key}" 
                        class="input" 
                        type="text" 
                        bind:value={formEnv[env.key]} 
                        placeholder="e.g. /Users/username/Vault"
                      />
                    {:else}
                      <input 
                        id="env-{env.key}" 
                        class="input" 
                        type={env.secret ? 'password' : 'text'} 
                        bind:value={formEnv[env.key]} 
                        placeholder={env.description}
                      />
                    {/if}
                  </div>
                {/each}
              {/if}
            </div>
          {/if}
        </div>

        <div class="modal-footer">
          <button class="btn btn-secondary" on:click={() => showModal = false}>
            Cancel
          </button>
          
          {#if isCustom || (currentServer.name !== 'google-calendar' && currentServer.name !== 'gmail')}
            <button class="btn btn-primary" on:click={saveAndConnect} disabled={saving}>
              {saving ? 'Connecting…' : 'Save & Connect'}
            </button>
          {/if}
        </div>
      </div>
    </div>
  {/if}

<style>
  .integrations-page {
    display: flex;
    flex-direction: column;
    gap: var(--space-6);
  }
  .subtitle {
    margin-top: 4px;
    font-size: var(--text-sm);
  }
  .integrations-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: var(--space-5);
  }
  .integration-card {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    min-height: 240px;
    position: relative;
    border: 1px solid var(--border);
    transition: all var(--transition);
  }
  .integration-card.connected {
    border-color: var(--border-accent);
    box-shadow: 0 0 12px rgba(0, 201, 167, 0.05);
  }
  .card-top {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .icon-wrapper {
    width: 44px;
    height: 44px;
    border-radius: var(--radius-md);
    display: flex;
    align-items: center;
    justify-content: center;
    box-shadow: var(--shadow-sm);
  }
  .fallback-letter {
    font-weight: 700;
    font-size: var(--text-lg);
  }
  .status-indicator {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    background: var(--bg-tertiary);
    padding: 3px 8px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
  }
  .status-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
  }
  .status-dot.running {
    background: var(--online);
    box-shadow: 0 0 8px var(--online);
    animation: pulse-glow 2s infinite;
  }
  .status-dot.stopped {
    background: var(--danger);
  }
  .status-dot.disabled {
    background: var(--offline);
  }
  @keyframes pulse-glow {
    0% { box-shadow: 0 0 0 0 rgba(0, 201, 167, 0.7); }
    70% { box-shadow: 0 0 0 6px rgba(0, 201, 167, 0); }
    100% { box-shadow: 0 0 0 0 rgba(0, 201, 167, 0); }
  }
  .card-info {
    flex-grow: 1;
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .integration-title {
    font-size: var(--text-lg);
    font-weight: 700;
    color: var(--text-primary);
  }
  .integration-desc {
    line-height: 1.4;
    color: var(--text-secondary);
  }
  .connected-info {
    margin-top: -2px;
  }
  .tool-count-badge {
    align-self: flex-start;
  }
  .card-actions {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    margin-top: auto;
    width: 100%;
  }
  .card-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    width: 100%;
    padding: 9px 14px;
    border-radius: var(--radius-md);
    font-size: var(--text-sm);
    font-weight: 500;
    cursor: pointer;
    transition: all 0.2s ease;
    border: 1px solid var(--border);
  }
  .card-btn-explore {
    background: rgba(0, 201, 167, 0.08);
    border-color: rgba(0, 201, 167, 0.25);
    color: var(--accent);
  }
  .card-btn-explore:hover {
    background: rgba(0, 201, 167, 0.15);
    border-color: var(--accent);
  }
  .card-btn-row {
    display: flex;
    gap: var(--space-2);
  }
  .card-btn-configure {
    flex: 1;
    background: rgba(255,255,255,0.03);
    color: var(--text-secondary);
  }
  .card-btn-configure:hover {
    background: rgba(255,255,255,0.07);
    color: var(--text-primary);
  }
  .card-btn-disconnect {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 8px 12px;
    border-radius: var(--radius-md);
    background: rgba(255, 107, 107, 0.06);
    border: 1px solid rgba(255, 107, 107, 0.2);
    color: var(--danger);
    font-size: var(--text-xs);
    font-weight: 500;
    cursor: pointer;
    transition: all 0.2s ease;
    white-space: nowrap;
  }
  .card-btn-disconnect:hover {
    background: rgba(255, 107, 107, 0.15);
    border-color: var(--danger);
  }
  .error-text {
    line-height: 1.25;
    background: rgba(255, 107, 107, 0.08);
    padding: 6px 10px;
    border-radius: var(--radius-sm);
    border: 1px solid rgba(255, 107, 107, 0.2);
  }

  /* ── Tool Explorer Drawer ── */
  .drawer-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0,0,0,0.55);
    backdrop-filter: blur(3px);
    z-index: 400;
  }
  .tool-drawer {
    position: fixed;
    top: 0;
    right: 0;
    bottom: 0;
    width: 420px;
    max-width: 95vw;
    background: var(--bg-secondary);
    border-left: 1px solid var(--border);
    box-shadow: -8px 0 40px rgba(0,0,0,0.5);
    z-index: 401;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  @keyframes drawer-slide-in {
    from { transform: translateX(100%); opacity: 0; }
    to   { transform: translateX(0);    opacity: 1; }
  }
  .animate-drawer-in {
    animation: drawer-slide-in 250ms cubic-bezier(0.16, 1, 0.3, 1) both;
  }
  .drawer-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    padding: var(--space-5) var(--space-5) var(--space-4);
    border-bottom: 1px solid var(--border);
    flex-shrink: 0;
  }
  .drawer-header-info {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    min-width: 0;
  }
  .drawer-icon-wrap {
    width: 40px;
    height: 40px;
    border-radius: var(--radius-md);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    color: var(--text-primary);
  }
  .drawer-close-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 34px;
    height: 34px;
    border-radius: var(--radius-md);
    background: rgba(255,255,255,0.04);
    border: 1px solid var(--border);
    color: var(--text-secondary);
    cursor: pointer;
    transition: all 0.2s ease;
    flex-shrink: 0;
  }
  .drawer-close-btn:hover {
    background: rgba(255,107,107,0.12);
    border-color: rgba(255,107,107,0.4);
    color: var(--danger);
  }
  .drawer-body {
    flex: 1;
    overflow-y: auto;
    padding: var(--space-4) var(--space-5);
  }
  .drawer-loading {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-12) 0;
  }
  .drawer-spinner {
    width: 28px;
    height: 28px;
    border: 2px solid var(--border);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  .drawer-tools-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .drawer-tool-item {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    width: 100%;
    text-align: left;
    padding: var(--space-3) var(--space-4);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    cursor: pointer;
    transition: all 0.15s ease;
  }
  .drawer-tool-item:hover {
    background: rgba(0,201,167,0.06);
    border-color: rgba(0,201,167,0.25);
  }
  .drawer-tool-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-2);
  }
  .drawer-tool-name {
    background: var(--accent-dim);
    color: var(--accent);
    padding: 2px 8px;
    border-radius: var(--radius-sm);
    font-size: var(--text-xs);
    font-weight: 600;
    font-family: var(--font-mono);
  }
  .drawer-copy-icon {
    color: var(--text-muted);
    opacity: 0;
    transition: opacity 0.15s ease;
    flex-shrink: 0;
  }
  .drawer-tool-item:hover .drawer-copy-icon {
    opacity: 1;
  }
  .drawer-tool-desc {
    font-size: var(--text-xs);
    color: var(--text-secondary);
    line-height: 1.4;
  }
  .drawer-tool-params {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    margin-top: 2px;
  }
  .param-chip {
    font-size: 10px;
    font-family: var(--font-mono);
    background: rgba(255,255,255,0.05);
    border: 1px solid var(--border);
    color: var(--text-muted);
    padding: 1px 6px;
    border-radius: var(--radius-sm);
  }

  .tools-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .tool-item {
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-4);
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-4);
  }
  @media (max-width: 768px) {
    .tool-item {
      grid-template-columns: 1fr;
    }
  }
  .tool-meta {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .tool-name-btn {
    align-self: flex-start;
    background: transparent;
    border: none;
    padding: 0;
    cursor: pointer;
    text-align: left;
  }
  .tool-name-btn code {
    background: var(--accent-dim);
    color: var(--accent);
    padding: 4px 8px;
    border-radius: var(--radius-sm);
    font-weight: 600;
  }
  .tool-name-btn:hover code {
    box-shadow: 0 0 8px var(--accent-glow);
  }
  .tool-description {
    font-size: var(--text-sm);
    color: var(--text-secondary);
  }
  .tool-schema {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .schema-title {
    font-size: var(--text-xs);
    font-weight: 600;
    text-transform: uppercase;
    color: var(--text-muted);
  }
  .schema-code {
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: var(--space-3);
    max-height: 150px;
    overflow-y: auto;
    font-size: 11px;
    color: var(--text-secondary);
  }

  /* Configuration Modal */
  /* Override global .modal-backdrop to escape the scroll container */
  :global(.modal-backdrop) {
    position: fixed !important;
    inset: 0 !important;
    top: 0 !important;
    left: 0 !important;
    right: 0 !important;
    bottom: 0 !important;
    display: flex !important;
    align-items: center !important;
    justify-content: center !important;
    z-index: 2000 !important;
  }
  .config-modal {
    max-width: 500px;
  }
  .modal-desc-sub {
    margin-top: 4px;
    line-height: 1.35;
  }
  .form-fields {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .modal-footer {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-3);
    margin-top: var(--space-6);
  }
  .env-section {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    border-top: 1px solid var(--border);
    padding-top: var(--space-4);
  }
  .env-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--space-1);
  }
  .env-rows {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .env-row {
    display: flex;
    gap: var(--space-2);
  }
  .env-row input {
    flex-grow: 1;
  }

  .skeleton-card {
    min-height: 240px;
    display: flex;
    flex-direction: column;
    justify-content: center;
  }

  /* Coming Soon Section */
  .coming-soon-banner {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: var(--space-8) var(--space-4);
    text-align: center;
    background: var(--bg-tertiary);
    border: 1px dashed var(--border);
    border-radius: var(--radius-lg);
    gap: var(--space-2);
  }
  .coming-soon-icon {
    font-size: 40px;
    margin-bottom: var(--space-2);
  }
  .coming-soon-banner h3 {
    font-size: var(--text-lg);
    font-weight: 600;
    color: var(--text-primary);
  }
  .coming-soon-banner p {
    font-size: var(--text-sm);
    color: var(--text-muted);
    max-width: 320px;
    line-height: 1.45;
  }
</style>
