<script lang="ts">
  /**
   * The Wallet page. Which Wallet backs it is decided by the deployment
   * (`wallet_source` of `GET /api/v1/auth/mode`): hosted Techstack uses the
   * central kombify Wallet natively; a self-hosted install keeps its built-in
   * Wallet exactly as it was. Nothing is shown until the mode is known, so the
   * wrong Wallet is never asked for its items.
   */
  import { tr } from "#lib/i18n.svelte.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { showInlineTabs } from "#lib/stores/deploymentMode.js";
  import KombifyWalletPage from "#lib/kombify-wallet/components/KombifyWalletPage.svelte";
  import LegacyWalletPage from "#lib/components/wallet/LegacyWalletPage.svelte";
</script>

{#if !authStore.modeDetected}
  <div class="mx-auto w-full max-w-7xl px-4 py-6 md:px-6 md:py-8">
    <p role="status" class="text-muted-foreground text-sm">
      {tr("kwallet.loading")}
    </p>
  </div>
{:else if authStore.walletSource === "kombify"}
  <KombifyWalletPage inlineTabs={$showInlineTabs} />
{:else}
  <LegacyWalletPage />
{/if}
