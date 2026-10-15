<script lang="ts">
  import { creation } from "./creation-controller.svelte.js";
  import { tr } from "#lib/i18n.svelte.js";
  import { ChevronLeft, ChevronRight, Lightbulb } from "@lucide/svelte";
  import CreationTipArt, { type TipArtKind } from "./CreationTipArt.svelte";

  interface Tip {
    art: TipArtKind;
    eyebrow: string;
    title: string;
    body: string;
  }

  // What follows this run depends on how the node is provided.
  let nextBody = $derived.by(() => {
    if (creation.serverProvisioningMode === "hypervisor") {
      return tr("wizard.server.hypervisor.progress");
    }
    if (creation.creationOperation === "add-server") {
      if (creation.serverProvisioningMode === "kombify-cloud") {
        return tr("ui.stacksCreatingCreationRunStatus.theAdditionalManagedNodeIs");
      }
      return creation.agentPairingRequired
        ? tr("ui.stacksCreatingCreationRunStatus.afterYouRunTheOne")
        : tr("ui.stacksCreatingCreationRunStatus.theAdditionalNodeIsRegistered");
    }
    if (creation.serverProvisioningMode === "kombify-cloud") {
      return tr("ui.stacksCreatingCreationRunStatus.kombifyWillProvisionTheSubscription");
    }
    if (creation.serverProvisioningMode === "connect-remote") {
      return tr("ui.stacksCreatingCreationRunStatus.kombifyWillUseTheRemote");
    }
    return tr("ui.stacksCreatingCreationRunStatus.youWillReceiveAWorker");
  });

  let tips = $derived<Tip[]>([
    {
      art: "next",
      eyebrow: tr("ui.stacksCreatingCreationRunStatus.afterCreation"),
      title: tr("ui.stacksCreating.tips.nextTitle"),
      body: nextBody,
    },
    {
      art: "reach",
      eyebrow: tr("ui.stacksCreating.tips.reachEyebrow"),
      title: tr("ui.stacksCreating.tips.reachTitle"),
      body: tr("ui.stacksCreating.tips.reachBody"),
    },
    {
      art: "restore",
      eyebrow: tr("ui.stacksCreating.tips.restoreEyebrow"),
      title: tr("ui.stacksCreating.tips.restoreTitle"),
      body: tr("ui.stacksCreating.tips.restoreBody"),
    },
    {
      art: "code",
      eyebrow: tr("ui.stacksCreating.tips.codeEyebrow"),
      title: tr("ui.stacksCreating.tips.codeTitle"),
      body: tr("ui.stacksCreating.tips.codeBody"),
    },
    {
      art: "grow",
      eyebrow: tr("ui.stacksCreating.tips.growEyebrow"),
      title: tr("ui.stacksCreating.tips.growTitle"),
      body: tr("ui.stacksCreating.tips.growBody"),
    },
    {
      art: "own",
      eyebrow: tr("ui.stacksCreating.tips.ownEyebrow"),
      title: tr("ui.stacksCreating.tips.ownTitle"),
      body: tr("ui.stacksCreating.tips.ownBody"),
    },
  ]);

  let index = $state(0);
  let hovered = $state(false);
  let focusWithin = $state(false);
  let paused = $derived(hovered || focusWithin);
  let tip = $derived(tips[index]);

  function go(step: number) {
    index = (index + step + tips.length) % tips.length;
  }
</script>

<!-- Passes the wait with short, illustrated facts. A tip advances when its
     segment has filled; hover or focus pauses it, reduced motion stops it. -->
<section
  data-kx="plate"
  class="p-5"
  aria-roledescription="carousel"
  aria-labelledby="creation-tips-label"
  data-testid="creation-good-to-know"
  onpointerenter={() => (hovered = true)}
  onpointerleave={() => (hovered = false)}
  onfocusin={() => (focusWithin = true)}
  onfocusout={(event) => {
    const next = event.relatedTarget as Node | null;
    if (!next || !event.currentTarget.contains(next)) focusWithin = false;
  }}
