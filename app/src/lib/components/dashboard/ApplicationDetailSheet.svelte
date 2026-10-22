<script lang="ts">
  import { ExternalLink } from "@lucide/svelte";
  import {
    EntityDetailSheet,
    type EntityAction,
    type EntityDownload,
    type EntityLink,
    type EntityPresence,
  } from "@kombiverselabs/ui/entity";
  import { tr } from "#lib/i18n.svelte.js";
  import type { AppCardAction } from "#lib/services/app-quick-actions.svelte.js";
  import type { CanonicalService, ServiceClient } from "#lib/api/services.js";
  import {
    applicationOpenUrl,
    applicationState,
    isStackedApp,
    serviceAccessUrl,
    serviceDisplayName,
    type ServiceTone,
  } from "#lib/dashboard/homelab-model.js";
  import ConnectDevicesGuide from "./ConnectDevicesGuide.svelte";

  /*
   * The detail view of an application, bound to Brand's EntityDetailSheet
   * (owner direction 2026-10-10: no own panel). M and L cards open it; S goes
   * straight to the app. Downloads, vendor links and support links appear
   * only when the catalog carries them.
   */
  interface Props {
    open: boolean;
    onclose: () => void;
    service: CanonicalService;
    members?: readonly CanonicalService[];
    actions?: readonly AppCardAction[];
  }

  let { open, onclose, service, members = [service], actions = [] }: Props =
    $props();

  const presenceByTone: Record<ServiceTone, EntityPresence> = {
    ok: "online",
    warn: "busy",
    error: "offline",
    off: "idle",
  };

  const catalog = $derived(service.catalog);
  const url = $derived(applicationOpenUrl(members));
  const status = $derived(applicationState(service, [...members]));
  const stacked = $derived(isStackedApp(members));

  // A private AI chat app gets the guide to use it from other devices; a
  // model router in the same application is the better API endpoint.
  const connect = $derived.by(() => {
    if (!url || !members.some((member) => member.catalog?.id === "open-webui"))
      return undefined;
    const base = url.replace(/\/+$/, "");
    const router = members.find((member) => member.catalog?.id === "litellm");
    const routerUrl = router ? serviceAccessUrl(router) : undefined;
    return routerUrl
      ? { url: base, apiBase: `${routerUrl.replace(/\/+$/, "")}/v1`, routerKey: true }
      : { url: base, apiBase: `${base}/api`, routerKey: false };
  });

  /** The visitor's platform, so the matching download comes first. */
  function currentPlatform(): ServiceClient["platform"] | undefined {
    if (typeof navigator === "undefined") return undefined;
    const ua = navigator.userAgent;
    if (/android/i.test(ua)) return "android";
    if (/iphone|ipad|ipod/i.test(ua)) return "ios";
    if (/windows/i.test(ua)) return "windows";
    if (/mac os/i.test(ua)) return "macos";
    if (/linux/i.test(ua)) return "linux";
    return undefined;
  }

  const platformName: Record<ServiceClient["platform"], string> = {
    android: "Android",
    ios: "iOS",
    windows: "Windows",
    macos: "macOS",
    linux: "Linux",
  };

  function downloadLabel(client: ServiceClient): string {
    if (client.kind === "store" && client.platform === "android")
      return "Google Play";
    if (client.kind === "store" && client.platform === "ios") return "App Store";
    return tr("ui.appSheet.downloadFor", {
      platform: platformName[client.platform],
    });
  }

  const downloads = $derived.by<EntityDownload[]>(() => {
    const own = currentPlatform();
    const clients = [...(catalog?.clients ?? [])].sort(
      (a, b) => Number(b.platform === own) - Number(a.platform === own),
    );
    return clients.map((client) => ({
      label: downloadLabel(client),
      href: client.url,
      meta: platformName[client.platform],
    }));
  });

  const links = $derived.by<EntityLink[]>(() => {
    const out: EntityLink[] = [];
    if (catalog?.guide_url)
      out.push({
        kind: "docs",
        href: catalog.guide_url,
        label: tr("ui.appSheet.guide"),
      });
    const vendor = catalog?.vendor;
    if (vendor?.website)
      out.push({ kind: "vendor", href: vendor.website, label: vendor.name });
    if (vendor?.docs) out.push({ kind: "docs", href: vendor.docs });
    for (const support of vendor?.support ?? [])
      out.push({
        kind: "support",
        href: support.url,
        label: tr(`ui.appSheet.${support.kind}`),
      });
    return out;
  });

  const sheetActions = $derived<EntityAction[]>([
    ...(url
      ? [
          {
            id: "open",
            label: tr("ui.services.open"),
            icon: ExternalLink,
            href: url,
            external: true,
          } satisfies EntityAction,
        ]
      : []),
    ...actions.map(
      (action): EntityAction => ({
        id: action.id,
        label: action.label,
        icon: action.icon as EntityAction["icon"],
        href: action.href,
        state: action.disabledReason
          ? "disabled"
          : action.busy
            ? "executing"
            : "ready",
        disabledReason: action.disabledReason,
        onSelect: () => void action.onSelect?.(),
      }),
    ),
    {
      id: "details",
      label: tr("ui.appSheet.fullDetails"),
      icon: ExternalLink,
      href: `/services?service=${encodeURIComponent(service.id)}`,
    },
  ]);
</script>

<EntityDetailSheet
  {open}
  {onclose}
  kind="application"
  name={serviceDisplayName(service)}
  appIcon
  presence={presenceByTone[status.tone]}
  statusLabel={status.label || undefined}
  tags={[
    stacked ? tr("ui.services.stacked") : tr("ui.services.unstacked"),
    ...(catalog?.vendor?.license ? [catalog.vendor.license] : []),
  ]}
  {links}
  {downloads}
  actions={sheetActions}
>
  {#if connect}
    <ConnectDevicesGuide {...connect} />
  {/if}
</EntityDetailSheet>
