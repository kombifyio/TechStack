<script lang="ts">
  import { Sparkles } from "@lucide/svelte";
  import { getIdentityCharacter, type StackIdentity } from "./identity";

  interface Props {
    identity: StackIdentity;
    class?: string;
  }

  let { identity, class: className = "" }: Props = $props();
  const character = $derived(getIdentityCharacter(identity.characterId));
  const tone = $derived(
    identity.glowColorOverride || character?.tone || "var(--primary)",
  );
</script>

<div
  class={`inline-flex min-w-0 items-center gap-2 rounded-full border border-primary/20 bg-primary/8 px-2.5 py-1 ${className}`}
  style={`--identity-tone: ${tone}`}
  data-testid="identity-badge"
>
  <span
    class="grid h-5 w-5 shrink-0 place-items-center rounded-full bg-[color:color-mix(in_oklab,var(--identity-tone)_22%,transparent)] text-[color:var(--identity-tone)]"
    aria-hidden="true"
  >
    <Sparkles class="h-3 w-3" />
  </span>
  <span class="truncate text-xs font-medium text-primary">{identity.name}</span>
</div>
