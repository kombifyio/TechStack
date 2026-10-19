<script lang="ts">
  /**
   * The Homelab tab of the settings: who uses the homelab and how they sign
   * in, edited in place (KOMBIFY-DESIGN-SYSTEM-STANDARD §10.2.1, a tool tab
   * holds its settings; it is never a pointer to another page). The
   * deployment can be picked with `?kit_deployment_id=`.
   */
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { tr } from "#lib/i18n.svelte.js";
  import { getHomelab, type HomelabView } from "#lib/api/homelab.js";
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import ErrorCallout from "#lib/components/ui/ErrorCallout.svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import HomelabSettingsWizard from "#lib/components/wizard/HomelabSettingsWizard.svelte";
  import { CREATE_HOMELAB_HREF } from "#lib/navigation.js";

  let view = $state<HomelabView | null>(null);
  let loading = $state(true);
  let error = $state(false);
  let deploymentId = $state("");

  async function load() {
    loading = true;
    error = false;
    try {
      view = await getHomelab();
      const requested = page.url.searchParams.get("kit_deployment_id");
      const deployments = view?.kit_deployments ?? [];
      deploymentId =
        deployments.find(
          (item) => (item.kit_deployment_id || item.id) === requested,
        )?.kit_deployment_id ||
        deployments[0]?.kit_deployment_id ||
        deployments[0]?.id ||
        "";
    } catch {
      error = true;
    } finally {
      loading = false;
    }
  }

  onMount(() => void load());
</script>

<div data-testid="homelab-settings-page">
  {#if loading}
    <LoadingNote label={tr("common.loading")} />
  {:else if error}
    <ErrorCallout
      message={tr("wizard.settings.loadError")}
      onRetry={() => void load()}
    />
  {:else if !deploymentId}
    <div
      class="flex flex-col items-start gap-3 rounded-xl border border-dashed border-border p-5"
      data-testid="homelab-settings-empty"
    >
      <p class="text-sm text-foreground">{tr("wizard.settings.empty")}</p>
      <Button variant="primary" onclick={() => void goto(CREATE_HOMELAB_HREF)}>
        {tr("nav.createHomelab")}
      </Button>
    </div>
  {:else}
    {#if (view?.kit_deployments.length ?? 0) > 1}
      <label class="mb-6 block max-w-sm text-sm">
        <span class="mb-1 block text-muted-foreground"
          >{tr("wizard.settings.deployment")}</span
        >
        <select
          bind:value={deploymentId}
          class="w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground"
          data-testid="homelab-settings-deployment"
        >
          {#each view?.kit_deployments ?? [] as deployment (deployment.id)}
            <option value={deployment.kit_deployment_id || deployment.id}
              >{deployment.name}</option
            >
          {/each}
        </select>
      </label>
    {/if}
    {#key deploymentId}
      <HomelabSettingsWizard {deploymentId} />
    {/key}
  {/if}
</div>
