<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { dialog } from '$lib/stores/dialog';
  import {
    models, llmStatus, llmLoading, llmError,
    runableModels, unrunableModels, ollamaReady,
    fetchModels, fetchLLMStatus, pullModel, deleteModel, fmtRAM,
    type ModelFeasibility, type PullProgressEvent
  } from '$lib/stores/llm';
  import { summary, nodes, files, timeAgo, appConfig } from '$lib/stores/cluster';
  import { onDestroy } from 'svelte';

  let activeTab = 'catalog';
  let gpuStatus: any = null;
  let gpuModels: string[] = [];
  let loadingGpu = false;
  
  // Generator states
  let prompt = '';
  let negativePrompt = 'blurry, low quality, distorted, bad hands';
  let selectedModel = '';
  let selectedSize = '1024x1024';
  let steps = 20;
  
  let currentJob: any = null;
  let isGenerating = false;
  let genError = '';
  let pollInterval: any = null;
  let installingModel = false;
  let installTaskId = '';
  
  async function loadGPUData() {
    try {
      const statusRes = await fetch('/api/gpu/status');
      if (statusRes.ok) gpuStatus = await statusRes.json();
      
      const modelsRes = await fetch('/api/gpu/models');
      if (modelsRes.ok) {
        gpuModels = await modelsRes.json();
        if (gpuModels.length > 0 && !selectedModel) {
          selectedModel = gpuModels[0];
        }
      }
    } catch (e) {
      console.error('Failed to load GPU details', e);
    }
  }

  async function handleGenerate() {
    if (!prompt.trim()) return;
    isGenerating = true;
    genError = '';
    currentJob = null;
    
    const [widthStr, heightStr] = selectedSize.split('x');
    const width = parseInt(widthStr, 10);
    const height = parseInt(heightStr, 10);
    
    try {
      const res = await fetch('/api/gpu/generate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          prompt,
          negative_prompt: negativePrompt,
          width,
          height,
          steps,
          model: selectedModel
        })
      });
      
      if (!res.ok) {
        const errData = await res.json().catch(() => ({ error: 'HTTP error' }));
        throw new Error(errData.error ?? 'Failed to submit generation');
      }
      
      currentJob = await res.json();
      
      if (pollInterval) clearInterval(pollInterval);
      pollInterval = setInterval(async () => {
        try {
          const pollRes = await fetch(`/api/gpu/generate/${currentJob.id}`);
          if (pollRes.ok) {
            const updated = await pollRes.json();
            currentJob = updated;
            if (updated.status === 'completed') {
              clearInterval(pollInterval);
              isGenerating = false;
              // Refresh storage files list
              const filesRes = await fetch('/api/storage');
              if (filesRes.ok) {
                const fs = await filesRes.json();
                files.set(fs ?? []);
              }
            } else if (updated.status === 'failed') {
              clearInterval(pollInterval);
              isGenerating = false;
              genError = updated.error ?? 'Generation failed';
            }
          }
        } catch (e) {
          console.error(e);
        }
      }, 2000);
    } catch (err: any) {
      isGenerating = false;
      genError = err.message ?? 'Failed to start generation';
    }
  }

  async function installGPUModel(modelName: string) {
    installingModel = true;
    try {
      const res = await fetch(`/api/gpu/install/${encodeURIComponent(modelName)}`, { method: 'POST' });
      if (res.ok) {
        const data = await res.json();
        installTaskId = data.task_id;
        dialog.alert(`Installation task submitted to scheduler (Task ID: ${installTaskId}). You can monitor progress in the Tasks tab.`, 'Installation Started', '🚀');
      }
    } catch (e) {
      console.error(e);
    } finally {
      installingModel = false;
    }
  }

  onDestroy(() => {
    if (pollInterval) clearInterval(pollInterval);
  });

  $: recentGenerations = $files
    .filter(f => f.path.startsWith('generated/'))
    .sort((a, b) => new Date(b.mod_time).getTime() - new Date(a.mod_time).getTime());

  // Pull state: modelTag → progress 0–100
  let pulling: Record<string, number> = {};
  let pullStatus: Record<string, string> = {};

  // Remove state
  let removing: Record<string, boolean> = {};
  let confirmTarget: string | null = null; // model tag waiting for confirmation

  // Quantization modal target
  let showQuantModalFor: ModelFeasibility | null = null;

  // Selected tag for chat when multiple are downloaded
  let selectedTags: Record<string, string> = {};

  // Cost savings
  let savedCosts = 0;

  // Online node count for bandwidth tip
  $: onlineNodeCount = $nodes.filter(n => n.status === 'online').length;

  // Custom model tag input
  let customTagInput = '';
  let pullingCustom = false;
  let customPullError = '';

  // Collapsible info cards
  let expandedInfo: Record<string, boolean> = {};

  onMount(async () => {
    // Load cost savings
    const saved = localStorage.getItem('openfabric_saved_costs');
    if (saved) {
      savedCosts = parseFloat(saved) || 0;
    }

    await fetchLLMStatus();
    await fetchModels();
    await loadGPUData();
  });

  // Track downloaded tags automatically to pre-select the first option
  $: if ($models) {
    for (const m of $models) {
      if (m.is_downloaded && m.downloaded_tags && m.downloaded_tags.length > 0 && !selectedTags[m.model]) {
        selectedTags[m.model] = m.downloaded_tags[0];
      }
    }
  }

  async function handlePull(model: ModelFeasibility, suffix: string = '') {
    const fullTag = model.model + suffix;
    pulling[fullTag] = 0;
    pullStatus[fullTag] = 'Starting…';
    pulling = pulling;
    showQuantModalFor = null; // close modal

    try {
      await pullModel(fullTag, (p: PullProgressEvent) => {
        pullStatus[fullTag] = p.status;
        if (p.total > 0) {
          pulling[fullTag] = Math.round((p.completed / p.total) * 100);
        }
        pulling = pulling;
      });
      // Refresh model list to update is_downloaded flag
      await fetchLLMStatus();
      await fetchModels();
    } catch (e: any) {
      pullStatus[fullTag] = 'Failed: ' + (e.message ?? 'unknown error');
    } finally {
      delete pulling[fullTag];
      pulling = pulling;
    }
  }

  async function handleCustomPull() {
    if (!customTagInput.trim()) return;
    const tag = customTagInput.trim();
    customPullError = '';
    pulling[tag] = 0;
    pullStatus[tag] = 'Starting…';
    pulling = pulling;
    pullingCustom = true;

    try {
      await pullModel(tag, (p: PullProgressEvent) => {
        pullStatus[tag] = p.status;
        if (p.total > 0) {
          pulling[tag] = Math.round((p.completed / p.total) * 100);
        }
        pulling = pulling;
      });
      customTagInput = '';
      await fetchLLMStatus();
      await fetchModels();
    } catch (e: any) {
      customPullError = e.message ?? 'Failed to pull model';
      pullStatus[tag] = 'Failed';
    } finally {
      delete pulling[tag];
      pulling = pulling;
      pullingCustom = false;
    }
  }

  function requestRemove(tag: string) {
    confirmTarget = tag;
  }

  async function confirmRemove() {
    if (!confirmTarget) return;
    const tag = confirmTarget;
    confirmTarget = null;
    removing[tag] = true;
    removing = removing;
    try {
      await deleteModel(tag);
      await fetchLLMStatus();
      await fetchModels();
    } catch (e: any) {
      dialog.alert('Remove failed: ' + (e.message ?? 'unknown error'), 'Remove Failed', '❌');
    } finally {
      delete removing[tag];
      removing = removing;
    }
  }

  function openChat(modelTag: string) {
    goto(`/models/chat?model=${encodeURIComponent(modelTag)}`);
  }

  function shardSummary(m: ModelFeasibility): string {
    if (!m.shard_plan) return 'Local only';
    const n = m.shard_plan.shards.length;
    if (n === 1) return `Runs on ${m.shard_plan.shards[0].node_name}`;
    const names = m.shard_plan.shards.map(s => s.node_name).join(' → ');
    return `Sharded across ${n} nodes: ${names}`;
  }

  function findFeasibilityForTag(tag: string): ModelFeasibility | null {
    if (!$models) return null;
    let match = $models.find(m => m.model === tag);
    if (match) return match;
    match = $models.find(m => m.downloaded_tags?.includes(tag));
    if (match) return match;
    match = $models.find(m => tag.startsWith(m.model));
    return match || null;
  }

  function toggleInfo(tag: string) {
    expandedInfo[tag] = !expandedInfo[tag];
    expandedInfo = expandedInfo;
  }
