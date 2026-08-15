<script lang="ts">
  import {
    findServicesWithoutCredentials,
    addServiceCredentials,
    type DiscoveredCredential,
  } from "$lib/wallet/integration";
  import type { PBService } from "$lib/stores/services";
  import Modal from "./Modal.svelte";

  interface Props {
    onCredentialsAdded: () => void;
    onClose: () => void;
  }

  let { onCredentialsAdded, onClose }: Props = $props();

  type DiscoveryItem = {
    service: PBService;
    missingCredentials: DiscoveredCredential[];
  };

  let loading = $state(true);
  let error = $state<string | null>(null);
  let discoveries = $state<DiscoveryItem[]>([]);
  let selectedCredentials = $state<Set<string>>(new Set());
  let adding = $state(false);

  // Load discovered services
  $effect(() => {
    loadDiscoveries();
  });

  async function loadDiscoveries() {
    loading = true;
    error = null;

    try {
      discoveries = await findServicesWithoutCredentials();
      // Pre-select all
      const allIds = new Set<string>();
      discoveries.forEach((d) => {
        d.missingCredentials.forEach((c, i) => {
          allIds.add(`${d.service.id}-${i}`);
        });
      });
      selectedCredentials = allIds;
    } catch (err) {
      error = err instanceof Error ? err.message : "Discovery failed";
    } finally {
      loading = false;
    }
  }

  function toggleCredential(serviceId: string, index: number) {
    const key = `${serviceId}-${index}`;
    if (selectedCredentials.has(key)) {
      selectedCredentials.delete(key);
    } else {
      selectedCredentials.add(key);
    }
    selectedCredentials = new Set(selectedCredentials);
  }

  function toggleAll() {
    if (selectedCredentials.size > 0) {
      selectedCredentials = new Set();
    } else {
      const allIds = new Set<string>();
      discoveries.forEach((d) => {
        d.missingCredentials.forEach((c, i) => {
          allIds.add(`${d.service.id}-${i}`);
        });
      });
      selectedCredentials = allIds;
    }
  }

  async function handleAddSelected() {
    adding = true;
    error = null;

    try {
      for (const discovery of discoveries) {
        for (let i = 0; i < discovery.missingCredentials.length; i++) {
          const key = `${discovery.service.id}-${i}`;
          if (selectedCredentials.has(key)) {
            await addServiceCredentials(discovery.missingCredentials[i]);
          }
        }
      }
      onCredentialsAdded();
      onClose();
    } catch (err) {
      error = err instanceof Error ? err.message : "Failed to add credentials";
    } finally {
      adding = false;
    }
  }

  function getCredentialTypeIcon(kind: string): string {
    switch (kind) {
      case "password":
        return "PW";
      case "api_key":
        return "API";
      case "ssh_key":
        return "SSH";
      case "oauth_token":
        return "OAuth";
      case "certificate":
        return "Cert";
      default:
        return "?";
    }
  }

  function getServiceIcon(type: string): string {
    switch (type) {
      case "pocketbase":
        return "DB";
      case "traefik":
        return "LB";
      case "headscale":
        return "VPN";
      case "monitoring":
        return "MON";
      default:
        return "SVC";
    }
  }
</script>

<Modal title="Service Credential Discovery" {onClose} maxWidth="2xl">
  <p class="text-gray-400 text-sm mb-6">
    We found services that could benefit from credential entries in your wallet.
  </p>

  {#if error}
    <div
      class="mb-4 p-3 rounded-lg bg-red-900/30 border border-red-500/50 text-red-300 text-sm"
    >
      {error}
    </div>
  {/if}

  {#if loading}
    <div class="space-y-4">
      {#each Array(3) as _}
        <div class="card animate-pulse h-20"></div>
      {/each}
    </div>
  {:else if discoveries.length === 0}
    <div class="text-center py-8">
      <svg
        class="w-12 h-12 mx-auto mb-4 text-green-400"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M5 13l4 4L19 7"
        />
      </svg>
      <p class="text-gray-300 mb-2">All services covered!</p>
      <p class="text-gray-500 text-sm">
        No services found that need wallet entries.
      </p>
    </div>
  {:else}
    <div class="mb-4 flex items-center justify-between">
      <button
        onclick={toggleAll}
        class="text-sm text-primary hover:text-primary"
      >
        {selectedCredentials.size > 0 ? "Deselect All" : "Select All"}
      </button>
      <span class="text-sm text-gray-500">
        {selectedCredentials.size} selected
      </span>
    </div>

    <div class="space-y-4 mb-6">
      {#each discoveries as discovery}
        <div class="rounded-lg border border-gray-700 bg-gray-800/50 p-4">
          <div class="flex items-center gap-3 mb-3">
            <span
              class="text-xs font-mono px-2 py-1 rounded bg-gray-700 text-primary"
              >{getServiceIcon(discovery.service.type)}</span
            >
            <div>
              <h3 class="text-white font-medium">
                {discovery.service.display_name || discovery.service.name}
              </h3>
              <p class="text-xs text-gray-500">
                {discovery.service.type} • {discovery.service.url || "No URL"}
              </p>
            </div>
          </div>

          <div class="space-y-2">
            {#each discovery.missingCredentials as cred, i}
              {@const key = `${discovery.service.id}-${i}`}
              <button
                onclick={() => toggleCredential(discovery.service.id, i)}
                class="w-full flex items-center gap-3 p-3 rounded-lg border transition-colors {selectedCredentials.has(
                  key,
                )
                  ? 'bg-primary/10 border-primary/50'
                  : 'bg-gray-900/50 border-gray-700 hover:border-gray-600'}"
              >
                <input
                  type="checkbox"
                  checked={selectedCredentials.has(key)}
                  class="w-4 h-4 rounded border-gray-600 bg-gray-800 text-primary"
                  onclick={(e) => e.stopPropagation()}
                  onchange={() => toggleCredential(discovery.service.id, i)}
                />
                <span
                  class="text-xs font-mono px-1.5 py-0.5 rounded bg-gray-700"
                  >{getCredentialTypeIcon(cred.kind)}</span
                >
                <div class="flex-1 text-left">
                  <p class="text-white text-sm">{cred.name}</p>
                  <p class="text-xs text-gray-500">
                    {cred.kind.replace("_", " ")}
                    {#if cred.username}• {cred.username}{/if}
                  </p>
                </div>
              </button>
            {/each}
          </div>
        </div>
      {/each}
    </div>

    <div
      class="p-3 rounded-lg bg-blue-900/20 border border-blue-700/50 text-sm text-blue-200 mb-6"
    >
      <strong>Note:</strong> Credential placeholders will be created. You'll need
      to fill in the actual secrets manually.
    </div>
  {/if}

  <div class="flex justify-end gap-3">
    <button
      onclick={onClose}
      class="px-4 py-2 text-sm bg-gray-700 hover:bg-gray-600 text-white rounded-lg transition-colors"
    >
      Cancel
    </button>
    {#if discoveries.length > 0}
      <button
        onclick={handleAddSelected}
        disabled={adding || selectedCredentials.size === 0}
        class="px-4 py-2 text-sm bg-primary hover:bg-primary/90 disabled:bg-primary/50 disabled:cursor-not-allowed text-white font-medium rounded-lg transition-colors"
      >
        {#if adding}
          Adding...
        {:else}
          Add {selectedCredentials.size} Credential{selectedCredentials.size !==
          1
            ? "s"
            : ""}
        {/if}
      </button>
    {/if}
  </div>
</Modal>
