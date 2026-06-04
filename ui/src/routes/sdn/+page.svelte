<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { appConfig } from '$lib/stores/cluster';
  
  interface NodeStatus {
    name: string;
    online: boolean;
    last_seen: string;
  }

  interface Segment {
    name: string;
    description: string;
    color: string;
    nodes: string[];
    internet: string;
    inter_segment: string;
    bandwidth_limit?: string;
  }

  interface Route {
    dst: string;
    via: string;
    metric: number;
  }

  interface Config {
    name: string;
    version: string;
    segments: Segment[];
    routes: Route[];
  }

  interface FlowRecord {
    src_ip: string;
    dst_ip: string;
    src_port: number;
    dst_port: number;
    proto: string;
    bytes_trans: number;
    packets_trans: number;
    policy_match: string;
    last_seen: string;
  }

  let activeVersion = 0;
  let activeHash = 'none';
  let iface = 'none';
  let lastError = '';
  let isCoordinator = false;
  let nodes: Record<string, NodeStatus> = {};
  let config: Config | null = null;
  let rulesDump = '';
  
  let yamlInput = '';
  let applyStatus = '';
  let applyError = '';

  let flows: FlowRecord[] = [];
  
  let statusInterval: any;
  let telemetryInterval: any;

  async function fetchStatus() {
    try {
      const res = await fetch('/api/sdn/status');
      if (res.ok) {
        const data = await res.json();
        activeVersion = data.active_version || 0;
        activeHash = data.active_hash || 'none';
        iface = data.interface || 'none';
        lastError = data.last_error || '';
        isCoordinator = data.is_coordinator || false;
        nodes = data.nodes || {};
        config = data.config || null;
        rulesDump = data.rules_dump || '';
        
        // Populate editor with active config if editor is empty
        if (!yamlInput && data.config) {
          yamlInput = generateDefaultYAML(data.config);
        }
      }
    } catch (e) {
      console.error(e);
    }
  }

  async function fetchTelemetry() {
    try {
      const res = await fetch('/api/sdn/telemetry');
      if (res.ok) {
        flows = await res.json();
        // Sort flows by last seen descending
        flows.sort((a, b) => new Date(b.last_seen).getTime() - new Date(a.last_seen).getTime());
      }
    } catch (e) {
      console.error(e);
    }
  }

  async function handleApply() {
    applyStatus = 'Applying...';
    applyError = '';
    try {
      const res = await fetch('/api/sdn/apply', {
        method: 'POST',
        headers: { 'Content-Type': 'application/yaml' },
        body: yamlInput
      });
      if (res.ok) {
        applyStatus = 'Successfully applied topology!';
        setTimeout(() => applyStatus = '', 4000);
        await fetchStatus();
      } else {
        const text = await res.text();
        applyError = text || 'Failed to apply topology';
        applyStatus = '';
      }
    } catch (e: any) {
      applyError = e.message || 'Error occurred';
      applyStatus = '';
    }
  }

  async function handleRollback() {
    if (!confirm('Are you sure you want to rollback to the previous configuration?')) return;
    try {
      const res = await fetch('/api/sdn/rollback', { method: 'POST' });
      if (res.ok) {
        alert('Rolled back successfully!');
        await fetchStatus();
      } else {
        const text = await res.text();
        alert('Rollback failed: ' + text);
      }
    } catch (e: any) {
      alert('Error: ' + e.message);
    }
  }

  function generateDefaultYAML(c: Config): string {
    // Generate simple YAML representation
    let yaml = `version: "1"\nname: "${c.name || 'home-lab'}"\n\nsegments:\n`;
    if (c.segments) {
      c.segments.forEach(s => {
        yaml += `  - name: ${s.name}\n`;
        yaml += `    description: "${s.description || ''}"\n`;
        yaml += `    color: "${s.color || '#00C9A7'}"\n`;
        yaml += `    nodes: ${JSON.stringify(s.nodes || [])}\n`;
        yaml += `    internet: ${s.internet || 'allow'}\n`;
        yaml += `    inter_segment: ${s.inter_segment || 'allow'}\n`;
        if (s.bandwidth_limit) {
          yaml += `    bandwidth_limit: "${s.bandwidth_limit}"\n`;
        }
        yaml += `\n`;
      });
    }
    return yaml;
  }

  // Prepopulate standard starter template
  function loadStarterTemplate() {
    yamlInput = `# ~/.openfabric/network.yaml
# Version-controlled. Deployable in seconds.

version: "1"
name: "home-lab"

segments:
  - name: trusted
    description: "Main devices - full cluster access"
    color: "#00C9A7"
    nodes: ["macbook", "desktop"]
    internet: allow
    inter_segment: allow

  - name: iot
    description: "Smart home devices - isolated"
    color: "#F59E0B"
    nodes: ["pi-sensor-1"]
    internet: allow
    inter_segment: deny
    cluster_access: deny

policies:
  - name: "block-telemetry"
    description: "Block Microsoft telemetry"
    match:
      dst_host:
        - "telemetry.microsoft.com"
        - "vortex.data.microsoft.com"
    action: deny
    apply_to: ["trusted", "iot"]
`;
  }

  onMount(() => {
    fetchStatus();
    fetchTelemetry();
    statusInterval = setInterval(fetchStatus, 5000);
    telemetryInterval = setInterval(fetchTelemetry, 3000);
  });

  onDestroy(() => {
    clearInterval(statusInterval);
    clearInterval(telemetryInterval);
  });
