<script lang="ts">
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import { onMount } from "svelte";
  import { PageHeader } from "@kombiverselabs/ui/shell";
  import HomelabSettingsWizard from "#lib/components/wizard/HomelabSettingsWizard.svelte";
  import { getHomelab, type HomelabView } from "#lib/api/homelab.js";
  import { tr } from "#lib/i18n.svelte.js";

  let view = $state<HomelabView | null>(null);
  let loading = $state(true);
  let error = $state("");
  let deploymentId = $state("");

  onMount(async () => {
    try {
      view = await getHomelab();
      const requested = new URLSearchParams(location.search).get(
        "kit_deployment_id",
      );
      const deployments = view?.kit_deployments ?? [];
      deploymentId =
        deployments.find(
          (item) => (item.kit_deployment_id || item.id) === requested,
        )?.kit_deployment_id ||
        deployments[0]?.kit_deployment_id ||
        deployments[0]?.id ||
        "";
    } catch {
      error = tr("wizard.settings.loadError");
    } finally {
      loading = false;
    }
  });
</script>

<main class="mx-auto max-w-5xl p-6 md:p-8" data-testid="homelab-settings-page">
  <PageHeader title={tr("wizard.settings.title")} />
  <p class="mb-6 text-sm text-muted-foreground">
    {tr("wizard.settings.intro")}
  </p>

  {#if loading}
    <LoadingNote label={tr("common.loading")} />
  {:else if error}
    <p class="rounded-lg border border-destructive/30 bg-destructive/10 p-4 text-sm text-destructive" role="alert">
      {error}
    </p>
  {:else if !deploymentId}
    <div class="rounded-lg border border-border bg-card/60 p-8 text-center" data-testid="homelab-settings-empty">
      <p class="text-foreground">{tr("wizard.settings.empty")}</p>
      <a class="mt-3 inline-block text-primary" href="/stacks/new">{tr("wizard.settings.openAddNode")}</a>
    </div>
  {:else}
    {#if (view?.kit_deployments.length ?? 0) > 1}
      <label class="mb-6 block max-w-sm text-sm">
        <span class="mb-1 block text-muted-foreground">{tr("wizard.settings.deployment")}</span>
        <select
          bind:value={deploymentId}
          class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
          data-testid="homelab-settings-deployment"
        >
          {#each view?.kit_deployments ?? [] as deployment (deployment.id)}
            <option value={deployment.kit_deployment_id || deployment.id}>{deployment.name}</option>
          {/each}
        </select>
      </label>
    {/if}
    {#key deploymentId}
      <HomelabSettingsWizard {deploymentId} />
    {/key}
  {/if}
</main>
