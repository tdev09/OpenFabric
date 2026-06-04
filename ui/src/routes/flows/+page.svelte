<script lang="ts">
  import { onMount } from 'svelte';
  import { fade, slide } from 'svelte/transition';
  import { nodes, onlineNodes, appConfig } from '$lib/stores/cluster';
  import { dialog } from '$lib/stores/dialog';

  interface Step {
    id: string;
    type: 'shell' | 'ai' | 'save' | 'notify';
    command?: string;
    node?: string;
    model?: string;
    prompt?: string;
    use_brain?: boolean;
    save_to?: string;
    content?: string;
    path?: string;
    message?: string;
  }

  interface Flow {
    id: string;
    name: string;
    description?: string;
    enabled: boolean;
    trigger: {
      type: 'manual' | 'schedule' | 'file_added' | 'file_modified';
      cron?: string;
      pattern?: string;
    };
    steps: Step[];
  }

  interface StepResult {
    id: string;
    status: string;
    output: string;
    error?: string;
    started_at: string;
    finished_at: string;
  }

  interface FlowRun {
    id: string;
    flow_id: string;
    flow_name: string;
    status: 'running' | 'completed' | 'failed';
    trigger: string;
    steps: StepResult[];
    started_at: string;
    finished_at?: string;
    error?: string;
    variables?: any;
  }

  let flows: Flow[] = [];
  let activeFlow: Flow | null = null;
  let isEditing = false;
  let isCreating = false;
  let editorMode: 'simple' | 'advanced' = 'simple';
  let rawYaml = '';

  let runs: FlowRun[] = [];
  let selectedFlowForHistory: Flow | null = null;
  let showHistoryDrawer = false;
  let expandedStepResults: Record<string, boolean> = {};

  let toasts: { id: number; message: string }[] = [];
  let toastId = 0;

  let simpleForm = {
    name: '',
    description: '',
    enabled: true,
    trigger: {
      type: 'manual' as 'manual' | 'schedule' | 'file_added' | 'file_modified',
      cron: '',
      pattern: ''
    },
    steps: [] as Step[]
  };

  const templates = [
    {
      name: 'Daily Brief',
      description: 'Fetch daily summary using RAG and trigger notifications',
      enabled: true,
      trigger: { type: 'schedule' as const, cron: '0 9 * * *', pattern: '' },
      steps: [
        { id: 'get_brief', type: 'ai' as const, model: 'auto', prompt: "Summarize today's key logs and local notes in storage. What are the key tasks for today?", use_brain: true },
        { id: 'notify_brief', type: 'notify' as const, message: 'Daily Brief: {{steps.get_brief.output | truncate:120}}' },
        { id: 'save_brief', type: 'save' as const, save_to: 'storage://briefs/{{date}}.txt', content: "Daily Brief for {{date}}:\n\n{{steps.get_brief.output}}" }
      ]
    },
    {
      name: 'PDF Indexer',
      description: 'Watches for new PDFs in storage and indexes them automatically',
      enabled: true,
      trigger: { type: 'file_added' as const, cron: '', pattern: '*.pdf' },
      steps: [
        { id: 'log_arrival', type: 'shell' as const, command: 'echo "New PDF detected {{trigger.filename}}"' },
        { id: 'notify_indexing', type: 'notify' as const, message: 'Indexing new document: {{trigger.filename}}' },
        { id: 'summarize_doc', type: 'ai' as const, model: 'auto', prompt: 'Summarize the PDF document named {{trigger.filename}} which was just uploaded to storage.', use_brain: true },
        { id: 'save_summary', type: 'save' as const, save_to: 'storage://summaries/{{trigger.filename}}.txt', content: "Summary of {{trigger.filename}} (indexed {{datetime}}):\n\n{{steps.summarize_doc.output}}" }
      ]
    },
    {
      name: 'Weekly Backup',
      description: 'Scheduled weekly shell compression backup task',
      enabled: true,
      trigger: { type: 'schedule' as const, cron: '0 0 * * 0', pattern: '' },
      steps: [
        { id: 'run_compress', type: 'shell' as const, command: 'tar -czf backup-{{date}}.tar.gz ~/.openfabric/storage/' },
        { id: 'notify_backup', type: 'notify' as const, message: 'Weekly backup complete: backup-{{date}}.tar.gz created' }
      ]
    }
  ];

  let es: EventSource;

  onMount(() => {
    async function init() {
      await loadFlows();
    }
    init();
    
    // Connect to SSE for flow-specific notifications
    es = new EventSource('/api/events');
    es.addEventListener('flow_notification', (e: any) => {
      try {
        const data = JSON.parse(e.data);
        showToast(`🔔 ${data.message || 'Notification received'}`);
      } catch (err) {
        console.error('Failed to parse flow notification', err);
      }
    });

    es.addEventListener('storage_updated', (e: any) => {
      try {
        const data = JSON.parse(e.data);
        if (data.filename) {
          showToast(`📁 Shared Storage updated: ${data.filename}`);
        }
      } catch {}
    });

    return () => {
      if (es) es.close();
    };
  });

  function showToast(message: string) {
    const id = toastId++;
    toasts = [...toasts, { id, message }];
    setTimeout(() => {
      toasts = toasts.filter(t => t.id !== id);
    }, 6000);
  }

  async function loadFlows() {
    try {
      const res = await fetch('/api/flows');
      if (res.ok) {
        flows = await res.json();
      }
    } catch (e) {
      console.error('Failed to load flows', e);
    }
  }

  function jsonToYaml(obj: any): string {
    let lines: string[] = [];
    if (obj.name) lines.push(`name: ${obj.name}`);
    if (obj.description) lines.push(`description: ${obj.description}`);
    lines.push(`enabled: ${obj.enabled}`);
    if (obj.trigger) {
      lines.push(`trigger:`);
      lines.push(`  type: ${obj.trigger.type}`);
      if (obj.trigger.cron) lines.push(`  cron: "${obj.trigger.cron}"`);
      if (obj.trigger.pattern) lines.push(`  pattern: "${obj.trigger.pattern}"`);
    }
    if (obj.steps && obj.steps.length > 0) {
      lines.push(`steps:`);
      for (let step of obj.steps) {
        lines.push(`  - id: ${step.id}`);
        lines.push(`    type: ${step.type}`);
        if (step.type === 'shell') {
          if (step.command) lines.push(`    command: ${JSON.stringify(step.command)}`);
          if (step.node) lines.push(`    node: ${step.node}`);
        } else if (step.type === 'ai') {
          if (step.model) lines.push(`    model: ${step.model}`);
          if (step.prompt) lines.push(`    prompt: ${JSON.stringify(step.prompt)}`);
          if (step.use_brain !== undefined) lines.push(`    use_brain: ${step.use_brain}`);
          if (step.save_to) lines.push(`    save_to: ${JSON.stringify(step.save_to)}`);
        } else if (step.type === 'save') {
          if (step.content) lines.push(`    content: ${JSON.stringify(step.content)}`);
          if (step.save_to) lines.push(`    save_to: ${JSON.stringify(step.save_to)}`);
        } else if (step.type === 'notify') {
          if (step.message) lines.push(`    message: ${JSON.stringify(step.message)}`);
        }
      }
    }
    return lines.join('\n');
  }

  function yamlToJson(yamlStr: string): any {
    const lines = yamlStr.split('\n');
    const obj: any = { name: '', description: '', enabled: true, trigger: { type: 'manual' }, steps: [] };
    let currentStep: any = null;
    let inTrigger = false;
    let inSteps = false;

    for (let line of lines) {
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith('#')) continue;

      const indent = line.length - line.trimStart().length;

      if (indent === 0) {
        inTrigger = false;
        inSteps = false;
        if (trimmed.startsWith('name:')) {
          obj.name = trimmed.slice(5).trim();
        } else if (trimmed.startsWith('description:')) {
          obj.description = trimmed.slice(12).trim();
        } else if (trimmed.startsWith('enabled:')) {
          obj.enabled = trimmed.slice(8).trim() === 'true';
        } else if (trimmed.startsWith('trigger:')) {
          inTrigger = true;
        } else if (trimmed.startsWith('steps:')) {
          inSteps = true;
        }
      } else if (indent === 2 && inTrigger) {
        if (trimmed.startsWith('type:')) {
          obj.trigger.type = trimmed.slice(5).trim();
        } else if (trimmed.startsWith('cron:')) {
          obj.trigger.cron = trimmed.slice(5).trim().replace(/^['"]|['"]$/g, '');
        } else if (trimmed.startsWith('pattern:')) {
          obj.trigger.pattern = trimmed.slice(8).trim().replace(/^['"]|['"]$/g, '');
        }
      } else if (inSteps) {
        if (trimmed.startsWith('-')) {
          if (currentStep) obj.steps.push(currentStep);
          currentStep = { id: '', type: 'shell' };
          const rest = trimmed.slice(1).trim();
          parseStepLine(currentStep, rest);
        } else if (currentStep) {
          parseStepLine(currentStep, trimmed);
        }
      }
    }
    if (currentStep) obj.steps.push(currentStep);
    return obj;
  }

  function parseStepLine(step: any, line: string) {
    if (!line) return;
    const idx = line.indexOf(':');
    if (idx === -1) return;
    const key = line.slice(0, idx).trim();
    let val = line.slice(idx + 1).trim();
    try {
      val = JSON.parse(val);
    } catch {
      val = val.replace(/^['"]|['"]$/g, '');
    }

    if (key === 'id') step.id = val;
    else if (key === 'type') step.type = val;
    else if (key === 'command') step.command = val;
    else if (key === 'node') step.node = val;
    else if (key === 'model') step.model = val;
    else if (key === 'prompt') step.prompt = val;
    else if (key === 'use_brain') step.use_brain = (val as any) === true || val === 'true';
    else if (key === 'save_to') step.save_to = val;
    else if (key === 'content') step.content = val;
    else if (key === 'path') step.path = val;
    else if (key === 'message') step.message = val;
  }

  function changeEditorMode(mode: 'simple' | 'advanced') {
    if (mode === editorMode) return;
    if (mode === 'advanced') {
      rawYaml = jsonToYaml(simpleForm);
    } else {
      try {
        const parsed = yamlToJson(rawYaml);
        simpleForm = {
          name: parsed.name || '',
          description: parsed.description || '',
          enabled: parsed.enabled ?? true,
          trigger: {
            type: parsed.trigger?.type || 'manual',
            cron: parsed.trigger?.cron || '',
            pattern: parsed.trigger?.pattern || ''
          },
          steps: parsed.steps || []
        };
      } catch (e) {
        showToast('⚠️ Could not parse YAML automatically. Reverted to default.');
      }
    }
    editorMode = mode;
  }

  function createNewFlow() {
    activeFlow = null;
    isEditing = false;
    isCreating = true;
    editorMode = 'simple';
    simpleForm = {
      name: '',
      description: '',
      enabled: true,
      trigger: { type: 'manual', cron: '', pattern: '' },
      steps: []
    };
    rawYaml = '';
  }

  function editFlow(flow: Flow) {
    activeFlow = flow;
    isEditing = true;
    isCreating = false;
    editorMode = 'simple';
    simpleForm = {
      name: flow.name,
      description: flow.description || '',
      enabled: flow.enabled,
      trigger: {
        type: flow.trigger.type || 'manual',
        cron: flow.trigger.cron || '',
        pattern: flow.trigger.pattern || ''
      },
      steps: (flow.steps || []).map(s => ({ ...s }))
    };
    rawYaml = jsonToYaml(flow);
  }

  function cancelEdit() {
    isEditing = false;
    isCreating = false;
    activeFlow = null;
  }

  function selectTemplate(tmpl: any) {
    createNewFlow();
    simpleForm = {
      name: tmpl.name,
      description: tmpl.description,
      enabled: tmpl.enabled,
      trigger: { ...tmpl.trigger },
      steps: tmpl.steps.map((s: any) => ({ ...s }))
    };
    rawYaml = jsonToYaml(simpleForm);
  }

  async function saveFlow() {
    try {
      let bodyData: any;
      let headers: any = {};
      if (editorMode === 'simple') {
        if (!simpleForm.name.trim()) {
          dialog.alert('Flow name is required', 'Validation Error', '⚠️');
          return;
        }
        bodyData = JSON.stringify(simpleForm);
        headers['Content-Type'] = 'application/json';
      } else {
        bodyData = rawYaml;
        headers['Content-Type'] = 'application/x-yaml';
      }

      let url = '/api/flows';
      let method = 'POST';
      if (isEditing && activeFlow) {
        url = `/api/flows/${activeFlow.id}`;
        method = 'PUT';
      }

      const res = await fetch(url, {
        method,
        headers,
        body: bodyData
      });

      if (res.ok) {
        await loadFlows();
        cancelEdit();
        showToast(isEditing ? '✓ Flow updated successfully' : '✓ Flow created successfully');
      } else {
        const err = await res.json();
        dialog.alert(`Failed to save flow: ${err.error || res.statusText}`, 'Error Saving Flow', '❌');
      }
    } catch (e: any) {
      dialog.alert(`Error saving flow: ${e.message}`, 'Error Saving Flow', '❌');
    }
  }

  async function deleteFlow(flowId: string) {
    const confirmed = await dialog.confirm(
      'Are you sure you want to delete this flow? This will remove the flow definition file.',
      'Delete Flow',
      '🗑️',
      'Delete',
      'Cancel',
      true
    );
    if (!confirmed) return;
    try {
      const res = await fetch(`/api/flows/${flowId}`, {
        method: 'DELETE'
      });
      if (res.ok) {
        flows = flows.filter(f => f.id !== flowId);
        showToast('✓ Flow deleted');
      } else {
        const err = await res.json();
        dialog.alert(`Delete failed: ${err.error}`, 'Delete Failed', '❌');
      }
    } catch (e) {
      console.error(e);
    }
  }

  async function toggleFlow(flow: Flow) {
    try {
      const res = await fetch(`/api/flows/${flow.id}/toggle`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled: !flow.enabled })
      });
      if (res.ok) {
        const updated = await res.json();
        flows = flows.map(f => f.id === flow.id ? updated : f);
        showToast(`Flow ${updated.enabled ? 'enabled' : 'disabled'}`);
      }
    } catch (e) {
      console.error(e);
    }
  }

  async function triggerRun(flowId: string) {
    try {
      const res = await fetch(`/api/flows/${flowId}/run`, {
        method: 'POST'
      });
      if (res.ok) {
        showToast('🚀 Flow run triggered');
        if (selectedFlowForHistory && selectedFlowForHistory.id === flowId) {
          loadRuns(flowId);
        }
      } else {
        const err = await res.json();
        dialog.alert(`Trigger failed: ${err.error}`, 'Trigger Failed', '❌');
      }
    } catch (e) {
      console.error(e);
    }
  }

  async function openHistory(flow: Flow) {
    selectedFlowForHistory = flow;
    showHistoryDrawer = true;
    runs = [];
    expandedStepResults = {};
    await loadRuns(flow.id);
  }

  async function loadRuns(flowId: string) {
    try {
      const res = await fetch(`/api/flows/${flowId}/runs`);
      if (res.ok) {
        runs = await res.json();
      }
    } catch (e) {
      console.error(e);
    }
  }

  async function deleteRun(runId: string) {
    const confirmed = await dialog.confirm(
      'Are you sure you want to delete this run execution log?',
      'Delete Run Log',
      '🗑️',
      'Delete',
      'Cancel',
      true
    );
    if (!confirmed) return;
    try {
      const res = await fetch(`/api/flows/${selectedFlowForHistory?.id}/runs/${runId}`, {
        method: 'DELETE'
      });
      if (res.ok) {
        runs = runs.filter(r => r.id !== runId);
        showToast('Run log deleted');
      }
    } catch (e) {
      console.error(e);
    }
  }

  function addStep() {
    const stepID = `step_${simpleForm.steps.length + 1}`;
    simpleForm.steps = [
      ...simpleForm.steps,
      { id: stepID, type: 'shell', command: '', node: '', prompt: '', model: 'auto', use_brain: false, save_to: '', content: '', message: '' }
    ];
  }

  function removeStep(idx: number) {
    simpleForm.steps = simpleForm.steps.filter((_, i) => i !== idx);
  }

  function moveStep(idx: number, dir: 'up' | 'down') {
    const target = dir === 'up' ? idx - 1 : idx + 1;
    if (target < 0 || target >= simpleForm.steps.length) return;
    const list = [...simpleForm.steps];
    const temp = list[idx];
    list[idx] = list[target];
    list[target] = temp;
    simpleForm.steps = list;
  }

  function toggleStepLog(resultId: string) {
    expandedStepResults[resultId] = !expandedStepResults[resultId];
  }

  // Derive stats for Flows dashboard header
  $: totalFlows = flows.length;
  $: activeFlowsCount = flows.filter(f => f.enabled).length;
  $: disabledFlowsCount = flows.filter(f => !f.enabled).length;
  $: scheduledFlowsCount = flows.filter(f => f.enabled && f.trigger.type === 'schedule').length;
  $: watcherFlowsCount = flows.filter(f => f.enabled && (f.trigger.type === 'file_added' || f.trigger.type === 'file_modified')).length;

  // Get active coordinator node name
  $: coordinatorNode = $nodes.find(n => n.status === 'online');
</script>

<svelte:head>
  <title>Fabric Flow - {$appConfig.project_name}</title>
  <meta name="description" content="Cluster-Native workflow automation engine" />
</svelte:head>

<div class="container animate-fade-in">
  <!-- Toast list -->
  <div class="toast-container">
    {#each toasts as toast (toast.id)}
      <div class="toast-card" transition:slide>
        {toast.message}
      </div>
    {/each}
  </div>

  <header class="header-row">
    <div>
      <h1 class="page-title">Fabric Flow</h1>
      <p class="subtitle text-secondary">Cluster-native workflow & RAG automation coordinator.</p>
    </div>
    {#if !isEditing && !isCreating}
      <button class="btn btn-primary create-flow-btn" on:click={createNewFlow}>
        <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" class="btn-svg"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
        Create Flow
      </button>
    {/if}
  </header>

  {#if !isEditing && !isCreating}
    <!-- Stats Metrics Dashboard Row -->
    <div class="stats-row" transition:slide>
      <div class="stat-card total-flow-theme card">
        <div class="stat-icon-wrapper">
          <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="stat-svg"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>
        </div>
        <div class="stat-details">
          <span class="stat-val">{totalFlows}</span>
          <span class="stat-lbl">Total Flows</span>
        </div>
      </div>
      <div class="stat-card active-flow-theme card">
        <div class="stat-icon-wrapper">
          <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="stat-svg"><polygon points="5 3 19 12 5 21 5 3"/></svg>
        </div>
        <div class="stat-details">
          <span class="stat-val">{activeFlowsCount}</span>
          <span class="stat-lbl">Active Pipelines</span>
        </div>
      </div>
      <div class="stat-card scheduled-flow-theme card">
        <div class="stat-icon-wrapper">
          <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="stat-svg"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>
        </div>
        <div class="stat-details">
          <span class="stat-val">{scheduledFlowsCount}</span>
          <span class="stat-lbl">Schedules</span>
        </div>
      </div>
      <div class="stat-card watcher-flow-theme card">
        <div class="stat-icon-wrapper">
          <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="stat-svg"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg>
        </div>
        <div class="stat-details">
          <span class="stat-val">{watcherFlowsCount}</span>
          <span class="stat-lbl">File Watchers</span>
        </div>
      </div>
    </div>
  {/if}

  {#if isEditing || isCreating}
    <!-- EDITOR MODE -->
    <div class="editor-panel card" transition:slide>
      <div class="editor-header">
        <h2 class="editor-title">{isEditing ? 'Modify Automation Flow' : 'Create New Automation Flow'}</h2>
        <div class="editor-toggle">
          <button class="toggle-btn" class:active={editorMode === 'simple'} on:click={() => changeEditorMode('simple')}>Visual Designer</button>
          <button class="toggle-btn" class:active={editorMode === 'advanced'} on:click={() => changeEditorMode('advanced')}>YAML Editor</button>
        </div>
      </div>

      {#if editorMode === 'simple'}
        <div class="editor-layout">
          <!-- Left side: Core details & Trigger Config -->
          <div class="editor-sidebar">
            <div class="card editor-meta-card">
              <h3 class="panel-subtitle">Metadata</h3>
              <div class="form-group">
                <label for="flow-name">Flow Name *</label>
                <input type="text" id="flow-name" bind:value={simpleForm.name} placeholder="e.g. Daily Brief" />
              </div>

              <div class="form-group">
                <label for="flow-desc">Description</label>
                <textarea id="flow-desc" bind:value={simpleForm.description} placeholder="Short summary of what this automation flow accomplishes..." rows={2}></textarea>
              </div>
            </div>

            <div class="card trigger-card">
              <h3 class="panel-subtitle">Trigger Settings</h3>
              
              <div class="form-group">
                <label for="trigger-type">Trigger Source</label>
                <select id="trigger-type" bind:value={simpleForm.trigger.type}>
                  <option value="manual">Manual Trigger Only</option>
                  <option value="schedule">Schedule (Cron Job)</option>
                  <option value="file_added">File Uploaded to Storage</option>
                  <option value="file_modified">File Updated in Storage</option>
                </select>
              </div>

              {#if simpleForm.trigger.type === 'schedule'}
                <div class="form-group nested-input" transition:slide>
                  <label for="trigger-cron">Cron Schedule *</label>
                  <input type="text" id="trigger-cron" bind:value={simpleForm.trigger.cron} placeholder="*/5 * * * * (every 5 minutes)" />
                  <p class="hint">Standard 5-field cron syntax (min, hour, day, month, weekday).</p>
                </div>
              {/if}

              {#if simpleForm.trigger.type === 'file_added' || simpleForm.trigger.type === 'file_modified'}
                <div class="form-group nested-input" transition:slide>
                  <label for="trigger-pattern">Filename Glob Pattern</label>
                  <input type="text" id="trigger-pattern" bind:value={simpleForm.trigger.pattern} placeholder="*.pdf (leave empty to watch all files)" />
                  <p class="hint">Matches filename extensions in the shared cluster storage namespace.</p>
                </div>
              {/if}
            </div>
          </div>

          <!-- Right side: Workflow Steps Tree -->
          <div class="editor-content-area">
            <div class="steps-section-header">
              <h3 class="panel-subtitle">Workflow Steps Timeline</h3>
              <button class="btn btn-secondary btn-sm" on:click={addStep}>
                <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="btn-svg"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
                Add Step
              </button>
            </div>

            {#if simpleForm.steps.length === 0}
              <div class="empty-steps-hint">
                <svg xmlns="http://www.w3.org/2000/svg" width="36" height="36" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="empty-steps-icon"><circle cx="12" cy="12" r="10"/><path d="M8 12h8"/><path d="M12 8v8"/></svg>
                <p>No workflow pipeline steps configured yet.</p>
                <button class="btn btn-secondary btn-sm" style="margin-top: 12px;" on:click={addStep}>Add your first step</button>
              </div>
            {:else}
              <div class="steps-list">
                {#each simpleForm.steps as step, idx (idx)}
                  <div class="step-card step-type-{step.type} card" transition:slide>
                    <div class="step-card-header">
                      <div class="step-num-badge">
                        <span class="step-counter-label">Step {idx + 1}</span>
                      </div>
                      
                      <div class="step-id-group">
                        <label for="step-id-{idx}">ID:</label>
                        <input type="text" id="step-id-{idx}" class="step-id-input" bind:value={step.id} />
                      </div>

                      <div class="step-actions">
                        <button class="btn-icon-control" disabled={idx === 0} on:click={() => moveStep(idx, 'up')} title="Move Up">
                          <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="18 15 12 9 6 15"/></svg>
                        </button>
                        <button class="btn-icon-control" disabled={idx === simpleForm.steps.length - 1} on:click={() => moveStep(idx, 'down')} title="Move Down">
                          <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><polyline points="6 9 12 15 18 9"/></svg>
                        </button>
                        <button class="btn-icon-control danger-btn" on:click={() => removeStep(idx)} title="Delete Step">
                          <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
                        </button>
                      </div>
                    </div>

                    <div class="form-group step-type-selector">
                      <label for="step-type-{idx}">Action Type</label>
                      <select id="step-type-{idx}" bind:value={step.type}>
                        <option value="shell">💻 Shell Command Execution</option>
                        <option value="ai">🤖 Local LLM Inference Prompt</option>
                        <option value="save">💾 Write Output to Shared Storage</option>
                        <option value="notify">🔔 SSE Notification Toast Alert</option>
                      </select>
                    </div>

                    <div class="step-details-pane">
                      {#if step.type === 'shell'}
                        <div class="form-group">
                          <label for="step-cmd-{idx}">Command String</label>
                          <textarea id="step-cmd-{idx}" class="mono-textarea" bind:value={step.command} placeholder="e.g. echo 'Finished running backup'" rows={2}></textarea>
                        </div>
                        <div class="form-group">
                          <label for="step-node-{idx}">Execution Node Bind</label>
                          <select id="step-node-{idx}" bind:value={step.node}>
                            <option value="">Balance automatically (Cluster Scheduler)</option>
                            {#each $nodes as node}
                              <option value={node.id}>{node.name} ({node.id.substring(0, 8)})</option>
                            {/each}
                          </select>
                        </div>
                      {:else if step.type === 'ai'}
                        <div class="form-group">
                          <label for="step-prompt-{idx}">Inference Prompt Template</label>
                          <textarea id="step-prompt-{idx}" bind:value={step.prompt} placeholder={"e.g. Summarize: {{steps.step_1.output}}"} rows={3}></textarea>
                          <p class="hint">Supports handlebar variable injection from prior steps (e.g. steps.step_name.output).</p>
                        </div>
                        <div class="form-row">
                          <div class="form-group">
                            <label for="step-model-{idx}">LLM Model</label>
                            <input type="text" id="step-model-{idx}" bind:value={step.model} placeholder="auto" />
                          </div>
                          <div class="checkbox-group">
                            <label class="checkbox-container">
                              <input type="checkbox" id="step-rag-{idx}" bind:checked={step.use_brain} />
                              <span class="checkmark"></span>
                              Inject Brain Context (Knowledge RAG)
                            </label>
                          </div>
                        </div>
                      {:else if step.type === 'save'}
                        <div class="form-group">
                          <label for="step-saveto-{idx}">Destination Storage URI</label>
                          <input type="text" id="step-saveto-{idx}" bind:value={step.save_to} placeholder={"storage://briefs/{{date}}.txt"} />
                        </div>
                        <div class="form-group">
                          <label for="step-content-{idx}">File Content Template</label>
                          <textarea id="step-content-{idx}" bind:value={step.content} placeholder="Enter output file content templates..." rows={3}></textarea>
                        </div>
                      {:else if step.type === 'notify'}
                        <div class="form-group">
                          <label for="step-msg-{idx}">Toast Message Content</label>
                          <input type="text" id="step-msg-{idx}" bind:value={step.message} placeholder={"e.g. Flow succeeded! Output: {{steps.ai_step.output}}"} />
                        </div>
                      {/if}
                    </div>
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        </div>
      {:else}
        <!-- Advanced YAML panel -->
        <div class="yaml-editor-pane">
          <p class="yaml-hint text-secondary">Input OpenFabric pipeline architecture directly in YAML configuration schema.</p>
          <div class="yaml-textarea-wrapper">
            <textarea class="yaml-textarea mono" bind:value={rawYaml} placeholder="# Write YAML definition here..." rows={18}></textarea>
          </div>
        </div>
      {/if}

      <div class="editor-buttons">
        <button class="btn btn-secondary" on:click={cancelEdit}>Cancel</button>
        <button class="btn btn-primary" on:click={saveFlow}>
          <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="btn-svg"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/><polyline points="17 21 17 13 7 13 7 21"/><polyline points="7 3 7 8 15 8"/></svg>
          Save Flow
        </button>
      </div>
    </div>
  {:else}
    <!-- LIST MODE -->
    <div class="layout-grid">
      <div class="flows-list-section">
        <h2 class="list-section-title">Active Automations</h2>
        {#if flows.filter(f => f.enabled).length === 0}
          <div class="empty-flows-card card">
            <p class="no-flows text-secondary">No active pipelines scheduled in the cluster coordinator. Select a starter template or click "+ Create Flow" to launch a pipeline.</p>
          </div>
        {:else}
          <div class="flows-grid">
            {#each flows.filter(f => f.enabled) as flow (flow.id)}
              <div class="flow-grid-card card" class:schedule-card={flow.trigger.type === 'schedule'} class:watcher-card={flow.trigger.type === 'file_added' || flow.trigger.type === 'file_modified'} class:manual-card={flow.trigger.type === 'manual'}>
                <div class="flow-card-info">
                  <div class="flow-card-header-row">
                    <div class="flow-icon-and-title">
                      <div class="flow-icon-container">
                        {#if flow.trigger.type === 'schedule'}
                          <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>
                        {:else if flow.trigger.type === 'file_added' || flow.trigger.type === 'file_modified'}
                          <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg>
                        {:else}
                          <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="5 3 19 12 5 21 5 3"/></svg>
                        {/if}
                      </div>
                      <h3>{flow.name}</h3>
                    </div>
                    <span class="badge badge-teal">Active</span>
                  </div>
                  
                  {#if flow.description}
                    <p class="flow-desc text-secondary">{flow.description}</p>
                  {/if}
                  
                  <div class="flow-meta">
                    <span class="meta-item">
                      Trigger: <strong class="text-accent">{flow.trigger.type}</strong>
                    </span>
                    {#if flow.trigger.cron}
                      <span class="meta-item cron-tag">
                        Cron: <code>{flow.trigger.cron}</code>
                      </span>
                    {/if}
                    {#if flow.trigger.pattern}
                      <span class="meta-item glob-tag">
                        Pattern: <code>{flow.trigger.pattern}</code>
                      </span>
                    {/if}
                  </div>

                  <!-- Small step visual preview indicators -->
                  <div class="flow-steps-preview">
                    <span class="steps-preview-label">Pipeline:</span>
                    {#each flow.steps || [] as step, idx}
                      <span class="step-preview-tag type-{step.type}" title="{step.id} ({step.type})">
                        {step.type === 'shell' ? 'Shell' : step.type === 'ai' ? 'LLM' : step.type === 'save' ? 'Store' : 'Notify'}
                      </span>
                      {#if idx < flow.steps.length - 1}
                        <span class="step-connector-arrow">➔</span>
                      {/if}
                    {/each}
                  </div>
                </div>
                
                <div class="flow-card-actions">
                  <button class="btn btn-secondary btn-sm run-action-btn" on:click={() => triggerRun(flow.id)} title="Trigger Flow Execution">
                    <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="5 3 19 12 5 21 5 3"/></svg>
                    Run
                  </button>
                  <button class="btn btn-secondary btn-sm" on:click={() => openHistory(flow)} title="Logs and Execution Run History">
                    <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/><polyline points="10 9 9 9 8 9"/></svg>
                    Logs
                  </button>
                  <button class="btn-grid-action-icon" on:click={() => editFlow(flow)} title="Modify Flow">
                    <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 1 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
                  </button>
                  <button class="btn-grid-action-icon" on:click={() => toggleFlow(flow)} title="Disable Flow">
                    <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"/><line x1="9" y1="9" x2="15" y2="15"/><line x1="15" y1="9" x2="9" y2="15"/></svg>
                  </button>
                  <button class="btn-grid-action-icon danger-icon-btn" on:click={() => deleteFlow(flow.id)} title="Delete Flow">
                    <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>
                  </button>
                </div>
              </div>
            {/each}
          </div>
        {/if}

        <h2 class="list-section-title" style="margin-top: var(--space-8)">Disabled Automations</h2>
        {#if flows.filter(f => !f.enabled).length === 0}
          <div class="empty-flows-card card">
            <p class="no-flows text-muted">No disabled automation flows in workspace.</p>
          </div>
        {:else}
          <div class="flows-grid disabled-flows-grid opacity-70">
            {#each flows.filter(f => !f.enabled) as flow (flow.id)}
              <div class="flow-grid-card card">
                <div class="flow-card-info">
                  <div class="flow-card-header-row">
                    <div class="flow-icon-and-title">
                      <div class="flow-icon-container">
                        <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"/><line x1="9" y1="9" x2="15" y2="15"/><line x1="15" y1="9" x2="9" y2="15"/></svg>
                      </div>
                      <h3>{flow.name}</h3>
                    </div>
                    <span class="badge badge-gray">Disabled</span>
                  </div>
                  {#if flow.description}
                    <p class="flow-desc text-secondary">{flow.description}</p>
                  {/if}
                  <div class="flow-meta">
                    <span class="meta-item">Trigger: {flow.trigger.type}</span>
                    <span class="meta-item">Steps: {flow.steps?.length || 0}</span>
                  </div>
                </div>
                <div class="flow-card-actions">
                  <button class="btn btn-secondary btn-sm" on:click={() => openHistory(flow)}>Logs</button>
                  <button class="btn-grid-action-icon" on:click={() => editFlow(flow)} title="Edit Flow"><svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 1 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg></button>
                  <button class="btn-grid-action-icon" on:click={() => toggleFlow(flow)} title="Enable Flow"><svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><polyline points="22 4 12 14.01 9 11.01"/></svg></button>
                  <button class="btn-grid-action-icon danger-icon-btn" on:click={() => deleteFlow(flow.id)} title="Delete Flow"><svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg></button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>

      <div class="sidebar-section">
        <div class="coordinator-card card">
          <div class="coordinator-title-row">
            <h3>Flow Coordinator</h3>
            <span class="active-pulse-dot"></span>
          </div>
          <div class="coord-details">
            <div class="coord-row">
              <span class="text-secondary">Elected Master:</span>
              <strong class="text-accent">{coordinatorNode ? coordinatorNode.name : 'Resolving...'}</strong>
            </div>
            <div class="coord-row">
              <span class="text-secondary">Synchronized:</span>
              <span class="text-accent">Cluster Heartbeat</span>
            </div>
            <div class="coord-row">
              <span class="text-secondary">Status:</span>
              <span class="badge badge-teal">Master Node</span>
            </div>
            <p class="coord-note text-muted">
              Scheduler processes run on the node with the highest memory capability. If the node fails, election is re-evaluated immediately.
            </p>
          </div>
        </div>

        <h3 class="sidebar-block-title">Starter Templates</h3>
        <div class="templates-list">
          {#each templates as tmpl}
            <div class="template-card card" on:click={() => selectTemplate(tmpl)}>
              <h4>{tmpl.name}</h4>
              <p class="tmpl-desc text-secondary">{tmpl.description}</p>
              
              <!-- Horizontal pipeline preview visualization -->
              <div class="template-pipeline-preview">
                {#each tmpl.steps as step, stepIdx}
                  <span class="preview-step-pill step-preview-type-{step.type}" title="{step.id} ({step.type})">
                    {step.type === 'shell' ? '💻' : step.type === 'ai' ? '🤖' : step.type === 'save' ? '💾' : '🔔'}
                  </span>
                  {#if stepIdx < tmpl.steps.length - 1}
                    <span class="preview-connector">→</span>
                  {/if}
                {/each}
              </div>

              <div class="tmpl-meta">
                <span>Trigger: <strong class="text-accent">{tmpl.trigger.type}</strong></span>
                <span>Steps: <strong>{tmpl.steps.length}</strong></span>
              </div>
            </div>
          {/each}
        </div>
      </div>
    </div>
  {/if}
</div>

<!-- HISTORY DRAWER OVERLAY -->
{#if showHistoryDrawer && selectedFlowForHistory}
  <div class="drawer-overlay" on:click={() => showHistoryDrawer = false}>
    <div class="drawer card" on:click|stopPropagation>
      <div class="drawer-header">
        <div>
          <h2>Pipeline Runs</h2>
          <p class="text-secondary">History for {selectedFlowForHistory.name}</p>
        </div>
        <button class="close-btn" on:click={() => showHistoryDrawer = false}>×</button>
      </div>

      <div class="drawer-content">
        <button class="btn btn-secondary btn-sm refresh-btn" on:click={() => loadRuns(selectedFlowForHistory!.id)}>
          <svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="btn-svg"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></svg>
          Refresh Logs
        </button>
        
        {#if runs.length === 0}
          <div class="empty-runs-state">
            <svg xmlns="http://www.w3.org/2000/svg" width="36" height="36" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" style="color: var(--text-muted); opacity: 0.5; margin-bottom: 8px;"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/></svg>
            <p>No executions found for this flow yet.</p>
          </div>
        {:else}
          <div class="runs-list">
            {#each runs as run (run.id)}
              <div class="run-log-item card">
                <div class="run-log-header">
                  <div class="run-status">
                    <span class="dot" class:online={run.status === 'completed'} class:running={run.status === 'running'} class:danger={run.status === 'failed'}></span>
                    <strong class="capitalize status-label" class:text-accent={run.status === 'completed'} class:text-warning={run.status === 'running'} class:text-danger={run.status === 'failed'}>{run.status}</strong>
                  </div>
                  <div class="run-time text-secondary">
                    {new Date(run.started_at).toLocaleTimeString()} ({new Date(run.started_at).toLocaleDateString()})
                  </div>
                  <button class="btn-icon-control danger-btn" on:click={() => deleteRun(run.id)} title="Delete Log Entry">
                    <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>
                  </button>
                </div>
                
                <div class="run-details">
                  <div class="run-row-meta">
                    <div class="run-row"><span class="text-muted">Run ID:</span> <code class="mono text-sm">{run.id.substring(0, 8)}</code></div>
                    <div class="run-row"><span class="text-muted">Trigger:</span> <span class="badge badge-gray">{run.trigger}</span></div>
                  </div>
                  
                  {#if run.error}
                    <div class="run-error banner banner-error">{run.error}</div>
                  {/if}

                  <div class="run-steps-timeline">
                    <h4 class="steps-timeline-title">Steps Processed</h4>
                    <div class="timeline-tree">
                      {#each run.steps as stepRes}
                        <div class="run-step-block">
                          <!-- Clicking step name toggles code block display -->
                          <div class="run-step-title-bar" on:click={() => toggleStepLog(stepRes.id)}>
                            <span class="step-dot" class:success={stepRes.status === 'completed'} class:fail={stepRes.status === 'failed'}></span>
                            <span class="run-step-id">{stepRes.id}</span>
                            <span class="badge badge-run-step" class:success-badge={stepRes.status === 'completed'} class:fail-badge={stepRes.status === 'failed'}>{stepRes.status}</span>
                            <span class="expander-arrow">{expandedStepResults[stepRes.id] ? '▼' : '▶'}</span>
                          </div>
                          
                          {#if stepRes.error}
                            <div class="step-err text-danger">{stepRes.error}</div>
                          {/if}
                          
                          {#if stepRes.output && expandedStepResults[stepRes.id]}
                            <div class="step-out-terminal" transition:slide>
                              <div class="terminal-header">
                                <span>output console</span>
                                <button class="copy-terminal-btn" on:click|stopPropagation={() => navigator.clipboard.writeText(stepRes.output)} title="Copy Terminal Output">Copy</button>
                              </div>
                              <pre class="step-out mono">{stepRes.output}</pre>
                            </div>
                          {/if}
                        </div>
                      {/each}
                    </div>
                  </div>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .container {
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: var(--space-6);
  }

  .header-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-bottom: 1px solid var(--border);
    padding-bottom: var(--space-4);
  }

  .page-title {
    font-size: var(--text-3xl);
    font-weight: 700;
    letter-spacing: -0.02em;
    background: linear-gradient(135deg, var(--text-primary) 30%, var(--accent) 100%);
    -webkit-background-clip: text;
    -webkit-text-fill-color: transparent;
  }

  .subtitle {
    font-size: var(--text-sm);
    margin-top: var(--space-1);
  }

  .create-flow-btn {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }

  .btn-svg {
    flex-shrink: 0;
  }

  /* Stats Row styling */
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
  
  .stat-card.total-flow-theme:hover { border-color: rgba(0, 201, 167, 0.4); box-shadow: 0 4px 20px rgba(0, 201, 167, 0.08); }
  .stat-card.total-flow-theme::before { background: var(--accent); }
  .stat-card.total-flow-theme .stat-icon-wrapper { background: rgba(0, 201, 167, 0.1); color: var(--accent); }

  .stat-card.active-flow-theme:hover { border-color: rgba(0, 224, 188, 0.4); box-shadow: 0 4px 20px rgba(0, 224, 188, 0.08); }
  .stat-card.active-flow-theme::before { background: #00e0bc; }
  .stat-card.active-flow-theme .stat-icon-wrapper { background: rgba(0, 224, 188, 0.1); color: #00e0bc; }

  .stat-card.scheduled-flow-theme:hover { border-color: rgba(0, 132, 255, 0.4); box-shadow: 0 4px 20px rgba(0, 132, 255, 0.08); }
  .stat-card.scheduled-flow-theme::before { background: #0084FF; }
  .stat-card.scheduled-flow-theme .stat-icon-wrapper { background: rgba(0, 132, 255, 0.1); color: #0084FF; }

  .stat-card.watcher-flow-theme:hover { border-color: rgba(167, 0, 255, 0.4); box-shadow: 0 4px 20px rgba(167, 0, 255, 0.08); }
  .stat-card.watcher-flow-theme::before { background: #A700FF; }
  .stat-card.watcher-flow-theme .stat-icon-wrapper { background: rgba(167, 0, 255, 0.1); color: #A700FF; }

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

  .layout-grid {
    display: grid;
    grid-template-columns: 1fr 340px;
    gap: var(--space-8);
  }

  /* Toast notification system */
  .toast-container {
    position: fixed;
    top: var(--space-6);
    right: var(--space-6);
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    z-index: 10000;
  }
  .toast-card {
    background: var(--bg-secondary);
    border: 1px solid var(--accent);
    box-shadow: var(--shadow-accent);
    padding: var(--space-3) var(--space-5);
    border-radius: var(--radius-md);
    font-size: var(--text-sm);
    color: var(--text-primary);
    animation: fade-in-toast 0.2s ease-out;
  }
  @keyframes fade-in-toast {
    from { opacity: 0; transform: translateY(-10px); }
    to { opacity: 1; transform: translateY(0); }
  }

  /* List Titles */
  .list-section-title {
    font-size: var(--text-lg);
    font-weight: 600;
    color: var(--text-primary);
    margin-bottom: var(--space-4);
  }

  /* Empty state flow layout */
  .empty-flows-card {
    background: var(--bg-secondary);
    padding: var(--space-8);
    border: 1px dashed var(--border);
    text-align: center;
  }
  .no-flows {
    font-size: var(--text-sm);
  }

  /* Flows Grid */
  .flows-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
    gap: var(--space-5);
  }

  .flow-grid-card {
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    padding: var(--space-5) var(--space-5);
    min-height: 190px;
    position: relative;
  }
  .flow-grid-card::before {
    content: '';
    position: absolute;
    top: 0;
    bottom: 0;
    left: 0;
    width: 3px;
    background: transparent;
    border-top-left-radius: var(--radius-lg);
    border-bottom-left-radius: var(--radius-lg);
  }
  .flow-grid-card:hover {
    border-color: var(--border-accent);
    transform: translateY(-2px);
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
  }

  /* Theme borders for flows */
  .flow-grid-card.schedule-card::before { background: #0084FF; }
  .flow-grid-card.watcher-card::before { background: #A700FF; }
  .flow-grid-card.manual-card::before { background: var(--accent); }

  .flow-card-info {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }

  .flow-card-header-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-2);
  }
  .flow-icon-and-title {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    min-width: 0;
  }
  .flow-icon-and-title h3 {
    font-size: var(--text-base);
    font-weight: 600;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .flow-icon-container {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border-radius: var(--radius-sm);
    background: var(--bg-tertiary);
    color: var(--text-secondary);
    flex-shrink: 0;
  }
  .schedule-card .flow-icon-container { color: #0084FF; background: rgba(0, 132, 255, 0.1); }
  .watcher-card .flow-icon-container { color: #b340ff; background: rgba(167, 0, 255, 0.1); }
  .manual-card .flow-icon-container { color: var(--accent); background: var(--accent-dim); }

  .flow-desc {
    font-size: var(--text-sm);
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    text-overflow: ellipsis;
    min-height: 40px;
  }

  .flow-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
    font-size: var(--text-xs);
    color: var(--text-muted);
  }
  .meta-item strong {
    color: var(--text-primary);
  }
  .cron-tag code, .glob-tag code {
    background: var(--bg-tertiary);
    padding: 2px 6px;
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    border: 1px solid var(--border);
    font-family: var(--font-mono);
  }

  /* Step Preview Tags */
  .flow-steps-preview {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    margin-top: 4px;
    border-top: 1px dashed var(--border);
    padding-top: var(--space-3);
  }
  .steps-preview-label {
    font-size: 11px;
    color: var(--text-muted);
    font-weight: 500;
    margin-right: 4px;
  }
  .step-preview-tag {
    font-size: 10px;
    padding: 1px 6px;
    border-radius: 4px;
    font-weight: 500;
  }
  .step-preview-tag.type-shell { background: rgba(0, 201, 167, 0.12); color: var(--accent); border: 1px solid rgba(0, 201, 167, 0.2); }
  .step-preview-tag.type-ai { background: rgba(0, 132, 255, 0.12); color: #43a3ff; border: 1px solid rgba(0, 132, 255, 0.2); }
  .step-preview-tag.type-save { background: rgba(167, 0, 255, 0.12); color: #c466ff; border: 1px solid rgba(167, 0, 255, 0.2); }
  .step-preview-tag.type-notify { background: rgba(255, 179, 0, 0.12); color: #ffbf33; border: 1px solid rgba(255, 179, 0, 0.2); }
  .step-connector-arrow {
    font-size: 10px;
    color: var(--text-muted);
    opacity: 0.5;
  }

  .flow-card-actions {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    border-top: 1px solid var(--border);
    padding-top: var(--space-3);
    margin-top: var(--space-2);
  }
  .run-action-btn {
    border-color: var(--accent-glow);
    color: var(--accent);
  }
  .run-action-btn:hover {
    background: var(--accent-dim);
  }
  .btn-grid-action-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    border-radius: var(--radius-sm);
    background: transparent;
    border: 1px solid var(--border);
    color: var(--text-secondary);
    cursor: pointer;
    transition: all var(--transition);
  }
  .btn-grid-action-icon:hover {
    border-color: var(--accent);
    color: var(--accent);
    background: var(--bg-tertiary);
  }
  .btn-grid-action-icon.danger-icon-btn:hover {
    border-color: var(--danger);
    color: var(--danger);
    background: rgba(255, 107, 107, 0.08);
  }

  .badge {
    padding: 2px var(--space-2);
    border-radius: var(--radius-sm);
    font-size: var(--text-xs);
    font-weight: 600;
    text-transform: uppercase;
  }
  .badge-teal { background: var(--accent-dim); color: var(--accent); border: 1px solid var(--border-accent); }
  .badge-gray { background: rgba(255, 255, 255, 0.04); color: var(--text-secondary); border: 1px solid var(--border); }

  .opacity-70 { opacity: 0.65; }

  /* Coordinator Panel */
  .coordinator-card {
    background: linear-gradient(135deg, var(--bg-card) 0%, rgba(0, 201, 167, 0.03) 100%);
    border: 1px solid var(--border);
  }
  .coordinator-title-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-bottom: 1px solid var(--border);
    padding-bottom: var(--space-2);
  }
  .active-pulse-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--accent);
    box-shadow: 0 0 10px var(--accent);
    animation: beacon-pulse 1.8s infinite;
  }
  @keyframes beacon-pulse {
    0% { transform: scale(0.9); opacity: 0.6; }
    50% { transform: scale(1.3); opacity: 1; box-shadow: 0 0 14px var(--accent); }
    100% { transform: scale(0.9); opacity: 0.6; }
  }
  .coord-details {
    margin-top: var(--space-3);
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    font-size: var(--text-sm);
  }
  .coord-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .coord-note {
    font-size: var(--text-xs);
    line-height: 1.4;
    border-top: 1px solid var(--border);
    padding-top: var(--space-3);
    margin-top: var(--space-2);
  }

  .sidebar-block-title {
    margin-top: var(--space-6);
    margin-bottom: var(--space-3);
    font-size: var(--text-base);
    font-weight: 600;
  }

  /* Templates list */
  .templates-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }
  .template-card {
    cursor: pointer;
    background: var(--bg-secondary);
    transition: all var(--transition);
    border: 1px solid var(--border);
  }
  .template-card:hover {
    border-color: var(--accent);
    background: var(--bg-card-hover);
    transform: translateY(-1px);
  }
  .tmpl-desc {
    font-size: var(--text-xs);
    margin: var(--space-1) 0;
    line-height: 1.4;
  }
  .template-pipeline-preview {
    display: flex;
    align-items: center;
    gap: 4px;
    margin: 8px 0;
    background: var(--bg-primary);
    padding: 6px 8px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
    width: fit-content;
  }
  .preview-step-pill {
    font-size: 10px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 20px;
    height: 20px;
    border-radius: 50%;
  }
  .step-preview-type-shell { background: rgba(0, 201, 167, 0.15); border: 1px solid var(--accent); }
  .step-preview-type-ai { background: rgba(0, 132, 255, 0.15); border: 1px solid #0084FF; }
  .step-preview-type-save { background: rgba(167, 0, 255, 0.15); border: 1px solid #A700FF; }
  .step-preview-type-notify { background: rgba(255, 179, 0, 0.15); border: 1px solid #FFB300; }
  .preview-connector {
    font-size: 10px;
    color: var(--text-muted);
  }
  .tmpl-meta {
    display: flex;
    justify-content: space-between;
    font-size: var(--text-xs);
    color: var(--text-muted);
    margin-top: var(--space-2);
    border-top: 1px solid var(--border);
    padding-top: var(--space-2);
  }

  /* Visual Designer Panel two-column styling */
  .editor-panel {
    display: flex;
    flex-direction: column;
    gap: var(--space-6);
  }
  .editor-title {
    font-size: var(--text-lg);
    font-weight: 600;
  }
  .editor-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-bottom: 1px solid var(--border);
    padding-bottom: var(--space-3);
  }
  .editor-toggle {
    display: inline-flex;
    background: rgba(22, 27, 34, 0.6);
    padding: 4px;
    border-radius: var(--radius-md);
    border: 1px solid var(--border);
    gap: 4px;
  }
  .toggle-btn {
    background: transparent;
    border: none;
    color: var(--text-secondary);
    padding: 6px 16px;
    font-size: var(--text-xs);
    font-weight: 600;
    cursor: pointer;
    border-radius: var(--radius-sm);
    transition: all var(--transition);
    white-space: nowrap;
  }
  .toggle-btn:hover {
    color: var(--text-primary);
  }
  .toggle-btn.active {
    background: var(--accent);
    color: #0D1117;
    box-shadow: 0 2px 8px rgba(0, 201, 167, 0.3);
  }

  .editor-layout {
    display: grid;
    grid-template-columns: 320px 1fr;
    gap: var(--space-6);
    align-items: start;
  }
  @media (max-width: 900px) {
    .editor-layout {
      grid-template-columns: 1fr;
    }
  }

  .editor-sidebar {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }
  .editor-meta-card, .trigger-card {
    background: var(--bg-secondary);
    padding: var(--space-5);
    border: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .panel-subtitle {
    font-size: var(--text-sm);
    font-weight: 600;
    color: var(--text-primary);
    border-bottom: 1px solid var(--border);
    padding-bottom: var(--space-2);
    margin-bottom: var(--space-1);
  }

  .form-group {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    flex: 1;
  }
  .form-group label {
    font-size: var(--text-xs);
    color: var(--text-secondary);
    font-weight: 500;
  }
  .form-group input, .form-group select, .form-group textarea {
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-2) var(--space-3);
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--text-sm);
    outline: none;
    transition: border-color var(--transition);
  }
  .form-group input:focus, .form-group select:focus, .form-group textarea:focus {
    border-color: var(--accent);
  }
  .form-group textarea {
    min-height: 70px;
    resize: vertical;
  }
  .nested-input {
    background: var(--bg-primary);
    padding: var(--space-3);
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
  }

  .form-row {
    display: flex;
    gap: var(--space-4);
    flex-wrap: wrap;
  }

  .checkbox-group {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    margin-top: var(--space-2);
  }
  .checkbox-container {
    display: flex;
    align-items: center;
    position: relative;
    padding-left: 24px;
    cursor: pointer;
    font-size: var(--text-sm);
    user-select: none;
  }
  .checkbox-container input {
    position: absolute;
    opacity: 0;
    cursor: pointer;
    height: 0;
    width: 0;
  }
  .checkmark {
    position: absolute;
    left: 0;
    height: 16px;
    width: 16px;
    background-color: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: 4px;
    transition: all var(--transition);
  }
  .checkbox-container:hover input ~ .checkmark {
    border-color: var(--accent);
  }
  .checkbox-container input:checked ~ .checkmark {
    background-color: var(--accent-dim);
    border-color: var(--accent);
  }
  .checkmark:after {
    content: "";
    position: absolute;
    display: none;
  }
  .checkbox-container input:checked ~ .checkmark:after {
    display: block;
  }
  .checkbox-container .checkmark:after {
    left: 5px;
    top: 2px;
    width: 4px;
    height: 8px;
    border: solid var(--accent);
    border-width: 0 2px 2px 0;
    transform: rotate(45deg);
  }

  /* Timeline step list styling */
  .editor-content-area {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .steps-section-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .steps-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-6);
    position: relative;
    padding-left: 24px;
  }
  .steps-list::before {
    content: '';
    position: absolute;
    left: 6px;
    top: 15px;
    bottom: 15px;
    width: 2px;
    background: var(--border);
  }

  .step-card {
    background: var(--bg-secondary);
    border-left: 3px solid var(--border);
    transition: all var(--transition);
    position: relative;
  }
  .step-card::after {
    content: '';
    position: absolute;
    left: -22px;
    top: 22px;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--border);
    border: 2px solid var(--bg-primary);
    transition: background var(--transition);
  }
  .step-card:hover {
    border-color: var(--border-accent);
  }

  /* Timeline Colors by Step Action Type */
  .step-card.step-type-shell { border-left-color: var(--accent); }
  .step-card.step-type-shell::after { background: var(--accent); }
  
  .step-card.step-type-ai { border-left-color: #0084FF; }
  .step-card.step-type-ai::after { background: #0084FF; }
  
  .step-card.step-type-save { border-left-color: #A700FF; }
  .step-card.step-type-save::after { background: #A700FF; }
  
  .step-card.step-type-notify { border-left-color: #FFB300; }
  .step-card.step-type-notify::after { background: #FFB300; }

  .step-card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-bottom: 1px solid var(--border);
    padding-bottom: var(--space-2);
    margin-bottom: var(--space-4);
    gap: var(--space-4);
  }
  .step-num-badge {
    background: var(--bg-tertiary);
    padding: 3px 10px;
    border-radius: 20px;
    border: 1px solid var(--border);
  }
  .step-counter-label {
    font-weight: 700;
    color: var(--text-primary);
    font-size: var(--text-xs);
  }
  .step-card.step-type-shell .step-counter-label { color: var(--accent); }
  .step-card.step-type-ai .step-counter-label { color: #0084FF; }
  .step-card.step-type-save .step-counter-label { color: #b340ff; }
  .step-card.step-type-notify .step-counter-label { color: #FFB300; }

  .step-id-group {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    flex-grow: 1;
  }
  .step-id-group label {
    font-size: var(--text-xs);
    color: var(--text-muted);
    font-weight: 500;
  }
  .step-id-input {
    background: var(--bg-tertiary) !important;
    border: 1px solid var(--border) !important;
    border-radius: var(--radius-sm) !important;
    padding: 2px 8px !important;
    font-family: var(--font-mono) !important;
    font-size: var(--text-xs) !important;
    width: 140px;
    color: var(--text-primary);
  }
  .step-id-input:focus {
    border-color: var(--accent) !important;
  }
  
  .step-actions {
    display: flex;
    gap: 4px;
  }
  .step-type-selector {
    margin-bottom: var(--space-3);
  }

  .step-details-pane {
    background: var(--bg-primary);
    padding: var(--space-4);
    border-radius: var(--radius-md);
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    border: 1px solid var(--border);
  }
  .mono-textarea {
    font-family: var(--font-mono);
  }

  .btn-icon-control {
    background: transparent;
    border: 1px solid var(--border);
    color: var(--text-secondary);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    border-radius: var(--radius-sm);
    transition: all var(--transition);
  }
  .btn-icon-control:hover:not(:disabled) {
    color: var(--accent);
    border-color: var(--accent);
    background: var(--bg-tertiary);
  }
  .btn-icon-control:disabled {
    opacity: 0.3;
    cursor: not-allowed;
  }
  .btn-icon-control.danger-btn:hover {
    color: var(--danger);
    border-color: var(--danger);
    background: rgba(255, 107, 107, 0.08);
  }

  .empty-steps-hint {
    background: var(--bg-secondary);
    padding: var(--space-10) var(--space-8);
    border: 1px dashed var(--border);
    border-radius: var(--radius-lg);
    text-align: center;
    color: var(--text-secondary);
    display: flex;
    flex-direction: column;
    align-items: center;
  }
  .empty-steps-icon {
    color: var(--text-muted);
    opacity: 0.5;
    margin-bottom: var(--space-3);
  }

  .yaml-editor-pane {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .yaml-textarea-wrapper {
    border-radius: var(--radius-md);
    background: #090d13;
    border: 1px solid var(--border);
    padding: var(--space-2);
  }
  .yaml-textarea {
    width: 100%;
    background: transparent;
    border: none;
    color: #e6edf3;
    font-size: var(--text-sm);
    line-height: 1.5;
    outline: none;
    resize: vertical;
    font-family: var(--font-mono);
  }
  .yaml-hint {
    font-size: var(--text-xs);
    margin-bottom: var(--space-2);
  }

  .editor-buttons {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-3);
    border-top: 1px solid var(--border);
    padding-top: var(--space-4);
    margin-top: var(--space-2);
  }

  /* History Drawer Overlay */
  .drawer-overlay {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    background: rgba(0, 0, 0, 0.75);
    backdrop-filter: blur(5px);
    z-index: 1000;
    display: flex;
    justify-content: flex-end;
  }
  .drawer {
    width: 520px;
    height: 100vh;
    border-radius: 0;
    border-left: 1px solid var(--border);
    border-top: none;
    border-bottom: none;
    display: flex;
    flex-direction: column;
    padding: 0;
    background: var(--bg-secondary);
    animation: slide-in 0.2s cubic-bezier(0.16, 1, 0.3, 1);
  }
  @keyframes slide-in {
    from { transform: translateX(100%); }
    to { transform: translateX(0); }
  }

  .drawer-header {
    padding: var(--space-5) var(--space-6);
    border-bottom: 1px solid var(--border);
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .close-btn {
    background: transparent;
    border: none;
    color: var(--text-secondary);
    font-size: var(--text-2xl);
    cursor: pointer;
    line-height: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border-radius: 50%;
    transition: all var(--transition);
  }
  .close-btn:hover {
    color: var(--accent);
    background: rgba(255, 255, 255, 0.05);
  }

  .drawer-content {
    flex: 1;
    overflow-y: auto;
    padding: var(--space-6);
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }

  .refresh-btn {
    align-self: flex-start;
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }

  .empty-runs-state {
    text-align: center;
    color: var(--text-secondary);
    padding: var(--space-10) var(--space-6);
    display: flex;
    flex-direction: column;
    align-items: center;
  }

  .runs-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-5);
  }

  .run-log-item {
    background: var(--bg-card);
    border: 1px solid var(--border);
    padding: var(--space-4);
  }
  .run-log-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-bottom: 1px solid var(--border);
    padding-bottom: var(--space-3);
    margin-bottom: var(--space-3);
  }
  .run-status {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    font-size: var(--text-sm);
  }
  .run-status .dot {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    box-shadow: 0 0 8px transparent;
  }
  .run-status .dot.online { background: var(--accent); box-shadow: 0 0 8px var(--accent); }
  .run-status .dot.running { background: var(--warning); animation: pulse 1s infinite alternate; }
  .run-status .dot.danger { background: var(--danger); box-shadow: 0 0 8px var(--danger); }
  .status-label {
    font-size: var(--text-xs);
    text-transform: uppercase;
  }
  .run-time {
    font-size: var(--text-xs);
  }

  @keyframes pulse {
    from { opacity: 0.4; }
    to { opacity: 1; }
  }

  .run-details {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    font-size: var(--text-sm);
  }
  .run-row-meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-4);
    background: var(--bg-primary);
    padding: var(--space-2) var(--space-3);
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
  }
  .run-row {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }

  .run-steps-timeline {
    margin-top: var(--space-4);
    border-top: 1px solid var(--border);
    padding-top: var(--space-4);
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }
  .steps-timeline-title {
    font-size: var(--text-xs);
    text-transform: uppercase;
    color: var(--text-muted);
    letter-spacing: 0.05em;
    font-weight: 600;
  }
  
  .timeline-tree {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    position: relative;
    padding-left: var(--space-4);
  }
  .timeline-tree::before {
    content: '';
    position: absolute;
    left: 4px;
    top: 8px;
    bottom: 8px;
    width: 1px;
    background: var(--border);
  }

  .run-step-block {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    padding: var(--space-3);
    border-radius: var(--radius-md);
    position: relative;
  }
  .run-step-block::before {
    content: '';
    position: absolute;
    left: -19px;
    top: 18px;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--border);
    border: 1px solid var(--bg-primary);
  }
  
  .run-step-title-bar {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    font-size: var(--text-xs);
    cursor: pointer;
    user-select: none;
  }
  .run-step-id {
    font-weight: 600;
    color: var(--text-primary);
    font-family: var(--font-mono);
  }
  .step-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
  }
  .step-dot.success { background: var(--accent); }
  .step-dot.fail { background: var(--danger); }
  
  .badge-run-step {
    font-size: 9px;
    padding: 1px 4px;
  }
  .success-badge { background: rgba(0, 201, 167, 0.1); color: var(--accent); }
  .fail-badge { background: rgba(255, 107, 107, 0.1); color: var(--danger); }
  .expander-arrow {
    margin-left: auto;
    color: var(--text-muted);
    font-size: 8px;
  }

  .step-err {
    font-size: var(--text-xs);
    margin-top: var(--space-2);
    background: rgba(255, 107, 107, 0.08);
    padding: var(--space-2);
    border-radius: var(--radius-sm);
    border: 1px solid rgba(255, 107, 107, 0.2);
  }
  
  /* Dark Terminal Styling for outputs */
  .step-out-terminal {
    margin-top: var(--space-3);
    background: #090e14;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    overflow: hidden;
  }
  .terminal-header {
    background: #141922;
    padding: 4px var(--space-3);
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-bottom: 1px solid var(--border);
    font-size: 10px;
    color: var(--text-muted);
    text-transform: uppercase;
  }
  .copy-terminal-btn {
    background: transparent;
    border: none;
    color: var(--accent);
    cursor: pointer;
    font-size: 10px;
  }
  .copy-terminal-btn:hover {
    text-decoration: underline;
  }
  .step-out {
    padding: var(--space-3);
    font-size: var(--text-xs);
    overflow-x: auto;
    white-space: pre-wrap;
    max-height: 250px;
    overflow-y: auto;
    color: #a9b1d6;
    line-height: 1.5;
  }

  .capitalize {
    text-transform: capitalize;
  }

  @media (max-width: 900px) {
    .layout-grid {
      grid-template-columns: 1fr;
    }
    .drawer {
      width: 100%;
    }
  }
</style>
