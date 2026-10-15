<script lang="ts">
  /**
   * A stack's credentials in the person's kombify Wallet: the Wallet items in
   * the scope `project:<stack id>`. New items are created in that scope and
   * made available to Techstack, which runs the stack with them.
   */
  import { tr } from "#lib/i18n.svelte.js";
  import WalletItemsPanel from "./WalletItemsPanel.svelte";

  let { stackId }: { stackId: string } = $props();
</script>

<section
  data-kx="plate"
  class="overflow-hidden"
  data-testid="stack-wallet-credentials"
>
  <div class="px-6 py-4 border-b border-border">
    <h2 class="text-lg font-semibold text-foreground">
      {tr("kwallet.stack.title")}
    </h2>
    <p class="text-sm text-muted-foreground">
      {tr("kwallet.stack.description")}
    </p>
  </div>
  <div class="p-6">
    <WalletItemsPanel
      fixedScope={`project:${stackId}`}
      allowTechstackOnCreate
    />
  </div>
</section>
