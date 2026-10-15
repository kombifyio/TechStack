<!--
  Confirms moving one application to another Node of the same deployment.
  The page owns the move itself; this dialog only picks the target.
-->
<script lang="ts">
  import { AlertTriangle, Loader2 } from "@lucide/svelte";
  import Modal from "#lib/components/Modal.svelte";
  import { tr, trParts } from "#lib/i18n.svelte.js";
  import type { RegistryServer } from "#lib/api/registry.js";

  interface Props {
    applicationName: string;
    sourceNodeName: string;
    targets: RegistryServer[];
    initialTargetId: string;
    saving: boolean;
    oncancel: () => void;
    onconfirm: (target: RegistryServer) => void;
  }

  let {
    applicationName,
    sourceNodeName,
    targets,
    initialTargetId,
    saving,
    oncancel,
    onconfirm,
  }: Props = $props();

  // svelte-ignore state_referenced_locally
  let targetId = $state(initialTargetId);
  const target = $derived(targets.find((server) => server.id === targetId));
</script>

<Modal
  title={tr("ui.services.moveApplication")}
  onClose={oncancel}
  maxWidth="md"
>
  <div class="space-y-4">
    <div
      class="flex gap-3 p-3.5 bg-warning/10 border border-warning/20 text-warning rounded-lg text-sm"
    >
      <AlertTriangle
        class="w-5 h-5 shrink-0 text-warning mt-0.5 animate-pulse"
      />
      <div>
        <span class="font-semibold block"
          >{tr("ui.services.applicationMove")}</span
        >
        {trParts("ui.services.startsControlledMove")[0]}<strong
          class="text-foreground"
          >{applicationName}</strong
        >{trParts("ui.services.startsControlledMove")[1]}
      </div>
    </div>

    <p class="text-sm text-muted-foreground leading-relaxed">
      {trParts("ui.services.aboutToMove")[0]}<strong class="text-foreground"
        >{sourceNodeName}</strong
      >{trParts("ui.services.aboutToMove")[1]}<strong class="text-foreground"
        >{target?.name ?? ""}</strong
      >{trParts("ui.services.aboutToMove")[2]}
    </p>

    <label class="block space-y-1.5 text-sm">
      <span class="font-medium text-foreground"
        >{tr("ui.services.targetNode")}</span
      >
      <select
        class="flex h-10 w-full rounded-lg border border-input bg-transparent px-3 py-2 text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
        bind:value={targetId}
      >
        {#each targets as server (server.id)}
          <option value={server.id}>
            {server.name} · {server.role_label}
          </option>
        {/each}
      </select>
    </label>

    <div
      class="text-xs bg-muted/45 border border-border p-3.5 rounded-lg space-y-2"
    >
      <div class="font-semibold text-foreground">
        {tr("ui.services.moveSteps")}
      </div>
      <ol class="list-decimal list-inside space-y-1.5 text-muted-foreground">
        <li>
          {tr("ui.services.theApplicationConfigWillBe")}
        </li>
        <li>
          {trParts("ui.services.temporaryInstance")[0]}<span
            class="text-foreground font-medium"
            >{target?.name ?? ""}</span
          >{trParts("ui.services.temporaryInstance")[1]}
        </li>
        <li>
          {tr("ui.services.youCanTestTheNew")}
        </li>
        <li>
          {tr("ui.services.uponYourManualVerificationThe")}
        </li>
      </ol>
    </div>
  </div>

  {#snippet footer()}
    <button
      type="button"
      class="px-4 py-2 rounded-lg bg-secondary text-secondary-foreground hover:bg-secondary/80 transition-colors disabled:opacity-50 cursor-pointer font-medium"
      onclick={oncancel}
      disabled={saving}
    >
      {tr("ui.importExportModal.cancel")}
    </button>
    <button
      type="button"
      data-kx="control"
      data-variant="primary"
      class="px-4 py-2 rounded-lg disabled:opacity-50 flex items-center gap-1.5 cursor-pointer font-semibold"
      onclick={() => target && onconfirm(target)}
      disabled={saving || !target}
    >
      {#if saving}
        <Loader2 class="w-4 h-4 animate-spin" />
      {/if}
      {tr("ui.services.startMove")}
    </button>
  {/snippet}
</Modal>
