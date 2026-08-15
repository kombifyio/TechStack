<!--
  RecoveryPassphraseCard - optional client-side hashed recovery passphrase
  for custom owner bootstraps. Plaintext never leaves the browser; only the
  argon2id hash is stored on the config.
-->
<script lang="ts">
  import type { OwnerStepState } from "$lib/wizard/owner-state.svelte";
  import { tr } from "$lib/i18n.svelte";

  interface Props {
    owner: OwnerStepState;
  }

  let { owner }: Props = $props();
</script>

<div class="card p-4 space-y-4" data-testid="recovery-passphrase-card">
  <div>
    <h3 class="text-lg font-semibold text-foreground">
      {tr("wizard.login.recovery.title")}
    </h3>
    <p class="text-sm text-muted-foreground mt-1">
      {tr("wizard.login.recovery.subtitle")}
    </p>
  </div>

  <div class="grid gap-3 md:grid-cols-2">
    <div>
      <label
        for="recovery-passphrase"
        class="block text-sm text-muted-foreground mb-1"
        >{tr("wizard.login.recovery.passphrase")}</label
      >
      <input
        id="recovery-passphrase"
        type="password"
        bind:value={owner.recoveryPassphrase}
        oninput={owner.resetRecoveryHash}
        autocomplete="new-password"
        class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground focus:border-primary focus:ring-1 focus:ring-primary"
      />
    </div>
    <div>
      <label
        for="recovery-passphrase-confirm"
        class="block text-sm text-muted-foreground mb-1"
        >{tr("wizard.login.recovery.confirm")}</label
      >
      <input
        id="recovery-passphrase-confirm"
        type="password"
        bind:value={owner.recoveryPassphraseConfirm}
        oninput={owner.resetRecoveryHash}
        onblur={owner.syncRecoveryHash}
        autocomplete="new-password"
        class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground focus:border-primary focus:ring-1 focus:ring-primary {owner.recoveryPassphraseConfirm &&
        !owner.recoveryPassphrasesMatch
          ? 'border-destructive'
          : ''}"
      />
    </div>
  </div>

  <div class="space-y-2">
    <p class="text-xs text-muted-foreground" data-testid="recovery-strength">
      Strength: {owner.recoveryStrength.score}/4
    </p>
    {#if owner.recoveryStrength.feedback.length > 0}
      <ul class="space-y-1 text-xs text-muted-foreground">
        {#each owner.recoveryStrength.feedback as item}
          <li>{item}</li>
        {/each}
      </ul>
    {/if}
    {#if owner.recoveryPassphraseConfirm && !owner.recoveryPassphrasesMatch}
      <p class="text-destructive text-xs">Recovery passphrases do not match.</p>
    {/if}
    {#if owner.isHashingRecovery}
      <p class="text-xs text-muted-foreground" data-testid="recovery-status">
        {tr("wizard.login.recovery.hashing")}
      </p>
    {:else if owner.hasRecoveryHash}
      <p class="text-xs text-success" data-testid="recovery-status">
        {tr("wizard.login.recovery.ready")}
      </p>
    {/if}
  </div>
</div>
