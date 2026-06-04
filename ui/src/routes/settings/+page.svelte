<script lang="ts">
  import { onMount } from 'svelte';
  import { settings, loadSettings, saveSettings, type Settings, appConfig } from '$lib/stores/cluster';

  let form: Settings = {
    cluster_name: '',
    device_name: '',
    api_port: 4892,
    auto_start: false,
    storage_sync_enabled: true,
    accept_tasks: true,
    network_access: 'local_network',
    memory_enabled: true,
    memory_auto_extract: true,
    sandbox_mode: true,
    allowed_commands: [],
    task_timeout: 5,
    image_gen_url: '',
    wol_memory_threshold: 0.20,
    policies: [],
  };

  let saving = false;
  let saved = false;
  let leaveConfirm = false;
  let newCommand = '';
  let activeSubTab: 'cluster' | 'features' | 'security' | 'gpu' | 'policies' | 'danger' = 'cluster';

  let showAddPolicy = false;
  let newPolicyName = '';
  let newPolicyAction: 'block' | 'backpressure' = 'block';
  let newPolicyTargetClass = 'all';
  let newPolicyMessage = '';
  let newPolicyRules: Array<{
    scope: 'cluster' | 'node';
    metric: 'cpu_percent' | 'ram_used_percent' | 'gpu_used_percent' | 'tasks_running' | 'throughput' | 'tokens_sec';
    operator: 'gt' | 'lt' | 'gte' | 'lte';
    value: number;
  }> = [
    { scope: 'cluster', metric: 'cpu_percent', operator: 'gt', value: 80 }
  ];

  function addRuleToForm() {
    newPolicyRules = [...newPolicyRules, { scope: 'cluster', metric: 'cpu_percent', operator: 'gt', value: 80 }];
  }

  function removeRuleFromForm(idx: number) {
    newPolicyRules = newPolicyRules.filter((_, i) => i !== idx);
  }

  function handleCreatePolicy() {
    if (!newPolicyName.trim()) return;
    const newPolicy = {
      id: 'policy-' + Math.random().toString(36).substr(2, 9),
      name: newPolicyName.trim(),
      enabled: true,
      rules: newPolicyRules,
      action: newPolicyAction,
      target_class: newPolicyTargetClass,
      message: newPolicyMessage.trim()
    };
    form.policies = [...(form.policies || []), newPolicy];
    showAddPolicy = false;
    resetPolicyForm();
  }

  function deletePolicy(id: string) {
    form.policies = (form.policies || []).filter(p => p.id !== id);
  }

  function resetPolicyForm() {
    newPolicyName = '';
    newPolicyAction = 'block';
    newPolicyTargetClass = 'all';
    newPolicyMessage = '';
    newPolicyRules = [{ scope: 'cluster', metric: 'cpu_percent', operator: 'gt', value: 80 }];
  }
  let imageGenMode: 'auto' | 'custom' = 'auto';
  let testingConnection = false;
  let testResult: { success: boolean; type?: string; url?: string; version?: string; error?: string } | null = null;

  function handleModeChange(mode: string) {
    if (mode === 'auto' || mode === 'custom') {
      imageGenMode = mode;
      if (mode === 'auto') {
        form.image_gen_url = '';
      } else if (!form.image_gen_url) {
        form.image_gen_url = 'http://localhost:7860';
      }
    }
  }

  async function testConnection() {
    testingConnection = true;
    testResult = null;
    try {
      const res = await fetch('/api/gpu/test', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url: form.image_gen_url })
      });
      const data = await res.json();
      if (res.ok) {
        testResult = {
          success: true,
          type: data.type,
          url: data.url,
          version: data.version
        };
      } else {
        testResult = {
          success: false,
          error: data.error || 'Connection failed'
        };
      }
    } catch (e: any) {
      testResult = {
        success: false,
        error: e.message || 'Network error'
      };
    } finally {
      testingConnection = false;
    }
  }

  function handleAddCommand() {
    const cmd = newCommand.trim().toLowerCase();
    if (cmd) {
      form.allowed_commands = [...(form.allowed_commands || []), cmd];
      newCommand = '';
    }
  }

  onMount(() => {
    loadSettings().then(() => {
      if ($settings) {
        form = { ...$settings, policies: $settings.policies || [] };
        imageGenMode = form.image_gen_url ? 'custom' : 'auto';
      }
    });
  });


  async function handleSave() {
    saving = true;
    saved = false;
    try {
      await saveSettings(form);
      saved = true;
      setTimeout(() => (saved = false), 2500);
    } finally {
      saving = false;
    }
  }
