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
  class={`flex items-center gap-3 ${className}`}
  style={`--identity-tone: ${tone}`}
>
  <span
    class="grid h-10 w-10 shrink-0 place-items-center rounded-xl bg-[color:color-mix(in_oklab,var(--identity-tone)_18%,transparent)] text-[color:var(--identity-tone)]"
    aria-hidden="true"
  >
    <Sparkles class="h-5 w-5" />
  </span>
  <div class="min-w-0">
    <p class="truncate text-sm font-semibold text-foreground">
      {identity.name}
    </p>
    <p class="text-xs text-muted-foreground">
      {character?.label || "Custom identity"}
    </p>
  </div>
</div>