</script>

<svelte:head>
  <title>SDN - {$appConfig.project_name}</title>
  <meta name="description" content="Translate declarative topology declarations into kernel rules across all active devices." />
</svelte:head>

<div class="sdn-container">
  <div class="header-row">
    <div>
      <h1>Fabric Software Defined Networking</h1>
      <p class="subtitle text-secondary">Translate declarative topology declarations into kernel rules across all active devices.</p>
    </div>
    
    <div class="badge-row">
      {#if isCoordinator}
        <span class="badge badge-accent">Control Plane Coordinator</span>
      {:else}
        <span class="badge badge-warning">SDN Worker Node</span>
      {/if}
      <span class="badge badge-info">Interface: {iface}</span>
    </div>
  </div>

  {#if lastError}
    <div class="banner banner-error" role="alert">
      <strong>Rule application error:</strong> {lastError}
    </div>
  {/if}

  <div class="grid grid-2">
    <!-- Left column: Editor & Topology -->
    <div class="card sdn-editor-card">
      <div class="card-header">
        <h3>Declarative Network YAML</h3>
        <div class="btn-group">
          <button class="btn btn-secondary btn-sm" on:click={loadStarterTemplate}>Load Template</button>
          {#if isCoordinator}
            <button class="btn btn-secondary btn-sm" on:click={handleRollback} disabled={activeVersion <= 1}>Rollback</button>
          {/if}
        </div>
      </div>

      <div class="editor-container">
        <textarea
          bind:value={yamlInput}
          placeholder="# Define your topology YAML here..."
          spellcheck="false"
          id="sdn-yaml-input"
        ></textarea>
      </div>

      <div class="action-footer">
        {#if applyStatus}
          <span class="text-accent status-msg">{applyStatus}</span>
        {/if}
        {#if applyError}
          <span class="text-error status-msg">{applyError}</span>
        {/if}
        {#if isCoordinator}
          <button class="btn btn-accent" on:click={handleApply}>Deploy Topology</button>
        {:else}
          <button class="btn btn-accent" disabled title="Only the coordinator can apply network updates">Deploy Topology (Locked)</button>
        {/if}
      </div>
    </div>

    <!-- Right column: Rules and Sync Telemetry -->
    <div class="card flex-column">
      <div class="card-header">
        <h3>Cluster Sync Status</h3>
        <span class="text-secondary text-xs">Version: {activeVersion} (Hash: {activeHash.slice(0, 8)})</span>
      </div>

      <div class="nodes-list">
        {#each Object.entries(nodes) as [nid, node]}
          <div class="node-item">
            <div class="node-info">
              <strong>{node.name || nid.slice(0, 12)}</strong>
              <span class="text-muted text-xs">{nid}</span>
            </div>
            <div class="sync-indicator">
              {#if node.online}
                <span class="dot online"></span>
                <span class="text-accent text-xs">Synchronized</span>
              {:else}
                <span class="dot offline"></span>
                <span class="text-secondary text-xs">Offline</span>
              {/if}
            </div>
          </div>
        {/each}
      </div>

      <hr class="divider" />

      <div class="card-header">
        <h3>Local Kernel Firewall Rules</h3>
      </div>
      <div class="rules-dump">
        <pre>{rulesDump || 'No rules loaded in platform interface.'}</pre>
      </div>
    </div>
  </div>

  <!-- Telemetry Row -->
  <div class="card sdn-telemetry-card">
    <div class="card-header">
      <h3>Real-Time Flow Telemetry</h3>
      <span class="badge badge-accent">Exporting inline pf/tc metrics</span>
    </div>

    <div class="table-container">
      <table>
        <thead>
          <tr>
            <th>Source</th>
            <th>Destination</th>
            <th>Protocol</th>
            <th>Volume</th>
            <th>Matched Policy</th>
            <th>Last Packets</th>
          </tr>
        </thead>
        <tbody>
          {#each flows.slice(0, 10) as flow}
            <tr>
              <td><span class="ip-addr src-ip">{flow.src_ip}</span></td>
              <td><span class="ip-addr dst-ip">{flow.dst_ip}:{flow.dst_port}</span></td>
              <td><span class="proto-tag">{flow.proto.toUpperCase()}</span></td>
              <td>{(flow.bytes_trans / 1024).toFixed(1)} KB ({flow.packets_trans} pkts)</td>
              <td>
                <span class="policy-tag" class:deny={flow.policy_match.includes('block') || flow.policy_match.includes('deny')}>
                  {flow.policy_match}
                </span>
              </td>
              <td class="text-secondary">{new Date(flow.last_seen).toLocaleTimeString()}</td>
            </tr>
          {:else}
            <tr>
              <td colspan="6" class="text-center text-muted">No network connection flows recorded yet.</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>
</div>

<style>
  .sdn-container {
    display: flex;
    flex-direction: column;
    gap: var(--space-6);
  }

  .header-row {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: var(--space-4);
  }

  .badge-row {
    display: flex;
    gap: var(--space-2);
  }

  .grid {
    display: grid;
    gap: var(--space-6);
  }

  .grid-2 {
    grid-template-columns: 1.2fr 0.8fr;
  }

  .flex-column {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }

  .card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    width: 100%;
  }

  .sdn-editor-card {
    display: flex;
    flex-direction: column;
    min-height: 480px;
  }

  .editor-container {
    flex: 1;
    margin: var(--space-4) 0;
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    overflow: hidden;
  }

  textarea {
    width: 100%;
    height: 100%;
    min-height: 300px;
    background: #0f172a;
    color: #f8fafc;
    font-family: var(--font-mono);
    font-size: var(--text-sm);
    padding: var(--space-4);
    border: none;
    resize: none;
    outline: none;
  }

  .action-footer {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: var(--space-4);
  }

  .status-msg {
    font-size: var(--text-sm);
    font-weight: 500;
  }

  .nodes-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }

  .node-item {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: var(--space-3) var(--space-4);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
  }

  .node-info {
    display: flex;
    flex-direction: column;
  }

  .sync-indicator {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }

  .divider {
    border: 0;
    border-top: 1px solid var(--border);
    margin: var(--space-2) 0;
  }

  .rules-dump {
    flex: 1;
    background: #0f172a;
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-4);
    max-height: 250px;
    overflow-y: auto;
  }

  pre {
    color: #38bdf8;
    font-family: var(--font-mono);
    font-size: var(--text-xs);
    margin: 0;
    white-space: pre-wrap;
  }

  .table-container {
    width: 100%;
    overflow-x: auto;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--text-sm);
  }

  th, td {
    padding: var(--space-3) var(--space-4);
    text-align: left;
    border-bottom: 1px solid var(--border);
  }

  th {
    font-weight: 600;
    color: var(--text-secondary);
    background: var(--bg-tertiary);
  }

  .ip-addr {
    font-family: var(--font-mono);
    font-weight: 500;
  }

  .src-ip { color: #f43f5e; }
  .dst-ip { color: #10b981; }

  .proto-tag {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    padding: 2px 6px;
    border-radius: 4px;
    font-size: var(--text-xs);
    font-family: var(--font-mono);
    font-weight: 600;
  }

  .policy-tag {
    background: #00c9a722;
    border: 1px solid #00c9a755;
    color: var(--accent);
    padding: 2px 6px;
    border-radius: 4px;
    font-size: var(--text-xs);
  }

  .policy-tag.deny {
    background: #ef444422;
    border: 1px solid #ef444455;
    color: #f43f5e;
  }

  @media (max-width: 1024px) {
    .grid-2 {
      grid-template-columns: 1fr;
    }
  }
</style>
