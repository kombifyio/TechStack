<script lang="ts">
  import { goto } from "$app/navigation";
  import { onMount } from "svelte";
  import {
    defaultIdentity,
    StackIdentityDisplay,
    StackIdentityEditor,
  } from "$lib/components/open-core";
  import { NotificationPreferences } from "$lib/components/open-core";
  import { parseApiError } from "$lib/api/errors";
  import {
    destroyStack,
    listStacks,
    pruneOrphanStacks,
    type Stack,
  } from "$lib/api/stacks";
  import { getHomelab, renameHomelab } from "$lib/api/homelab";
  import { authStore } from "$lib/stores/auth.svelte";
  import {
    hydrateStackIdentityFromBackend,
    saveStackIdentityToBackend,
    type StackIdentity,
  } from "$lib/stores/stackIdentity";

  let stackIdentityLoading = $state(false);
  let stackIdentityLoaded = $state(false);
  let stackIdentityEditable = $state(false);
  let stackIdentityError = $state("");
  let stackIdentityValue = $state<StackIdentity | null>(null);

  let showOrphanPruneConfirm = $state(false);
  let pruningOrphans = $state(false);
  let orphanPruneError = $state<string | null>(null);
  let orphanPruneSuccess = $state(false);

  // Deployment deletion lives here, not on the dashboard: it is a deliberate,
  // confirmed action rather than a control the operator meets on every visit.
  // The homelab name: generated as "homelab" at creation time, so the owner
  // needs a way to set the name they actually use. It titles the dashboard.
  let homelabName = $state("");
  let homelabNameInput = $state("");
  let homelabNameSaving = $state(false);
  let homelabNameError = $state<string | null>(null);
  let homelabNameSaved = $state(false);
  // The API distinguishes two states the card must not conflate: a 404 (no
  // homelab provisioned at all) and a legal `homelab: null` alongside existing
  // deployments (pre-backfill lanes, PocketBase-only self-host).
  let homelabMissing = $state(false);
  let homelabUnadopted = $state(false);

  let deployments = $state<Stack[]>([]);
  let deploymentsLoading = $state(false);
  let deploymentsError = $state<string | null>(null);
  let deleteCandidate = $state<Stack | null>(null);
  let deleteConfirmName = $state("");
  let deletingDeployment = $state(false);
  let deleteError = $state<string | null>(null);
  const deleteConfirmed = $derived(
    deleteCandidate !== null &&
      deleteConfirmName.trim() === (deleteCandidate?.name ?? "").trim(),
  );

  const fallbackIdentity = $derived({
    name: "",
    characterId: defaultIdentity.characterId,
    animationStyle: defaultIdentity.animationStyle,
    savedAt: null,
    animationEnabled: defaultIdentity.animationEnabled,
    iconStyle: defaultIdentity.iconStyle,
    glowColorOverride: defaultIdentity.glowColorOverride,
  });

  async function loadStackIdentitySettings() {
    stackIdentityLoading = true;
    stackIdentityError = "";

    try {
      const response = await hydrateStackIdentityFromBackend();
      stackIdentityValue = response?.stack_identity ?? null;
      stackIdentityEditable = response?.editable ?? false;
      stackIdentityLoaded = true;
    } catch (err) {
      stackIdentityError =
        err instanceof Error ? err.message : "Failed to load stack identity";
    } finally {
      stackIdentityLoading = false;
    }
  }

  async function handleIdentitySave(updated: StackIdentity) {
    stackIdentityError = "";

    try {
      const response = await saveStackIdentityToBackend(updated);
      stackIdentityValue = response.stack_identity ?? updated;
      stackIdentityEditable = response.editable;
    } catch (err) {
      stackIdentityError =
        err instanceof Error ? err.message : "Failed to save stack identity";
    }
  }

  function openOrphanPruneConfirm() {
    orphanPruneError = null;
    orphanPruneSuccess = false;
    showOrphanPruneConfirm = true;
  }

  function cancelOrphanPrune() {
    if (pruningOrphans) return;
    showOrphanPruneConfirm = false;
  }

  async function confirmOrphanPrune() {
    pruningOrphans = true;
    orphanPruneError = null;

    try {
      await pruneOrphanStacks();
      orphanPruneSuccess = true;
      showOrphanPruneConfirm = false;
      await goto("/stacks", { invalidateAll: true });
    } catch (err) {
      const parsed = parseApiError(err);
      orphanPruneError = parsed.message || "Cleanup failed";
    } finally {
      pruningOrphans = false;
    }
  }

  async function loadHomelab() {
    homelabNameError = null;
    try {
      const view = await getHomelab();
      homelabName = view?.homelab?.name ?? "";
      homelabNameInput = homelabName;
      homelabMissing = view === null;
      homelabUnadopted = view !== null && !view.homelab;
    } catch (err) {
      homelabNameError = parseApiError(err).message || "Failed to load homelab";
    }
  }

  async function saveHomelabName() {
    const next = homelabNameInput.trim();
    if (!next || next === homelabName || homelabNameSaving) return;
    homelabNameSaving = true;
    homelabNameError = null;
    homelabNameSaved = false;
    try {
      const updated = await renameHomelab(next);
      homelabName = updated.name;
      homelabNameInput = updated.name;
      homelabNameSaved = true;
    } catch (err) {
      homelabNameError = parseApiError(err).message || "Rename failed";
    } finally {
      homelabNameSaving = false;
    }
  }

  async function loadDeployments() {
    deploymentsLoading = true;
    deploymentsError = null;
    try {
      deployments = await listStacks();
    } catch (err) {
      deploymentsError =
        parseApiError(err).message || "Failed to load deployments";
      deployments = [];
    } finally {
      deploymentsLoading = false;
    }
  }

  function openDeleteConfirm(deployment: Stack) {
    if (deployment.demo_anchor) return;
    deleteCandidate = deployment;
    deleteConfirmName = "";
    deleteError = null;
  }

  function cancelDelete() {
    if (deletingDeployment) return;
    deleteCandidate = null;
    deleteConfirmName = "";
    deleteError = null;
  }

  async function confirmDelete() {
    const target = deleteCandidate;
    if (!target || deletingDeployment || !deleteConfirmed) return;
    deletingDeployment = true;
    deleteError = null;
    try {
      if (target.legacy) {
        // A legacy row carries no runtime; the orphan path is the only one
        // that can retire it.
        const result = await pruneOrphanStacks(target.id);
        if ((result.pruned_legacy || 0) + (result.pruned_stacks || 0) === 0) {
          throw new Error(
            result.warnings?.[0] ||
              "The legacy entry is still attached to a runtime and cannot be removed as an orphan.",
          );
        }
      } else {
        await destroyStack(target.id);
      }
      deleteCandidate = null;
      deleteConfirmName = "";
      await loadDeployments();
    } catch (err) {
      deleteError = parseApiError(err).message || "Deletion failed";
    } finally {
      deletingDeployment = false;
    }
  }

  async function handleLogout() {
    await authStore.logout({ manualLogin: true });
  }

  onMount(() => {
    void loadStackIdentitySettings();
    void loadHomelab();
    void loadDeployments();
  });
