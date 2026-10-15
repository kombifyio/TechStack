<script lang="ts">
  import { onMount, untrack } from "svelte";
  import { tr, formatDateTime, formatNumber } from "#lib/i18n.svelte.js";
  import {
    getDomainMonitoring,
    type DomainMonitoringReport,
    type DomainCondition,
    type ExpiryEvidence,
    type DriftEvidence,
    type DomainMonitoringRow,
  } from "#lib/api/domain-monitoring.js";

  let {
    serverId = "",
    refreshKey = 0,
  }: {
    serverId?: string;
    refreshKey?: number;
  } = $props();

  let report = $state<DomainMonitoringReport | null>(null);
  let loading = $state(true);
  let unavailable = $state(false);
  let now = $state(Date.now());
  let reportScope = "";
  let request = 0;

  $effect(() => {
    const scope = serverId;
    void refreshKey;
    const current = ++request;
    const controller = new AbortController();
    untrack(() => {
      if (scope !== reportScope) {
        report = null;
        unavailable = false;
      }
      reportScope = scope;
      loading = true;
      void getDomainMonitoring(scope || undefined, controller.signal).then(
        (value) => {
          if (current !== request) return;
          report = value;
          now = Date.now();
          unavailable = false;
          loading = false;
        },
        () => {
          if (current !== request) return;
          now = Date.now();
          unavailable = true;
          loading = false;
        },
      );
    });
    return () => {
      request++;
      controller.abort();
    };
  });

  onMount(() => {
    // Evidence ages even when requests fail or the parent does not refresh.
    const timer = setInterval(() => (now = Date.now()), 15_000);
    return () => clearInterval(timer);
  });

  function stamp(value?: string): string {
    return value && Number.isFinite(Date.parse(value))
      ? formatDateTime(value, { dateStyle: "short", timeStyle: "short" })
      : tr("domains.unknown");
  }

  function freshness(fact: ExpiryEvidence | DriftEvidence): string {
    if (unavailable || fact.freshness === "stale") return "stale";
    if (fact.state === "unknown") return "unknown";
    if (fact.freshness !== "fresh" || !fact.observed_at || !fact.fresh_until)
      return "unknown";
    const observed = Date.parse(fact.observed_at);
    const until = Date.parse(fact.fresh_until);
    if (
      !Number.isFinite(observed) ||
      !Number.isFinite(until) ||
      observed > now ||
      until <= observed
    )
      return "unknown";
    return until > now ? "fresh" : "stale";
  }

  function remaining(fact: ExpiryEvidence): number | undefined {
    if (fact.state !== "known") return undefined;
    const deadline = fact.expires_at ? Date.parse(fact.expires_at) : NaN;
    if (Number.isFinite(deadline)) return (deadline - now) / 1000;
    const generated = report?.generated_at
      ? Date.parse(report.generated_at)
      : NaN;
    if (
      typeof fact.remaining_seconds === "number" &&
      Number.isFinite(fact.remaining_seconds) &&
      Number.isFinite(generated)
    )
      return fact.remaining_seconds - Math.max(0, now - generated) / 1000;
    return undefined;
  }

  function timeRemaining(fact: ExpiryEvidence): string {
    const seconds = remaining(fact);
    if (seconds === undefined) return tr("domains.unknown");
    const days = formatNumber(Math.abs(seconds) / 86400, {
      maximumFractionDigits: 1,
    });
    return tr(seconds <= 0 ? "domains.expired" : "domains.remaining", { days });
  }

  function driftLabel(fact: DriftEvidence): string {
    if (fact.state === "drifted") return tr("domains.drifted");
    if (fact.state === "matching" && freshness(fact) === "fresh")
      return tr("domains.matching");
    return tr("domains.unknown");
  }

  function conditionRecovered(condition: DomainCondition): boolean {
    return (
      condition.state === "resolved" &&
      !!condition.resolved_at &&
      Number.isFinite(Date.parse(condition.resolved_at))
    );
  }

  function conditionsUnique(conditions: DomainCondition[]): DomainCondition[] {
    // Backend IDs represent persistent conditions, not each polling occurrence.
    return conditions.filter(
      (condition, index) =>
        conditions.findIndex((item) => item.id === condition.id) === index,
    );
  }

  function codeLabel(group: "kind" | "reason" | "field", code: string): string {
    const base = group === "kind" ? code.split(":")[0] : code;
    const key = `domains.${group}.${base}`;
    const label = tr(key);
    return label === key ? code : label;
  }

  function expectationSource(fact: DriftEvidence): string {
    const key = `domains.source.${fact.expected_source || "unknown"}`;
    const label = tr(key);
    return label === key
      ? fact.expected_source || tr("domains.unknown")
      : label;
  }

  function conditionFreshness(
    domain: DomainMonitoringRow,
    condition: DomainCondition,
  ): string {
    if (
      condition.kind.startsWith("registration") ||
      condition.kind === "stale_observation:registration"
    )
      return freshness(domain.registration);
    if (
      condition.kind.startsWith("dns") ||
      condition.kind === "stale_observation:dns"
    )
      return freshness(domain.dns);
    return freshness(
      condition.kind === "certificate_mismatch" ? domain.tls_drift : domain.tls,
    );
  }