</script>

<svelte:head>
  <title>Settings - {$appConfig.project_name}</title>
  <meta name="description" content="Configure your {$appConfig.project_name} cluster" />
</svelte:head>

<div class="settings-container animate-fade-in">
  <div class="section-header">
    <h1>Settings</h1>
    {#if saved}
      <div class="banner banner-info saved-banner" role="status">✓ Settings saved</div>
    {/if}
  </div>

  {#if !$settings}
    <div class="card"><p class="text-muted">Loading settings…</p></div>
  {:else}
    <div class="settings-wrapper">
      <!-- Sidebar Navigation -->
      <aside class="settings-sidebar">
        <button class="sidebar-btn" class:active={activeSubTab === 'cluster'} on:click={() => activeSubTab = 'cluster'}>
          <span class="btn-icon">🌐</span>
          <span class="btn-text">Cluster & Device</span>
        </button>
        <button class="sidebar-btn" class:active={activeSubTab === 'features'} on:click={() => activeSubTab = 'features'}>
          <span class="btn-icon">⚙️</span>
          <span class="btn-text">Features & Memory</span>
        </button>
        <button class="sidebar-btn" class:active={activeSubTab === 'security'} on:click={() => activeSubTab = 'security'}>
          <span class="btn-icon">🛡️</span>
          <span class="btn-text">Task Security</span>
        </button>
        <button class="sidebar-btn" class:active={activeSubTab === 'gpu'} on:click={() => activeSubTab = 'gpu'}>
          <span class="btn-icon">🎨</span>
          <span class="btn-text">GPU & Image Gen</span>
        </button>
        <button class="sidebar-btn" class:active={activeSubTab === 'policies'} on:click={() => activeSubTab = 'policies'}>
          <span class="btn-icon">⚖️</span>
          <span class="btn-text">Policy Engine</span>
        </button>
        <button class="sidebar-btn danger" class:active={activeSubTab === 'danger'} on:click={() => activeSubTab = 'danger'}>
          <span class="btn-icon">⚠️</span>
          <span class="btn-text">Danger Zone</span>
        </button>
      </aside>

      <!-- Settings Fields Content -->
      <main class="settings-content card glassmorphic">
        {#if activeSubTab === 'cluster'}
          <div class="tab-content animate-fade-in">
            <h2 class="content-title">Cluster & Device Configuration</h2>
            <p class="content-desc">Manage your node identification and connection parameters within the {$appConfig.project_name} cluster.</p>

            <div class="form-group">
              <div class="field">
                <label for="cluster-name" class="field-label">Cluster name</label>
                <input id="cluster-name" class="input input-premium" bind:value={form.cluster_name} placeholder="my-cluster" />
                <p class="field-hint">A friendly name for this cluster. Shown to other nodes.</p>
              </div>
              <div class="field">
                <label for="device-name" class="field-label">Device name</label>
                <input id="device-name" class="input input-premium" bind:value={form.device_name} placeholder="my-macbook" />
                <p class="field-hint">The custom name identifying this node uniquely.</p>
              </div>
              <div class="field">
                <label for="api-port" class="field-label">API port</label>
                <input id="api-port" class="input input-premium" type="number" bind:value={form.api_port} min="1024" max="65535" />
                <p class="field-hint">Default: 4892. Requires restarting the agent to take effect.</p>
              </div>
            </div>
          </div>
        {:else if activeSubTab === 'features'}
          <div class="tab-content animate-fade-in">
            <h2 class="content-title">System Features & Fabric Memory</h2>
            <p class="content-desc">Enable or disable background services, cluster synchronization, and agent-level preferences.</p>

            <div class="toggle-list">
              <div class="toggle-card">
                <div class="toggle-card-info">
                  <span class="toggle-card-title">Auto-start on login</span>
                  <span class="toggle-card-desc">Launch {$appConfig.project_name} agent automatically when you log in</span>
                </div>
                <label class="toggle">
                  <input id="auto-start" type="checkbox" bind:checked={form.auto_start} />
                  <span class="toggle-slider"></span>
                </label>
              </div>

              <div class="toggle-card">
                <div class="toggle-card-info">
                  <span class="toggle-card-title">Storage synchronization</span>
                  <span class="toggle-card-desc">Sync shared files across all cluster nodes</span>
                </div>
                <label class="toggle">
                  <input id="storage-sync" type="checkbox" bind:checked={form.storage_sync_enabled} />
                  <span class="toggle-slider"></span>
                </label>
              </div>

              <div class="toggle-card">
                <div class="toggle-card-info">
                  <span class="toggle-card-title">Accept remote tasks</span>
                  <span class="toggle-card-desc">Allow other nodes to run tasks on this device</span>
                </div>
                <label class="toggle">
                  <input id="accept-tasks" type="checkbox" bind:checked={form.accept_tasks} />
                  <span class="toggle-slider"></span>
                </label>
              </div>

              <div class="toggle-card">
                <div class="toggle-card-info">
                  <span class="toggle-card-title">Network access</span>
                  <span class="toggle-card-desc">Allow other devices on your Wi-Fi to connect (Turn off on public Wi-Fi)</span>
                </div>
                <label class="toggle">
                  <input
                    id="network-access"
                    type="checkbox"
                    checked={form.network_access === 'local_network'}
                    on:change={(e) => {
                      form.network_access = e.currentTarget.checked ? 'local_network' : 'localhost_only';
                    }}
                  />
                  <span class="toggle-slider"></span>
                </label>
              </div>

              <div class="toggle-card">
                <div class="toggle-card-info">
                  <span class="toggle-card-title">Fabric Memory</span>
                  <span class="toggle-card-desc">Enable persistent context layer to remember details across chat sessions</span>
                </div>
                <label class="toggle">
                  <input id="memory-enabled" type="checkbox" bind:checked={form.memory_enabled} />
                  <span class="toggle-slider"></span>
                </label>
              </div>

              <div class="toggle-card" style={!form.memory_enabled ? "opacity: 0.5; pointer-events: none;" : ""}>
                <div class="toggle-card-info">
                  <span class="toggle-card-title">Auto-extract context</span>
                  <span class="toggle-card-desc">Automatically extract facts and preferences from your conversations</span>
                </div>
                <label class="toggle">
                  <input id="memory-auto-extract" type="checkbox" bind:checked={form.memory_auto_extract} disabled={!form.memory_enabled} />
                  <span class="toggle-slider"></span>
                </label>
              </div>
            </div>
          </div>
        {:else if activeSubTab === 'security'}
          <div class="tab-content animate-fade-in">
            <h2 class="content-title">Task Security Sandbox</h2>
            <p class="content-desc">Restrict task commands from external or local agents to prevent shell injection vulnerabilities.</p>

            <div class="toggle-card bg-highlight">
              <div class="toggle-card-info">
                <span class="toggle-card-title">Sandbox Mode (Recommended)</span>
                <span class="toggle-card-desc">Enforce command allowlist to restrict arbitrary shell execution</span>
              </div>
              <label class="toggle">
                <input id="sandbox-mode" type="checkbox" bind:checked={form.sandbox_mode} />
                <span class="toggle-slider"></span>
              </label>
            </div>

            {#if form.sandbox_mode}
              <div class="field animate-fade-in">
                <label class="field-label">Allowed Commands Allowlist</label>
                <div class="command-tags">
                  {#each form.allowed_commands || [] as cmd, idx}
                    <span class="command-tag">
                      {cmd}
                      <button type="button" class="tag-remove" on:click={() => {
                        form.allowed_commands = form.allowed_commands.filter((_, i) => i !== idx);
                      }}>&times;</button>
                    </span>
                  {/each}
                </div>
                
                <div class="add-command-row">
                  <input 
                    id="new-command-input" 
                    class="input input-premium input-sm" 
                    placeholder="e.g. ffmpeg" 
                    bind:value={newCommand}
                    on:keydown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault();
                        handleAddCommand();
                      }
                    }}
                  />
                  <button type="button" class="btn btn-secondary btn-sm" on:click={handleAddCommand}>
                    + Add command
                  </button>
                </div>
                <p class="field-hint">Press Enter or click Add to append a command. Base names only, no paths.</p>
              </div>
            {/if}

            <div class="field">
              <label for="task-timeout" class="field-label">Task timeout (minutes)</label>
              <input id="task-timeout" class="input input-premium" type="number" bind:value={form.task_timeout} min="1" max="1440" />
              <p class="field-hint">Max execution duration for any single command before it is automatically stopped.</p>
            </div>

            <div class="field">
              <label for="wol-memory-threshold" class="field-label">Auto-Wake RAM Threshold (free %)</label>
              <input id="wol-memory-threshold" class="input input-premium" type="number" step="0.05" min="0.00" max="0.95" bind:value={form.wol_memory_threshold} />
              <p class="field-hint">The cluster memory pressure threshold to wake up sleeping nodes (e.g. 0.20 wakes a node when free RAM falls below 20%). Set to 0 to disable.</p>
            </div>
          </div>
        {:else if activeSubTab === 'gpu'}
          <div class="tab-content animate-fade-in">
            <h2 class="content-title">GPU & Image Generation</h2>
            <p class="content-desc">Configure the local image generation backend (AUTOMATIC1111 or ComfyUI) used for creating visual assets.</p>

            <div class="form-group">
              <div class="field">
                <label for="image-gen-mode-select" class="field-label">Image Generation Service</label>
                <div class="select-wrapper">
                  <select
                    id="image-gen-mode-select"
                    class="input input-premium"
                    value={imageGenMode}
                    on:change={(e) => handleModeChange(e.currentTarget.value)}
                  >
                    <option value="auto">Auto-detect (scans common ports)</option>
                    <option value="custom">Custom URL</option>
                  </select>
                </div>
                <p class="field-hint">Auto-detect scans common default ports (7860-7863 for AUTOMATIC1111, 8188-8189 for ComfyUI).</p>
              </div>

              {#if imageGenMode === 'custom'}
                <div class="field animate-fade-in">
                  <label for="image-gen-url" class="field-label">Custom URL</label>
                  <input
                    id="image-gen-url"
                    class="input input-premium"
                    bind:value={form.image_gen_url}
                    placeholder="http://localhost:7860"
                  />
                  <p class="field-hint">Input the full base URL (e.g. http://localhost:7860 or http://192.168.1.50:8188).</p>
                </div>
              {/if}

              <div class="gpu-connection-test-section">
                <button
                  type="button"
                  class="btn btn-secondary btn-test-conn"
                  on:click={testConnection}
                  disabled={testingConnection}
                >
                  {testingConnection ? 'Testing…' : 'Test Connection'}
                </button>

                {#if testResult}
                  <div class="test-result-wrapper animate-fade-in">
                    {#if testResult.success}
                      <div class="test-result success">
                        <div class="result-headline">
                          <span class="icon">✓</span> Connected to <strong>{testResult.type === 'automatic1111' ? 'AUTOMATIC1111' : 'ComfyUI'}</strong>
                        </div>
                        <div class="result-details">
                          Running at <code>{testResult.url}</code><br/>
                          Status: <span class="badge">{testResult.version}</span>
                        </div>
                      </div>
                    {:else}
                      <div class="test-result error">
                        <div class="result-headline">
                          <span class="icon">✗</span> Connection failed
                        </div>
                        <div class="result-details">
                          {testResult.error}
                        </div>
                      </div>
                    {/if}
                  </div>
                {/if}
              </div>
            </div>
          </div>
        {:else if activeSubTab === 'policies'}
          <div class="tab-content animate-fade-in">
            <h2 class="content-title">Fabric Policy Engine</h2>
            <p class="content-desc">Define and manage cluster-wide dynamic policies built on top of telemetry metrics. Policies are automatically synchronized across all trusted peers.</p>

            <div class="policies-list">
              {#each form.policies || [] as p, idx (p.id)}
                <div class="policy-card card glassmorphic animate-fade-in">
                  <div class="policy-header">
                    <div class="policy-info">
                      <span class="policy-name">{p.name}</span>
                      <span class="policy-meta">
                        Action: <span class="badge badge-action">{p.action}</span> |
                        Target Class: <span class="badge badge-class">{p.target_class || 'all'}</span>
                      </span>
                    </div>
                    <div class="policy-actions">
                      <label class="toggle toggle-sm">
                        <input type="checkbox" bind:checked={p.enabled} />
                        <span class="toggle-slider"></span>
                      </label>
                      <button type="button" class="btn btn-secondary btn-sm btn-delete" on:click={() => deletePolicy(p.id)}>
                        Delete
                      </button>
                    </div>
                  </div>

                  <div class="policy-rules">
                    <span class="rules-label">Conditions (evaluated when all match):</span>
                    <ul class="rules-list">
                      {#each p.rules || [] as r}
                        <li>
                          <span class="scope-tag">{r.scope === 'cluster' ? 'Cluster Avg' : 'Node Local'}</span>
                          <code>{r.metric}</code>
                          <span class="op-tag">{r.operator}</span>
                          <strong>{r.value}</strong>
                        </li>
                      {/each}
                    </ul>
                  </div>

                  {#if p.message}
                    <div class="policy-message">
                      <span class="message-label">Denial Message:</span>
                      <span class="message-content">"{p.message}"</span>
                    </div>
                  {/if}
                </div>
              {:else}
                <div class="empty-state">
                  <p class="text-muted">No policies defined. Use the button below to add cluster-wide telemetry guards.</p>
                </div>
              {/each}
            </div>

            {#if showAddPolicy}
              <div class="add-policy-form card bg-highlight animate-fade-in">
                <h3 class="panel-subtitle">Create Policy</h3>
                <div class="form-group" style="margin-top: var(--space-4);">
                  <div class="field">
                    <label for="new-policy-name" class="field-label">Policy name</label>
                    <input id="new-policy-name" class="input input-premium" bind:value={newPolicyName} placeholder="e.g. Reject Shell tasks on High CPU" />
                  </div>

                  <div class="form-row-grid">
                    <div class="field">
                      <label for="new-policy-action" class="field-label">Action</label>
                      <div class="select-wrapper">
                        <select id="new-policy-action" class="input input-premium" bind:value={newPolicyAction}>
                          <option value="block">Block (reject execution)</option>
                          <option value="backpressure">Backpressure (soft warning)</option>
                        </select>
                      </div>
                    </div>

                    <div class="field">
                      <label for="new-policy-target" class="field-label">Target task class</label>
                      <div class="select-wrapper">
                        <select id="new-policy-target" class="input input-premium" bind:value={newPolicyTargetClass}>
                          <option value="all">All tasks</option>
                          <option value="shell">Shell tasks</option>
                          <option value="llm">LLM tasks</option>
                          <option value="gpu">GPU tasks</option>
                          <option value="cpu">CPU-intensive tasks</option>
                          <option value="io">IO tasks</option>
                        </select>
                      </div>
                    </div>
                  </div>

                  <div class="field">
                    <label for="new-policy-message" class="field-label">Custom Denial Message</label>
                    <input id="new-policy-message" class="input input-premium" bind:value={newPolicyMessage} placeholder="Leave blank for default" />
                  </div>

                  <div class="rules-editor">
                    <label class="field-label">Rules (All must match to trigger)</label>
                    {#each newPolicyRules as r, rIdx}
                      <div class="rule-edit-row animate-fade-in">
                        <div class="select-wrapper select-scope">
                          <select class="input input-premium input-sm" bind:value={r.scope}>
                            <option value="cluster">Cluster Avg</option>
                            <option value="node">Node Local</option>
                          </select>
                        </div>

                        <div class="select-wrapper select-metric">
                          <select class="input input-premium input-sm" bind:value={r.metric}>
                            <option value="cpu_percent">CPU %</option>
                            <option value="ram_used_percent">RAM Used %</option>
                            <option value="gpu_used_percent">GPU Used %</option>
                            <option value="tasks_running">Tasks Running</option>
                            {#if r.scope === 'cluster'}
                              <option value="throughput">Throughput (tasks/sec)</option>
                              <option value="tokens_sec">LLM speed (tokens/sec)</option>
                            {/if}
                          </select>
                        </div>

                        <div class="select-wrapper select-operator">
                          <select class="input input-premium input-sm" bind:value={r.operator}>
                            <option value="gt">&gt;</option>
                            <option value="lt">&lt;</option>
                            <option value="gte">&gt;=</option>
                            <option value="lte">&lt;=</option>
                          </select>
                        </div>

                        <input type="number" class="input input-premium input-sm rule-val-input" step="any" bind:value={r.value} />

                        {#if newPolicyRules.length > 1}
                          <button type="button" class="btn btn-secondary btn-sm btn-remove-rule" on:click={() => removeRuleFromForm(rIdx)}>
                            &times;
                          </button>
                        {/if}
                      </div>
                    {/each}

                    <button type="button" class="btn btn-secondary btn-sm btn-add-rule-inner" style="align-self: flex-start;" on:click={addRuleToForm}>
                      + Add Rule
                    </button>
                  </div>

                  <div class="form-actions-row">
                    <button type="button" class="btn btn-secondary" on:click={() => { showAddPolicy = false; resetPolicyForm(); }}>
                      Cancel
                    </button>
                    <button type="button" class="btn btn-primary" on:click={handleCreatePolicy} disabled={!newPolicyName.trim()}>
                      Create Policy
                    </button>
                  </div>
                </div>
              </div>
            {:else}
              <button type="button" class="btn btn-secondary btn-add-policy animate-fade-in" on:click={() => showAddPolicy = true}>
                + Add Policy
              </button>
            {/if}
          </div>
        {:else if activeSubTab === 'danger'}
          <div class="tab-content animate-fade-in">
            <h2 class="content-title danger-title">Danger Zone</h2>
            <p class="content-desc">Perform high-risk actions. Disconnecting or leaving the cluster will stop resource sharing.</p>

            <div class="danger-card card">
              {#if leaveConfirm}
                <div class="banner banner-error" role="alert">
                  Are you sure? This device will disconnect from the cluster.
                  <div style="display:flex; gap: 8px; margin-top: 12px;">
                    <button class="btn btn-secondary btn-sm" on:click={() => leaveConfirm = false}>Cancel</button>
                    <button class="btn btn-danger btn-sm" id="confirm-leave-btn">Yes, leave cluster</button>
                  </div>
                </div>
              {:else}
                <div class="danger-row">
                  <div>
                    <span class="danger-card-title">Leave cluster</span>
                    <p class="danger-card-desc">Remove this device from the cluster. Your local data is preserved.</p>
                  </div>
                  <button class="btn btn-danger" id="leave-cluster-btn" on:click={() => leaveConfirm = true}>
                    Leave Cluster
                  </button>
                </div>
              {/if}
            </div>
          </div>
        {/if}

        <div class="settings-footer">
          <button class="btn btn-primary btn-save" on:click={handleSave} disabled={saving} id="save-settings-btn">
            {saving ? 'Saving…' : 'Save Settings'}
          </button>
        </div>
      </main>
    </div>
  {/if}
</div>

<style>
  .settings-container {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
    width: 100%;
  }

  .settings-wrapper {
    display: grid;
    grid-template-columns: 240px 1fr;
    gap: var(--space-6);
    align-items: start;
  }

  .settings-sidebar {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }

  .sidebar-btn {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-3) var(--space-4);
    background: transparent;
    border: 1px solid transparent;
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    cursor: pointer;
    text-align: left;
    transition: all 0.2s ease;
    font-size: var(--text-sm);
    font-weight: 500;
  }

  .sidebar-btn:hover {
    background: rgba(255, 255, 255, 0.03);
    color: var(--text-primary);
  }

  .sidebar-btn.active {
    background: rgba(0, 201, 167, 0.08);
    border-color: rgba(0, 201, 167, 0.2);
    color: var(--accent);
  }

  .sidebar-btn.danger.active {
    background: rgba(255, 107, 107, 0.08);
    border-color: rgba(255, 107, 107, 0.2);
    color: var(--danger);
  }

  .settings-content {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--space-6);
    box-shadow: 0 4px 30px rgba(0, 0, 0, 0.2);
    backdrop-filter: blur(10px);
    -webkit-backdrop-filter: blur(10px);
  }

  .tab-content {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }

  .content-title {
    font-size: var(--text-lg);
    font-weight: 600;
    color: var(--text-primary);
    margin-bottom: var(--space-1);
  }

  .content-desc {
    font-size: var(--text-sm);
    color: var(--text-muted);
    line-height: 1.5;
    margin-bottom: var(--space-4);
  }

  .form-group {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }

  .field-label {
    font-size: var(--text-xs);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    font-weight: 600;
    color: var(--text-secondary);
  }

  .field-hint {
    font-size: var(--text-xs);
    color: var(--text-muted);
  }

  .input-premium {
    background: rgba(255, 255, 255, 0.02);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: 10px 14px;
    font-size: var(--text-sm);
    color: var(--text-primary);
    transition: all 0.2s ease;
  }

  .input-premium:focus {
    background: rgba(255, 255, 255, 0.04);
    border-color: var(--accent);
    box-shadow: 0 0 0 2px rgba(0, 201, 167, 0.15);
    outline: none;
  }

  .toggle-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }

  .toggle-card {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-4);
    padding: var(--space-4);
    background: rgba(255, 255, 255, 0.01);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    transition: all 0.2s ease;
  }

  .toggle-card:hover {
    background: rgba(255, 255, 255, 0.02);
    border-color: rgba(255, 255, 255, 0.1);
  }

  .toggle-card-info {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .toggle-card-title {
    font-size: var(--text-sm);
    font-weight: 500;
    color: var(--text-primary);
  }

  .toggle-card-desc {
    font-size: var(--text-xs);
    color: var(--text-muted);
  }

  .bg-highlight {
    background: rgba(0, 201, 167, 0.02);
    border-color: rgba(0, 201, 167, 0.15);
  }

  .bg-highlight:hover {
    background: rgba(0, 201, 167, 0.04);
    border-color: rgba(0, 201, 167, 0.25);
  }

  .command-tags {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    padding: var(--space-3);
    background: rgba(0, 0, 0, 0.15);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    margin-bottom: var(--space-2);
    max-height: 200px;
    overflow-y: auto;
  }

  .command-tag {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    background: rgba(255, 255, 255, 0.06);
    border: 1px solid rgba(255, 255, 255, 0.08);
    color: var(--text-primary);
    padding: 3px 8px;
    border-radius: var(--radius-sm);
    font-family: monospace;
    font-size: var(--text-xs);
  }

  .tag-remove {
    background: none;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    font-size: var(--text-sm);
    padding: 0 2px;
    line-height: 1;
  }

  .tag-remove:hover {
    color: var(--danger);
  }

  .add-command-row {
    display: flex;
    gap: var(--space-2);
  }

  .input-sm {
    padding: 8px 12px;
    font-size: var(--text-xs);
  }

  .danger-card {
    border-color: rgba(255, 107, 107, 0.2);
    background: rgba(255, 107, 107, 0.01);
  }

  .danger-card:hover {
    border-color: rgba(255, 107, 107, 0.35);
    background: rgba(255, 107, 107, 0.02);
  }

  .danger-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-4);
    width: 100%;
  }

  .danger-card-title {
    font-size: var(--text-sm);
    font-weight: 500;
    color: var(--text-primary);
  }

  .danger-card-desc {
    font-size: var(--text-xs);
    color: var(--text-muted);
    margin-top: 4px;
  }

  .settings-footer {
    display: flex;
    justify-content: flex-end;
    margin-top: var(--space-6);
    padding-top: var(--space-4);
    border-top: 1px solid var(--border);
  }

  .btn-save {
    padding: 10px 24px;
    font-size: var(--text-sm);
    font-weight: 500;
  }

  .btn-icon {
    font-size: 16px;
  }

  .btn-text {
    font-size: var(--text-sm);
  }

  .saved-banner {
    margin: 0;
    padding: 6px 16px;
  }

  /* GPU tab styling */
  .select-wrapper {
    position: relative;
    width: 100%;
  }
  .select-wrapper select {
    appearance: none;
    background-image: url("data:image/svg+xml;charset=utf-8,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'%3E%3Cpath fill='none' stroke='%23ffffff' stroke-linecap='round' stroke-linejoin='round' stroke-width='2' d='m2 5 6 6 6-6'/%3E%3C/svg%3E");
    background-repeat: no-repeat;
    background-position: right 1rem center;
    background-size: 10px;
    padding-right: 2.5rem;
  }
  .gpu-connection-test-section {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    margin-top: var(--space-2);
    align-items: flex-start;
  }
  .btn-test-conn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: var(--space-3) var(--space-6);
  }
  .test-result-wrapper {
    width: 100%;
  }
  .test-result {
    padding: var(--space-4) var(--space-5);
    border-radius: var(--radius-md);
    font-size: var(--text-sm);
    line-height: 1.5;
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    border: 1px solid transparent;
  }
  .test-result.success {
    background: rgba(0, 201, 167, 0.08);
    border-color: rgba(0, 201, 167, 0.2);
    color: var(--text-primary);
  }
  .test-result.success .icon {
    color: var(--accent);
    margin-right: var(--space-2);
    font-weight: bold;
  }
  .test-result.error {
    background: rgba(255, 107, 107, 0.08);
    border-color: rgba(255, 107, 107, 0.2);
    color: var(--text-primary);
  }
  .test-result.error .icon {
    color: var(--danger);
    margin-right: var(--space-2);
    font-weight: bold;
  }
  .result-headline {
    font-size: var(--text-sm);
    font-weight: 600;
  }
  .result-details {
    font-size: var(--text-xs);
    color: var(--text-muted);
  }
  .result-details code {
    font-family: var(--font-mono);
    color: var(--text-secondary);
  }
  .badge {
    background: rgba(255, 255, 255, 0.05);
    padding: 2px 6px;
    border-radius: 4px;
    font-size: var(--text-xs);
  }

  /* Policy Engine CSS */
  .policies-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    margin-bottom: var(--space-4);
  }
  .policy-card {
    padding: var(--space-4);
    border: 1px solid var(--border);
    background: rgba(255, 255, 255, 0.01);
    transition: all 0.2s ease;
  }
  .policy-card:hover {
    border-color: rgba(255, 255, 255, 0.08);
    background: rgba(255, 255, 255, 0.02);
  }
  .policy-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    border-bottom: 1px solid rgba(255, 255, 255, 0.05);
    padding-bottom: var(--space-3);
    margin-bottom: var(--space-3);
  }
  .policy-info {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .policy-name {
    font-size: var(--text-base);
    font-weight: 600;
    color: var(--text-primary);
  }
  .policy-meta {
    font-size: var(--text-xs);
    color: var(--text-muted);
  }
  .badge-action {
    background: rgba(255, 107, 107, 0.15);
    color: var(--danger);
    text-transform: uppercase;
  }
  .badge-class {
    background: rgba(0, 201, 167, 0.15);
    color: var(--accent);
    text-transform: uppercase;
  }
  .policy-actions {
    display: flex;
    align-items: center;
    gap: var(--space-3);
  }
  .toggle-sm {
    transform: scale(0.85);
  }
  .btn-delete {
    background: rgba(255, 107, 107, 0.1);
    color: var(--danger);
    border-color: rgba(255, 107, 107, 0.2);
  }
  .btn-delete:hover {
    background: rgba(255, 107, 107, 0.2);
    border-color: rgba(255, 107, 107, 0.4);
  }
  .policy-rules {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    font-size: var(--text-sm);
  }
  .rules-label {
    font-weight: 500;
    color: var(--text-secondary);
  }
  .rules-list {
    margin: 0;
    padding-left: var(--space-5);
    list-style-type: square;
    color: var(--text-muted);
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .scope-tag {
    background: rgba(255, 255, 255, 0.05);
    padding: 2px 6px;
    border-radius: 4px;
    font-size: var(--text-xs);
    font-weight: 500;
    color: var(--text-secondary);
    margin-right: 6px;
  }
  .op-tag {
    font-family: monospace;
    font-weight: bold;
    color: var(--accent);
  }
  .policy-rules code {
    background: rgba(255, 255, 255, 0.03);
    padding: 2px 4px;
    border-radius: 4px;
  }
  .policy-message {
    margin-top: var(--space-3);
    font-size: var(--text-xs);
    color: var(--text-muted);
    background: rgba(0, 0, 0, 0.1);
    padding: var(--space-2) var(--space-3);
    border-radius: var(--radius-sm);
    border-left: 2px solid var(--accent);
  }
  .message-label {
    font-weight: 600;
    color: var(--text-secondary);
    margin-right: 4px;
  }
  .empty-state {
    padding: var(--space-6);
    text-align: center;
    border: 1px dashed var(--border);
    border-radius: var(--radius-md);
    background: rgba(255, 255, 255, 0.01);
  }
  .form-row-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-4);
  }
  .rule-edit-row {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    margin-bottom: var(--space-2);
  }
  .rule-edit-row .select-wrapper {
    flex: 1;
  }
  .rule-val-input {
    width: 90px;
    text-align: center;
  }
  .btn-remove-rule {
    padding: 6px 10px;
    color: var(--danger);
    background: rgba(255, 107, 107, 0.1);
    border-color: rgba(255, 107, 107, 0.2);
  }
  .btn-remove-rule:hover {
    background: rgba(255, 107, 107, 0.25);
  }
  .rules-editor {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    margin-top: var(--space-3);
    border-top: 1px solid rgba(255, 255, 255, 0.05);
    padding-top: var(--space-4);
  }
  .form-actions-row {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-3);
    margin-top: var(--space-5);
    border-top: 1px solid rgba(255, 255, 255, 0.05);
    padding-top: var(--space-4);
  }
  .btn-add-policy {
    align-self: flex-start;
  }

  @media (max-width: 640px) {
    .settings-wrapper {
      grid-template-columns: 1fr;
      gap: var(--space-4);
    }
  }
</style>
