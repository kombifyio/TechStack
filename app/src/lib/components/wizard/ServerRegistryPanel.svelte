<script lang="ts">
  import ServerProvisioningStep from "./ServerProvisioningStep.svelte";
  import ServiceRegistrySelector from "./ServiceRegistrySelector.svelte";
  import {
    getRegistryNodeRoleChoices,
    getStackKitFoundationChoices,
    type RegistryNodeRole,
    type ManagedProviderID,
    type StackConfig,
    type StackKitFoundation,
  } from "$lib/wizard";

  interface Props {
    config: StackConfig;
    showRole?: boolean;
    showServices?: boolean;
    onmanagedproviderselect?: (provider: ManagedProviderID) => void;
  }

  let {
    config = $bindable(),
    showRole = true,
    showServices = false,
    onmanagedproviderselect,
  }: Props = $props();

  const foundations = getStackKitFoundationChoices();
  const roles = getRegistryNodeRoleChoices();

  $effect(() => {
    config.serverProvisioning.stackkitFoundation ||= config.kit;
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
  data-testid="server-registry-module"
  data-stackkit-foundation={config.serverProvisioning.stackkitFoundation}
  data-node-role={config.serverProvisioning.nodeRole}
>
  <ServerProvisioningStep bind:config {onmanagedproviderselect} />

  <section class="space-y-3" data-testid="stackkit-foundation-selector">
    <div>
      <h3 class="text-base font-semibold text-foreground">
        StackKit foundation
      </h3>
      <p class="text-sm text-muted-foreground">
        Every registered server is bound to one concrete StackKit foundation.
      </p>
    </div>
    {#if foundations.length === 1}
      <div
        class="rounded-lg border border-primary/30 bg-primary/5 p-4"
        data-testid={foundations[0].testId}
      >
        <span class="font-medium text-foreground">{foundations[0].label}</span>
        <span class="mt-1 block text-sm text-muted-foreground">
          {foundations[0].description}
        </span>
      </div>
    {:else}
      <div class="grid gap-3 md:grid-cols-2">
        {#each foundations as foundation (foundation.value)}
          <button
            type="button"
            class="rounded-lg border border-border bg-card/70 p-4 text-left transition-colors hover:border-primary/60 {config
              .serverProvisioning.stackkitFoundation === foundation.value
              ? 'border-primary bg-primary/5 ring-1 ring-primary/20'
              : ''}"
            aria-pressed={config.serverProvisioning.stackkitFoundation ===
              foundation.value}
            onclick={() => selectFoundation(foundation.value)}
            data-testid={foundation.testId}
          >
            <span class="font-medium text-foreground">{foundation.label}</span>
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
          Foundation Node is the product label for the first/core server.
        </p>
      </div>
      <div class="grid gap-3 md:grid-cols-3">
        {#each roles as role (role.value)}
          <button
            type="button"
            class="rounded-lg border border-border bg-card/70 p-4 text-left transition-colors hover:border-primary/60 {config
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