</script>

{#snippet evidence(fact: ExpiryEvidence | DriftEvidence)}
  <div
    class="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground"
  >
    <span class:text-warning={freshness(fact) === "stale"}>
      {tr(`domains.${freshness(fact)}`)}
    </span>
    <span>{tr("domains.checked")}: {stamp(fact.observed_at)}</span>
    <span>{tr("domains.source")}: {fact.source || tr("domains.unknown")}</span>
    {#if fact.last_attempt_at}
      <span>{tr("domains.lastAttempt")}: {stamp(fact.last_attempt_at)}</span>
    {/if}
  </div>
  {#if fact.reason_code}
    <p class="mt-1 break-words text-xs text-muted-foreground">
      {tr("domains.reason")}: {codeLabel("reason", fact.reason_code)}
    </p>
  {/if}
{/snippet}

{#snippet expiry(title: string, fact: ExpiryEvidence)}
  <section class="min-w-0 rounded-lg border border-border bg-background p-3">
    <h4 class="text-sm font-medium">{title}</h4>
    <p class="mt-2 text-sm">
      {tr("domains.expires")}: {fact.state === "known"
        ? stamp(fact.expires_at)
        : tr("domains.unknown")}
    </p>
    {#if fact.state === "unknown" && fact.expires_at}
      <p class="mt-1 text-xs text-muted-foreground">
        {tr("domains.lastExpiry")}: {stamp(fact.expires_at)}
      </p>
    {/if}
    <p
      class="mt-1 text-sm font-medium"
      class:text-destructive={(remaining(fact) ?? Infinity) <= 0}
    >
      {timeRemaining(fact)}
    </p>
    {@render evidence(fact)}
  </section>
{/snippet}

{#snippet drift(title: string, fact: DriftEvidence)}
  <section class="min-w-0 rounded-lg border border-border bg-background p-3">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h4 class="text-sm font-medium">{title}</h4>
      <span
        class="text-xs"
        class:text-warning={fact.state === "drifted"}
        class:text-muted-foreground={fact.state !== "drifted"}
      >
        {driftLabel(fact)}
      </span>
    </div>
    {#if fact.detected_at}
      <p class="mt-2 text-xs text-muted-foreground">
        {tr("domains.detected")}: {stamp(fact.detected_at)}
      </p>
    {/if}
    <p class="mt-2 text-xs text-muted-foreground">
      {tr("domains.expectedSource")}: {expectationSource(fact)}
    </p>
    {#each fact.differences as difference, index (`${difference.field}:${index}`)}
      <div class="mt-3 border-t border-border pt-2 text-xs">
        <p class="break-words font-medium">
          {codeLabel("field", difference.field)}{#if difference.state}
            · {tr(
              `domains.${difference.state === "matching" ? (freshness(fact) === "fresh" ? "matching" : "unknown") : difference.state}`,
            )}{/if}
        </p>
        <dl class="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
          <dt class="text-muted-foreground">{tr("domains.expected")}</dt>
          <dd class="break-all font-mono">
            {difference.expected.length
              ? difference.expected.map((value) => value || '""').join(", ")
              : tr("domains.unknown")}
          </dd>
          <dt class="text-muted-foreground">{tr("domains.observed")}</dt>
          <dd class="break-all font-mono">
            {difference.observed.length
              ? difference.observed.map((value) => value || '""').join(", ")
              : tr(
                  difference.state === "unknown"
                    ? "domains.unknown"
                    : "domains.noValues",
                )}
          </dd>
        </dl>
        {#if difference.reason_code}<p class="mt-1 text-muted-foreground">
            {codeLabel("reason", difference.reason_code)}
          </p>{/if}
      </div>
    {/each}
    {@render evidence(fact)}
  </section>
{/snippet}

<section
  class="mb-4 rounded-lg border border-border bg-card p-4"
  aria-labelledby={`domain-monitoring-title-${serverId || "fleet"}`}
  data-testid="domain-monitoring"
>
  <h2
    id={`domain-monitoring-title-${serverId || "fleet"}`}
    class="text-sm font-semibold text-foreground"
  >
    {tr("domains.title")}
  </h2>
  <p class="mt-1 text-xs text-muted-foreground">{tr("domains.intro")}</p>

  {#if unavailable}
    <p
      class="mt-3 text-sm text-warning"
      role="status"
      data-testid="domain-monitoring-unavailable"
    >
      {tr(report ? "domains.retained" : "domains.unavailable")}
    </p>
  {:else if loading && !report}
    <p class="mt-3 text-sm text-muted-foreground" role="status">
      {tr("domains.loading")}
    </p>
  {/if}

  {#if report}
    {#if report.domains.length === 0 && !unavailable}
      <p class="mt-3 text-sm text-muted-foreground">{tr("domains.empty")}</p>
    {/if}
    <div class="mt-4 space-y-4">
      {#each report.domains as domain (domain.id)}
        <article
          class="rounded-lg border border-border p-3 sm:p-4"
          data-testid="domain-monitoring-row"
          data-domain-id={domain.id}
        >
          <h3 class="break-all font-mono text-sm font-semibold">
            {domain.domain}
          </h3>
          {#if domain.tls_port && domain.tls_port !== 443}<p
              class="mt-1 text-xs text-muted-foreground"
            >
              {tr("domains.port")}: {domain.tls_port}
            </p>{/if}
          {#if domain.routing_revision}<p
              class="mt-1 text-xs text-muted-foreground"
            >
              {tr("domains.revision", { revision: domain.routing_revision })}
            </p>{/if}
          {#if domain.registered_domain && domain.registered_domain !== domain.domain}
            <p class="mt-1 break-all text-xs text-muted-foreground">
              {tr("domains.registration")}: {domain.registered_domain}
            </p>
          {/if}
          <div
            class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground"
          >
            {#if domain.server_id}
              <a
                class="underline underline-offset-2 hover:text-foreground"
                href={`/monitoring/${encodeURIComponent(domain.server_id)}`}
              >
                {tr("domains.server")}: {domain.server_id}
              </a>
            {/if}
            <span
              >{tr("domains.services")}: {domain.services.length
                ? domain.services
                    .map((service) => service.name || service.id)
                    .join(", ")
                : tr("domains.unknown")}</span
            >
          </div>
          <div class="mt-3 grid gap-3 lg:grid-cols-2">
            {@render expiry(tr("domains.registration"), domain.registration)}
            <div>
              {@render expiry(tr("domains.tls"), domain.tls)}
              {#if domain.tls.issuer}
                <p class="mt-1 break-words text-xs text-muted-foreground">
                  {tr("domains.issuer")}: {domain.tls.issuer}
                </p>
              {/if}
              {#if domain.tls.state === "known" && typeof domain.tls.valid === "boolean" && freshness(domain.tls) === "fresh"}
                <p
                  class="mt-1 text-xs"
                  class:text-destructive={domain.tls.valid === false}
                >
                  {tr(domain.tls.valid ? "domains.valid" : "domains.invalid")}
                </p>
              {/if}
            </div>
            {@render drift(tr("domains.dns"), domain.dns)}
            {@render drift(tr("domains.tlsDrift"), domain.tls_drift)}
          </div>
          {#if domain.conditions.length}
            <section class="mt-3 border-t border-border pt-3">
              <h4 class="text-xs font-semibold">{tr("domains.conditions")}</h4>
              <ul class="mt-2 space-y-2">
                {#each conditionsUnique(domain.conditions) as condition (condition.id)}
                  <li
                    class="rounded-md border border-border p-2 text-xs"
                    data-testid="domain-condition"
                    data-condition-id={condition.id}
                    data-state={condition.state}
                  >
                    <p
                      class="break-words font-medium"
                      class:text-warning={!conditionRecovered(condition)}
                    >
                      {codeLabel("kind", condition.kind)} · {tr(
                        conditionRecovered(condition)
                          ? "domains.resolved"
                          : "domains.active",
                      )}
                    </p>
                    <div
                      class="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-muted-foreground"
                    >
                      <span
                        >{tr(
                          `domains.${conditionFreshness(domain, condition)}`,
                        )}</span
                      >
                      <span
                        >{tr("domains.detected")}: {stamp(
                          condition.first_detected_at,
                        )}</span
                      >
                      <span
                        >{tr("domains.lastObserved")}: {stamp(
                          condition.last_observed_at,
                        )}</span
                      >
                      {#if conditionRecovered(condition)}
                        <span
                          >{tr("domains.resolved")}: {stamp(
                            condition.resolved_at,
                          )}</span
                        >
                      {/if}
                    </div>
                    {#if condition.evidence_ref}
                      <p class="mt-1 break-all text-muted-foreground">
                        {tr("domains.evidence")}:
                        <span class="font-mono">{condition.evidence_ref}</span>
                      </p>
                    {/if}
                  </li>
                {/each}
              </ul>
            </section>
          {/if}
        </article>
      {/each}
    </div>
  {/if}
</section>
