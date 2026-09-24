<script lang="ts">
  /**
   * Product Button adapter over @kombiverselabs/ui/primitives.
   * Stamps data-testid onto the real <button> so Playwright enable/click
   * checks keep working. Theme and recipes stay in the design package.
   */
  import { Button as PackageButton } from "@kombiverselabs/ui/primitives";
  import type { Snippet } from "svelte";

  interface Props {
    children: Snippet;
    type?: "button" | "submit" | "reset";
    variant?: "primary" | "secondary" | "ghost" | "destructive" | "outline";
    size?: "sm" | "md" | "lg";
    disabled?: boolean;
    ariaLabel?: string;
    onclick?: (event: MouseEvent) => void;
    class?: string;
    testId?: string;
    /**
     * Coach-mark anchor (ONBOARDING-JOURNEY-STANDARD §6). Stamped onto the
     * real <button> like testId, because the coach-mark measures the element
     * it points at and the wrapping span has no box of its own.
     */
    anchor?: string;
  }

  let {
    children,
    variant = "secondary",
    testId,
    anchor,
    class: className = "",
    ...rest
  }: Props = $props();

  const packageVariant = $derived(
    variant === "outline" ? "secondary" : variant,
  );

  let host: HTMLElement | undefined = $state();

  $effect(() => {
    const button = host?.querySelector("button");
    if (!button) {
      return;
    }
    if (testId) {
      button.setAttribute("data-testid", testId);
    } else {
      button.removeAttribute("data-testid");
    }
    if (anchor) {
      button.setAttribute("data-onboarding-anchor", anchor);
    } else {
      button.removeAttribute("data-onboarding-anchor");
    }
  });
</script>

<span bind:this={host} class="contents">
  <PackageButton variant={packageVariant} class={className} {...rest}>
    {@render children()}
  </PackageButton>
</span>
