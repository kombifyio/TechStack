<!--
  FeatureCard Component
  
  A selectable toggle card with title, description, and help tooltip.
  Used for feature/goal selection in wizards.
-->
<script lang="ts">
  import CardHelpHint from "./CardHelpHint.svelte";

  interface Props {
    checked?: boolean;
    title: string;
    description: string;
    icon?: string;
    helpText?: string;
    helpTip?: string;
    hint?: string;
    testId?: string;
    onchange?: (checked: boolean) => void;
  }

  let {
    checked = false,
    title,
    description,
    icon,
    helpText,
    helpTip,
    hint,
    testId,
    onchange,
  }: Props = $props();

  function handleClick() {
    onchange?.(!checked);
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      handleClick();
    }
  }
</script>

<div
  role="checkbox"
  aria-checked={checked}
  tabindex="0"
  onclick={handleClick}
  onkeydown={handleKeydown}
  data-testid={testId}
  class="group relative rounded-xl border-2 p-6 cursor-pointer transition-all duration-200 hover:shadow-lg {checked
    ? 'border-primary bg-primary/10 shadow-primary/20 shadow-lg'
    : 'border-border bg-card hover:border-primary/30 hover:bg-card/80'}"
>
  <!-- Selection indicator -->
  <div class="absolute right-4 top-4">
    {#if checked}
      <div
        class="w-7 h-7 rounded-full bg-primary flex items-center justify-center shadow-lg shadow-primary/30"
      >
        <svg
          class="w-4 h-4 text-primary-foreground"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="3"
            d="M5 13l4 4L19 7"
          />
        </svg>
      </div>
    {:else}
      <div
        class="w-7 h-7 rounded-full border-2 border-muted-foreground/30 group-hover:border-primary/50 transition-colors"
      ></div>
    {/if}
  </div>

  <div class="pr-12">
    <!-- Icon above title if provided -->
    {#if icon}
      <div
        class="w-12 h-12 rounded-xl flex items-center justify-center mb-4 transition-colors {checked
          ? 'bg-primary/20'
          : 'bg-muted'}"
      >
        <span class="text-2xl">{icon}</span>
      </div>
    {/if}

    <!-- Title with help button -->
    <div class="mb-2 flex items-start gap-3">
      <h3 class="text-foreground font-semibold text-lg leading-tight">
        {title}
      </h3>
      {#if helpText}
        <CardHelpHint {helpText} {helpTip} />
      {/if}
      {#if hint}
        <span
          class="max-w-full rounded-full border border-primary/25 bg-primary/10 px-2 py-1 text-[11px] font-medium text-primary"
        >
          {hint}
        </span>
      {/if}
    </div>

    <!-- Description -->
    <p class="text-sm text-muted-foreground leading-relaxed">{description}</p>
  </div>
</div>
