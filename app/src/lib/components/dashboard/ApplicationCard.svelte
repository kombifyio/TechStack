<script lang="ts">
  import {
    ApplicationCard as BrandApplicationCard,
    DEFAULT_ENTITY_LABELS,
    type EntityAction,
    type EntityIcon,
    type EntityLabels,
    type EntityPlacement,
  } from "@kombiverselabs/ui/entity";
  import type { AppCardAction } from "#lib/services/app-quick-actions.svelte.js";
  import { tr, trn } from "#lib/i18n.svelte.js";
  import { canonicalServiceLogo } from "#lib/application-logo.js";
  import type {
    CanonicalService,
    ServicePlacementClass,
  } from "#lib/api/services.js";
  import {
    applicationOpenUrl,
    applicationState,
    isStackedApp,
    serviceDisplayName,
  } from "#lib/dashboard/homelab-model.js";

  /*
   * Techstack's adapter over Brand's ApplicationCard (entity-cards concept):
   * it resolves the address, logo, status, placement and quick actions of an
   * application from its services and leaves the drawing to Brand.
   * - `s`: the app icon alone; a click opens the application.
   * - `m`: one 52 px line with Open, the direct quick actions and the "…" menu.
   * - `l`: 74 px, the height of the smallest service card, adding the status
   *   in words and the address.
   */
  interface Props {
    service: CanonicalService;
    /** Every component of the application; defaults to the service alone. */
    members?: readonly CanonicalService[];
    size?: "s" | "m" | "l";
    /** Where the host runs, used when the services report no placement. */
    placement?: ServicePlacementClass;
    /** Quick actions. `direct` ones sit on the card, the rest in the "…" menu. */
    actions?: readonly AppCardAction[];
    onDetails: () => void;
    testId?: string;
  }

  let {
    service,
    members = [service],
    size = "m",
    placement,
    actions = [],
    onDetails,
    testId = "application-card",
  }: Props = $props();

  const name = $derived(serviceDisplayName(service));
  const url = $derived(applicationOpenUrl(members));
  const appState = $derived(applicationState(service, [...members]));
  const stacked = $derived(isStackedApp(members));

  // Viewer override, then the recognition's logo (theme variant), then the
  // mirrored icon of the vendor domain; none lets Brand draw the monogram, and
  // Brand's avatar falls back to it as well when the image fails to load.
  const logoUrl = $derived(canonicalServiceLogo(service).src);

  /** Where the app opens, or how many components it has when it cannot. */
  const meta = $derived.by(() => {
    if (url) {
      try {
        return new URL(url).host;
      } catch {
        return url;
      }
    }
    return trn("ui.services.componentCount", members.length);
  });

  const where = $derived.by<EntityPlacement | undefined>(() => {
    for (const member of members) {
      const measured = member.placement_class;
      if (measured === "local" || measured === "cloud" || measured === "managed")
        return measured;
    }
    if (members.some((member) => member.target_kind === "managed_workload"))
      return "managed";
    return placement === "local" || placement === "cloud" || placement === "managed"
      ? placement
      : undefined;
  });

  function toEntity(action: AppCardAction): EntityAction {
    return {
      id: action.id,
      label: action.label,
      icon: action.icon as unknown as EntityIcon,
      href: action.href,
      state: action.disabledReason
        ? "disabled"
        : action.busy
          ? "executing"
          : undefined,
      disabledReason: action.disabledReason,
      onSelect: action.onSelect ? () => void action.onSelect?.() : undefined,
    };
  }
  const quick = $derived(actions.filter((a) => a.direct).map(toEntity));
  const menu = $derived(actions.filter((a) => !a.direct).map(toEntity));

  const D = DEFAULT_ENTITY_LABELS;
  const labels = $derived<Partial<EntityLabels>>({
    kind: { ...D.kind, application: tr("ui.services.application") },
    application: {
      ...D.application,
      open: tr("entity.app.open"),
      details: tr("entity.app.details"),
      more: tr("entity.app.more"),
      noAddress: tr("ui.servicesApplicationId.noReachableAddressReported"),
      management: {
        stacked: tr("ui.services.stacked"),
        unstacked: tr("ui.services.unstacked"),
      },
      status: { ...D.application.status, unknown: tr("ui.types.unknown") },
    },
    placement: {
      local: tr("ui.services.placement.local"),
      cloud: tr("ui.services.placement.cloud"),
      managed: tr("ui.nodeRow.placementManaged"),
    },
  });
</script>

<BrandApplicationCard
  {size}
  {name}
  {logoUrl}
  status={appState.label ? { label: appState.label, tone: appState.tone } : undefined}
  management={stacked ? "stacked" : "unstacked"}
  placement={where}
  {meta}
  href={url ?? undefined}
  actions={quick}
  {menu}
  {onDetails}
  {labels}
  data-testid={testId}
  data-service-id={service.id}
/>
