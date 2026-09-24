<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import ServerProvisioningStep from "./ServerProvisioningStep.svelte";
  import ServiceRegistrySelector from "./ServiceRegistrySelector.svelte";
  import {
    getRegistryNodeRoleChoices,
    getStackKitFoundationChoices,
    type RegistryNodeRole,
    type ManagedProviderID,
    type StackConfig,
    type StackKitFoundation,
  } from "#lib/wizard/index.js";

  interface Props {
    config: StackConfig;
    showProvisioning?: boolean;
    showRole?: boolean;
    showServices?: boolean;
    lockFoundation?: boolean;
    joinSurface?: boolean;
    onmanagedproviderselect?: (provider: ManagedProviderID) => void;
    recommendedStackKit?: string;
    flat?: boolean;
  }

  let {
    config = $bindable(),
    showProvisioning = true,
    showRole = true,
    showServices = false,
    lockFoundation = false,
    joinSurface = false,
    onmanagedproviderselect,
    recommendedStackKit,
    flat = false,
  }: Props = $props();

  const foundations = getStackKitFoundationChoices();
  const boundFoundation = $derived(
    foundations.find(
      (foundation) =>
        foundation.value === config.serverProvisioning.stackkitFoundation,
    ) ?? foundations[0],
  );
  /*
   * Registering a main Node must stay possible (owner direction 2026-09-04) —
   * the recommended homelab runs two of them, Basement and Cloud. It is not
   * reachable from HERE though: a join targets one kit deployment, and one
   * kit deployment has exactly one control plane, so the backend records a
   * requested Foundation as a worker (buildJoinRunRequest -> joinServer) and
   * the projection refuses a second controller outright. A second main Node
   * is a second StackKit deployment, founded from the creation wizard, which
   * already adds a deployment to an existing homelab.
   *
   * So the option is withheld here and named instead — offering a control
   * that silently downgrades what it promises is the worse failure.
   */
  const roles = $derived(
    joinSurface
      ? getRegistryNodeRoleChoices().filter(
          (role) => role.value !== "foundation",
        )
      : getRegistryNodeRoleChoices(),
  );

  $effect(() => {
    config.serverProvisioning.stackkitFoundation ||= config.kit;
    if (joinSurface) {
      // Default only. This used to overwrite an explicit `foundation` choice
      // on every effect run, so the selection snapped back as you made it.
      config.serverProvisioning.nodeRole ||= "worker";
      return;
    }
    config.serverProvisioning.nodeRole ||= "foundation";
  });

  function selectFoundation(value: StackKitFoundation) {
    config.serverProvisioning.stackkitFoundation = value;
    config.kit = value;
  }

  function selectRole(value: RegistryNodeRole) {
    config.serverProvisioning.nodeRole = value;
  }
</script>

<div
  class="space-y-6"
  class:flat
  data-testid="server-registry-module"
  data-stackkit-foundation={config.serverProvisioning.stackkitFoundation}
  data-node-role={config.serverProvisioning.nodeRole}
>
  {#if showProvisioning}
    <ServerProvisioningStep bind:config {onmanagedproviderselect} />
  {/if}

  <section class="space-y-3" data-testid="stackkit-foundation-selector">
    <div>
      <h3 class="text-base font-semibold text-foreground">
        StackKit foundation
      </h3>
      <p class="text-sm text-muted-foreground">
        {#if lockFoundation}
          This Node joins the existing StackKit. The foundation is already
          bound.
        {:else}
          Every registered server is bound to one concrete StackKit foundation.
        {/if}
      </p>
    </div>
    {#if lockFoundation || foundations.length === 1}
      <div
        class="foundation-option rounded-lg border border-primary/30 bg-primary/5 p-4"
        data-testid={boundFoundation.testId}
      >
        <span class="font-medium text-foreground">{boundFoundation.label}</span>
        <span class="mt-1 block text-sm text-muted-foreground">
          {boundFoundation.description}
        </span>
      </div>
    {:else}
      <div class="grid gap-3 md:grid-cols-2">
        {#each foundations as foundation (foundation.value)}
          <button
            type="button"
            class="foundation-option rounded-lg border border-border bg-card/70 p-4 text-left transition-colors hover:border-primary/60 {config
              .serverProvisioning.stackkitFoundation === foundation.value
              ? 'border-primary bg-primary/5 ring-1 ring-primary/20'
              : ''}"
            aria-pressed={config.serverProvisioning.stackkitFoundation ===
              foundation.value}
            onclick={() => selectFoundation(foundation.value)}
            data-testid={foundation.testId}
          >
            <span
              class="flex flex-wrap items-center gap-2 font-medium text-foreground"
            >
              {foundation.label}
              {#if foundation.value === recommendedStackKit}
                <span
                  class="rounded-full border border-primary/25 bg-primary/10 px-2 py-0.5 text-[11px] text-primary"
                  >{tr("common.recommended")}</span
                >
              {/if}
            </span>
            <span class="mt-1 block text-sm text-muted-foreground">
              {foundation.description}
            </span>
          </button>
        {/each}
      </div>
    {/if}
  </section>

  {#if showRole}
    <section class="space-y-3" data-testid="server-role-selector">
      <div>
        <h3 class="text-base font-semibold text-foreground">Server role</h3>
        <p class="text-sm text-muted-foreground">
          {#if joinSurface}
            This Node joins the existing StackKit deployment, which already has
            its Foundation Node. A second main Node runs its own StackKit
            deployment — <a
              class="underline underline-offset-2 hover:text-foreground"
              href="/stacks/new"
              data-testid="server-role-found-deployment-link"
              >add one to this homelab</a
            >.
          {:else}
            Foundation Node is the product label for the first/core server.
          {/if}
        </p>
      </div>
      <div class="grid gap-3 md:grid-cols-3">
        {#each roles as role (role.value)}
          <button
            type="button"
            class="role-option rounded-lg border border-border bg-card/70 p-4 text-left transition-colors hover:border-primary/60 {config
              .serverProvisioning.nodeRole === role.value
              ? 'border-primary bg-primary/5 ring-1 ring-primary/20'
              : ''}"
            aria-pressed={config.serverProvisioning.nodeRole === role.value}
            onclick={() => selectRole(role.value)}
            data-testid={role.testId}
          >
            <span class="font-medium text-foreground">{role.label}</span>
            <span class="mt-1 block text-sm text-muted-foreground">
              {role.description}
            </span>
          </button>
        {/each}
      </div>
    </section>
  {/if}

  {#if showServices}
    <section class="space-y-3" data-testid="server-registry-services">
      <div>
        <h3 class="text-base font-semibold text-foreground">
          Optional services
        </h3>
        <p class="text-sm text-muted-foreground">
          These selections feed the same Service Registry contract used by
          Service Management.
        </p>
      </div>
      <ServiceRegistrySelector services={config.services} />
    </section>
  {/if}
</div>

<style>
  .flat .foundation-option,
  .flat .role-option {
    border-radius: 0;
    border-width: 0 0 1px;
    box-shadow: none;
    background: transparent;
    padding-inline: 0;
  }
</style>
