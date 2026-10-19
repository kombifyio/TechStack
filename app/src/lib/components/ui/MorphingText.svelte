<script lang="ts">
  /**
   * MorphingText - Typewriter-style text animation that cycles through words
   * Inspired by: https://magicui.design/docs/components/morphing-text
   *
   * Usage:
   * <MorphingText texts={["Creating", "Validating", "Deploying"]} decorative />
   *
   * The animation is decoration. It stays still (the first word, no cursor)
   * for people who prefer reduced motion, and `decorative` hides it from
   * assistive technology: a changing word is not something to announce, so
   * the surrounding text carries the meaning on its own.
   */
  import { onMount, onDestroy } from "svelte";

  interface Props {
    texts: string[];
    interval?: number;
    typingSpeed?: number;
    showCursor?: boolean;
    /** Hide the animated word from assistive technology. */
    decorative?: boolean;
    class?: string;
  }

  let {
    texts = ["Loading", "Processing", "Completing"],
    interval = 3000,
    typingSpeed = 50,
    showCursor = true,
    decorative = false,
    class: className = "",
  }: Props = $props();

  let currentIndex = $state(0);
  let displayText = $state("");
  let isTyping = $state(false);
  let isDeleting = $state(false);
  let minWidthCh = $derived(Math.max(1, ...texts.map((text) => text.length)));

  let animationTimer: ReturnType<typeof setInterval> | null = null;
  let typeTimer: ReturnType<typeof setTimeout> | null = null;
  let reducedMotion = $state(false);

  function stopTimers() {
    if (animationTimer) clearInterval(animationTimer);
    if (typeTimer) clearTimeout(typeTimer);
    animationTimer = null;
    typeTimer = null;
    isTyping = false;
    isDeleting = false;
  }

  function start() {
    stopTimers();
    if (texts.length === 0) return;
    if (reducedMotion) {
      currentIndex = 0;
      displayText = texts[0];
      return;
    }
    // Start with first word
    typeText(texts[0], () => {
      // Begin cycling after first word is typed
      if (texts.length > 1) {
        animationTimer = setInterval(cycleToNextWord, interval);
      }
    });
  }

  function typeText(text: string, onComplete: () => void) {
    isTyping = true;
    isDeleting = false;
    let charIndex = 0;

    const type = () => {
      if (charIndex <= text.length) {
        displayText = text.slice(0, charIndex);
        charIndex++;
        typeTimer = setTimeout(type, typingSpeed);
      } else {
        isTyping = false;
        onComplete();
      }
    };

    type();
  }

  function deleteText(onComplete: () => void) {
    isDeleting = true;
    isTyping = false;
    let charIndex = displayText.length;

    const del = () => {
      if (charIndex >= 0) {
        displayText = displayText.slice(0, charIndex);
        charIndex--;
        typeTimer = setTimeout(del, typingSpeed / 2);
      } else {
        isDeleting = false;
        onComplete();
      }
    };

    del();
  }

  function cycleToNextWord() {
    deleteText(() => {
      currentIndex = (currentIndex + 1) % texts.length;
      typeText(texts[currentIndex], () => {});
    });
  }

  onMount(() => {
    const query = window.matchMedia("(prefers-reduced-motion: reduce)");
    reducedMotion = query.matches;
    start();
    const onChange = (event: MediaQueryListEvent) => {
      reducedMotion = event.matches;
      start();
    };
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  });

  onDestroy(stopTimers);
</script>

<span
  class="morphing-text inline-flex items-center whitespace-nowrap font-semibold {className}"
  style={`min-width: ${minWidthCh}ch;`}
  aria-hidden={decorative ? "true" : undefined}
>
  <span class="text-primary">{displayText}</span>
  {#if showCursor && !reducedMotion}
    <span
      class="cursor ml-0.5 w-0.5 h-[1.2em] bg-primary"
      class:blinking={!isTyping && !isDeleting}
    ></span>
  {/if}
</span>

<style>
  .cursor {
    display: inline-block;
  }

  .cursor.blinking {
    animation: blink 1s step-end infinite;
  }

  @media (prefers-reduced-motion: reduce) {
    .cursor.blinking {
      animation: none;
    }
  }

  @keyframes blink {
    0%,
    50% {
      opacity: 1;
    }
    51%,
    100% {
      opacity: 0;
    }
  }
</style>
