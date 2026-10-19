<script lang="ts">
  /**
   * An on/off switch: a button with `role="switch"` and `aria-checked`, so a
   * setting that takes effect at once is a switch and not a pair of buttons.
   * The state is also written out ("On" / "Off"), never carried by colour
   * alone.
   */
  interface Props {
    checked: boolean;
    /** Accessible name when no visible label points at it with `for`. */
    label?: string;
    onchange: (next: boolean) => void;
    disabled?: boolean;
    id?: string;
    testId?: string;
    onText: string;
    offText: string;
    class?: string;
  }

  let {
    checked,
    label,
    onchange,
    disabled = false,
    id,
    testId,
    onText,
    offText,
    class: className = "",
  }: Props = $props();
</script>

<button
  {id}
  type="button"
  role="switch"
  aria-checked={checked}
  aria-label={label}
  {disabled}
  data-testid={testId}
  data-kx="control"
  class="inline-flex min-h-9 items-center gap-3 rounded-full px-3 py-1.5 text-sm disabled:cursor-not-allowed disabled:opacity-60 {className}"
  onclick={() => onchange(!checked)}
>
  <span
    class="relative inline-block h-5 w-9 shrink-0 rounded-full transition-colors {checked
      ? 'bg-primary'
      : 'bg-muted-foreground/40'}"
    aria-hidden="true"
  >
    <span
      class="absolute top-0.5 h-4 w-4 rounded-full bg-background shadow transition-[inset-inline-start] motion-reduce:transition-none {checked
        ? 'start-[1.125rem]'
        : 'start-0.5'}"
    ></span>
  </span>
  {checked ? onText : offText}
</button>
