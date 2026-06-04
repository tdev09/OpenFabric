<script lang="ts">
  import { onMount } from 'svelte';
  import { files, uploadFile, deleteFile, formatBytes, appConfig } from '$lib/stores/cluster';

  let dragging = false;
  let uploading = false;
  let uploadError = '';

  let showConfirmDelete = false;
  let fileToDeletePath = '';
  let fileToDeleteName = '';

  let brainStatus: any = null;
  let reindexingFile: string | null = null;

  async function loadBrainStatus() {
    try {
      const res = await fetch('/api/brain/status');
      if (res.ok) {
        brainStatus = await res.json();
      }
    } catch {}
  }

  async function reindexFile(filename: string) {
    reindexingFile = filename;
    try {
      await fetch('/api/brain/reindex', { method: 'POST' });
      // Poll to catch completion
      setTimeout(loadBrainStatus, 1500);
    } catch {} finally {
      reindexingFile = null;
    }
  }

  onMount(() => {
    loadBrainStatus();
    const unsubscribe = files.subscribe(() => {
      loadBrainStatus();
    });
    return () => unsubscribe();
  });

  async function handleDrop(e: DragEvent) {
    e.preventDefault();
    dragging = false;
    const dropped = e.dataTransfer?.files;
    if (!dropped?.length) return;
    for (const f of Array.from(dropped)) {
      await upload(f);
    }
  }

  async function handleFileInput(e: Event) {
    const input = e.target as HTMLInputElement;
    if (!input.files?.length) return;
    for (const f of Array.from(input.files)) {
      await upload(f);
    }
    input.value = '';
  }

  async function upload(f: File) {
    uploading = true;
    uploadError = '';
    try {
      await uploadFile(f);
    } catch (err: any) {
      uploadError = err.message ?? 'Upload failed';
    } finally {
      uploading = false;
    }
  }

  function askDelete(path: string, name: string) {
    fileToDeletePath = path;
    fileToDeleteName = name;
    showConfirmDelete = true;
  }

  async function confirmDelete() {
    if (!fileToDeletePath) return;
    showConfirmDelete = false;
    try {
      await deleteFile(fileToDeletePath);
    } catch {}
    fileToDeletePath = '';
    fileToDeleteName = '';
  }

  function syncBadgeClass(status: string) {
    if (status === 'synced')  return 'badge-online';
    if (status === 'syncing') return 'badge-warning';
    return 'badge-offline';
  }

  function fileIcon(name: string): string {
    const ext = name.split('.').pop()?.toLowerCase() ?? '';
    const map: Record<string, string> = {
      pdf: '📄', jpg: '🖼️', jpeg: '🖼️', png: '🖼️', gif: '🖼️', svg: '🖼️',
      mp4: '🎬', mov: '🎬', avi: '🎬',
      mp3: '🎵', wav: '🎵', flac: '🎵',
      zip: '📦', tar: '📦', gz: '📦',
      txt: '📝', md: '📝',
      py: '🐍', js: '📜', ts: '📜', go: '🔵', sh: '⚙️',
    };
    return map[ext] ?? '📁';
  }
</script>

<svelte:head>
  <title>Storage - {$appConfig.project_name}</title>
  <meta name="description" content="Shared fabric storage across all cluster nodes" />
</svelte:head>

