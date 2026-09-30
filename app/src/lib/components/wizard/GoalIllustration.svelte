<script lang="ts">
  import { Bot, Network, Workflow } from "@lucide/svelte";
  import type { CreationGoalId } from "#lib/wizard/creationGoals.js";
  import { theme } from "#lib/stores/theme.js";

  const appearance = theme.resolved;

  let {
    goal,
    compact = false,
    priority = false,
  }: { goal: CreationGoalId; compact?: boolean; priority?: boolean } = $props();

  const illustratedGoals = new Set<CreationGoalId>([
    "photos",
    "files",
    "vault",
    "media",
    "smart-home",
    "dev",
    "mail",
    "game",
  ]);
  const fallbackIcons = { network: Network, automation: Workflow, ai: Bot };
  const FallbackIcon = $derived(
    fallbackIcons[goal as keyof typeof fallbackIcons] ?? Network,
  );
</script>

<div class="scene" class:compact data-goal={goal} aria-hidden="true">
  {#if illustratedGoals.has(goal)}
    <img
      src={`/illustrations/use-cases/${goal}${$appearance === "dark" ? "-night" : ""}.webp`}
      alt=""
      loading={priority ? "eager" : "lazy"}
      decoding="async"
    />
  {:else}
    <FallbackIcon size={36} strokeWidth={1.5} />
  {/if}
</div>

<style>
  .scene {
    position: relative;
    width: 100%;
    height: 100%;
    display: block;
    overflow: hidden;
    color: var(--primary);
    background: color-mix(in oklch, var(--primary) 5%, var(--card));
  }
  .scene img {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
    object-position: center 35%;
  }
  .scene:not(:has(img)) {
    display: grid;
    place-items: center;
  }
  :global(.goals-grid:not(.focus) .goal-frame:not(.expanded)) .scene img,
  .compact img {
    object-position: center 55%;
  }
  .scene:has(img)::after {
    content: "";
    position: absolute;
    inset: 0;
    pointer-events: none;
    background: linear-gradient(
      to bottom,
      transparent 35%,
      rgb(8 12 19 / 45%) 68%,
      rgb(8 12 19 / 92%)
    );
  }
  .scene.compact:has(img)::after {
    display: none;
  }
  .compact {
    width: 80px;
    height: 80px;
    border-radius: 9px;
  }
  @media (prefers-reduced-motion: no-preference) {
    .scene img {
      transition: transform var(--kx-dur-morph) var(--kx-ease-morph);
    }
    :global(.goal-frame:hover) .scene img {
      transform: scale(1.035);
    }
  }
</style>
