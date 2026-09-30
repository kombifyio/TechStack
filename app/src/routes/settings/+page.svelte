<script lang="ts">
  import { goto } from "$app/navigation";
  import { onMount } from "svelte";
  import { tr, trParts } from "#lib/i18n.svelte.js";
  import {
    defaultIdentity,
    StackIdentityDisplay,
    StackIdentityEditor,
  } from "#lib/components/open-core/index.js";
  import { NotificationPreferences } from "#lib/components/open-core/index.js";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    applyOrphanCleanup,
    destroyStack,
    listKitDeployments,
    planOrphanCleanup,
    type KitDeployment,
    type StackPruneResponse,
  } from "#lib/api/stacks.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { theme, type Theme } from "#lib/stores/theme.js";
  import {
    dashboardPreset,
    dashboardOwnerKey,
  } from "#lib/stores/dashboardPreset.svelte.js";
  import {
    DASHBOARD_PRESETS,
    isPresetAvailable,
  } from "#lib/dashboard/presets.js";
  import {
    FINISHES,
    NAV_FLYOUT_PREFERENCES,
    type Finish,
  } from "@kombiverselabs/design/contract";
  import { PageHeader } from "@kombiverselabs/ui/shell";
  import Button from "#lib/components/ui/Button.svelte";
  import LanguageSwitcher from "#lib/components/ui/LanguageSwitcher.svelte";
  import {
    hydrateStackIdentityFromBackend,
    saveStackIdentityToBackend,
    type StackIdentity,
  } from "#lib/stores/stackIdentity.js";

  // Appearance card state. `deviceFinish` mirrors whether this device holds
  // an explicit finish choice (section-4 tier 2); picking a finish sets it,
  // "Follow account" clears it and the account default (tier 3) shows again.
  let deviceFinishChosen = $state(false);
  const APPEARANCE_MODES: readonly Theme[] = ["dark", "light", "system"];
  const themePreference = theme.preference;
  const resolvedAppearance = theme.resolved;
  // The finish swatches render the real package recipes in miniature, so
  // they need the RESOLVED appearance the page is showing, not the `system`
  // preference — the same resolution the theme store applies to the root.
  const swatchAppearance = $derived($resolvedAppearance);

  function pickFinish(finish: Finish) {
    theme.setFinish(finish);
    deviceFinishChosen = true;
  }

  function followAccount() {
    theme.followAccountFinish();
    deviceFinishChosen = false;
  }

  let stackIdentityLoading = $state(false);
  let stackIdentityLoaded = $state(false);
  let stackIdentityEditable = $state(false);
  let stackIdentityError = $state("");
  let stackIdentityValue = $state<StackIdentity | null>(null);

  let showOrphanPruneConfirm = $state(false);
  let pruningOrphans = $state(false);
  let orphanPruneError = $state<string | null>(null);
  let orphanPruneSuccess = $state(false);
  let orphanPrunePlan = $state<StackPruneResponse | null>(null);

  // Deployment deletion lives here, not on the dashboard: it is a deliberate,
  // confirmed action rather than a control the operator meets on every visit.
  // The homelab name: generated as "homelab" at creation time, so the owner
  // needs a way to set the name they actually use. It titles the dashboard.
  // The API distinguishes two states the card must not conflate: a 404 (no
  // homelab provisioned at all) and a legal `homelab: null` alongside existing
  // deployments (pre-backfill lanes, PocketBase-only self-host).

  let deployments = $state<KitDeployment[]>([]);
  let deploymentsLoading = $state(false);
  let deploymentsError = $state<string | null>(null);
  let deleteCandidate = $state<KitDeployment | null>(null);
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
        err instanceof Error ? err.message : tr("ui.settings.failedToLoadHomelabIdentity");
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
        err instanceof Error ? err.message : tr("ui.settings.failedToSaveHomelabIdentity");
    }
  }

  async function openOrphanPruneConfirm() {
    pruningOrphans = true;
    orphanPruneError = null;
    orphanPruneSuccess = false;
    orphanPrunePlan = null;
    try {
      orphanPrunePlan = await planOrphanCleanup();
      showOrphanPruneConfirm = true;
    } catch (err) {
      const parsed = parseApiError(err);
      orphanPruneError = parsed.message || tr("ui.settings.cleanupReviewFailed");
    } finally {
      pruningOrphans = false;
    }
  }

  function cancelOrphanPrune() {
    if (pruningOrphans) return;
    showOrphanPruneConfirm = false;
    orphanPrunePlan = null;
  }

  async function confirmOrphanPrune() {
    pruningOrphans = true;
    orphanPruneError = null;

    try {
      const plan = orphanPrunePlan;
      if (!plan || plan.candidates.length === 0) {
        showOrphanPruneConfirm = false;
        orphanPruneSuccess = true;
        return;
      }
      await applyOrphanCleanup(plan.digest);
      orphanPruneSuccess = true;
      showOrphanPruneConfirm = false;
      orphanPrunePlan = null;
      // SvelteKit 3 deprecates invalidateAll in favour of refreshAll.
      await goto("/dashboard", { refreshAll: true });
    } catch (err) {
      const parsed = parseApiError(err);
      orphanPruneError = parsed.message || tr("ui.settings.cleanupFailed");
    } finally {
      pruningOrphans = false;
    }
  }

  async function loadDeployments() {
    deploymentsLoading = true;
    deploymentsError = null;
    try {
      deployments = await listKitDeployments();
    } catch (err) {
      deploymentsError =
        parseApiError(err).message || tr("ui.settings.failedToLoadDeployments");
      deployments = [];
    } finally {
      deploymentsLoading = false;
    }
  }

  function openDeleteConfirm(deployment: KitDeployment) {
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
        const plan = await planOrphanCleanup(target.id);
        if (plan.candidates.length === 0) {
          throw new Error(
            plan.warnings?.[0] ||
              tr("ui.settings.thisEntryIsNotVerified"),
          );
        }
        await applyOrphanCleanup(plan.digest, target.id);
      } else {
        await destroyStack(target.id);
      }
      deleteCandidate = null;
      deleteConfirmName = "";
      await loadDeployments();
    } catch (err) {
      deleteError = parseApiError(err).message || tr("ui.settings.deletionFailed");
    } finally {
      deletingDeployment = false;
    }
  }

  async function handleLogout() {
    await authStore.logout({ manualLogin: true });
  }

  onMount(() => {
    deviceFinishChosen = theme.hasDeviceFinish();
    void loadStackIdentitySettings();
    void loadDeployments();
  });

  const finishValue = theme.finish;
  const navFlyoutValue = theme.navFlyout;
  const navFlyoutLabels: Record<
    (typeof NAV_FLYOUT_PREFERENCES)[number],
    string
  > = $derived({
    auto: tr("ui.settings.followFinish"),
    flag: tr("ui.settings.flag"),
    overlap: tr("ui.settings.overlap"),
    tether: tr("ui.settings.tether"),
    float: tr("ui.settings.float"),
  });
