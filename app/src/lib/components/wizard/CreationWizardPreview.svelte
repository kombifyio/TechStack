<script lang="ts">
  import { untrack } from "svelte";
  import { ArrowLeft, RotateCcw } from "@lucide/svelte";
  import { tr, getLocale, setLocale } from "#lib/i18n.svelte.js";
  import {
    createDefaultConfig,
    CANONICAL_USE_CASE_GOALS,
  } from "#lib/wizard/index.js";
  import { createPreviewCatalog } from "#lib/wizard/creationPreview.js";
  import type { CreationVariant } from "#lib/wizard/creationGoals.js";
  import CreationGoals from "./CreationGoals.svelte";
  import ThemeToggle from "../ui/ThemeToggle.svelte";
  import ServerStep from "./steps/ServerStep.svelte";

  let { initialVariant = "discover" }: { initialVariant?: CreationVariant } =
    $props();
  const catalog = createPreviewCatalog();
  function initialConfig() {
    const config = createDefaultConfig();
    const goals = Object.fromEntries(
      CANONICAL_USE_CASE_GOALS.map((goal) => [goal, goal === "files"]),
    ) as NonNullable<typeof config.goals>;
    goals.everything = false;
    config.goals = goals;
    return config;
  }
  let config = $state(initialConfig());
  let variant = $state(untrack(() => initialVariant));
  let previewStep = $state<"goals" | "node">("goals");
</script>

<div class="creation-preview-page">
  <nav class="preview-navigation" aria-label={tr("wizard.preview.navigation")}>
    <a href="/stacks/new"
      ><ArrowLeft size={15} aria-hidden="true" />{tr("wizard.preview.back")}</a
    >
    <div class="preview-actions">
      <ThemeToggle />
      <button
        type="button"
        onclick={() => {
          config = initialConfig();
        }}
        ><RotateCcw size={14} aria-hidden="true" />{tr(
          "wizard.preview.reset",
        )}</button
      >
      <select
        aria-label={tr("wizard.preview.language")}
        value={getLocale()}
        onchange={(event) =>
          setLocale(event.currentTarget.value === "de" ? "de" : "en")}
        ><option value="en">English</option><option value="de">Deutsch</option
        ></select
      >
    </div>
  </nav>
  <aside class="preview-notice" role="note">
    {tr("wizard.preview.sandboxNotice")}
  </aside>
  <div
    class="preview-steps"
    role="group"
    aria-label={tr("wizard.preview.navigation")}
  >
    <button
      type="button"
      aria-pressed={previewStep === "goals"}
      onclick={() => (previewStep = "goals")}
      >{tr("wizard.server.preview.goals")}</button
    >
    <button
      type="button"
      aria-pressed={previewStep === "node"}
      onclick={() => (previewStep = "node")}
      >{tr("wizard.server.preview.node")}</button
    >
  </div>
  {#if previewStep === "node"}
    <ServerStep bind:config reviewMode={true} />
  {:else}
    <CreationGoals
      bind:config
      bind:variant
      {catalog}
      reviewMode={true}
      onGoalChange={(goal, value) => {
        if (config.goals) config.goals[goal] = value;
      }}
      onSettingChange={(goal, setting, value) => {
        config.useCaseSettings ??= {};
        config.useCaseSettings[goal] = {
          ...config.useCaseSettings[goal],
          [setting]: value,
        };
      }}
    />
  {/if}
</div>

<style>
  .creation-preview-page {
    padding: clamp(20px, 3vw, 32px);
    padding-top: 24px;
    width: 100%;
    min-width: 0;
  }
  .preview-navigation,
  .preview-actions {
    display: flex;
    align-items: center;
    gap: 16px;
  }
  .preview-steps {
    display: flex;
    gap: 4px;
    margin-block: 18px 24px;
    padding: 4px;
    border: 1px solid var(--border);
    border-radius: var(--radius-control, 10px);
    width: fit-content;
  }
  .preview-steps button {
    padding: 7px 14px;
    font-size: 12px;
    color: var(--muted-foreground);
    border-radius: calc(var(--radius-control, 10px) - 4px);
  }
  .preview-steps button[aria-pressed="true"] {
    background: var(--muted);
    color: var(--foreground);
  }
  .preview-navigation {
    justify-content: space-between;
    flex-wrap: wrap;
    margin-bottom: 18px;
    font-size: 12px;
    color: var(--muted-foreground);
  }
  a,
  button {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    min-height: 36px;
  }
  button {
    cursor: pointer;
  }
  select {
    padding: 7px;
    color: var(--foreground);
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  .preview-notice {
    padding: 12px 16px;
    margin-bottom: 28px;
    border-inline-start: 2px solid var(--primary);
    background: var(--muted);
    color: var(--muted-foreground);
    font-size: 12px;
    line-height: 1.7;
  }
  :is(a, button, select):focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 4px;
  }
  @media (max-width: 520px) {
    .preview-navigation {
      align-items: flex-start;
    }
    .preview-actions {
      gap: 8px;
    }
  }
</style>
