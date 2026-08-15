<script lang="ts">
  import { afterNavigate } from "$app/navigation";
  import * as Sentry from "@sentry/sveltekit";
  import { getClientBootstrap } from "$lib/client/bootstrap";
  import { appVersion } from "$lib/config";
  import {
    classifyRoute,
    createPostHogClient,
    isValidEventName,
    sanitizeNavigationTarget,
    toTechstackAnalyticsUser,
    type AnalyticsUser,
  } from "$lib/analytics/posthog";
  import { authStore } from "$lib/stores/auth.svelte";

  let lastIdentityKey = $state("");
  let lastRouteKey = $state("");

  afterNavigate(({ to }) => {
    if (!to?.url) return;
    captureRoute(to.url);
  });

  $effect(() => {
    const user = getAnalyticsUser();
    const identityKey = [
      user?.authSubject,
      user?.organizationId,
      user?.role,
      user?.isStaff ? "staff" : "customer",
    ].join("|");

    if (user?.authSubject && identityKey !== lastIdentityKey) {
      lastIdentityKey = identityKey;
      Sentry.setUser({ id: user.authSubject });
      Sentry.setTag("organization_id", user.organizationId ?? "unscoped");
      void createClient().capture("auth:session_identified", {
        user,
        properties: {
          deployment_mode: authStore.deploymentMode,
        },
      });
      captureRoute(new URL(window.location.href));
      return;
    }

    if (!user?.authSubject && lastIdentityKey) {
      lastIdentityKey = "";
      lastRouteKey = "";
      Sentry.setUser(null);
      Sentry.setTag("organization_id", "unscoped");
    }
  });

  function createClient() {
    const bootstrap = getClientBootstrap();
    return createPostHogClient({
      apiKey: bootstrap.telemetry.posthog.key || undefined,
      host: bootstrap.telemetry.posthog.host || undefined,
      environment: bootstrap.telemetry.posthog.environment || undefined,
      edition:
        bootstrap.kombifyEdition ||
        (authStore.deploymentMode === "saas"
          ? "saas-embedded"
          : "selfhost-oss"),
      appVersion,
      location: window.location,
    });
  }

  function getAnalyticsUser(): AnalyticsUser | null {
    return toTechstackAnalyticsUser(authStore.cloudUser);
  }

  function captureRoute(url: URL): void {
    const user = getAnalyticsUser();
    if (!user?.authSubject) {
      return;
    }

    const routeKey = `${url.pathname}${url.hash || ""}`;
    if (routeKey === lastRouteKey) {
      return;
    }

    lastRouteKey = routeKey;
    const routeGroup = classifyRoute(url.pathname);
    void createClient().capture("techstack:page_viewed", {
      user,
      properties: {
        page_id: routeGroup,
      },
    });
  }

  function handleClick(event: MouseEvent): void {
    const user = getAnalyticsUser();
    if (!user?.authSubject) {
      return;
    }

    const target = event.target;
    if (!(target instanceof Element)) {
      return;
    }

    const explicitTarget = target.closest<HTMLElement>(
      "[data-posthog-event], [data-analytics-event]",
    );
    const explicitEvent =
      explicitTarget?.dataset.posthogEvent ??
      explicitTarget?.dataset.analyticsEvent;
    if (explicitTarget && explicitEvent && isValidEventName(explicitEvent)) {
      void createClient().capture(explicitEvent, {
        user,
        properties: {
          item_id: explicitTarget.dataset.analyticsItemId,
          navigation_area: explicitTarget.dataset.analyticsNavigationArea,
          deployment_mode: authStore.deploymentMode,
        },
      });
      return;
    }

    const anchor = target.closest<HTMLAnchorElement>("a[href]");
    if (!anchor) {
      return;
    }

    const href = sanitizeNavigationTarget(anchor.href, window.location.origin);
    if (!href) {
      return;
    }

    void createClient().capture("navigation:item_clicked", {
      user,
      properties: {
        item_id: classifyRoute(href),
        navigation_area: classifyRoute(window.location.pathname),
      },
    });
  }
</script>

<svelte:window onclick={handleClick} />