>
  <div class="flex items-center justify-between gap-3">
    <p
      id="creation-tips-label"
      class="flex items-center gap-2 text-xs font-semibold uppercase tracking-wide text-primary"
    >
      <Lightbulb class="h-4 w-4" aria-hidden="true" />
      {tr("ui.stacksCreating.tips.label")}
    </p>
    <div class="flex items-center gap-1">
      <button
        type="button"
        class="tip-nav"
        aria-label={tr("ui.stacksCreating.tips.previous")}
        onclick={() => go(-1)}
      >
        <ChevronLeft class="h-4 w-4 rtl:rotate-180" aria-hidden="true" />
      </button>
      <button
        type="button"
        class="tip-nav"
        aria-label={tr("ui.stacksCreating.tips.next")}
        onclick={() => go(1)}
      >
        <ChevronRight class="h-4 w-4 rtl:rotate-180" aria-hidden="true" />
      </button>
    </div>
  </div>

  {#key index}
    <div
      class="tip mt-4"
      role="group"
      aria-roledescription="slide"
      aria-label="{index + 1} / {tips.length}"
    >
      <div
        class="tip-stage aspect-[16/9] overflow-hidden rounded-xl border border-border/60"
      >
        <CreationTipArt kind={tip.art} />
      </div>
      <div class="min-h-[9.5rem]">
        <p class="mt-5 text-xs font-medium text-muted-foreground">
          {tip.eyebrow}
        </p>
        <h3 class="mt-1 text-lg font-semibold leading-snug text-foreground">
          {tip.title}
        </h3>
        <p class="mt-2 text-sm leading-relaxed text-muted-foreground">
          {tip.body}
        </p>
      </div>
    </div>
  {/key}

  <div class="mt-4 flex items-center gap-1.5">
    {#each tips as item, i (item.art)}
      <button
        type="button"
        class="tip-segment"
        aria-label={tr("ui.stacksCreating.tips.show", {
          number: i + 1,
          total: tips.length,
        })}
        aria-current={i === index ? "true" : undefined}
        onclick={() => (index = i)}
      >
        <span class="tip-segment-track">
          {#if i === index}
            <span
              class="tip-segment-fill"
              class:paused
              onanimationend={() => go(1)}
            ></span>
          {:else if i < index}
            <span class="tip-segment-fill tip-segment-done"></span>
          {/if}
        </span>
      </button>
    {/each}
  </div>
</section>

<style>
  .tip-stage {
    background:
      radial-gradient(
        120% 90% at 0% 0%,
        color-mix(in oklab, var(--primary) 12%, transparent),
        transparent 60%
      ),
      radial-gradient(
        90% 80% at 100% 100%,
        color-mix(in oklab, var(--success) 12%, transparent),
        transparent 60%
      ),
      var(--card);
  }

  .tip-nav {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2rem;
    height: 2rem;
    border-radius: 0.5rem;
    color: var(--muted-foreground);
    transition:
      background-color var(--kx-dur-fast) var(--kx-ease-out),
      color var(--kx-dur-fast) var(--kx-ease-out);
  }

  .tip-nav:hover,
  .tip-nav:focus-visible {
    background: color-mix(in oklab, var(--muted) 70%, transparent);
    color: var(--foreground);
  }

  .tip-segment {
    flex: 1;
    padding-block: 0.5rem;
  }

  .tip-segment-track {
    position: relative;
    display: block;
    height: 0.25rem;
    overflow: hidden;
    border-radius: 9999px;
    background: color-mix(in oklab, var(--muted-foreground) 18%, transparent);
  }

  .tip-segment-fill {
    position: absolute;
    inset: 0;
    border-radius: inherit;
    background: var(--primary);
    transform-origin: left;
  }

  .tip-segment-done {
    opacity: 0.35;
  }

  :global([dir="rtl"]) .tip-segment-fill {
    transform-origin: right;
  }

  @media (prefers-reduced-motion: no-preference) {
    .tip {
      animation: tip-rise var(--kx-dur-morph) var(--kx-ease-out) both;
    }

    .tip-segment-fill:not(.tip-segment-done) {
      animation: tip-dwell 9s linear both;
    }

    .tip-segment-fill.paused {
      animation-play-state: paused;
    }
  }

  @keyframes tip-rise {
    from {
      opacity: 0;
      transform: translateY(4px);
    }
  }

  @keyframes tip-dwell {
    from {
      transform: scaleX(0);
    }
    to {
      transform: scaleX(1);
    }
  }
</style>
