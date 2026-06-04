<script lang="ts">
  import { onMount } from 'svelte';
  import NodeCard from '$lib/components/NodeCard.svelte';
  import AddDeviceCard from '$lib/components/AddDeviceCard.svelte';
  import JoinClusterCard from '$lib/components/JoinClusterCard.svelte';
  import { dialog } from '$lib/stores/dialog';
  import { nodes, onlineNodes, appConfig, timeAgo } from '$lib/stores/cluster';
  import {
    wolDevices,
    discoveredDevices,
    scanning,
    wakingDevices,
    loadWOLDevices,
    registerWOLDevice,
    unregisterWOLDevice,
    wakeWOLDevice,
    scanWOLNetwork
  } from '$lib/stores/wol';

  let activeTab = 'topology'; // 'topology' | 'wol'

  // Form states
  let newName = '';
  let newMAC = '';
  let newBroadcastIP = '';
  let newLinkedNodeID = '';

  let formError = '';
  let formSuccess = '';
  let isSubmitting = false;

  // Scanner status
  let scanError = '';

  onMount(() => {
    loadWOLDevices();
  });

  async function handleRegister() {
    formError = '';
    formSuccess = '';
    
    if (!newName.trim() || !newMAC.trim()) {
      formError = 'Name and MAC Address are required.';
      return;
    }

    isSubmitting = true;
    try {
      await registerWOLDevice({
        name: newName,
        mac: newMAC,
        broadcast_ip: newBroadcastIP || undefined,
        linked_node_id: newLinkedNodeID || undefined
      });
      formSuccess = 'Device registered successfully!';
      // Reset form
      newName = '';
      newMAC = '';
      newBroadcastIP = '';
      newLinkedNodeID = '';
    } catch (err: any) {
      formError = err.message || 'Failed to register device.';
    } finally {
      isSubmitting = false;
    }
  }

  async function handleUnregister(mac: string) {
    const confirmed = await dialog.confirm(
      'Are you sure you want to unregister this device?',
      'Unregister Device',
      '🔌',
      'Remove',
      'Cancel',
      true
    );
    if (!confirmed) return;
    try {
      await unregisterWOLDevice(mac);
    } catch (err: any) {
      await dialog.alert(err.message || 'Failed to unregister', 'Error', '❌');
    }
  }

  async function handleScan() {
    scanError = '';
    try {
      await scanWOLNetwork();
    } catch (err: any) {
      scanError = err.message || 'Network scan failed.';
    }
  }

  function prefillRegistration(discovered: any) {
    newName = `Discovered Node (${discovered.ip})`;
    newMAC = discovered.mac;
    newLinkedNodeID = discovered.linked_node_id || '';
    // Scroll to form
    const formEl = document.getElementById('wol-register-form');
    if (formEl) {
      formEl.scrollIntoView({ behavior: 'smooth' });
    }
  }
</script>

<svelte:head>
  <title>Devices - {$appConfig.project_name}</title>
  <meta name="description" content="All devices in your {$appConfig.project_name} cluster" />
</svelte:head>

