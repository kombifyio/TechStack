<script lang="ts">
  /**
   * The homelab's title: the saved Stack Identity (character icon and the
   * name in its chosen text effect), without a badge behind it. Before an
   * identity is saved, the plain fallback text (owner direction 2026-09-26).
   */
  import {
    StackIdentityIcon,
    StackIdentityName,
  } from "@kombiverselabs/ui/identity";
  import type { StackIdentity } from "@kombiverselabs/ui/identity";

  interface Props {
    identity: StackIdentity | null | undefined;
    fallback: string;
    class?: string;
  }

  let { identity, fallback, class: className = "" }: Props = $props();
  let saved = $derived(identity?.savedAt && identity.name?.trim() ? identity : null);
</script>

<div class="flex min-w-0 items-center gap-3">
  {#if saved}
    <StackIdentityIcon identity={saved} size="md" class="shrink-0" />
  {/if}
  <h1 class="min-w-0 truncate {className}" data-testid="homelab-title">
    {#if saved}
      <StackIdentityName identity={saved} />
    {:else}
      {fallback}
    {/if}
  </h1>
</div>
