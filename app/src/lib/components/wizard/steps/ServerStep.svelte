<!--
  ServerStep - wizard step 2 (Where should the server come from?).

  Embeddable module (ADR-0036 / D4c): a thin heading shell around the
  ServerRegistryPanel so the services dashboard and expansion runs can reuse
  the exact provisioning surface.
-->
<script lang="ts">
  import { ServerRegistryPanel } from "../index";
  import type { ManagedProviderID, StackConfig } from "$lib/wizard";
  import { tr } from "$lib/i18n.svelte";

  interface Props {
    config: StackConfig;
    showRole?: boolean;
    showServices?: boolean;
    onmanagedproviderselect?: (provider: ManagedProviderID) => void;
  }

  let {
    config = $bindable(),
    showRole = false,
    showServices = false,
    onmanagedproviderselect,
  }: Props = $props();
</script>

<div class="space-y-8" data-testid="easy-step-2">
  <div class="text-center mb-4">
    <h2 class="text-2xl font-semibold text-foreground mb-3">
      {tr("wizard.server.title")}
    </h2>
    <p class="text-muted-foreground max-w-lg mx-auto">
      {tr("wizard.server.subtitle")}
    </p>
  </div>

  <ServerRegistryPanel
    bind:config
    {showRole}
    {showServices}
    {onmanagedproviderselect}
  />
</div>