<div class="devices-page animate-fade-in">
  <!-- Header Section -->
  <div class="section-header">
    <div>
      <h1>Devices</h1>
      <p class="text-secondary" style="margin-top: 4px; font-size: var(--text-sm)">
        {$onlineNodes.length} online · {$nodes.length - $onlineNodes.length} offline
      </p>
    </div>
    <div class="header-badges">
      <span class="badge badge-online">{$onlineNodes.length} online</span>
      {#if $nodes.length > $onlineNodes.length}
        <span class="badge badge-offline">{$nodes.length - $onlineNodes.length} offline</span>
      {/if}
    </div>
  </div>

  <!-- Tab Switcher -->
  <div class="tabs-nav-container">
    <div class="tabs-nav">
      <button 
        class="tab-btn" 
        class:active={activeTab === 'topology'} 
        on:click={() => activeTab = 'topology'}
      >
        <span class="tab-icon">🌐</span> Cluster Topology
      </button>
      <button 
        class="tab-btn" 
        class:active={activeTab === 'wol'} 
        on:click={() => activeTab = 'wol'}
      >
        <span class="tab-icon">⚡</span> Wake-on-LAN (WoL)
      </button>
    </div>
  </div>

  {#if activeTab === 'topology'}
    <!-- Onboarding & Join Controls -->
    <div class="devices-dashboard-grid">
      <AddDeviceCard />
      <JoinClusterCard />
    </div>

    <div class="divider"></div>

    <div class="section-header">
      <h2>Cluster Topology</h2>
    </div>

    {#if $nodes.length === 0}
      <div class="empty-state card">
        <div class="empty-icon">📡</div>
        <h3>No devices in cluster</h3>
        <p>Install OpenFabric on any device on the same Wi-Fi network. It will appear here automatically - no configuration needed.</p>
      </div>
    {:else}
      <div class="devices-grid">
        {#each $nodes as node (node.id)}
          <NodeCard {node} />
        {/each}
      </div>
    {/if}
  {:else}
    <!-- Wake-on-LAN View -->
    <div class="wol-dashboard">
      
      <!-- Registered Devices List -->
      <div class="wol-section card">
        <div class="card-header">
          <h3>Registered WoL Devices</h3>
          <p class="card-subtitle">Devices configured for manual or automatic cluster waking</p>
        </div>
        
        {#if $wolDevices.length === 0}
          <div class="wol-empty">
            <span class="empty-ico">🔌</span>
            <p>No devices registered for Wake-on-LAN yet.</p>
            <p class="text-secondary" style="font-size: var(--text-sm)">Use the ARP scanner below or the manual form to register a device.</p>
          </div>
        {:else}
          <div class="table-container">
            <table class="wol-table">
              <thead>
                <tr>
                  <th>Device Name</th>
                  <th>MAC Address</th>
                  <th>Last IP</th>
                  <th>Linked Cluster Node</th>
                  <th>Last Woken</th>
                  <th>Wake Count</th>
                  <th style="text-align: right">Actions</th>
                </tr>
              </thead>
              <tbody>
                {#each $wolDevices as dev (dev.mac)}
                  {@const node = $nodes.find(n => n.id === dev.linked_node_id)}
                  {@const isOnline = node?.status === 'online'}
                  <tr>
                    <td>
                      <div class="dev-name-cell">
                        <span class="status-indicator" class:online={isOnline}></span>
                        <strong>{dev.name}</strong>
                      </div>
                    </td>
                    <td><code class="code-mac">{dev.mac}</code></td>
                    <td>{dev.last_ip || '-'}</td>
                    <td>
                      {#if node}
                        <span class="badge" class:badge-online={isOnline} class:badge-offline={!isOnline}>
                          {node.name} ({isOnline ? 'Online' : 'Offline'})
                        </span>
                      {:else}
                        <span class="badge badge-unlinked">Unlinked</span>
                      {/if}
                    </td>
                    <td>{dev.last_woken ? timeAgo(dev.last_woken) : 'Never'}</td>
                    <td style="font-variant-numeric: tabular-nums">{dev.wake_count}</td>
                    <td>
                      <div class="action-buttons">
                        <button 
                          class="action-btn wake-btn" 
                          disabled={$wakingDevices[dev.mac] || isOnline}
                          on:click={() => wakeWOLDevice(dev.mac)}
                        >
                          {$wakingDevices[dev.mac] ? '⚡ Waking...' : '⚡ Wake'}
                        </button>
                        <button 
                          class="action-btn delete-btn" 
                          on:click={() => handleUnregister(dev.mac)}
                        >
                          Remove
                        </button>
                      </div>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </div>

      <!-- Left-Right Grid for Form & Scan -->
      <div class="wol-management-grid">
        
        <!-- Manual Register Form -->
        <div id="wol-register-form" class="wol-section card">
          <div class="card-header">
            <h3>Register Device Manually</h3>
            <p class="card-subtitle">Add a device to the Wake-on-LAN registry</p>
          </div>
          
          <form on:submit|preventDefault={handleRegister} class="wol-form">
            {#if formError}
              <div class="alert alert-error">{formError}</div>
            {/if}
            {#if formSuccess}
              <div class="alert alert-success">{formSuccess}</div>
            {/if}

            <div class="form-group">
              <label for="dev-name">Device Name *</label>
              <input 
                type="text" 
                id="dev-name" 
                bind:value={newName} 
                placeholder="e.g. My Desktop Workstation" 
                required 
              />
            </div>

            <div class="form-group">
              <label for="dev-mac">MAC Address *</label>
              <input 
                type="text" 
                id="dev-mac" 
                bind:value={newMAC} 
                placeholder="e.g. 00:11:22:33:44:55" 
                required 
              />
            </div>

            <div class="form-group">
              <label for="dev-bcast">Directed Broadcast IP (Optional)</label>
              <input 
                type="text" 
                id="dev-bcast" 
                bind:value={newBroadcastIP} 
                placeholder="e.g. 192.168.1.255" 
              />
              <span class="field-hint">Auto-detected if left empty.</span>
            </div>

            <div class="form-group">
              <label for="dev-link">Link to Cluster Node</label>
              <select id="dev-link" bind:value={newLinkedNodeID}>
                <option value="">-- No Link (Standalone) --</option>
                {#each $nodes as n}
                  <option value={n.id}>{n.name} ({n.id.slice(0, 8)}... - {n.status})</option>
                {/each}
              </select>
              <span class="field-hint">Links device offline state to card Wake buttons and AutoWaker.</span>
            </div>

            <button type="submit" class="submit-btn" disabled={isSubmitting}>
              {isSubmitting ? 'Registering...' : 'Register Device'}
            </button>
          </form>
        </div>

        <!-- ARP Network Sweep Discovery -->
        <div class="wol-section card">
          <div class="card-header header-with-action">
            <div>
              <h3>Local Network Sweep</h3>
              <p class="card-subtitle">Scan your local network for active machines</p>
            </div>
            <button 
              class="scan-trigger-btn" 
              disabled={$scanning} 
              on:click={handleScan}
            >
              {$scanning ? '📡 Sweeping...' : '🔍 Scan Network'}
            </button>
          </div>

          {#if $scanning}
            <div class="scan-loading-state">
              <div class="radar-ping">
                <div class="ring ring-1"></div>
                <div class="ring ring-2"></div>
                <div class="ring ring-3"></div>
              </div>
              <p>Sending fast sweeps across active subnets...</p>
              <p class="text-secondary" style="font-size: var(--text-sm)">This will trigger ARP table updates on your system.</p>
            </div>
          {:else if scanError}
            <div class="alert alert-error">{scanError}</div>
          {:else if $discoveredDevices.length === 0}
            <div class="scan-empty">
              <span class="scan-ico">🔍</span>
              <p>Scan has not been run or no active devices were found.</p>
              <p class="text-secondary" style="font-size: var(--text-sm)">Click "Scan Network" to sweep local IP prefixes.</p>
            </div>
          {:else}
            <div class="scan-results-container">
              <div class="scan-header-summary">
                Discovered {$discoveredDevices.length} devices
              </div>
              <div class="scan-list">
                {#each $discoveredDevices as dev}
                  <div class="discovered-item">
                    <div class="disc-details">
                      <div class="disc-ip-line">
                        <strong class="ip-addr">{dev.ip}</strong>
                        <span class="iface-badge">{dev.interface}</span>
                      </div>
                      <code class="mac-addr">{dev.mac}</code>
                      {#if dev.linked_node_id}
                        {@const matchedNode = $nodes.find(n => n.id === dev.linked_node_id)}
                        <span class="match-badge">Matched: {matchedNode?.name || 'Cluster Node'}</span>
                      {/if}
                    </div>
                    <button 
                      class="quick-reg-btn"
                      on:click={() => prefillRegistration(dev)}
                    >
                      Quick Add
                    </button>
                  </div>
                {/each}
              </div>
            </div>
          {/if}
        </div>

      </div>
    </div>
  {/if}
</div>

<style>
  /* Base Layout */
  .devices-page { display: flex; flex-direction: column; gap: var(--space-6); }
  .header-badges { display: flex; gap: var(--space-2); align-items: center; }
  
  .devices-dashboard-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: var(--space-6);
  }

  .devices-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
    gap: var(--space-4);
  }

  /* Tabs Nav */
  .tabs-nav-container {
    border-bottom: 1px solid rgba(255, 255, 255, 0.08);
    margin-bottom: var(--space-2);
  }
  .tabs-nav {
    display: flex;
    gap: var(--space-4);
  }
  .tab-btn {
    background: transparent;
    border: none;
    border-bottom: 2px solid transparent;
    color: var(--text-secondary);
    padding: var(--space-3) var(--space-4);
    font-weight: 600;
    cursor: pointer;
    transition: all 0.2s ease;
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  .tab-btn:hover {
    color: var(--text-primary);
  }
  .tab-btn.active {
    color: #38bdf8;
    border-bottom-color: #38bdf8;
  }
  .tab-icon {
    font-size: 16px;
  }

  /* WoL Layout */
  .wol-dashboard {
    display: flex;
    flex-direction: column;
    gap: var(--space-6);
  }
  .wol-section {
    background: linear-gradient(145deg, rgba(18,26,44,0.6) 0%, rgba(12,18,32,0.45) 100%);
    border: 1px solid rgba(255,255,255,0.06);
    backdrop-filter: blur(20px);
    border-radius: 16px;
    padding: 24px;
    box-shadow: 0 4px 30px rgba(0,0,0,0.3);
  }
  .card-header {
    margin-bottom: 20px;
  }
  .card-subtitle {
    font-size: var(--text-sm);
    color: var(--text-secondary);
    margin-top: 4px;
  }
  
  /* Tables */
  .table-container {
    overflow-x: auto;
    border: 1px solid rgba(255, 255, 255, 0.05);
    border-radius: 12px;
  }
  .wol-table {
    width: 100%;
    border-collapse: collapse;
    text-align: left;
    font-size: var(--text-sm);
  }
  .wol-table th {
    background: rgba(255, 255, 255, 0.02);
    border-bottom: 1px solid rgba(255, 255, 255, 0.06);
    padding: 14px 16px;
    color: var(--text-secondary);
    font-weight: 600;
  }
  .wol-table td {
    padding: 14px 16px;
    border-bottom: 1px solid rgba(255, 255, 255, 0.04);
  }
  .wol-table tr:hover {
    background: rgba(255, 255, 255, 0.01);
  }
  .dev-name-cell {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .status-indicator {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: #ef4444; /* offline red */
    box-shadow: 0 0 6px rgba(239, 68, 68, 0.6);
  }
  .status-indicator.online {
    background: #00c9a7;
    box-shadow: 0 0 8px rgba(0, 201, 167, 0.7);
  }
  .code-mac {
    font-family: 'JetBrains Mono', monospace;
    font-size: 13px;
    background: rgba(255, 255, 255, 0.03);
    padding: 2px 6px;
    border-radius: 4px;
    color: #e2e8f0;
  }

  /* Badges */
  .badge-unlinked {
    background: rgba(255, 255, 255, 0.04);
    border-color: rgba(255, 255, 255, 0.08);
    color: var(--text-secondary);
  }

  /* Buttons */
  .action-buttons {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
  }
  .action-btn {
    border: 1px solid transparent;
    padding: 6px 12px;
    font-size: 11px;
    font-weight: 700;
    border-radius: 6px;
    cursor: pointer;
    transition: all 0.2s ease;
    white-space: nowrap;
  }
  .wake-btn {
    background: rgba(0, 201, 167, 0.15);
    border-color: rgba(0, 201, 167, 0.3);
    color: #00c9a7;
  }
  .wake-btn:hover:not(:disabled) {
    background: rgba(0, 201, 167, 0.25);
    border-color: #00c9a7;
    box-shadow: 0 0 10px rgba(0, 201, 167, 0.2);
  }
  .wake-btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
    background: transparent;
    border-color: rgba(255,255,255,0.06);
    color: rgba(255,255,255,0.25);
  }
  .delete-btn {
    background: transparent;
    border-color: rgba(255, 77, 109, 0.15);
    color: rgba(255, 77, 109, 0.6);
  }
  .delete-btn:hover {
    background: rgba(255, 77, 109, 0.1);
    border-color: #ff4d6d;
    color: #ff4d6d;
  }

  /* Management Grid */
  .wol-management-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-6);
  }
  @media (max-width: 900px) {
    .wol-management-grid {
      grid-template-columns: 1fr;
    }
  }

  /* Form */
  .wol-form {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .form-group {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .form-group label {
    font-size: 12px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-secondary);
  }
  .form-group input, .form-group select {
    background: rgba(0, 0, 0, 0.2);
    border: 1px solid rgba(255, 255, 255, 0.08);
    color: var(--text-primary);
    padding: 10px 14px;
    border-radius: 8px;
    font-size: var(--text-sm);
    transition: all 0.2s ease;
  }
  .form-group input:focus, .form-group select:focus {
    border-color: #38bdf8;
    outline: none;
    box-shadow: 0 0 10px rgba(56, 189, 248, 0.15);
  }
  .field-hint {
    font-size: 10px;
    color: rgba(255, 255, 255, 0.35);
  }
  .submit-btn {
    background: #38bdf8;
    border: none;
    color: #0c1220;
    padding: 12px;
    font-weight: 700;
    border-radius: 8px;
    cursor: pointer;
    transition: all 0.2s ease;
    margin-top: 8px;
  }
  .submit-btn:hover:not(:disabled) {
    background: #7dd3fc;
    box-shadow: 0 0 16px rgba(56, 189, 248, 0.3);
  }
  .submit-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  /* Alerts */
  .alert {
    padding: 10px 14px;
    border-radius: 8px;
    font-size: var(--text-sm);
    font-weight: 500;
  }
  .alert-error {
    background: rgba(239, 68, 68, 0.1);
    border: 1px solid rgba(239, 68, 68, 0.25);
    color: #f87171;
  }
  .alert-success {
    background: rgba(16, 185, 129, 0.1);
    border: 1px solid rgba(16, 185, 129, 0.25);
    color: #34d399;
  }

  /* Scanner card */
  .header-with-action {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 12px;
  }
  .scan-trigger-btn {
    background: rgba(56, 189, 248, 0.12);
    border: 1px solid rgba(56, 189, 248, 0.25);
    color: #38bdf8;
    padding: 8px 16px;
    font-weight: 700;
    font-size: 12px;
    border-radius: 8px;
    cursor: pointer;
    transition: all 0.2s ease;
    white-space: nowrap;
  }
  .scan-trigger-btn:hover:not(:disabled) {
    background: rgba(56, 189, 248, 0.22);
    border-color: #38bdf8;
    box-shadow: 0 0 12px rgba(56, 189, 248, 0.2);
  }
  .scan-trigger-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .scan-empty, .wol-empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    text-align: center;
    padding: 40px 20px;
    gap: 10px;
    color: var(--text-secondary);
  }
  .scan-ico, .empty-ico {
    font-size: 32px;
    opacity: 0.6;
    margin-bottom: 8px;
  }

  /* Scanner Loading Radar */
  .scan-loading-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    padding: 40px 20px;
    gap: 14px;
    color: var(--text-secondary);
  }
  .radar-ping {
    position: relative;
    width: 60px;
    height: 60px;
    display: flex;
    align-items: center;
    justify-content: center;
    margin-bottom: 10px;
  }
  .ring {
    position: absolute;
    width: 100%;
    height: 100%;
    border: 2px solid #38bdf8;
    border-radius: 50%;
    animation: radar-pulse 2s infinite linear;
    opacity: 0;
  }
  .ring-1 { animation-delay: 0s; }
  .ring-2 { animation-delay: 0.66s; }
  .ring-3 { animation-delay: 1.33s; }

  @keyframes radar-pulse {
    0% { transform: scale(0.2); opacity: 0.8; }
    100% { transform: scale(1.2); opacity: 0; }
  }

  /* Scan Results */
  .scan-results-container {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .scan-header-summary {
    font-size: 11px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-secondary);
    border-bottom: 1px solid rgba(255, 255, 255, 0.05);
    padding-bottom: 8px;
  }
  .scan-list {
    display: flex;
    flex-direction: column;
    gap: 10px;
    max-height: 380px;
    overflow-y: auto;
    padding-right: 6px;
  }
  /* Custom scrollbar for list */
  .scan-list::-webkit-scrollbar {
    width: 6px;
  }
  .scan-list::-webkit-scrollbar-track {
    background: rgba(0, 0, 0, 0.1);
  }
  .scan-list::-webkit-scrollbar-thumb {
    background: rgba(255, 255, 255, 0.08);
    border-radius: 3px;
  }

  .discovered-item {
    background: rgba(255, 255, 255, 0.02);
    border: 1px solid rgba(255, 255, 255, 0.04);
    border-radius: 10px;
    padding: 12px 16px;
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 12px;
    transition: all 0.2s ease;
  }
  .discovered-item:hover {
    background: rgba(255, 255, 255, 0.04);
    border-color: rgba(56, 189, 248, 0.15);
  }
  .disc-details {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  .disc-ip-line {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .ip-addr {
    color: var(--text-primary);
  }
  .iface-badge {
    font-size: 8px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: #38bdf8;
    background: rgba(56, 189, 248, 0.1);
    padding: 1px 5px;
    border-radius: 4px;
  }
  .mac-addr {
    font-family: 'JetBrains Mono', monospace;
    font-size: 11px;
    color: var(--text-secondary);
  }
  .match-badge {
    font-size: 8px;
    font-weight: 700;
    color: #00c9a7;
    background: rgba(0, 201, 167, 0.1);
    align-self: flex-start;
    padding: 1px 5px;
    border-radius: 4px;
    margin-top: 2px;
  }
  .quick-reg-btn {
    background: rgba(255, 255, 255, 0.05);
    border: 1px solid rgba(255, 255, 255, 0.1);
    color: var(--text-primary);
    padding: 6px 12px;
    font-size: 11px;
    font-weight: 600;
    border-radius: 6px;
    cursor: pointer;
    transition: all 0.2s ease;
    white-space: nowrap;
  }
  .quick-reg-btn:hover {
    background: rgba(255, 255, 255, 0.1);
    border-color: rgba(255, 255, 255, 0.2);
  }
</style>
