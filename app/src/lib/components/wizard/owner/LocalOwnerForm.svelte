<!--
  LocalOwnerForm - username/email/display name for a custom local owner.
  Renders inside the "local owner" source card once that source is selected.
-->
<script lang="ts">
  import type { StackConfig } from "$lib/wizard";
  import { tr } from "$lib/i18n.svelte";

  interface Props {
    config: StackConfig;
  }

  let { config }: Props = $props();

  const ownerDisplayNamePreview = $derived(
    config.owner.displayName.trim() ||
      config.owner.username.trim() ||
      config.owner.email.trim(),
  );
</script>

<div class="mt-4 pt-4 border-t border-border space-y-3">
  <div class="grid gap-3 md:grid-cols-2">
    <div>
      <label for="owner-username" class="block text-sm text-muted-foreground mb-1"
        >{tr("wizard.login.owner.username")}</label
      >
      <input
        id="owner-username"
        type="text"
        bind:value={config.owner.username}
        placeholder="owner"
        class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:ring-1 focus:ring-primary"
      />
    </div>
    <div>
      <label for="owner-email" class="block text-sm text-muted-foreground mb-1"
        >{tr("wizard.login.owner.email")}</label
      >
      <input
        id="owner-email"
        type="email"
        bind:value={config.owner.email}
        placeholder="owner@example.com"
        class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:ring-1 focus:ring-primary"
      />
    </div>
  </div>
  <div>
    <label
      for="owner-display-name"
      class="block text-sm text-muted-foreground mb-1"
      >{tr("wizard.login.owner.displayName")}</label
    >
    <input
      id="owner-display-name"
      type="text"
      bind:value={config.owner.displayName}
      placeholder={config.owner.username || "Owner"}
      class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:ring-1 focus:ring-primary"
    />
  </div>
  {#if ownerDisplayNamePreview}
    <p class="text-xs text-muted-foreground">
      {tr("wizard.login.owner.preview")}: {ownerDisplayNamePreview}
    </p>
  {/if}
</div>
