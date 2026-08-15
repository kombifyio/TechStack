<script lang="ts">
  /**
   * MorphingText - Typewriter-style text animation that cycles through words
   * Inspired by: https://magicui.design/docs/components/morphing-text
   *
   * Usage:
   * <MorphingText texts={["Creating", "Validating", "Deploying"]} />
   */
  import { onMount, onDestroy } from "svelte";

  interface Props {
    texts: string[];
    interval?: number;
    typingSpeed?: number;
    showCursor?: boolean;
    class?: string;
  }

  let {
    texts = ["Loading", "Processing", "Completing"],
    interval = 3000,
    typingSpeed = 50,
    showCursor = true,
    class: className = "",
  }: Props = $props();

  let currentIndex = $state(0);
  let displayText = $state("");
  let isTyping = $state(false);
  let isDeleting = $state(false);
  let minWidthCh = $derived(Math.max(1, ...texts.map((text) => text.length)));

  let animationTimer: ReturnType<typeof setInterval> | null = null;
  let typeTimer: ReturnType<typeof setTimeout> | null = null;

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
    if (texts.length > 0) {
      // Start with first word
      typeText(texts[0], () => {
        // Begin cycling after first word is typed
        if (texts.length > 1) {
          animationTimer = setInterval(cycleToNextWord, interval);
        }
      });
    }
  });

  onDestroy(() => {
    if (animationTimer) clearInterval(animationTimer);
    if (typeTimer) clearTimeout(typeTimer);
  });
</script>

<span
  class="morphing-text inline-flex items-center whitespace-nowrap font-semibold {className}"
  style={`min-width: ${minWidthCh}ch;`}
>
  <span class="text-primary">{displayText}</span>
  {#if showCursor}
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
