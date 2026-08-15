<script lang="ts">
  import { onMount } from "svelte";
  import {
    getAutoInstallStatus,
    installDependencies,
    type AutoInstallStatus,
    type InstallResult,
  } from "$lib/api/autoinstall";

  let status: AutoInstallStatus | null = null;
  let loading = false;
  let installing = false;
  let error: string | null = null;
  let installResults: InstallResult[] | null = null;

  onMount(async () => {
    await checkStatus();
  });

  async function checkStatus() {
    loading = true;
    error = null;

    try {
      status = await getAutoInstallStatus();
    } catch (e: any) {
      error = e.message || "Failed to check dependency status";
    } finally {
      loading = false;
    }
  }

  async function handleInstallAll() {
    installing = true;
    error = null;
    installResults = null;

    try {
      const response = await installDependencies();
      installResults = response.results;

      if (response.failed && response.failed.length > 0) {
        error = `${response.failed.length} installation(s) failed`;
      }

      // Refresh status
      await checkStatus();
    } catch (e: any) {
      error = e.message || "Installation failed";
    } finally {
      installing = false;
    }
  }

  async function handleInstallOne(depName: string) {
    installing = true;
    error = null;

    try {
      const response = await installDependencies({ dependencies: [depName] });
      installResults = response.results;

      await checkStatus();
    } catch (e: any) {
      error = e.message || "Installation failed";
    } finally {
      installing = false;
    }
  }
</script>

