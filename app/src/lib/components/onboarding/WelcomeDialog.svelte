<script lang="ts">
  /**
   * The first thing a new operator sees, and the only screen of the
   * introduction that asks for something back.
   *
   * It is deliberately NOT an introduction stop. A stop is a hint about chrome
   * that already exists; this one says what Techstack is and names the
   * homelab, which is a product decision written to stack identity. The
   * package's spotlight tour starts after it (see `introduction.ts`).
   *
   * The subtree pins `data-finish="kombify"` for the same reason the package's
   * card does: this runs before anybody has chosen a design, so there is no
   * choice to honour yet. Appearance is left alone.
   */
  import { X } from "@lucide/svelte";
  import { tr } from "#lib/i18n.svelte.js";

  interface Props {
    open: boolean;
    /** Pre-filled from stack identity; empty when nothing has been named yet. */
    name?: string;
    /**
     * False when kombify Cloud owns the identity. The welcome still runs — it
     * just stops pretending the reader can rename something they cannot.
     */
    nameEditable?: boolean;
    saving?: boolean;
    error?: string;
    /** Carries the chosen name, already trimmed and defaulted. */
    onStart: (name: string) => void;
    onDismiss: () => void;
    onExplore: () => void;
  }

  let {
    open,
    name = "",
    nameEditable = true,
    saving = false,
    error = "",
    onStart,
    onDismiss,
    onExplore,
  }: Props = $props();

  let value = $state("");
  let field = $state<HTMLInputElement | null>(null);
  let dialog = $state<HTMLDivElement | null>(null);
  let primed = false;

  const fallbackName = $derived(tr("introduction.welcome.name_placeholder"));

  $effect(() => {
    if (!open) {
      primed = false;
      return;
    }
    if (primed) return;
    primed = true;
    value = name;
    requestAnimationFrame(() => {
      if (nameEditable) field?.select();
      else dialog?.focus();
    });
  });

  function submit() {
    if (saving) return;
    onStart(value.trim() || fallbackName);
  }

  function onKeydown(event: KeyboardEvent) {
    if (event.key === "Escape") onDismiss();
  }
</script>

{#if open}
  <!-- data-finish is pinned, not inherited: see the file header. -->
  <div class="welcome" data-finish="kombify">
    <div data-kx="scrim" aria-hidden="true"></div>
    <div
      bind:this={dialog}
      class="welcome__card"
      data-kx="menu-plate"
      data-testid="introduction-welcome"
      role="dialog"
      aria-modal="true"
      aria-labelledby="introduction-welcome-title"
      tabindex="-1"
      onkeydown={onKeydown}
    >
      <header class="flex items-start justify-between gap-4">
        <h2
          id="introduction-welcome-title"
          class="text-2xl font-semibold text-foreground"
        >
          {tr("introduction.welcome.title")}
        </h2>
        <button
          type="button"
          data-kx="control"
          class="grid h-8 w-8 shrink-0 place-items-center rounded-lg"
          aria-label={tr("onboarding.ui.introduction.close")}
          onclick={onDismiss}
        >
          <X class="h-4 w-4" />
        </button>
      </header>

      <p class="mt-3 text-sm leading-relaxed text-muted-foreground">
        {tr("introduction.welcome.body")}
      </p>

      {#if nameEditable}
        <div class="mt-6">
          <label
            class="block text-base font-medium text-foreground"
            for="introduction-welcome-name"
          >
            {tr("introduction.welcome.name_label")}
          </label>
          <input
            bind:this={field}
            bind:value
            id="introduction-welcome-name"
            class="mt-3 w-full rounded-xl border border-border bg-input px-4 py-3 text-lg text-foreground outline-none focus:border-primary focus:ring-2 focus:ring-primary/30"
            data-testid="introduction-welcome-name"
            placeholder={fallbackName}
            maxlength="30"
            autocomplete="off"
            onkeydown={(event) => {
              if (event.key === "Enter") submit();
            }}
          />
          <p class="mt-2 text-xs text-muted-foreground">
            {tr("introduction.welcome.name_hint")}
          </p>
        </div>
      {/if}

      {#if error}
        <p class="mt-4 text-sm text-destructive">{error}</p>
      {/if}

      <footer class="mt-7 flex flex-wrap items-center justify-end gap-2">
        <button
          type="button"
          data-kx="control"
          class="rounded-lg px-3 py-2 text-sm"
          disabled={saving}
          onclick={onExplore}
        >
          {tr("onboarding.ui.explore")}
        </button>
        <button
          type="button"
          data-kx="control"
          class="mr-auto rounded-lg px-3 py-2 text-sm text-muted-foreground"
          onclick={onDismiss}
        >
          {tr("onboarding.ui.introduction.skip")}
        </button>
        <button
          type="button"
          data-kx="control"
          data-variant="primary"
          class="rounded-lg px-4 py-2 text-sm"
          data-testid="introduction-welcome-start"
          disabled={saving}
          onclick={submit}
        >
          {#if saving}
            {tr("common.loading")}
          {:else}
            {nameEditable
              ? tr("introduction.welcome.cta")
              : tr("introduction.welcome.cta_plain")}
          {/if}
        </button>
      </footer>
    </div>
  </div>
{/if}

<style>
  /*
   * Geometry only, matching the package's introduction card so the two read
   * as one flow. The z-index range is the package's on purpose: these never
   * render together, and sharing it keeps one ceiling in the product.
   */
  .welcome :global([data-kx~="scrim"]) {
    z-index: 2147483500;
  }
  .welcome__card {
    position: fixed;
    z-index: 2147483502;
    left: 50%;
    top: 50%;
    width: min(620px, calc(100vw - 32px));
    max-height: calc(100vh - 48px);
    overflow-y: auto;
    transform: translate(-50%, -50%);
    padding: 2rem;
    outline: none;
  }
</style>
