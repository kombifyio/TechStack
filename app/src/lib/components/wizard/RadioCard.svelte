<!--
  RadioCard Component
  
  A selectable radio card with icon, title, and description.
  Used for single-choice selections in wizards.
-->
<script lang="ts">
  import CardHelpHint from "./CardHelpHint.svelte";

  interface Props {
    name: string;
    value: string;
    selected?: boolean;
    icon?: string;
    title: string;
    description?: string;
    helpText?: string;
    helpTip?: string;
    badge?: string;
    hint?: string;
    disabled?: boolean;
    disabledReason?: string;
    testId?: string;
    onselect?: (value: string) => void;
  }

  let {
    name,
    value,
    selected = false,
    icon,
    title,
    description,
    helpText,
    helpTip,
    badge,
    hint,
    disabled = false,
    disabledReason,
    testId,
    onselect,
  }: Props = $props();

  function handleClick() {
    if (disabled) return;
    onselect?.(value);
  }

  function handleKeydown(e: KeyboardEvent) {
    if (disabled) return;
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      handleClick();
    }
  }
</script>

<div
  role="radio"
  aria-checked={selected}
  aria-disabled={disabled}
  tabindex={disabled ? -1 : 0}
  onclick={handleClick}
  onkeydown={handleKeydown}
  data-testid={testId}
  class="group relative min-w-0 rounded-xl border-2 p-4 transition-all duration-200 sm:p-6 {disabled
    ? 'cursor-not-allowed border-border bg-muted/20 opacity-70'
    : 'cursor-pointer hover:shadow-lg'} {selected
    ? 'border-primary bg-primary/10 shadow-primary/20 shadow-lg'
    : disabled
      ? ''
      : 'border-border bg-card hover:border-primary/30 hover:bg-card/80'}"
>
  <!-- Selection indicator -->
  <div class="absolute right-4 top-4">
    {#if selected}
      <div
        class="w-7 h-7 rounded-full bg-primary flex items-center justify-center shadow-lg shadow-primary/30"
      >
        <div class="w-3 h-3 rounded-full bg-primary-foreground"></div>
      </div>
    {:else}
      <div
        class="w-7 h-7 rounded-full border-2 border-muted-foreground/30 group-hover:border-primary/50 transition-colors"
      ></div>
    {/if}
  </div>

  <div class="min-w-0 pr-9 sm:pr-12">
    <!-- Icon above title if provided -->
    {#if icon}
      <div
        class="w-12 h-12 rounded-xl flex items-center justify-center mb-4 transition-colors {selected
          ? 'bg-primary/20'
          : 'bg-muted'}"
      >
        <span class="text-2xl">{icon}</span>
      </div>
    {/if}

    <!-- Title with help button -->
    <div class="mb-2 flex min-w-0 flex-wrap items-start gap-2 sm:gap-3">
      <h3
        class="min-w-0 text-base font-semibold leading-tight text-foreground sm:text-lg"
      >
        {title}
      </h3>
      {#if badge}
        <span
          class="max-w-full shrink-0 rounded-full border border-warning/30 bg-warning/10 px-2 py-1 text-[11px] font-medium uppercase tracking-wide text-warning"
        >
          {badge}
        </span>
      {/if}
      {#if hint}
        <span
          class="max-w-full shrink-0 rounded-full border border-primary/25 bg-primary/10 px-2 py-1 text-[11px] font-medium text-primary"
        >
          {hint}
        </span>
      {/if}
      {#if helpText}
        <CardHelpHint {helpText} {helpTip} />
      {/if}
    </div>

    <!-- Description -->
    {#if description}
      <p class="text-sm text-muted-foreground leading-relaxed">{description}</p>
    {/if}
    {#if disabled && disabledReason}
      <p class="mt-3 text-sm text-warning leading-relaxed">
        {disabledReason}
      </p>
    {/if}
  </div>
</div>
