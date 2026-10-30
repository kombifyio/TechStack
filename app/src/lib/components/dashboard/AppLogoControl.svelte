<script lang="ts">
  import { Select } from "@kombiverselabs/ui/primitives";
  import Button from "#lib/components/ui/Button.svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import { appIconUrl, isAppIconSlug } from "#lib/brand-logo.js";
  import {
    appLogoKey,
    getAppLogoOverride,
    setAppLogoOverride,
  } from "#lib/app-logo-overrides.svelte.js";

  /*
   * One application's logo, per viewer: Automatic (the recognition's logo),
   * Hide logo (the monogram) or Use icon… (a dashboard-icons name on our asset
   * host). The choice stays in this browser and applies to every list that
   * shows the application.
   */
  let { applicationKey }: { applicationKey: string } = $props();

  type Choice = "auto" | "hidden" | "icon";

  const key = $derived(appLogoKey(applicationKey));
  const stored = $derived(getAppLogoOverride(key));
  // "Use icon…" is picked before a name exists, so it is not stored yet.
  let picked = $state<Choice | undefined>();
  let draft = $state("");
  let invalid = $state(false);
  let failedSlug = $state("");

  const choice = $derived<Choice>(
    picked ?? (stored ? (stored.mode === "hidden" ? "hidden" : "icon") : "auto"),
  );
  const storedSlug = $derived(stored?.mode === "icon" ? stored.slug : "");
  const options = $derived([
    { value: "auto", label: tr("ui.appLogo.auto") },
    { value: "hidden", label: tr("ui.appLogo.hide") },
    { value: "icon", label: tr("ui.appLogo.useIcon") },
  ]);

  // Another application: start from its own stored choice.
  $effect(() => {
    void key;
    picked = undefined;
    invalid = false;
  });

  function choose(next: string) {
    invalid = false;
    if (next === "icon") {
      picked = "icon";
      draft = storedSlug || draft;
      return;
    }
    picked = undefined;
    setAppLogoOverride(key, next === "hidden" ? { mode: "hidden" } : undefined);
  }

  function apply(event: SubmitEvent) {
    event.preventDefault();
    const slug = draft.trim().toLowerCase();
    if (!isAppIconSlug(slug)) {
      invalid = true;
      return;
    }
    invalid = false;
    picked = undefined;
    setAppLogoOverride(key, { mode: "icon", slug });
  }

  function reset() {
    picked = undefined;
    invalid = false;
    setAppLogoOverride(key, undefined);
  }
</script>

{#if key}
  <div
    class="flex flex-col gap-2"
    data-testid="app-logo-control"
    data-application-key={key}
  >
    <Select
      label={tr("ui.appLogo.label")}
      {options}
      value={choice}
      onchange={choose}
    />
    {#if choice === "icon"}
      <form class="flex flex-col gap-1.5" onsubmit={apply}>
        <label class="flex flex-col gap-1 text-xs text-muted-foreground">
          <span>{tr("ui.appLogo.iconName")}</span>
          <span class="flex items-center gap-2">
            {#if storedSlug}
              <img
                src={appIconUrl(storedSlug)}
                alt=""
                class="h-8 w-8 shrink-0 rounded-md object-contain"
                onerror={() => (failedSlug = storedSlug)}
              />
            {/if}
            <input
              type="text"
              class="min-w-0 flex-1 rounded-md border border-input bg-background px-3 py-2 text-sm text-foreground"
              bind:value={draft}
              maxlength="80"
              autocomplete="off"
              autocapitalize="off"
              spellcheck="false"
              placeholder={tr("ui.appLogo.iconPlaceholder")}
              aria-invalid={invalid}
              data-testid="app-logo-icon-name"
            />
            <Button
              type="submit"
              size="sm"
              variant="secondary"
              testId="app-logo-apply"
            >
              {tr("ui.appLogo.apply")}
            </Button>
          </span>
        </label>
        {#if invalid}
          <p role="alert" class="text-xs text-warning">
            {tr("ui.appLogo.iconInvalid")}
          </p>
        {:else if storedSlug && failedSlug === storedSlug}
          <p role="status" class="text-xs text-warning">
            {tr("ui.appLogo.iconFailed")}
          </p>
        {:else}
          <p class="text-xs text-muted-foreground">
            {tr("ui.appLogo.iconHint")}
          </p>
        {/if}
      </form>
    {/if}
    {#if stored}
      <div>
        <Button
          size="sm"
          variant="ghost"
          testId="app-logo-reset"
          onclick={reset}
        >
          {tr("ui.appLogo.reset")}
        </Button>
      </div>
    {/if}
  </div>
{/if}