<div class="storage-page animate-fade-in">
  <div class="section-header">
    <div>
      <h1>Storage</h1>
      <p class="text-secondary" style="margin-top: 4px; font-size: var(--text-sm)">
        {$files.length} file{$files.length !== 1 ? 's' : ''} in shared storage
      </p>
    </div>
    <label class="btn btn-primary" for="file-upload" id="upload-btn">
      {uploading ? '⏳ Uploading…' : '⬆ Upload File'}
      <input
        id="file-upload"
        type="file"
        multiple
        style="display:none"
        on:change={handleFileInput}
        disabled={uploading}
      />
    </label>
  </div>

  {#if uploadError}
    <div class="banner banner-error" role="alert">{uploadError}</div>
  {/if}

  <!-- Drop zone -->
  <div
    class="drop-zone card"
    class:drag-active={dragging}
    on:dragover|preventDefault={() => dragging = true}
    on:dragleave={() => dragging = false}
    on:drop={handleDrop}
    role="region"
    aria-label="File drop zone"
  >
    {#if dragging}
      <div class="drop-hint">
        <span style="font-size: 40px">📂</span>
        <p>Drop to add to fabric storage</p>
      </div>
    {:else if $files.length === 0}
      <div class="empty-state">
        <div class="empty-icon">🗂️</div>
        <h3>No shared files yet</h3>
        <p>Drag and drop files here or click "Upload File" to add them to the shared fabric.</p>
      </div>
    {:else}
      <div class="table-container">
        <table class="file-table">
          <thead>
            <tr>
              <th>Name</th>
              <th class="hide-mobile">Size</th>
              <th class="hide-mobile">Node</th>
              <th class="hide-mobile">Sync</th>
              <th class="hide-mobile">Brain Index</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {#each $files as file (file.path)}
              <tr class="file-row">
                <td>
                  <div class="file-name">
                    <span class="file-icon">{fileIcon(file.name)}</span>
                    <div class="file-details">
                      <span class="name-text">{file.name}</span>
                      <div class="mobile-meta text-muted">
                        <span>{formatBytes(file.size)}</span>
                        <span>·</span>
                        <span>Node: {file.node_id.slice(0, 6)}…</span>
                        <span>·</span>
                        <span class="badge {syncBadgeClass(file.sync_status)}" style="padding: 1px 6px; font-size: 9px; line-height: 1;">{file.sync_status}</span>
                        <span>·</span>
                        {#if brainStatus && brainStatus.file_statuses && brainStatus.file_statuses[file.name]}
                          {@const indexInfo = brainStatus.file_statuses[file.name]}
                          <span class="brain-index-status indexed" title="Last indexed: {new Date(indexInfo.last_indexed).toLocaleString()}">
                            🧠 Indexed
                          </span>
                        {:else if reindexingFile === file.name}
                          <span class="brain-index-status indexing">
                            ⏳ Indexing
                          </span>
                        {:else}
                          <span class="brain-index-status not-indexed">
                            Not indexed
                          </span>
                        {/if}
                      </div>
                    </div>
                  </div>
                </td>
                <td class="mono text-muted hide-mobile">{formatBytes(file.size)}</td>
                <td class="mono text-muted hide-mobile">{file.node_id.slice(0, 10)}…</td>
                <td class="hide-mobile">
                  <span class="badge {syncBadgeClass(file.sync_status)}">{file.sync_status}</span>
                </td>
                <td class="hide-mobile">
                  {#if brainStatus && brainStatus.file_statuses && brainStatus.file_statuses[file.name]}
                    {@const indexInfo = brainStatus.file_statuses[file.name]}
                    <span class="brain-index-status indexed" title="Last indexed: {new Date(indexInfo.last_indexed).toLocaleString()}">
                      🧠 Indexed ({indexInfo.chunks} chunks)
                    </span>
                  {:else if reindexingFile === file.name}
                    <span class="brain-index-status indexing">
                      ⏳ Indexing…
                    </span>
                  {:else}
                    <span class="brain-index-status not-indexed">
                      Not indexed
                    </span>
                  {/if}
                </td>
                <td>
                  <div class="row-actions">
                    <button
                      class="btn btn-ghost btn-xs reindex-btn"
                      on:click={() => reindexFile(file.name)}
                      title="Re-index this file"
                    >⟳</button>
                    <a
                      href="/api/storage/{encodeURIComponent(file.path)}"
                      class="btn btn-ghost btn-xs"
                      download={file.name}
                      id="download-{file.name}"
                    >↓</a>
                    <button
                      class="btn btn-danger btn-xs"
                      on:click={() => askDelete(file.path, file.name)}
                      id="delete-{file.name}"
                    >✕</button>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </div>

  <!-- Confirm delete dialog -->
  {#if showConfirmDelete}
    <div class="confirm-overlay" on:click|self={() => showConfirmDelete = false} on:keydown={(e) => { if (e.key === 'Escape') showConfirmDelete = false; }} role="button" tabindex="-1" aria-label="Close dialog">
      <div class="confirm-dialog">
        <div class="confirm-icon">🗑️</div>
        <h3 id="confirm-title">Delete file?</h3>
        <p>Are you sure you want to delete <code class="mono">{fileToDeleteName}</code>? This action cannot be undone.</p>
        <div class="confirm-actions">
          <button class="btn btn-ghost btn-sm" on:click={() => showConfirmDelete = false}>Cancel</button>
          <button class="btn btn-danger btn-sm" on:click={confirmDelete}>Yes, delete</button>
        </div>
      </div>
    </div>
  {/if}
</div>


<style>
  .storage-page { display: flex; flex-direction: column; gap: var(--space-5); }

  .drop-zone {
    border: 2px dashed var(--border);
    transition: all var(--transition);
    min-height: 200px;
  }
  .drop-zone.drag-active {
    border-color: var(--accent);
    background: var(--accent-dim);
  }
  .drop-hint {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--space-3);
    min-height: 200px;
    color: var(--accent);
    font-weight: 500;
  }

  .file-table {
    width: 100%;
    border-collapse: collapse;
  }
  .file-table th {
    text-align: left;
    font-size: var(--text-xs);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-muted);
    padding: 0 var(--space-3) var(--space-3);
    border-bottom: 1px solid var(--border);
  }
  .file-row td {
    padding: var(--space-3);
    border-bottom: 1px solid var(--border);
    font-size: var(--text-sm);
    transition: background var(--transition);
  }
  .file-row:hover td { background: var(--bg-card-hover); }
  .file-row:last-child td { border-bottom: none; }

  .file-name {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--text-primary);
  }
  .file-icon { font-size: 18px; flex-shrink: 0; }
  
  .file-details {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .name-text {
    word-break: break-all;
  }
  .mobile-meta {
    display: none;
    align-items: center;
    gap: var(--space-2);
    font-size: var(--text-xs);
    flex-wrap: wrap;
    margin-top: 2px;
  }

  .row-actions { display: flex; gap: var(--space-1); flex-shrink: 0; }
  .btn-xs {
    padding: 2px 8px;
    font-size: var(--text-xs);
    border-radius: var(--radius-sm);
  }
  .reindex-btn {
    opacity: 0;
    transition: opacity var(--transition);
  }
  .file-row:hover .reindex-btn {
    opacity: 1;
  }

  .table-container {
    width: 100%;
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
  }

  @media (max-width: 768px) {
    .hide-mobile {
      display: none !important;
    }
    .mobile-meta {
      display: flex;
    }
    .reindex-btn {
      opacity: 1 !important;
    }
  }
  .brain-index-status {
    font-size: var(--text-xs);
    font-weight: 500;
  }
  .brain-index-status.indexed {
    color: var(--accent);
  }
  .brain-index-status.indexing {
    color: var(--warning);
  }
  .brain-index-status.not-indexed {
    color: var(--text-muted);
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
  .confirm-dialog code { color: var(--accent); }
  .btn-sm {
    padding: 6px 14px;
    font-size: var(--text-sm);
    border-radius: var(--radius-sm);
  }
</style>
