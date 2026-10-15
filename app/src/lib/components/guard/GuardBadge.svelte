<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { ShieldAlert, ShieldCheck, ShieldQuestionMark } from "@lucide/svelte";
  import { collectGuardEvidence } from "#lib/api/guard.js";
  import type { CanonicalServer } from "#lib/api/registry.js";
  import { assessGuard } from "#lib/guard/facts.js";

  interface Props {
    server: CanonicalServer | null | undefined;
    /** Worker capability map, when the caller holds it. */
    capabilities?: unknown;
    /** Makes the badge a link to the full Guard panel. */
    href?: string;
  }

  let { server, capabilities, href }: Props = $props();

  // Derived from the same persisted facts as the panel, at the moment the list
  // was read; a list refresh re-derives it.
  const assessment = $derived(
    assessGuard(collectGuardEvidence(server, capabilities), Date.now()),
  );

  const total = $derived(assessment.facts.length);
  const label = $derived(
    assessment.overall === "ready"
      ? tr("guard.badge.ready")
      : assessment.overall === "attention"
        ? tr("guard.badge.attention", { count: assessment.attentionCount })
        : tr("guard.badge.unverified", {
            ok: assessment.okCount,
            total,
          }),
  );
  const tone = $derived(
    assessment.overall === "ready"
      ? "border-success/40 text-success"
      : assessment.overall === "attention"
        ? "border-warning/50 text-warning"
        : "border-border text-muted-foreground",
  );
</script>

{#snippet content()}
  {#if assessment.overall === "ready"}
    <ShieldCheck class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
  {:else if assessment.overall === "attention"}
    <ShieldAlert class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
  {:else}
    <ShieldQuestionMark class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
  {/if}
  <span>{label}</span>
{/snippet}

{#if href}
  <a
    {href}
    class="inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] font-semibold {tone}"
    data-testid="guard-badge"
    data-overall={assessment.overall}
    title={tr("guard.badge.open")}
  >
    {@render content()}
  </a>
{:else}
  <span
    class="inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] font-semibold {tone}"
    data-testid="guard-badge"
    data-overall={assessment.overall}
  >
    {@render content()}
  </span>
{/if}
