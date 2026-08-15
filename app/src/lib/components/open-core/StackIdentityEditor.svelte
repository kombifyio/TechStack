<script lang="ts">
  import { Sparkles } from "@lucide/svelte";
  import {
    defaultIdentity,
    getIdentityCharacter,
    identityCharacters,
    normalizeStackIdentity,
    type IconStyle,
    type StackIdentity,
  } from "./identity";

  interface Props {
    identity: StackIdentity;
    onSave?: (identity: StackIdentity) => void | Promise<void>;
    onReset?: () => void;
    class?: string;
  }

  let { identity, onSave, onReset, class: className = "" }: Props = $props();
  let name = $state("");
  let characterId = $state(defaultIdentity.characterId);
  let animationEnabled = $state(defaultIdentity.animationEnabled);
  let iconStyle = $state<IconStyle>(defaultIdentity.iconStyle);
  let glowColorOverride = $state<string | null>(
    defaultIdentity.glowColorOverride,
  );
  let saving = $state(false);
  let saved = $state(false);

  const character = $derived(getIdentityCharacter(characterId));
  const tone = $derived(
    glowColorOverride || character?.tone || "var(--primary)",
  );

  $effect(() => {
    name = identity.name;
    characterId = identity.characterId;
    animationEnabled = identity.animationEnabled;
    iconStyle = identity.iconStyle;
    glowColorOverride = identity.glowColorOverride;
  });

  async function save() {
    saving = true;
    saved = false;
    try {
      await onSave?.(
        normalizeStackIdentity({
          name,
          characterId,
          animationEnabled,
          iconStyle,
          glowColorOverride,
          savedAt: new Date().toISOString(),
        }),
      );
      saved = true;
    } finally {
      saving = false;
    }
  }

  function reset() {
    name = defaultIdentity.name;
    characterId = defaultIdentity.characterId;
    animationEnabled = defaultIdentity.animationEnabled;
    iconStyle = defaultIdentity.iconStyle;
    glowColorOverride = defaultIdentity.glowColorOverride;
    onReset?.();
  }
</script>

<section
  class={`overflow-hidden rounded-xl border border-border bg-card ${className}`}
  data-testid="identity-editor"
>
  <div class="flex flex-col gap-4 p-5 sm:flex-row sm:items-center">
    <span
      class={`grid h-12 w-12 shrink-0 place-items-center rounded-xl text-[color:var(--identity-tone)] ${iconStyle === "outlined" ? "border border-current bg-transparent" : "bg-[color:color-mix(in_oklab,var(--identity-tone)_18%,transparent)]"}`}
      style={`--identity-tone: ${tone}`}
      aria-hidden="true"
    >
      <Sparkles class="h-6 w-6" />
    </span>
    <label class="min-w-0 flex-1">
      <span class="mb-1 block text-xs font-medium text-muted-foreground"
        >Stack name</span
      >
      <input
        bind:value={name}
        maxlength="30"
        class="input w-full"
        data-testid="stack-name-input"
      />
    </label>
  </div>

  <div class="grid gap-4 border-t border-border bg-muted/20 p-5 md:grid-cols-2">
    <label class="block">
      <span class="mb-1 block text-xs font-medium text-muted-foreground"
        >Identity</span
      >
      <select
        bind:value={characterId}
        class="input w-full"
        data-testid="identity-character"
      >
        {#each identityCharacters as option (option.id)}
          <option value={option.id}>{option.label}</option>
        {/each}
      </select>
    </label>
    <label class="block">
      <span class="mb-1 block text-xs font-medium text-muted-foreground"
        >Icon style</span
      >
      <select
        bind:value={iconStyle}
        class="input w-full"
        data-testid="identity-icon-style"
      >
        <option value="filled">Filled</option>
        <option value="glass">Glass</option>
        <option value="gradient">Gradient</option>
        <option value="outlined">Outlined</option>
      </select>
    </label>
    <label class="flex items-center gap-3 text-sm text-foreground">
      <input
        type="checkbox"
        bind:checked={animationEnabled}
        data-testid="identity-animation-toggle"
      />
      Enable identity animations
    </label>
    <label class="block">
      <span class="mb-1 block text-xs font-medium text-muted-foreground"
        >Accent override</span
      >
      <input
        value={glowColorOverride ?? ""}
        oninput={(event) =>
          (glowColorOverride = event.currentTarget.value || null)}
        placeholder={character?.tone || "Use identity default"}
        class="input w-full"
      />
    </label>
  </div>

  <div
    class="flex items-center justify-between gap-3 border-t border-border p-5"
  >
    <button type="button" class="btn btn-secondary" onclick={reset}
      >Reset</button
    >
    <div class="flex items-center gap-3">
      {#if saved}<span class="text-xs text-success">Saved</span>{/if}
      <button
        type="button"
        class="btn btn-primary"
        onclick={() => void save()}
        disabled={saving}
        data-testid="save-identity"
      >
        {saving ? "Saving..." : "Save"}
      </button>
    </div>
  </div>
</section>