</script>

<main class="mx-auto max-w-4xl p-6 md:p-8">
  <header class="mb-8">
    <h1 class="text-3xl font-bold text-foreground">Settings</h1>
  </header>

  <div class="space-y-6">
    <section class="card">
      <div class="card-content">
        <div
          class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between"
        >
          <div class="min-w-0">
            <h2 class="text-lg font-semibold text-foreground">Account</h2>
            <p class="mt-1 truncate text-sm text-muted-foreground">
              {authStore.userEmail || "Signed in"}
            </p>
          </div>
          <button onclick={handleLogout} class="btn btn-secondary shrink-0">
            Logout
          </button>
        </div>
      </div>
    </section>

    <section class="card" data-testid="settings-homelab-card">
      <div class="card-content">
        <h2 class="text-lg font-semibold text-foreground">Homelab</h2>
        <p class="mt-1 text-sm text-muted-foreground">
          The name of your homelab. It titles your dashboard and groups every
          kit deployment you operate.
        </p>
        {#if homelabMissing}
          <p class="mt-3 text-sm text-muted-foreground">
            No homelab yet — run the creation wizard first.
          </p>
        {:else if homelabUnadopted}
          <p
            class="mt-3 text-sm text-muted-foreground"
            data-testid="settings-homelab-unadopted"
          >
            This homelab predates named homelabs. Naming becomes available once
            its umbrella record exists; your deployments are unaffected.
          </p>
        {:else}
          <div class="mt-3 flex flex-wrap items-end gap-3">
            <div class="min-w-0 flex-1">
              <label
                class="mb-1 block text-sm text-muted-foreground"
                for="settings-homelab-name"
              >
                Name
              </label>
              <input
                id="settings-homelab-name"
                class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
                data-testid="settings-homelab-name-input"
                bind:value={homelabNameInput}
                maxlength="100"
                autocomplete="off"
              />
            </div>
            <button
              type="button"
              class="btn btn-primary shrink-0"
              data-testid="settings-homelab-name-save"
              onclick={saveHomelabName}
              disabled={homelabNameSaving ||
                homelabNameInput.trim() === "" ||
                homelabNameInput.trim() === homelabName}
            >
              {homelabNameSaving ? "Saving..." : "Save"}
            </button>
          </div>
        {/if}
        {#if homelabNameError}
          <p class="mt-3 text-sm text-destructive">{homelabNameError}</p>
        {:else if homelabNameSaved}
          <p class="mt-3 text-sm text-success">Homelab name saved.</p>
        {/if}
      </div>
    </section>

    <section class="card">
      <div class="card-content">
        <div class="mb-4">
          <h2 class="text-lg font-semibold text-foreground">Stack Identity</h2>
        </div>

        {#if stackIdentityLoading}
          <p class="text-sm text-muted-foreground">Loading...</p>
        {:else if stackIdentityEditable && stackIdentityLoaded}
          <StackIdentityEditor
            identity={stackIdentityValue ?? fallbackIdentity}
            onSave={handleIdentitySave}
          />
        {:else if stackIdentityLoaded && stackIdentityValue}
          <StackIdentityDisplay identity={stackIdentityValue} />
          <p
            class="mt-4 rounded-lg border border-border bg-muted/40 p-3 text-sm text-muted-foreground"
          >
            Managed by kombify Cloud.
          </p>
        {:else if stackIdentityLoaded}
          <p class="text-sm text-muted-foreground">
            No stack identity configured.
          </p>
        {/if}

        {#if stackIdentityError}
          <p class="mt-3 text-sm text-destructive">{stackIdentityError}</p>
        {/if}
      </div>
    </section>

    <section class="card">
      <div class="card-content">
        <NotificationPreferences apiBase="/api/v1/notifications" />
      </div>
    </section>

    <section class="card border-destructive/30">
      <div class="card-content">
        <h2 class="mb-4 text-lg font-semibold text-destructive">Danger Zone</h2>

        <div
          class="rounded-lg border border-destructive/30 bg-destructive/5 p-4"
          data-testid="settings-prune-orphans-card"
        >
          <div
            class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between"
          >
            <div class="min-w-0">
              <h3 class="font-medium text-foreground">
                Clean up orphaned entries
              </h3>
              <p class="mt-1 text-sm text-muted-foreground">
                Removes only orphaned stack entries without an active
                server/lease or worker. Active Managed Runtimes and servers
                remain unchanged.
              </p>
            </div>
            <button
              onclick={openOrphanPruneConfirm}
              class="btn btn-secondary shrink-0"
              data-testid="settings-prune-orphans-button"
              disabled={pruningOrphans}
            >
              {pruningOrphans ? "Cleaning up..." : "Clean up"}
            </button>
          </div>

          {#if orphanPruneError}
            <p class="mt-3 text-sm text-destructive">{orphanPruneError}</p>
          {/if}
          {#if orphanPruneSuccess}
            <p class="mt-3 text-sm text-success">
              Cleanup complete. Redirecting...
            </p>
          {/if}
        </div>

        <div
          class="mt-4 rounded-lg border border-destructive/30 bg-destructive/5 p-4"
          data-testid="settings-delete-deployment-card"
        >
          <div class="min-w-0">
            <h3 class="font-medium text-foreground">Delete a deployment</h3>
            <p class="mt-1 text-sm text-muted-foreground">
              Decommissions the deployment's managed runtime and removes its
              entry. To remove a single server instead, use its decommission
              action on the server.
            </p>
          </div>

          {#if deploymentsLoading}
            <p class="mt-3 text-sm text-muted-foreground">
              Loading deployments...
            </p>
          {:else if deploymentsError}
            <p class="mt-3 text-sm text-destructive">{deploymentsError}</p>
          {:else if deployments.length === 0}
            <p class="mt-3 text-sm text-muted-foreground">
              No deployments to delete.
            </p>
          {:else}
            <ul class="mt-3 space-y-2">
              {#each deployments as deployment (deployment.id)}
                <li
                  class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border bg-card p-3"
                  data-testid="settings-deployment-row"
                >
                  <div class="min-w-0">
                    <p class="truncate font-medium text-foreground">
                      {deployment.name}
                    </p>
                    <p class="text-xs text-muted-foreground">
                      {deployment.state || deployment.status || "unknown"}
                    </p>
                  </div>
                  <button
                    type="button"
                    class="btn btn-secondary shrink-0"
                    data-testid="settings-delete-deployment-button"
                    onclick={() => openDeleteConfirm(deployment)}
                    disabled={deployment.demo_anchor || deletingDeployment}
                    title={deployment.demo_anchor
                      ? "The demo deployment is protected and cannot be deleted."
                      : "Delete this deployment"}
                  >
                    Delete
                  </button>
                </li>
              {/each}
            </ul>
          {/if}
        </div>
      </div>
    </section>
  </div>
</main>

{#if deleteCandidate}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-background/80 p-4 backdrop-blur-sm"
    role="dialog"
    aria-modal="true"
    data-testid="settings-delete-deployment-modal"
  >
    <div class="card w-full max-w-md shadow-2xl">
      <div class="card-content">
        <div class="mb-4">
          <h2 class="text-lg font-semibold text-destructive">
            Delete "{deleteCandidate.name}"?
          </h2>
          <p class="mt-1 text-sm text-muted-foreground">
            This decommissions the managed runtime and removes the deployment
            entry. It cannot be undone.
          </p>
        </div>

        <label
          class="mb-1 block text-sm text-muted-foreground"
          for="settings-delete-deployment-name"
        >
          Type <span class="font-medium text-foreground"
            >{deleteCandidate.name}</span
          > to confirm
        </label>
        <input
          id="settings-delete-deployment-name"
          class="mb-4 w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
          data-testid="settings-delete-deployment-input"
          bind:value={deleteConfirmName}
          autocomplete="off"
        />

        {#if deleteError}
          <p class="mb-4 text-sm text-destructive">{deleteError}</p>
        {/if}

        <div class="flex gap-3">
          <button
            onclick={cancelDelete}
            disabled={deletingDeployment}
            class="btn btn-secondary flex-1"
          >
            Cancel
          </button>
          <button
            onclick={confirmDelete}
            disabled={deletingDeployment || !deleteConfirmed}
            data-testid="settings-delete-deployment-confirm"
            class="btn btn-primary flex-1"
          >
            {deletingDeployment ? "Deleting..." : "Delete"}
          </button>
        </div>
      </div>
    </div>
  </div>
{/if}

{#if showOrphanPruneConfirm}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-background/80 p-4 backdrop-blur-sm"
    role="dialog"
    aria-modal="true"
    data-testid="settings-prune-orphans-modal"
  >
    <div class="card w-full max-w-md shadow-2xl">
      <div class="card-content">
        <div class="mb-4">
          <h2 class="text-lg font-semibold text-foreground">
            Clean up orphaned entries?
          </h2>
          <p class="mt-1 text-sm text-muted-foreground">
            Removes only orphaned entries. No infrastructure is destroyed.
          </p>
        </div>

        <div class="mb-4 rounded-lg border border-border bg-muted/40 p-3">
          <p class="text-sm text-foreground">
            Only stack entries without an active server/lease or worker are
            removed. Active Managed Runtimes, servers, and running leases remain
            unchanged. To decommission a deployment completely, use Delete a
            deployment below; for a single server use its decommission action.
          </p>
        </div>

        {#if orphanPruneError}
          <p class="mb-4 text-sm text-destructive">{orphanPruneError}</p>
        {/if}

        <div class="flex gap-3">
          <button
            onclick={cancelOrphanPrune}
            disabled={pruningOrphans}
            class="btn btn-secondary flex-1"
          >
            Cancel
          </button>
          <button
            onclick={confirmOrphanPrune}
            disabled={pruningOrphans}
            data-testid="settings-prune-orphans-confirm"
            class="btn btn-primary flex-1"
          >
            {pruningOrphans ? "Cleaning up..." : "Clean up"}
          </button>
        </div>
      </div>
    </div>
  </div>
{/if}
