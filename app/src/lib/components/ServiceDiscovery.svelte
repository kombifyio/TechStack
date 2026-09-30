<script lang="ts">
  import { tr, trn } from "#lib/i18n.svelte.js";
  import {
    findServicesWithoutCredentials,
    addServiceCredentials,
    type CredentialService,
    type DiscoveredCredential,
  } from "#lib/wallet/integration.js";
  import Modal from "./Modal.svelte";

  interface Props {
    onCredentialsAdded: () => void;
    onClose: () => void;
  }

  let { onCredentialsAdded, onClose }: Props = $props();

  type DiscoveryItem = {
    service: CredentialService;
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
      error = err instanceof Error ? err.message : tr("ui.serviceDiscovery.discoveryFailed");
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
      error = err instanceof Error ? err.message : tr("ui.serviceDiscovery.failedToAddCredentials");
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
        return tr("ui.credentialForm.cert");
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

<Modal title={tr("ui.discovery.title")} {onClose} maxWidth="2xl">
  <p class="text-muted-foreground text-sm mb-6">
    {tr("ui.discovery.found")}
  </p>

  {#if error}
    <div
      class="mb-4 p-3 rounded-lg bg-destructive/10 border border-destructive/30 text-destructive text-sm"
    >
      {error}
    </div>
  {/if}

  {#if loading}
    <div class="space-y-4">
      {#each Array(3) as _}
        <div data-kx="plate" class="animate-pulse h-20"></div>
      {/each}
    </div>
  {:else if discoveries.length === 0}
    <div class="text-center py-8">
      <svg
        class="w-12 h-12 mx-auto mb-4 text-success"
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
      <p class="text-foreground mb-2">{tr("ui.discovery.allCovered")}</p>
      <p class="text-muted-foreground text-sm">
        {tr("ui.discovery.noneNeeded")}
      </p>
    </div>
  {:else}
    <div class="mb-4 flex items-center justify-between">
      <button
        onclick={toggleAll}
        class="text-sm text-primary hover:text-primary"
      >
        {selectedCredentials.size > 0 ? tr("ui.serviceDiscovery.deselectAll") : tr("ui.serviceDiscovery.selectAll")}
      </button>
      <span class="text-sm text-muted-foreground">
        {tr("ui.discovery.selected", { count: selectedCredentials.size })}
      </span>
    </div>

    <div class="space-y-4 mb-6">
      {#each discoveries as discovery}
        <div data-kx="plate" class="p-4">
          <div class="flex items-center gap-3 mb-3">
            <span data-kx="tag" class="text-xs font-mono px-2 py-1 text-primary"
              >{getServiceIcon(discovery.service.type)}</span
            >
            <div>
              <h3 class="text-foreground font-medium">
                {discovery.service.display_name || discovery.service.name}
              </h3>
              <p class="text-xs text-muted-foreground">
                {discovery.service.type} • {discovery.service.url || tr("ui.serviceDiscovery.noUrl")}
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
                  : 'bg-background/50 border-border hover:border-muted-foreground'}"
              >
                <input
                  type="checkbox"
                  checked={selectedCredentials.has(key)}
                  class="w-4 h-4 rounded border-border bg-input text-primary"
                  onclick={(e) => e.stopPropagation()}
                  onchange={() => toggleCredential(discovery.service.id, i)}
                />
                <span data-kx="tag" class="text-xs font-mono px-1.5 py-0.5"
                  >{getCredentialTypeIcon(cred.kind)}</span
                >
                <div class="flex-1 text-left">
                  <p class="text-foreground text-sm">{cred.name}</p>
                  <p class="text-xs text-muted-foreground">
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
      class="p-3 rounded-lg bg-info/10 border border-info/30 text-sm text-info mb-6"
    >
      <strong>{tr("ui.discovery.noteLead")}</strong> {tr("ui.discovery.noteBody")}
    </div>
  {/if}

  <div class="flex justify-end gap-3">
    <button
      onclick={onClose}
      data-kx="control"
      class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
    >
      {tr("common.cancel")}
    </button>
    {#if discoveries.length > 0}
      <button
        onclick={handleAddSelected}
        disabled={adding || selectedCredentials.size === 0}
        data-kx="control"
        data-variant="primary"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
      >
        {#if adding}
          {tr("ui.serviceDiscovery.adding")}
        {:else}
          {trn("ui.discovery.addCredentials", selectedCredentials.size)}
        {/if}
      </button>
    {/if}
  </div>
</Modal>
