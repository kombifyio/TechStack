<script lang="ts">
  /**
   * The Wallet page of hosted Techstack: the person's kombify Wallet, drawn
   * with the Brand entity cards like the other homelab pages. The size switch
   * is presentation only and is remembered in this browser.
   */
  import { onMount } from "svelte";
  import { PageHeader } from "@kombiverselabs/ui/shell";
  import { SegmentedControl } from "@kombiverselabs/ui/primitives";
  import type { EntityCardSize } from "@kombiverselabs/ui/entity";
  import { tr } from "#lib/i18n.svelte.js";
  import WalletItemsPanel from "./WalletItemsPanel.svelte";

  let { inlineTabs = false }: { inlineTabs?: boolean } = $props();

  const VIEW_KEY = "techstack.wallet.view";
  const VIEWS: EntityCardSize[] = ["mark", "tile", "row", "card"];
  let view = $state<EntityCardSize>("row");

  function setView(next: string) {
    view = next as EntityCardSize;
    try {
      localStorage.setItem(VIEW_KEY, next);
    } catch {
      // Storage is a convenience; the view still switches.
    }
  }

  onMount(() => {
    try {
      const stored = localStorage.getItem(VIEW_KEY);
      if (stored && (VIEWS as string[]).includes(stored))
        view = stored as EntityCardSize;
    } catch {
      // No stored preference.
    }
  });
</script>

<div
  class="mx-auto flex w-full max-w-7xl flex-col gap-6 {inlineTabs
    ? 'h-full min-h-0 overflow-y-auto'
    : 'p-4 md:p-6'}"
  data-testid="kombify-wallet-page"
>
  <PageHeader title={tr("kwallet.title")} description={tr("kwallet.lead")}>
    {#snippet actions()}
      <SegmentedControl
        items={VIEWS.map((value) => ({
          value,
          label: tr(`kwallet.view.${value}`),
        }))}
        value={view}
        label={tr("kwallet.view.label")}
        onchange={setView}
      />
    {/snippet}
  </PageHeader>

  <WalletItemsPanel {view} />
</div>