</script>

<main class="mx-auto max-w-4xl p-6 md:p-8">
  <PageHeader title={tr("ui.settings.settings")} />

  <div class="space-y-6">
    <section data-kx="plate">
      <div class="p-6">
        <div
          class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between"
        >
          <div class="min-w-0">
            <h2 class="text-lg font-semibold text-foreground">{tr("ui.settings.account")}</h2>
            <p class="mt-1 truncate text-sm text-muted-foreground">
              {authStore.userEmail || tr("ui.settings.signedIn")}
            </p>
          </div>
          <Button variant="secondary" class="shrink-0" onclick={handleLogout}>
            {tr("ui.settings.logout")}
          </Button>
        </div>
      </div>
    </section>

    <section id="language" class="scroll-mt-6" data-kx="plate">
      <div class="p-6">
        <div
          class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between"
        >
          <div class="min-w-0">
            <h2 class="text-lg font-semibold text-foreground">
              {tr("settings.language")}
            </h2>
            <p class="mt-1 text-sm text-muted-foreground">
              {tr("settings.language.description")}
            </p>
          </div>
          <LanguageSwitcher
            class="rounded-lg border border-border bg-card px-3 py-1.5 text-sm text-foreground"
          />
        </div>
      </div>
    </section>

    <section
      id="appearance"
      class="scroll-mt-6"
      data-kx="plate"
      data-testid="settings-appearance-card"
    >
      <div class="p-6">
        <h2 class="text-lg font-semibold text-foreground">{tr("ui.settings.appearance")}</h2>
        <p class="mt-1 text-sm text-muted-foreground">
          {tr("ui.settings.lightOrDarkAndThe")}
        </p>
        <div class="mt-4 flex flex-col gap-4">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <span class="text-sm font-medium text-foreground">{tr("ui.settings.mode")}</span>
            <div
              class="flex flex-wrap gap-1"
              role="radiogroup"
              aria-label={tr("ui.settings.appearanceMode")}
              data-testid="settings-appearance-mode"
            >
              <button
                type="button"
                role="radio"
                aria-checked={$themePreference === null}
                data-kx={$themePreference === null
                  ? "control selection"
                  : "control"}
                class="rounded-lg px-3 py-1.5 text-sm"
                onclick={() => theme.followDefaultTheme()}
                >{tr("appearance.inherit")}</button
              >
              {#each APPEARANCE_MODES as mode}
                <button
                  type="button"
                  role="radio"
                  aria-checked={$themePreference === mode}
                  data-kx={$themePreference === mode
                    ? "control selection"
                    : "control"}
                  class="rounded-lg px-3 py-1.5 text-sm capitalize"
                  onclick={() => theme.set(mode)}
                >
                  {tr(`appearance.${mode}`)}
                </button>
              {/each}
            </div>
          </div>
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div class="flex items-center gap-3">
              <span class="text-sm font-medium text-foreground">{tr("ui.settings.surface")}</span>
              {#if deviceFinishChosen}
                <button
                  type="button"
                  data-kx="control"
                  class="rounded-lg px-2 py-1 text-xs text-muted-foreground"
                  data-testid="settings-finish-follow-account"
                  onclick={followAccount}
                >
                  {tr("ui.settings.followAccount")}
                </button>
              {/if}
            </div>
            <div
              class="flex flex-wrap gap-1"
              role="radiogroup"
              aria-label={tr("ui.settings.surfaceFinish")}
              data-testid="settings-finish"
            >
              {#each FINISHES as finishOption}
                <button
                  type="button"
                  role="radio"
                  aria-checked={$finishValue === finishOption}
                  data-kx={$finishValue === finishOption
                    ? "control selection"
                    : "control"}
                  class="flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm capitalize"
                  onclick={() => pickFinish(finishOption)}
                >
                  <!-- A miniature of the finish itself. The swatch stamps the
                       finish and the resolved appearance on its own subtree, so
                       the package's real plate and selection recipes render at a
                       quarter scale — nothing here draws a second version of a
                       design (KOMBIFY-DESIGN-SYSTEM-STANDARD §6). -->
                  <span
                    class="finish-swatch"
                    aria-hidden="true"
                    data-finish={finishOption}
                    data-appearance={swatchAppearance}
                  >
                    <span class="finish-swatch-scene"></span>
                    <span class="finish-swatch-frame">
                      <span class="finish-swatch-plate" data-kx="plate">
                        <span
                          class="finish-swatch-pill"
                          data-kx="control selection"
                        ></span>
                      </span>
                    </span>
                  </span>
                  {finishOption}
                </button>
              {/each}
            </div>
          </div>
          <div class="flex flex-wrap items-center justify-between gap-3">
            <span class="text-sm font-medium text-foreground">
              {tr("ui.settings.navigationPreview")}
            </span>
            <div
              class="flex flex-wrap gap-1"
              role="radiogroup"
              aria-label={tr("ui.settings.navigationPreviewShape")}
              data-testid="settings-nav-flyout"
            >
              {#each NAV_FLYOUT_PREFERENCES as shape (shape)}
                <button
                  type="button"
                  role="radio"
                  aria-checked={$navFlyoutValue === shape}
                  data-kx={$navFlyoutValue === shape
                    ? "control selection"
                    : "control"}
                  class="rounded-lg px-3 py-1.5 text-sm"
                  onclick={() => theme.setNavFlyout(shape)}
                >
                  {navFlyoutLabels[shape]}
                </button>
              {/each}
            </div>
          </div>
          <div class="flex flex-col gap-2">
            <div class="flex flex-wrap items-center justify-between gap-3">
              <span class="text-sm font-medium text-foreground">{tr("ui.settings.dashboard")}</span>
              <div
                class="flex flex-wrap gap-1"
                role="radiogroup"
                aria-label={tr("ui.settings.dashboardLayout")}
                data-testid="settings-dashboard-preset"
              >
                {#each DASHBOARD_PRESETS.filter( (preset) => isPresetAvailable( preset.id, { ownerKey: dashboardOwnerKey() } ) ) as preset (preset.id)}
                  <button
                    type="button"
                    role="radio"
                    aria-checked={dashboardPreset.current === preset.id}
                    data-kx={dashboardPreset.current === preset.id
                      ? "control selection"
                      : "control"}
                    class="rounded-lg px-3 py-1.5 text-sm"
                    title={preset.description}
                    onclick={() => dashboardPreset.set(preset.id)}
                  >
                    {preset.label}
                  </button>
                {/each}
              </div>
            </div>
            <p class="text-xs text-muted-foreground">
              {DASHBOARD_PRESETS.find(
                (preset) => preset.id === dashboardPreset.current,
              )?.description} {tr("ui.settings.savedOnDevice")}
            </p>
          </div>
        </div>
      </div>
    </section>

    <section data-kx="plate">
      <div class="p-6">
        <div class="mb-4">
          <h2 class="text-lg font-semibold text-foreground">
            {tr("ui.settings.homelabIdentity")}
          </h2>
        </div>

        {#if stackIdentityLoading}
          <p class="text-sm text-muted-foreground">{tr("ui.login.loading")}</p>
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
            {tr("ui.settings.managedByKombifyCloud")}
          </p>
        {:else if stackIdentityLoaded}
          <p class="text-sm text-muted-foreground">
            {tr("ui.settings.noHomelabIdentityConfigured")}
          </p>
        {/if}

        {#if stackIdentityError}
          <p class="mt-3 text-sm text-destructive">{stackIdentityError}</p>
        {/if}
      </div>
    </section>

    <section data-kx="plate">
      <div class="p-6">
        <NotificationPreferences apiBase="/api/v1/notifications" />
      </div>
    </section>

    <section class="rounded-xl border border-destructive/30 bg-destructive/5">
      <div class="p-6">
        <h2 class="mb-4 text-lg font-semibold text-destructive">{tr("ui.settings.dangerZone")}</h2>

        <div
          class="rounded-lg border border-destructive/30 bg-destructive/5 p-4"
          data-testid="settings-prune-orphans-card"
        >
          <div
            class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between"
          >
            <div class="min-w-0">
              <h3 class="font-medium text-foreground">{tr("ui.settings.cleanUpTestResidue")}</h3>
              <p class="mt-1 text-sm text-muted-foreground">
                {tr("ui.settings.reviewsExactOwnerScopedE2e")}
              </p>
            </div>
            <Button
              variant="secondary"
              class="shrink-0"
              testId="settings-prune-orphans-button"
              onclick={openOrphanPruneConfirm}
              disabled={pruningOrphans}
            >
              {pruningOrphans ? tr("ui.settings.reviewing") : tr("ui.settings.reviewCleanup")}
            </Button>
          </div>

          {#if orphanPruneError}
            <p class="mt-3 text-sm text-destructive">{orphanPruneError}</p>
          {/if}
          {#if orphanPruneSuccess}
            <p class="mt-3 text-sm text-success">
              {tr("ui.settings.noVerifiedTestResidueRemains")}
            </p>
          {/if}
        </div>

        <div
          class="mt-4 rounded-lg border border-destructive/30 bg-destructive/5 p-4"
          data-testid="settings-delete-deployment-card"
        >
          <div class="min-w-0">
            <h3 class="font-medium text-foreground">{tr("ui.settings.deleteADeployment")}</h3>
            <p class="mt-1 text-sm text-muted-foreground">
              {tr("ui.settings.decommissionsTheDeploymentSManaged")}
            </p>
          </div>

          {#if deploymentsLoading}
            <p class="mt-3 text-sm text-muted-foreground">
              {tr("ui.settings.loadingDeployments")}
            </p>
          {:else if deploymentsError}
            <p class="mt-3 text-sm text-destructive">{deploymentsError}</p>
          {:else if deployments.length === 0}
            <p class="mt-3 text-sm text-muted-foreground">
              {tr("ui.settings.noDeploymentsToDelete")}
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
                  <Button
                    variant="secondary"
                    class="shrink-0"
                    testId="settings-delete-deployment-button"
                    onclick={() => openDeleteConfirm(deployment)}
                    disabled={deployment.demo_anchor || deletingDeployment}
                    ariaLabel={deployment.demo_anchor
                      ? tr("ui.settings.theDemoDeploymentIsProtected")
                      : tr("ui.settings.deleteThisDeployment")}
                  >
                    {tr("ui.settings.delete")}
                  </Button>
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
    <div data-kx="plate" class="w-full max-w-md shadow-2xl">
      <div class="p-6">
        <div class="mb-4">
          <h2 class="text-lg font-semibold text-destructive">
            {tr("ui.settings.deleteNamed", { name: deleteCandidate.name })}
          </h2>
          <p class="mt-1 text-sm text-muted-foreground">
            {tr("ui.settings.thisDecommissionsTheManagedRuntime")}
          </p>
        </div>

        <label
          class="mb-1 block text-sm text-muted-foreground"
          for="settings-delete-deployment-name"
        >
          {trParts("ui.settings.typeToConfirm")[0]}<span class="font-medium text-foreground"
            >{deleteCandidate.name}</span
          >{trParts("ui.settings.typeToConfirm")[1]}
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
          <Button
            variant="secondary"
            class="flex-1"
            onclick={cancelDelete}
            disabled={deletingDeployment}
          >
            {tr("ui.importExportModal.cancel")}
          </Button>
          <Button
            variant="destructive"
            class="flex-1"
            testId="settings-delete-deployment-confirm"
            onclick={confirmDelete}
            disabled={deletingDeployment || !deleteConfirmed}
          >
            {deletingDeployment ? tr("ui.settings.deleting") : tr("ui.settings.delete")}
          </Button>
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
    <div data-kx="plate" class="w-full max-w-md shadow-2xl">
      <div class="p-6">
        <div class="mb-4">
          <h2 class="text-lg font-semibold text-foreground">
            {tr("ui.settings.reviewVerifiedTestResidue")}
          </h2>
          <p class="mt-1 text-sm text-muted-foreground">
            {tr("ui.settings.thisExactPlanIsBound")}
          </p>
        </div>

        <div class="mb-4 rounded-lg border border-border bg-muted/40 p-3">
          <p class="text-sm text-foreground">
            {tr("ui.settings.pruneQualifies", { owner: "e2e-*", strict: "runtime-cloud-<provider>-<timestamp>" })}
          </p>
        </div>

        {#if orphanPrunePlan?.candidates.length}
          <ul class="mb-4 space-y-2" data-testid="settings-prune-orphans-plan">
            {#each orphanPrunePlan.candidates as candidate (candidate.resource_type + candidate.id)}
              <li class="rounded-lg border border-border bg-card p-3">
                <p class="font-medium text-foreground">{candidate.name}</p>
                <p class="mt-1 break-all text-xs text-muted-foreground">
                  {candidate.resource_type} · {candidate.id} · {candidate.class}
                </p>
              </li>
            {/each}
          </ul>
        {:else}
          <p
            class="mb-4 text-sm text-success"
            data-testid="settings-prune-orphans-empty"
          >
            {tr("ui.settings.noVerifiedTestResidueWas")}
          </p>
        {/if}

        {#if orphanPruneError}
          <p class="mb-4 text-sm text-destructive">{orphanPruneError}</p>
        {/if}

        <div class="flex gap-3">
          <Button
            variant="secondary"
            class="flex-1"
            onclick={cancelOrphanPrune}
            disabled={pruningOrphans}
          >
            {orphanPrunePlan?.candidates.length ? tr("ui.importExportModal.cancel") : tr("ui.groupedTaskList.done")}
          </Button>
          {#if orphanPrunePlan?.candidates.length}
            <Button
              variant="primary"
              class="flex-1"
              testId="settings-prune-orphans-confirm"
              onclick={confirmOrphanPrune}
              disabled={pruningOrphans}
            >
              {pruningOrphans ? tr("ui.settings.applying") : tr("ui.settings.applyExactPlan")}
            </Button>
          {/if}
        </div>
      </div>
    </div>
  </div>
{/if}

<style>
  /* Finish swatch geometry (product-owned). The frame renders the package's
     real plate and selection recipes at a quarter scale; the scene behind it
     exists so a glass finish has colour to bend, derived from the product
     accent exactly as the app-role scene derives its own hues. No material
     value is authored here. */
  .finish-swatch {
    position: relative;
    display: inline-block;
    flex: none;
    width: 2.25rem;
    height: 1.375rem;
    overflow: hidden;
    border-radius: 0.3rem;
    isolation: isolate;
  }
  .finish-swatch-scene {
    position: absolute;
    inset: 0;
    background:
      radial-gradient(
        60% 80% at 20% 30%,
        oklch(from var(--primary) 0.6 0.19 h),
        transparent 70%
      ),
      radial-gradient(
        60% 80% at 85% 80%,
        oklch(from var(--primary) 0.66 0.18 calc(h + 70)),
        transparent 70%
      ),
      var(--kx-bg, var(--background));
  }
  .finish-swatch-frame {
    position: absolute;
    inset: 3px;
  }
  .finish-swatch-plate {
    display: block;
    width: 120px;
    height: 64px;
    zoom: 0.25;
  }
  .finish-swatch-pill {
    position: absolute;
    left: 12px;
    top: 18px;
    display: block;
    width: 52px;
    height: 28px;
  }
</style>