</script>

<svelte:head>
  <title>Models - {$appConfig.project_name}</title>
  <meta name="description" content="Run AI models distributed across your {$appConfig.project_name} cluster." />
</svelte:head>

<div class="models-page animate-fade-in">
  <!-- Page header -->
  <div class="page-header">
    <div>
      <h1 class="page-title">Models</h1>
      <p class="page-sub">Distributed LLM inference across your cluster</p>
    </div>
    <div class="header-meta">
      {#if savedCosts > 0}
        <div class="cost-savings-badge">
          <span class="savings-label">💸 Saved this month</span>
          <span class="savings-value">${savedCosts.toFixed(2)}</span>
        </div>
      {/if}
      <div class="cluster-ram-badge">
        <span class="ram-label">Cluster RAM</span>
        <span class="ram-value mono">{fmtRAM($summary.total_ram)}</span>
      </div>
      <button class="btn btn-ghost btn-sm" on:click={() => { fetchLLMStatus(); fetchModels(); }}>
        ↺ Refresh
      </button>
    </div>
  </div>

  <!-- Bandwidth Tip for 3+ Nodes -->
  {#if onlineNodeCount >= 3}
    <div class="banner banner-tip">
      <span class="banner-icon">💡</span>
      <div>
        <strong>Tip:</strong>
        <span>Ethernet cables between nodes improve distributed inference speed by 3–5× vs Wi-Fi.</span>
      </div>
    </div>
  {/if}

  <!-- Ollama not installed warning -->
  {#if $llmStatus && !$llmStatus.ollama_ready}
    <div class="banner banner-warn">
      <span class="banner-icon">⚠️</span>
      <div>
        <strong>Ollama not detected on this device.</strong>
        <span>Install it to run AI models locally.</span>
        <a href="https://ollama.com" target="_blank" rel="noopener" class="btn btn-accent btn-sm" style="margin-left: 12px">
          Install Ollama →
        </a>
      </div>
    </div>
  {/if}

  {#if $llmError}
    <div class="banner banner-error">{$llmError}</div>
  {/if}

  <!-- Navigation Tabs -->
  <div class="tab-header">
    <button class="tab-btn" class:active={activeTab === 'catalog'} on:click={() => activeTab = 'catalog'}>
      📁 Model Catalog
    </button>
    <button class="tab-btn" class:active={activeTab === 'generate'} on:click={() => { activeTab = 'generate'; loadGPUData(); }}>
      🎨 Image Generator
    </button>
  </div>

  {#if activeTab === 'catalog'}
  <!-- Collapsible Minimum Hardware Panel -->
  <details class="hardware-details">
    <summary class="hardware-summary">
      <span class="summary-icon">❓</span> What hardware do I need to run models?
    </summary>
    <div class="hardware-content">
      <p>Distributed inference pooled compute works seamlessly on standard network components. As you add devices, {$appConfig.project_name} shards the transformer layers proportionally.</p>
      <div class="table-container">
        <table class="hardware-table">
          <thead>
            <tr>
              <th>Device Type</th>
              <th>Minimum for Participation</th>
              <th>Recommended</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td class="mono">Raspberry Pi</td>
              <td>Pi 4 with 8GB RAM</td>
              <td>Pi 5 with 8GB RAM</td>
            </tr>
            <tr>
              <td class="mono">Mac</td>
              <td>Any Apple Silicon (M1+)</td>
              <td>M2 Pro or higher</td>
            </tr>
            <tr>
              <td class="mono">Windows / Linux</td>
              <td>8GB RAM, 4-core CPU</td>
              <td>16GB RAM, modern CPU</td>
            </tr>
            <tr>
              <td class="mono">GPU Node</td>
              <td>RTX 3060 12GB</td>
              <td>RTX 3090 24GB</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="hardware-network-info">
        <p><strong>Network recommendations:</strong></p>
        <ul>
          <li>Minimum local bandwidth: <strong>10 Mbps</strong> (100 Mbps recommended)</li>
          <li>Minimum Wi-Fi: <strong>Wi-Fi 5 (802.11ac) 5GHz band</strong></li>
          <li>For clusters of 3+ devices, <strong>wired Ethernet</strong> is strongly recommended to reduce generation latency.</li>
        </ul>
      </div>
    </div>
  </details>

  {#if $llmLoading}
    <div class="loading-state">
      <div class="spinner"></div>
      <span>Checking cluster…</span>
    </div>
  {:else}
    <!-- SECTION 1: PULLED ON YOUR CLUSTER -->
    <section class="model-section">
      <h2 class="section-title">
        <span class="section-dot green"></span>
        Pulled on Your Cluster
      </h2>
      <div class="model-list">
        {#if $llmStatus && $llmStatus.local_models && $llmStatus.local_models.length > 0}
          {#each $llmStatus.local_models as tag (tag)}
            {@const feas = findFeasibilityForTag(tag)}
            <div class="model-card pulled-card" class:distributed={feas && !feas.fits_single_node}>
              <div class="model-info">
                <div class="model-header">
                  <span class="model-status-dot green"></span>
                  <span class="model-name">{tag}</span>
                  {#if feas}
                    <span class="quant-badge">{feas.quantization}</span>
                    {#if !feas.fits_single_node}
                      <span class="distributed-badge">⚡ Distributed</span>
                    {/if}
                  {:else}
                    <span class="quant-badge">Dynamic</span>
                  {/if}
                </div>
                <p class="model-description">
                  {feas ? feas.description : 'Custom model tag pulled via free-text input.'}
                </p>

                {#if feas}
                  <div class="model-meta">
                    <span class="meta-item">
                      <span class="meta-label">RAM required</span>
                      <span class="meta-value mono">{fmtRAM(feas.required_ram)}</span>
                    </span>
                    {#if feas.shard_plan}
                      <span class="meta-item shard-summary">
                        <span class="meta-label">Shard plan</span>
                        <span class="meta-value">{shardSummary(feas)}</span>
                      </span>
                    {/if}
                  </div>
                {/if}

                <!-- Collapsible Info details -->
                {#if expandedInfo[tag] && feas && feas.shard_plan && feas.shard_plan.model}
                  <div class="expanded-info-panel animate-slide-down">
                    <div class="info-grid">
                      <div class="info-row">
                        <span class="info-lbl">Total Layers</span>
                        <span class="info-val mono">{feas.shard_plan.model.total_layers}</span>
                      </div>
                      <div class="info-row">
                        <span class="info-lbl">Attention Heads</span>
                        <span class="info-val mono">{feas.shard_plan.model.head_count}</span>
                      </div>
                      <div class="info-row">
                        <span class="info-lbl">Embedding Length</span>
                        <span class="info-val mono">{feas.shard_plan.model.embed_length}</span>
                      </div>
                      <div class="info-row">
                        <span class="info-lbl">Quantization</span>
                        <span class="info-val mono">{feas.shard_plan.model.quantization || 'unknown'}</span>
                      </div>
                    </div>
                  </div>
                {/if}
              </div>

              <div class="model-actions">
                <button class="btn btn-accent btn-sm" id="chat-{tag.replace(':', '-')}"
                  on:click={() => openChat(tag)}>
                  💬 Chat
                </button>
                {#if feas && feas.shard_plan}
                  <button class="btn btn-secondary btn-sm" on:click={() => toggleInfo(tag)}>
                    ℹ️ {expandedInfo[tag] ? 'Hide' : 'Info'}
                  </button>
                {/if}
                <button
                  class="btn btn-danger btn-sm"
                  id="remove-{tag.replace(':', '-')}"
                  disabled={removing[tag]}
                  on:click={() => requestRemove(tag)}>
                  {removing[tag] ? '⏳ Removing…' : '🗑 Remove'}
                </button>
              </div>
            </div>
          {/each}
        {:else}
          <div class="empty-state-inner">
            <span class="empty-icon-small">📥</span>
            <p>No models pulled yet. Pull one of the catalog models below or enter a custom tag.</p>
          </div>
        {/if}
      </div>
    </section>

    <!-- SECTION 2: AVAILABLE TO PULL -->
    <section class="model-section">
      <h2 class="section-title">
        <span class="section-dot amber"></span>
        Available to Pull
      </h2>
      <div class="model-list">
        {#each $models as m (m.model)}
          {@const pulledVariants = m.downloaded_tags || []}
          <div class="model-card" class:distributed={!m.fits_single_node} class:low-ram-warn={!m.can_run}>
            <div class="model-info">
              <div class="model-header">
                <span class="model-status-dot" class:green={m.is_downloaded} class:amber={!m.is_downloaded}></span>
                <span class="model-name">{m.model}</span>
                <span class="quant-badge">{m.quantization}</span>
                {#if !m.can_run}
                  <span class="low-ram-badge">⚠️ Low RAM</span>
                {/if}
                {#if !m.fits_single_node}
                  <span class="distributed-badge">⚡ Distributed</span>
                {/if}
              </div>
              <p class="model-description">{m.description}</p>
              <div class="model-meta">
                <span class="meta-item">
                  <span class="meta-label">Estimated RAM</span>
                  <span class="meta-value mono">{fmtRAM(m.required_ram)}</span>
                </span>
                {#if !m.can_run}
                  <span class="meta-item shortage">
                    <span class="meta-label">Missing</span>
                    <span class="meta-value mono accent-red">{fmtRAM(Math.max(0, m.required_ram - m.cluster_ram))}</span>
                  </span>
                {/if}
              </div>

              <!-- Pull progress bars -->
              {#each Object.keys(pulling) as tag}
                {#if tag.startsWith(m.model)}
                  <div class="pull-progress">
                    <div class="progress-bar">
                      <div class="progress-fill" style="width: {pulling[tag]}%"></div>
                    </div>
                    <span class="progress-label">{tag.includes('-') ? tag.split('-')[1] : 'base'}: {pullStatus[tag] ?? ''} · {pulling[tag]}%</span>
                  </div>
                {/if}
              {/each}
            </div>

            <div class="model-actions">
              {#if !Object.keys(pulling).some(t => t.startsWith(m.model))}
                <button class="btn btn-secondary btn-sm" id="pull-{m.model.replace(':', '-')}"
                  on:click={() => showQuantModalFor = m}>
                  ⬇ Pull
                </button>
              {/if}
              {#if pulledVariants.length > 0}
                <span class="pulled-variant-badge">✓ {pulledVariants.length} pulled</span>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    </section>

    <!-- SECTION 3: ANY OLLAMA MODEL -->
    <section class="model-section">
      <h2 class="section-title">
        <span class="section-dot blue"></span>
        Any Ollama Model
      </h2>
      <div class="custom-pull-card card glassmorphic animate-fade-in">
        <div class="custom-pull-header">
          <h3>Pull Custom Tag</h3>
          <p class="custom-pull-desc">Enter any official or custom model tag from the <a href="https://ollama.com/library" target="_blank" rel="noopener">Ollama Library</a> (e.g., <code class="mono">gemma:2b</code> or <code class="mono">qwen2.5:0.5b</code>).</p>
        </div>
        
        <div class="custom-pull-body">
          <div class="input-row">
            <input 
              type="text" 
              placeholder="e.g. gemma:2b" 
              bind:value={customTagInput} 
              disabled={pullingCustom}
              class="custom-tag-input"
            />
            <button 
              class="btn btn-accent pull-btn" 
              disabled={pullingCustom || !customTagInput.trim()}
              on:click={handleCustomPull}
            >
              {#if pullingCustom}
                <div class="spinner spinner-white"></div>
                Pulling…
              {:else}
                ⬇ Pull Model
              {/if}
            </button>
          </div>
          <p class="ram-disclaimer">💡 RAM requirement unknown until pulled. We'll dynamically determine hardware compatibility and plan sharding once the weights are downloaded.</p>
          {#if customPullError}
            <div class="custom-pull-error animate-slide-down">⚠️ {customPullError}</div>
          {/if}

          <!-- Pull progress for custom tag -->
          {#each Object.keys(pulling) as tag}
            {#if !findFeasibilityForTag(tag)}
              <div class="pull-progress custom-progress-bar">
                <div class="progress-bar">
                  <div class="progress-fill" style="width: {pulling[tag]}%"></div>
                </div>
                <span class="progress-label">{tag}: {pullStatus[tag] ?? ''} · {pulling[tag]}%</span>
              </div>
            {/if}
          {/each}
        </div>
      </div>
    </section>
  {/if}
  {:else if activeTab === 'generate'}
    <div class="gpu-generate-container card glassmorphic">
      <div class="gen-left">
        <div class="form-group">
          <label for="prompt">Prompt</label>
          <textarea id="prompt" bind:value={prompt} placeholder="A photorealistic mountain lake at sunset, 8k..." rows="3"></textarea>
        </div>
        
        <div class="form-group">
          <label for="neg-prompt">Negative Prompt</label>
          <textarea id="neg-prompt" bind:value={negativePrompt} placeholder="blurry, low quality, distorted..." rows="2"></textarea>
        </div>

        <div class="form-row">
          <div class="form-group">
            <label for="model">Model</label>
            <select id="model" bind:value={selectedModel}>
              {#each gpuModels as m}
                <option value={m}>{m}</option>
              {/each}
              {#if gpuModels.length === 0}
                <option value="">No models available</option>
              {/if}
            </select>
          </div>

          <div class="form-group">
            <label for="size">Size</label>
            <select id="size" bind:value={selectedSize}>
              <option value="1024x1024">Square (1024×1024)</option>
              <option value="1152x896">Landscape (1152×896)</option>
              <option value="896x1152">Portrait (896×1152)</option>
              <option value="512x512">Legacy Square (512×512)</option>
            </select>
          </div>
        </div>

        <div class="generator-action-row">
          <div class="form-group steps-slider">
            <label for="steps">Steps: {steps}</label>
            <input type="range" id="steps" min="10" max="50" bind:value={steps} />
          </div>
          
          <div class="btn-wrap">
            <button class="btn btn-accent pull-btn" disabled={isGenerating || !prompt.trim() || !gpuStatus?.gpu_nodes} on:click={handleGenerate}>
              {#if isGenerating}
                <div class="spinner spinner-white"></div> Generating…
              {:else}
                🎨 Generate
              {/if}
            </button>
          </div>
        </div>
        
        {#if gpuStatus}
          <div class="gpu-telemetry-info">
            <span>🟢 {gpuStatus.gpu_nodes > 0 ? `${gpuStatus.gpu_nodes} GPU Node(s) active` : 'No GPU Nodes active'}</span>
            {#if gpuStatus.gpu_nodes > 0}
              <span class="vram-info">Pooled VRAM: {fmtRAM(gpuStatus.free_vram)} free / {fmtRAM(gpuStatus.total_vram)} total</span>
            {/if}
          </div>
        {/if}
        
        {#if genError}
          <p class="error-msg">⚠️ {genError}</p>
        {/if}
      </div>

      <div class="gen-right">
        <div class="preview-area">
          {#if currentJob}
            {#if currentJob.status === 'pending'}
              <div class="preview-placeholder">
                <div class="pulse-ring"></div>
                <span>Queueing request...</span>
              </div>
            {:else}
              {#if currentJob.status === 'running'}
                <div class="preview-placeholder">
                  <div class="spinner spinner-large"></div>
                  <span>Generating image ({steps} steps)...</span>
                </div>
              {:else}
                {#if currentJob.status === 'completed' && currentJob.result}
                  <div class="preview-img-wrap">
                    <img src="/api/storage/{currentJob.result.storage_path}" alt="Generated result" />
                    <a href="/api/storage/{currentJob.result.storage_path}" download="generation.png" class="btn btn-secondary btn-sm download-btn">⬇ Download</a>
                  </div>
                {:else}
                  {#if currentJob.status === 'failed'}
                    <div class="preview-placeholder error-placeholder">
                      <span>Generation failed</span>
                      <span class="sub-error">{currentJob.error}</span>
                    </div>
                  {/if}
                {/if}
              {/if}
            {/if}
          {:else}
            <div class="preview-placeholder">
              <span>Your generated image will appear here</span>
            </div>
          {/if}
        </div>
      </div>
    </div>

    {#if gpuStatus && gpuStatus.gpu_nodes > 0 && gpuModels.length === 0}
      <div class="banner banner-warn install-assistant-banner">
        <span class="banner-icon">💡</span>
        <div class="install-banner-content">
          <strong>GPU Active but no models found.</strong>
          <span>Install Stable Diffusion model on your GPU node to get started.</span>
          <div class="install-actions">
            <button class="btn btn-accent btn-sm" disabled={installingModel} on:click={() => installGPUModel('sdxl_base')}>Install SDXL</button>
            <button class="btn btn-secondary btn-sm" disabled={installingModel} on:click={() => installGPUModel('flux_schnell')}>Install FLUX.1</button>
          </div>
        </div>
      </div>
    {/if}

    <h3 class="recent-gen-title">Recent Generations</h3>
    {#if recentGenerations.length === 0}
      <div class="empty-state-inner">
        <span class="empty-icon-small">🖼️</span>
        <p>No recent generations found. Generate an image to populate this gallery.</p>
      </div>
    {:else}
      <div class="recent-gen-grid">
        {#each recentGenerations as img}
          <div class="recent-img-card">
            <img src="/api/storage/{img.path}" alt={img.name} />
            <div class="recent-img-overlay">
              <span class="img-date">{timeAgo(img.mod_time)}</span>
              <a href="/api/storage/{img.path}" download={img.name} class="btn btn-ghost btn-sm">⬇ Download</a>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</div>

<!-- Quantization Modal -->
{#if showQuantModalFor}
  <div class="confirm-overlay" on:click|self={() => showQuantModalFor = null} role="dialog" aria-modal="true" aria-labelledby="quant-modal-title">
    <div class="confirm-dialog quant-modal">
      <h3 id="quant-modal-title">Pull Model: {showQuantModalFor.model}</h3>
      <p>Select a weight quantization level. Poorer nodes load fewer layers, while higher precision requires more RAM across the cluster.</p>
      
      <div class="quant-options">
        <button class="quant-option-card" on:click={() => handlePull(showQuantModalFor!, '')}>
          <div class="quant-option-info">
            <span class="quant-option-badge recommended">✅ Recommended (balanced)</span>
            <span class="quant-option-desc">Good quality, fastest execution. Uses 4-bit quantization (Q4_K_M).</span>
          </div>
          <div class="quant-option-ram mono">{fmtRAM(showQuantModalFor.required_ram)} RAM</div>
        </button>

        <button class="quant-option-card" on:click={() => handlePull(showQuantModalFor!, '-q8_0')}>
          <div class="quant-option-info">
            <span class="quant-option-badge quality">🔬 High quality</span>
            <span class="quant-option-desc">Near-original quality, 2× size. Uses 8-bit quantization (Q8_0).</span>
          </div>
          <div class="quant-option-ram mono">{fmtRAM(showQuantModalFor.required_ram * 1.8)} RAM</div>
        </button>

        <button class="quant-option-card" on:click={() => handlePull(showQuantModalFor!, '-fp16')}>
          <div class="quant-option-info">
            <span class="quant-option-badge max">💎 Maximum quality</span>
            <span class="quant-option-desc">Full precision, 3.5× size. Uses 16-bit precision (F16).</span>
          </div>
          <div class="quant-option-ram mono">{fmtRAM(showQuantModalFor.required_ram * 3.5)} RAM</div>
        </button>
      </div>

      <div class="confirm-actions">
        <button class="btn btn-ghost btn-sm" on:click={() => showQuantModalFor = null}>Cancel</button>
      </div>
    </div>
  </div>
{/if}

<!-- Confirm remove dialog -->
{#if confirmTarget}
  <div class="confirm-overlay" on:click|self={() => confirmTarget = null} role="dialog" aria-modal="true" aria-labelledby="confirm-title">
    <div class="confirm-dialog">
      <div class="confirm-icon">🗑️</div>
      <h3 id="confirm-title">Remove model?</h3>
      <p>This will delete <code class="mono">{confirmTarget}</code> from Ollama on this device. You can re-pull it later.</p>
      <div class="confirm-actions">
        <button class="btn btn-ghost btn-sm" on:click={() => confirmTarget = null}>Cancel</button>
        <button class="btn btn-danger btn-sm" on:click={confirmRemove}>Yes, remove</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .models-page { display: flex; flex-direction: column; gap: var(--space-6); }

  .page-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--space-4);
  }
  .page-title {
    font-size: var(--text-2xl);
    font-weight: 700;
    color: var(--text-primary);
    margin: 0;
  }
  .page-sub { font-size: var(--text-sm); color: var(--text-muted); margin: var(--space-1) 0 0; }

  .header-meta { display: flex; align-items: center; gap: var(--space-4); flex-shrink: 0; }
  
  .cost-savings-badge {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 2px;
    background: rgba(16, 185, 129, 0.1);
    border: 1px solid rgba(16, 185, 129, 0.3);
    border-radius: var(--radius-md);
    padding: var(--space-2) var(--space-4);
  }
  .savings-label { font-size: var(--text-xs); color: rgba(16, 185, 129, 0.85); text-transform: uppercase; letter-spacing: .06em; font-weight: 500; }
  .savings-value { font-size: var(--text-lg); color: #10b981; font-weight: 700; }

  .cluster-ram-badge {
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 2px;
    background: var(--bg-card);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-2) var(--space-4);
  }
  .ram-label { font-size: var(--text-xs); color: var(--text-muted); text-transform: uppercase; letter-spacing: .06em; }
  .ram-value { font-size: var(--text-lg); color: var(--accent); font-weight: 700; }

  /* Banners */
  .banner {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-4) var(--space-5);
    border-radius: var(--radius-md);
    font-size: var(--text-sm);
  }
  .banner-warn  { background: rgba(251,191,36,.1);  border: 1px solid rgba(251,191,36,.3); color: #fbbf24; }
  .banner-error { background: rgba(239,68,68,.1);   border: 1px solid rgba(239,68,68,.3);  color: #ef4444; }
  .banner-tip   { background: rgba(245,158,11,.1);  border: 1px solid rgba(245,158,11,.3); color: #f59e0b; }
  .banner-icon { font-size: 1.25rem; }

  /* Collapsible Details Block */
  .hardware-details {
    background: var(--bg-card);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    overflow: hidden;
  }
  .hardware-summary {
    padding: var(--space-4) var(--space-5);
    font-weight: 600;
    font-size: var(--text-sm);
    color: var(--text-primary);
    cursor: pointer;
    user-select: none;
    transition: background var(--transition);
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  .hardware-summary:hover {
    background: rgba(255, 255, 255, 0.02);
  }
  .summary-icon { font-size: 1.1rem; }
  .hardware-content {
    padding: var(--space-5);
    border-top: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .hardware-content p {
    font-size: var(--text-sm);
    color: var(--text-muted);
    margin: 0;
    line-height: 1.5;
  }
  .table-container {
    width: 100%;
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
  }
  .hardware-table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--text-xs);
    margin-top: var(--space-2);
  }
  .hardware-table th, .hardware-table td {
    padding: var(--space-3) var(--space-4);
    text-align: left;
    border-bottom: 1px solid var(--border);
  }
  .hardware-table th {
    color: var(--text-muted);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: .06em;
    font-size: 0.65rem;
    background: var(--bg-tertiary);
  }
  .hardware-table td {
    color: var(--text-primary);
  }
  .hardware-table tr:last-child td { border-bottom: none; }
  .hardware-network-info ul {
    margin: var(--space-2) 0 0;
    padding-left: var(--space-5);
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: var(--text-xs);
    color: var(--text-muted);
  }

  /* Loading */
  .loading-state {
    display: flex; align-items: center; gap: var(--space-3);
    color: var(--text-muted); font-size: var(--text-sm);
    padding: var(--space-8) 0;
  }
  .spinner {
    width: 18px; height: 18px;
    border: 2px solid var(--border);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: spin .8s linear infinite;
    flex-shrink: 0;
  }
  .spinner-white {
    border-color: rgba(255, 255, 255, 0.3);
    border-top-color: #fff;
  }
  @keyframes spin { to { transform: rotate(360deg); } }

  /* Sections */
  .model-section { display: flex; flex-direction: column; gap: var(--space-4); }
  .section-title {
    display: flex; align-items: center; gap: var(--space-2);
    font-size: var(--text-sm); font-weight: 600;
    text-transform: uppercase; letter-spacing: .08em;
    color: var(--text-muted);
  }
  .section-dot {
    width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0;
  }
  .section-dot.green { background: var(--online); box-shadow: 0 0 6px var(--online); }
  .section-dot.amber { background: #f59e0b; box-shadow: 0 0 6px #f59e0b; }
  .section-dot.blue  { background: #3b82f6; box-shadow: 0 0 6px #3b82f6; }

  /* Responsive Grid for Cards */
  .model-list {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: var(--space-4);
  }

  .model-card {
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    background: var(--bg-card);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--space-5);
    transition: all var(--transition);
    height: 100%;
    min-height: 250px;
    box-sizing: border-box;
  }
  .model-card:hover {
    border-color: var(--border-accent);
    box-shadow: var(--shadow-accent);
  }
  .model-card.distributed {
    border-left: 3px solid var(--accent);
  }
  .model-card.low-ram-warn {
    border-left: 3px solid #ef4444;
  }

  .model-info {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    flex-grow: 1;
  }
  .model-header {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    flex-wrap: wrap;
  }

  .model-status-dot {
    width: 10px; height: 10px; border-radius: 50%; flex-shrink: 0;
  }
  .model-status-dot.green { background: var(--online); box-shadow: 0 0 5px var(--online); }
  .model-status-dot.amber { background: #f59e0b; }

  .model-name {
    font-size: var(--text-base);
    font-weight: 700;
    color: var(--text-primary);
    font-family: var(--font-mono);
    word-break: break-all;
  }
  .quant-badge {
    font-size: var(--text-xs);
    font-family: var(--font-mono);
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    padding: 2px 8px;
    border-radius: var(--radius-sm);
    color: var(--text-muted);
  }
  .distributed-badge {
    font-size: var(--text-xs);
    font-weight: 600;
    background: rgba(0,201,167,.12);
    border: 1px solid rgba(0,201,167,.3);
    color: var(--accent);
    padding: 2px 8px;
    border-radius: var(--radius-sm);
  }
  .low-ram-badge {
    font-size: var(--text-xs);
    font-weight: 600;
    background: rgba(239, 68, 68, 0.12);
    border: 1px solid rgba(239, 68, 68, 0.3);
    color: #ef4444;
    padding: 2px 8px;
    border-radius: var(--radius-sm);
  }
  
  .model-description {
    font-size: var(--text-xs);
    color: var(--text-muted);
    margin: 0;
    line-height: 1.5;
    display: -webkit-box;
    -webkit-line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
    text-overflow: ellipsis;
    min-height: 4.5em;
  }

  /* Compact Metadata Box */
  .model-meta {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    background: var(--bg-tertiary);
    border-radius: var(--radius-md);
    padding: var(--space-3);
    margin-top: auto;
  }
  .meta-item {
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-size: var(--text-xs);
    gap: var(--space-2);
  }
  .meta-label {
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: .06em;
    font-size: 0.65rem;
    flex-shrink: 0;
  }
  .meta-value {
    font-size: var(--text-xs);
    color: var(--text-primary);
    text-align: right;
    font-weight: 500;
  }
  .meta-value.accent-red { color: #ef4444; }

  .shard-summary {
    flex-direction: column;
    align-items: stretch;
    gap: 4px;
    border-top: 1px solid rgba(255, 255, 255, 0.05);
    padding-top: var(--space-2);
    margin-top: var(--space-1);
  }
  .shard-summary .meta-label {
    text-align: left;
  }
  .shard-summary .meta-value {
    text-align: left;
    word-break: break-word;
    font-size: 0.7rem;
    line-height: 1.3;
    color: var(--accent);
  }

  /* Info block */
  .expanded-info-panel {
    background: rgba(0, 0, 0, 0.2);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-3);
    margin-top: var(--space-2);
  }
  .info-grid {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .info-row {
    display: flex;
    justify-content: space-between;
    font-size: var(--text-xs);
  }
  .info-lbl { color: var(--text-muted); }
  .info-val { color: var(--text-primary); font-weight: 500; }

  /* Compact Actions Row */
  .model-actions {
    display: flex;
    gap: var(--space-2);
    align-items: center;
    justify-content: flex-start;
    flex-wrap: wrap;
    margin-top: var(--space-4);
    border-top: 1px solid var(--border);
    padding-top: var(--space-3);
    flex-shrink: 0;
  }

  .pulled-variant-badge {
    font-size: var(--text-xs);
    color: var(--online);
    font-weight: 500;
    margin-left: auto;
  }

  /* Pull progress */
  .pull-progress {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    margin-top: var(--space-2);
  }
  .progress-bar {
    height: 4px;
    background: var(--bg-tertiary);
    border-radius: 2px;
    overflow: hidden;
  }
  .progress-fill {
    height: 100%;
    background: var(--accent);
    border-radius: 2px;
    transition: width .3s ease;
  }
  .progress-label {
    font-size: var(--text-xs);
    color: var(--text-muted);
    font-family: var(--font-mono);
  }

  /* Buttons */
  .btn-accent {
    background: var(--accent);
    color: #000;
    font-weight: 600;
  }
  .btn-accent:hover { background: #00b396; }
  .btn-danger {
    background: rgba(239, 68, 68, 0.12);
    border: 1px solid rgba(239, 68, 68, 0.35);
    color: #ef4444;
    font-weight: 600;
  }
  .btn-danger:hover:not(:disabled) {
    background: rgba(239, 68, 68, 0.22);
    border-color: rgba(239, 68, 68, 0.6);
  }
  .btn-danger:disabled { opacity: .5; cursor: not-allowed; }
  .btn-sm { padding: 6px 14px; font-size: var(--text-sm); border-radius: var(--radius-sm); height: 32px; box-sizing: border-box; display: inline-flex; align-items: center; }

  /* Tabs */
  .tab-header {
    display: flex;
    gap: var(--space-2);
    margin-bottom: var(--space-6);
    border-bottom: 1px solid var(--border);
    padding-bottom: 2px;
  }
  .tab-btn {
    background: transparent;
    border: none;
    border-bottom: 2px solid transparent;
    color: var(--text-muted);
    padding: var(--space-3) var(--space-5);
    font-size: var(--text-sm);
    font-weight: 600;
    cursor: pointer;
    transition: all var(--transition);
  }
  .tab-btn:hover {
    color: var(--text-primary);
  }
  .tab-btn.active {
    color: var(--accent);
    border-bottom-color: var(--accent);
  }

  /* GPU Generate Layout */
  .gpu-generate-container {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-6);
    padding: var(--space-6);
    background: rgba(18, 26, 44, 0.4);
    border: 1px solid var(--border);
    border-radius: var(--radius-xl);
  }
  .gen-left {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }
  .form-group {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .form-group label {
    font-size: var(--text-xs);
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: .06em;
    color: var(--text-muted);
  }
  .form-group textarea, .form-group select {
    background: rgba(0, 0, 0, 0.2);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-3) var(--space-4);
    font-size: var(--text-sm);
    color: #fff;
    resize: none;
  }
  .form-group textarea:focus, .form-group select:focus {
    border-color: var(--border-accent);
    outline: none;
  }
  .form-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-4);
  }
  .steps-slider {
    flex-grow: 1;
  }
  .steps-slider input {
    accent-color: var(--accent);
    width: 100%;
  }
  .btn-wrap {
    align-self: flex-end;
    height: 42px;
  }
  .btn-wrap button {
    height: 100%;
  }
  .flex-align-center {
    display: flex;
    gap: var(--space-4);
    align-items: center;
  }
  .generator-action-row {
    display: flex;
    gap: var(--space-4);
    align-items: center;
  }
  .gpu-telemetry-info {
    font-size: var(--text-xs);
    color: var(--text-muted);
    display: flex;
    justify-content: space-between;
    padding-top: var(--space-2);
    border-top: 1px solid var(--border);
  }
  .error-msg {
    color: #ef4444;
    font-size: var(--text-xs);
    margin: 0;
  }

  /* Preview area (Right side) */
  .preview-area {
    width: 100%;
    aspect-ratio: 1/1;
    background: rgba(0, 0, 0, 0.3);
    border: 1px dashed var(--border);
    border-radius: var(--radius-lg);
    display: flex;
    align-items: center;
    justify-content: center;
    overflow: hidden;
    position: relative;
  }
  .preview-placeholder {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-3);
    color: var(--text-muted);
    font-size: var(--text-sm);
  }
  .preview-img-wrap {
    width: 100%;
    height: 100%;
    position: relative;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .preview-img-wrap img {
    max-width: 100%;
    max-height: 100%;
    object-fit: contain;
  }
  .download-btn {
    position: absolute;
    bottom: var(--space-4);
    right: var(--space-4);
    background: rgba(0, 0, 0, 0.6);
    backdrop-filter: blur(4px);
    border-color: rgba(255, 255, 255, 0.15);
  }

  /* Installation assistance */
  .install-assistant-banner {
    margin-top: var(--space-4);
  }
  .install-banner-content {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    gap: var(--space-4);
  }
  .install-actions {
    display: flex;
    gap: var(--space-2);
  }

  /* Recent Generations Grid */
  .recent-gen-title {
    font-size: var(--text-md);
    font-weight: 700;
    color: var(--text-primary);
    margin: var(--space-8) 0 var(--space-4);
  }
  .recent-gen-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: var(--space-4);
  }
  .recent-img-card {
    background: var(--bg-card);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    overflow: hidden;
    aspect-ratio: 1/1;
    position: relative;
    cursor: pointer;
    transition: all var(--transition);
  }
  .recent-img-card:hover {
    border-color: var(--border-accent);
    transform: scale(1.02);
  }
  .recent-img-card img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .recent-img-overlay {
    position: absolute;
    inset: 0;
    background: linear-gradient(to top, rgba(0, 0, 0, 0.8) 0%, transparent 50%);
    display: flex;
    justify-content: space-between;
    align-items: flex-end;
    padding: var(--space-3);
    opacity: 0;
    transition: opacity var(--transition);
  }
  .recent-img-card:hover .recent-img-overlay {
    opacity: 1;
  }
  .img-date {
    font-size: var(--text-xs);
    color: #fff;
    font-weight: 500;
  }
  .recent-img-overlay .btn-ghost {
    background: rgba(255, 255, 255, 0.1);
    color: #fff;
    border-color: transparent;
  }

  /* Spinner sizing */
  .spinner-large {
    width: 32px;
    height: 32px;
    border-width: 3px;
  }
  .sub-error {
    font-size: var(--text-xs);
    color: #ef4444;
    max-width: 250px;
    text-align: center;
  }

  /* Pulse animation for queue placeholder */
  .pulse-ring {
    width: 24px;
    height: 24px;
    border-radius: 50%;
    background: var(--accent);
    box-shadow: 0 0 0 0 rgba(0, 201, 167, 0.7);
    animation: pulse 1.5s infinite cubic-bezier(0.66, 0, 0, 1);
  }
  @keyframes pulse {
    to {
      box-shadow: 0 0 0 16px rgba(0, 201, 167, 0);
    }
  }

  @media (max-width: 860px) {
    .gpu-generate-container {
      grid-template-columns: 1fr;
    }
    .preview-area {
      aspect-ratio: 16/9;
    }
  }

  /* Custom pull block */
  .custom-pull-card {
    background: var(--bg-card);
    border: 1px solid var(--border);
    border-radius: var(--radius-xl);
    padding: var(--space-6);
  }
  .glassmorphic {
    background: rgba(255, 255, 255, 0.02);
    backdrop-filter: blur(10px);
    box-shadow: 0 8px 32px 0 rgba(0, 0, 0, 0.2);
  }
  .custom-pull-header h3 { font-size: var(--text-lg); font-weight: 700; color: var(--text-primary); margin: 0; }
  .custom-pull-desc { font-size: var(--text-sm); color: var(--text-muted); margin: var(--space-1) 0 0; }
  .custom-pull-desc code { color: var(--accent); }
  .custom-pull-body { display: flex; flex-direction: column; gap: var(--space-4); margin-top: var(--space-5); }
  .input-row { display: flex; gap: var(--space-3); }
  .custom-tag-input {
    flex-grow: 1;
    background: rgba(0,0,0,0.2);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-3) var(--space-4);
    font-size: var(--text-sm);
    color: #fff;
    font-family: var(--font-mono);
  }
  .custom-tag-input:focus { border-color: var(--border-accent); outline: none; }
  .pull-btn { height: auto; padding: var(--space-3) var(--space-6); border-radius: var(--radius-md); font-weight: 600; display: inline-flex; align-items: center; gap: 8px; }
  .ram-disclaimer { font-size: var(--text-xs); color: var(--text-muted); margin: 0; }
  .custom-pull-error { font-size: var(--text-xs); color: #ef4444; margin-top: 4px; }
  .custom-progress-bar { margin-top: var(--space-4); }

  /* Quant Selection Modal */
  .quant-options {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    width: 100%;
    margin: var(--space-4) 0;
  }
  .quant-option-card {
    display: flex;
    justify-content: space-between;
    align-items: center;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-4);
    cursor: pointer;
    text-align: left;
    transition: all var(--transition);
    width: 100%;
    box-sizing: border-box;
    color: var(--text-primary);
  }
  .quant-option-card:hover {
    border-color: var(--border-accent);
    background: rgba(255, 255, 255, 0.02);
  }
  .quant-option-info {
    display: flex;
    flex-direction: column;
    gap: 4px;
    align-items: flex-start;
  }
  .quant-option-badge {
    font-size: var(--text-xs);
    font-weight: 700;
    padding: 2px 8px;
    border-radius: var(--radius-sm);
  }
  .quant-option-badge.recommended { background: rgba(0, 201, 167, 0.12); color: var(--accent); }
  .quant-option-badge.quality { background: rgba(245, 158, 11, 0.12); color: #f59e0b; }
  .quant-option-badge.max { background: rgba(59, 130, 246, 0.12); color: #3b82f6; }
  .quant-option-desc { font-size: var(--text-xs); color: var(--text-muted); line-height: 1.4; }
  .quant-option-ram { font-size: var(--text-sm); font-weight: 600; color: var(--text-primary); margin-left: var(--space-4); flex-shrink: 0; }
  .quant-modal { max-width: 600px; }

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
  .confirm-dialog code { color: var(--accent); }
  .confirm-actions { display: flex; gap: var(--space-3); margin-top: var(--space-2); }

  .empty-state-inner {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    grid-column: 1 / -1;
    padding: var(--space-8);
    background: rgba(0, 0, 0, 0.1);
    border: 1px dashed var(--border);
    border-radius: var(--radius-lg);
    text-align: center;
    color: var(--text-muted);
  }
  .empty-icon-small { font-size: 1.5rem; margin-bottom: 8px; }
  .empty-state-inner p { font-size: var(--text-xs); margin: 0; }

  @media (max-width: 768px) {
    .page-header {
      flex-direction: column;
      align-items: stretch;
      gap: var(--space-4);
    }
    .header-meta {
      width: 100%;
      justify-content: space-between;
      flex-wrap: wrap;
      gap: var(--space-2);
    }
    .cost-savings-badge, .cluster-ram-badge {
      flex-grow: 1;
      align-items: center;
    }
  }

  @media (max-width: 600px) {
    .input-row {
      flex-direction: column;
    }
    .pull-btn {
      width: 100%;
      justify-content: center;
    }
    .generator-action-row {
      flex-direction: column;
      align-items: stretch;
      gap: var(--space-3);
    }
    .btn-wrap {
      width: 100%;
      height: 42px;
      align-self: auto;
    }
    .btn-wrap button {
      width: 100%;
    }
    .gpu-telemetry-info {
      flex-direction: column;
      gap: 6px;
      align-items: flex-start;
    }
  }

  @media (max-width: 480px) {
    .quant-option-card {
      flex-direction: column;
      align-items: flex-start;
      gap: var(--space-3);
    }
    .quant-option-ram {
      margin-left: 0;
      align-self: flex-end;
    }
    .form-row {
      grid-template-columns: 1fr;
      gap: var(--space-3);
    }
  }
</style>
