<script lang="ts">
  let coordinatorIP = '';
  let joinCode = '';
  let isLoading = false;
  let statusMsg = '';
  let statusType: 'success' | 'error' | '' = '';

  async function handleJoin() {
    // Clean up join code (e.g. remove "fabric-" prefix if entered)
    let token = joinCode.trim();
    if (token.toLowerCase().startsWith('fabric-')) {
      token = token.slice(7);
    }

    const isP2P = token.startsWith('ofj_');

    if (!isP2P && (!coordinatorIP || !joinCode)) {
      statusType = 'error';
      statusMsg = 'Please enter both Coordinator IP and Join Code.';
      return;
    }
    if (isP2P && !joinCode) {
      statusType = 'error';
      statusMsg = 'Please enter the Connection Token.';
      return;
    }

    isLoading = true;
    statusMsg = '';
    statusType = '';

    try {
      const endpoint = isP2P ? '/api/cluster/join-p2p' : '/api/cluster/join-remote';
      const reqBody = isP2P
        ? { token: token.trim() }
        : { coordinator_ip: coordinatorIP.trim(), token: token.trim() };

      const res = await fetch(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(reqBody)
      });

      const data = await res.json();
      if (!res.ok) {
        throw new Error(data.error || 'Failed to join cluster');
      }

      statusType = 'success';
      statusMsg = isP2P 
        ? 'Successfully joined the cluster over P2P! This dashboard will now reload cluster state.'
        : 'Successfully joined the cluster! This dashboard will now reload cluster state.';
      coordinatorIP = '';
      joinCode = '';
      
      // Reload page state to see the new cluster after 2 seconds
      setTimeout(() => {
        window.location.reload();
      }, 2000);

    } catch (err: any) {
      statusType = 'error';
      statusMsg = err.message || 'An unexpected error occurred.';
    } finally {
      isLoading = false;
    }
  }
</script>

<div class="card join-cluster-card">
  <h3>Join a cluster</h3>
  <p class="desc text-secondary">Connect this local device to a remote coordinator.</p>

  <div class="form">
    <div class="form-group">
      <label for="coordinator-ip">Coordinator IP / Host</label>
      <input
        type="text"
        id="coordinator-ip"
        class="input"
        placeholder="e.g. 192.168.0.198 or 10.0.0.5"
        bind:value={coordinatorIP}
        disabled={isLoading}
      />
    </div>

    <div class="form-group">
      <label for="join-code">Join Code</label>
      <input
        type="text"
        id="join-code"
        class="input"
        placeholder="e.g. fabric-7x9k2m"
        bind:value={joinCode}
        disabled={isLoading}
      />
    </div>

    <button class="btn btn-primary join-btn" on:click={handleJoin} disabled={isLoading}>
      {isLoading ? 'Joining Cluster...' : 'Join Cluster'}
    </button>
  </div>

  {#if statusMsg}
    <div class="status-banner banner banner-{statusType === 'success' ? 'info' : 'error'} animate-fade-in">
      <span>{statusType === 'success' ? '✓' : '⚠️'}</span>
      <p>{statusMsg}</p>
    </div>
  {/if}
</div>

<style>
  .join-cluster-card {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 380px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
  }

  h3 {
    font-size: var(--text-lg);
    font-weight: 600;
  }

  .desc {
    font-size: var(--text-sm);
    margin-bottom: var(--space-6);
  }

  .form {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    flex-grow: 1;
  }

  .form-group {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }

  label {
    font-size: var(--text-xs);
    font-weight: 600;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  .join-btn {
    margin-top: var(--space-2);
    width: 100%;
    justify-content: center;
  }

  .status-banner {
    margin-top: var(--space-4);
  }
</style>