<div class="autoinstall-container">
  <div class="header">
    <h2>🔧 Dependency Manager</h2>
    <button on:click={checkStatus} disabled={loading} class="btn-refresh">
      {loading ? "⏳ Checking..." : "🔄 Refresh"}
    </button>
  </div>

  {#if error}
    <div class="alert alert-error">
      <strong>Error:</strong>
      {error}
    </div>
  {/if}

  {#if loading}
    <div class="loading">
      <div class="spinner"></div>
      <p>Checking dependencies...</p>
    </div>
  {:else if status}
    {#if status.count === 0}
      <div class="alert alert-success">
        <strong>✅ All dependencies installed!</strong>
        <p>OpenTofu, Cloudflared, and all required tools are ready.</p>
      </div>
    {:else}
      <div class="alert alert-warning">
        <strong>⚠️ Missing {status.count} dependencies</strong>
        <p>kombify-TechStack requires these tools to function properly.</p>
      </div>

      <div class="dependency-list">
        {#each status.missing as dep}
          <div class="dependency-card">
            <div class="dep-header">
              <h3>{dep.name}</h3>
              {#if dep.required}
                <span class="badge badge-required">Required</span>
              {:else}
                <span class="badge badge-optional">Optional</span>
              {/if}
            </div>
            <p class="dep-reason">{dep.reason}</p>
            <div class="dep-info">
              <code>{dep.executable_name}</code>
              {#if dep.version}
                <span class="version">Version: {dep.version}</span>
              {/if}
            </div>
            <button
              on:click={() => handleInstallOne(dep.name)}
              disabled={installing}
              class="btn-install"
            >
              {installing ? "⏳ Installing..." : "📥 Install"}
            </button>
          </div>
        {/each}
      </div>

      <div class="actions">
        <button
          on:click={handleInstallAll}
          disabled={installing}
          class="btn-install-all"
        >
          {installing ? "⏳ Installing all..." : "📥 Install All Dependencies"}
        </button>
      </div>
    {/if}
  {/if}

  {#if installResults && installResults.length > 0}
    <div class="install-results">
      <h3>Installation Results</h3>
      {#each installResults as result}
        <div class="result-card {result.success ? 'success' : 'error'}">
          <div class="result-header">
            <strong>{result.dependency.name}</strong>
            {#if result.success}
              <span class="status-icon">✅</span>
            {:else}
              <span class="status-icon">❌</span>
            {/if}
          </div>
          {#if result.success}
            <p>
              Installed successfully
              {#if result.version}
                <span class="version">({result.version})</span>
              {/if}
            </p>
            {#if result.installed_path}
              <code class="path">{result.installed_path}</code>
            {/if}
          {:else}
            <p class="error-msg">{result.error || "Installation failed"}</p>
          {/if}
          <small class="strategy">Strategy: {result.strategy}</small>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .autoinstall-container {
    max-width: 800px;
    margin: 0 auto;
    padding: 2rem;
  }

  .header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 2rem;
  }

  .header h2 {
    margin: 0;
    font-size: 1.8rem;
  }

  .btn-refresh {
    padding: 0.5rem 1rem;
    border: 1px solid #ddd;
    border-radius: 4px;
    background: white;
    cursor: pointer;
    font-size: 0.9rem;
  }

  .btn-refresh:hover:not(:disabled) {
    background: #f5f5f5;
  }

  .btn-refresh:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .alert {
    padding: 1rem;
    border-radius: 4px;
    margin-bottom: 1.5rem;
  }

  .alert-success {
    background: #d4edda;
    border: 1px solid #c3e6cb;
    color: #155724;
  }

  .alert-warning {
    background: #fff3cd;
    border: 1px solid #ffc107;
    color: #856404;
  }

  .alert-error {
    background: #f8d7da;
    border: 1px solid #f5c6cb;
    color: #721c24;
  }

  .loading {
    text-align: center;
    padding: 3rem;
  }

  .spinner {
    width: 40px;
    height: 40px;
    margin: 0 auto 1rem;
    border: 4px solid #f3f3f3;
    border-top: 4px solid #3498db;
    border-radius: 50%;
    animation: spin 1s linear infinite;
  }

  @keyframes spin {
    0% {
      transform: rotate(0deg);
    }
    100% {
      transform: rotate(360deg);
    }
  }

  .dependency-list {
    display: grid;
    gap: 1rem;
    margin-bottom: 2rem;
  }

  .dependency-card {
    border: 1px solid #ddd;
    border-radius: 8px;
    padding: 1.5rem;
    background: white;
  }

  .dep-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.5rem;
  }

  .dep-header h3 {
    margin: 0;
    font-size: 1.2rem;
  }

  .badge {
    padding: 0.25rem 0.75rem;
    border-radius: 12px;
    font-size: 0.75rem;
    font-weight: bold;
  }

  .badge-required {
    background: #dc3545;
    color: white;
  }

  .badge-optional {
    background: #6c757d;
    color: white;
  }

  .dep-reason {
    color: #666;
    margin: 0.5rem 0;
  }

  .dep-info {
    display: flex;
    gap: 1rem;
    align-items: center;
    margin: 1rem 0;
  }

  .dep-info code {
    background: #f5f5f5;
    padding: 0.25rem 0.5rem;
    border-radius: 4px;
    font-family: "Courier New", monospace;
  }

  .version {
    color: #666;
    font-size: 0.9rem;
  }

  .btn-install {
    padding: 0.5rem 1rem;
    background: #007bff;
    color: white;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    font-size: 0.9rem;
  }

  .btn-install:hover:not(:disabled) {
    background: #0056b3;
  }

  .btn-install:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .actions {
    text-align: center;
    margin: 2rem 0;
  }

  .btn-install-all {
    padding: 1rem 2rem;
    background: #28a745;
    color: white;
    border: none;
    border-radius: 4px;
    cursor: pointer;
    font-size: 1.1rem;
    font-weight: bold;
  }

  .btn-install-all:hover:not(:disabled) {
    background: #218838;
  }

  .btn-install-all:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .install-results {
    margin-top: 2rem;
    border-top: 2px solid #ddd;
    padding-top: 2rem;
  }

  .install-results h3 {
    margin-bottom: 1rem;
  }

  .result-card {
    border: 1px solid #ddd;
    border-radius: 4px;
    padding: 1rem;
    margin-bottom: 1rem;
  }

  .result-card.success {
    border-left: 4px solid #28a745;
    background: #f8fff9;
  }

  .result-card.error {
    border-left: 4px solid #dc3545;
    background: #fff5f5;
  }

  .result-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.5rem;
  }

  .status-icon {
    font-size: 1.2rem;
  }

  .path {
    display: block;
    background: #f5f5f5;
    padding: 0.5rem;
    border-radius: 4px;
    margin: 0.5rem 0;
    font-family: "Courier New", monospace;
    font-size: 0.85rem;
  }

  .error-msg {
    color: #721c24;
    margin: 0.5rem 0;
  }

  .strategy {
    color: #666;
    font-style: italic;
  }
</style>
