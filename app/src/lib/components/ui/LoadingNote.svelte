<!--
  The one way content that is still arriving is shown: a quiet line of text
  with a small animated mark, in place of the content. No card, no skeleton
  block, no layout the real content will not have.
-->
<script lang="ts">
  interface Props {
    /** Already translated, e.g. tr("ui.homelab.loading"). */
    label: string;
    class?: string;
    testid?: string;
  }

  let { label, class: className = "", testid }: Props = $props();
</script>

<p
  class="loading-note inline-flex items-center gap-2 text-sm text-muted-foreground {className}"
  role="status"
  aria-live="polite"
  data-testid={testid}
>
  <span class="loading-note-dots" aria-hidden="true">
    <span></span><span></span><span></span>
  </span>
  <span>{label}</span>
</p>

<style>
  .loading-note-dots {
    display: inline-flex;
    gap: 3px;
  }
  .loading-note-dots span {
    width: 4px;
    height: 4px;
    border-radius: 9999px;
    background: currentColor;
    opacity: 0.25;
    animation: loading-note-dot 1.2s ease-in-out infinite;
  }
  .loading-note-dots span:nth-child(2) {
    animation-delay: 0.2s;
  }
  .loading-note-dots span:nth-child(3) {
    animation-delay: 0.4s;
  }
  @keyframes loading-note-dot {
    0%,
    80%,
    100% {
      opacity: 0.25;
    }
    40% {
      opacity: 1;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .loading-note-dots span {
      animation: none;
      opacity: 0.6;
    }
  }
</style>
