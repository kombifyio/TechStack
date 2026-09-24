<script lang="ts">
  import "../app.css";
  import { page } from "$app/state";
  import { onMount } from "svelte";
  import { initI18n } from "#lib/i18n.svelte.js";
  import { getInfo } from "#lib/api/health.js";
  import { getInstance } from "#lib/api/instance.js";
  import { appDeployLabel, productIdentityLabel } from "#lib/config.js";
  import ReloginModal from "#lib/components/ReloginModal.svelte";
  import InAppDialogHost from "#lib/components/InAppDialogHost.svelte";
  import TechstackAnalytics from "#lib/components/TechstackAnalytics.svelte";
  import {
    Toast,
    KeyboardShortcuts,
    FooterModern,
  } from "#lib/components/index.js";
  import {
    SidebarNav,
    InlineTabNav,
  } from "#lib/components/navigation/index.js";
  import { afterNavigate } from "$app/navigation";
  import { TRANSIENT_PANEL_DISMISS_EVENT } from "@kombiverselabs/ui/shell";
  import { navPreview } from "#lib/stores/navPreview.svelte.js";
  import { onboardingStore } from "#lib/onboarding/store.svelte.js";
  import IntroductionHost from "#lib/components/onboarding/IntroductionHost.svelte";
  import CompanionHost from "#lib/components/CompanionHost.svelte";
  import { features, loadFeatures } from "#lib/stores/features.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { theme } from "#lib/stores/theme.js";
  import {
    deploymentMode,
    isHostNavigationOwned,
    showSidebar,
    showInlineTabs,
  } from "#lib/stores/deploymentMode.js";
  import { withHostNavigation } from "#lib/embedded-navigation.js";
  import { initBridge, destroyBridge } from "#lib/stores/postMessageBridge.js";
  import {
    clearStackIdentity,
    hydrateStackIdentityFromBackend,
    initStackIdentity,
  } from "#lib/stores/stackIdentity.js";
  import { Menu, X } from "@lucide/svelte";

  let { children } = $props();
  let productIdentity = $state("");
  let instanceId = $state("");
  // Start with sidebar closed by default to avoid mobile overlay blocking clicks
  let sidebarOpen = $state(false);
  let sidebarCollapsed = $state(false);
  let sidebarWidth = $state(256);
  const MOBILE_BREAKPOINT = 768;
  const COMPACT_RAIL_BREAKPOINT = 1024;
  let viewportWidth = $state(COMPACT_RAIL_BREAKPOINT);
  const isMobileViewport = $derived(viewportWidth < MOBILE_BREAKPOINT);
  const isCompactViewport = $derived(
    viewportWidth >= MOBILE_BREAKPOINT &&
      viewportWidth < COMPACT_RAIL_BREAKPOINT,
  );
  let authInitialized = $state(false);
  let authSyncVersion = 0;

  $effect(() => {
    if (!authInitialized) {
      return;
    }

    const authenticated = authStore.isAuthenticated;
    const sessionKind = authStore.v2SessionActive ? "v2" : "local";
    const userEmail = authStore.userEmail;
    const currentVersion = ++authSyncVersion;

    if (!authenticated) {
      features.reset();
      clearStackIdentity();
      navPreview.clear();
      onboardingStore.clear();
      return;
    }
    // Flyout contents: loaded once per identity, never on hover.
    navPreview.load(userEmail ?? "anonymous");
    void onboardingStore.load(userEmail ?? "anonymous");

    void (async () => {
      try {
        await refreshEmbeddedSessionIfNeeded();
        await Promise.all([
          loadFeatures(true),
          hydrateStackIdentityFromBackend(),
        ]);
      } finally {
        const staleSync = currentVersion !== authSyncVersion;
        const sessionChanged =
          !authStore.isAuthenticated ||
          authStore.userEmail !== userEmail ||
          (authStore.v2SessionActive ? "v2" : "local") !== sessionKind;

        if (!staleSync && sessionChanged) {
          features.reset();
          clearStackIdentity();
        }
      }
    })();
  });

  // Any transient panel (nav flyout, user menu) closes on navigation; the
  // package listens for this event and the product decides when to fire it.
  afterNavigate(() => {
    window.dispatchEvent(new CustomEvent(TRANSIENT_PANEL_DISMISS_EVENT));

    // Cloud-owned embeds may navigate through content links or a parent
    // postMessage without repeating the ownership flag. Keep it in the URL so
    // a subsequent reload cannot unexpectedly restore the tool's own chrome.
    if ($isHostNavigationOwned) {
      const currentPath = `${window.location.pathname}${window.location.search}${window.location.hash}`;
      const preservedPath = withHostNavigation(currentPath);
      if (preservedPath !== currentPath) {
        window.history.replaceState(window.history.state, "", preservedPath);
      }
    }
  });

  // Initialize i18n and deployment mode on mount
  onMount(() => {
    // Initialize bridge early when in iframe to receive theme messages ASAP
    if (window.parent !== window) {
      initBridge();
    }

    initStackIdentity();
    theme.init();
    initI18n();
    loadApiInfo();
    void (async () => {
      try {
        await authStore.init({ embedded: window.parent !== window });
        await deploymentMode.init(authStore.deploymentMode);
        await refreshEmbeddedSessionIfNeeded();
      } finally {
        authInitialized = true;
      }
    })();

    const syncViewport = () => {
      viewportWidth = window.innerWidth;
      if (viewportWidth >= MOBILE_BREAKPOINT) sidebarOpen = false;
    };
    syncViewport();

    window.addEventListener("resize", syncViewport);
    return () => {
      destroyBridge();
      window.removeEventListener("resize", syncViewport);
      authInitialized = false;
    };
  });

  async function loadApiInfo() {
    try {
      const info = await getInfo();
      productIdentity = productIdentityLabel(info.version, info.revision);
    } catch (err) {
      console.error("Failed to fetch API info", err);
      productIdentity = appDeployLabel;
    }
    try {
      const inst = await getInstance();
      if (inst?.id) {
        instanceId = inst.id;
      }
    } catch (err) {
      console.error("Failed to fetch instance identity", err);
    }
  }

  async function refreshEmbeddedSessionIfNeeded() {
    if (typeof window === "undefined") return;
    if (window.parent === window) return;
    if (authStore.deploymentMode !== "saas") return;

    const { refreshEmbeddedCloudSession } =
      await import("#lib/auth/embedded-session.js");
    await refreshEmbeddedCloudSession();
  }

  // Use derived stores for reactive values in Svelte 5
  const isLoggedIn = $derived(authStore.isAuthenticated);
  // SvelteKit 3: `$app/state` replaces the `$app/stores` page store, so the
  // former `derived(page, …)` store becomes a plain `$derived` rune.
  const isAuthPage = $derived(page.url.pathname === "/login");

  function toggleSidebar() {
    sidebarOpen = !sidebarOpen;
  }

  function toggleSidebarCollapse() {
    sidebarCollapsed = !sidebarCollapsed;
    persistRail();
  }

  /*
   * Rail width is a device preference, like the finish: it belongs to this
   * browser and never to the account, so it lives in localStorage and is read
   * once on mount. Guarded because the layout also renders on the server.
   */
  const RAIL_KEY = "techstack-rail-width";

  function handleRailResize(next: number) {
    sidebarWidth = next;
    persistRail();
  }

  function persistRail() {
    if (typeof localStorage === "undefined") return;
    try {
      localStorage.setItem(RAIL_KEY, String(sidebarWidth));
      localStorage.setItem(RAIL_KEY + "-collapsed", String(sidebarCollapsed));
    } catch {
      // A browser that refuses storage still gets a working rail.
    }
  }

  $effect(() => {
    if (typeof localStorage === "undefined") return;
    try {
      const stored = Number(localStorage.getItem(RAIL_KEY));
      if (Number.isFinite(stored) && stored > 0) sidebarWidth = stored;
      sidebarCollapsed =
        localStorage.getItem(RAIL_KEY + "-collapsed") === "true";
    } catch {
      // ignore: defaults are fine
    }
  });
</script>

<svelte:head>
  <title>kombify-Techstack</title>
  <meta name="description" content="Hybrid Infrastructure Control Plane" />
</svelte:head>

{#if authInitialized && isLoggedIn && !isAuthPage}
  <!-- Authenticated Layout -->

  {#if $showSidebar}
    <!-- Self-Hosted Mode: Full Sidebar Layout -->
    <div data-kx="app" class="flex h-screen overflow-hidden">
      <!-- Mobile menu button -->
      <button
        data-kx="control"
        onclick={toggleSidebar}
        class="min-[48rem]:hidden fixed top-4 left-4 z-50 rounded-lg border border-border bg-card p-2 text-foreground shadow-lg transition-colors hover:bg-accent"
        aria-label="Toggle menu"
      >
        {#if sidebarOpen}
          <X class="w-6 h-6" />
        {:else}
          <Menu class="w-6 h-6" />
        {/if}
      </button>

      <!-- Sidebar overlay for mobile -->
      {#if sidebarOpen}
        <button
          type="button"
          class="min-[48rem]:hidden fixed inset-0 z-30 bg-black/65 backdrop-blur-[1px]"
          onclick={toggleSidebar}
          aria-label="Close menu"
        ></button>
      {/if}

      <!-- Sidebar positioning wrapper: geometry only. The package SidebarNav
           owns the shared navigation material. From 48rem through narrow
           desktop it remains visible in its compact 76px form; only true
           mobile uses the drawer. -->
      <div
        data-onboarding-anchor="techstack-nav"
        class="fixed inset-y-0 left-0 z-40 h-full max-w-[86vw] overflow-hidden shadow-2xl transition-transform duration-300 min-[48rem]:relative min-[48rem]:max-w-none min-[48rem]:shadow-none {sidebarOpen
          ? 'translate-x-0'
          : '-translate-x-full min-[48rem]:translate-x-0'}"
      >
        <SidebarNav
          collapsed={sidebarCollapsed || isCompactViewport}
          onToggleCollapse={isCompactViewport
            ? undefined
            : toggleSidebarCollapse}
          width={isCompactViewport ? 76 : sidebarWidth}
          onResize={isCompactViewport ? undefined : handleRailResize}
          onNavigate={() => {
            if (isMobileViewport) sidebarOpen = false;
          }}
          apiVersion={productIdentity}
          onOpenLocalAdmin={undefined}
        />
      </div>

      <!-- Main content -->
      <main
        class="flex-1 min-w-0 overflow-auto overflow-x-hidden flex flex-col"
      >
        <ReloginModal />
        <IntroductionHost />
        <div class="flex-1 min-w-0">
          {@render children()}
        </div>
        <FooterModern
          class="shrink-0"
          variant="compact"
          apiVersion={productIdentity}
          {instanceId}
          showLegal={authStore.deploymentMode === "saas"}
        />
      </main>
    </div>
  {:else if $isHostNavigationOwned}
    <!-- Cloud-owned embedded mode: the parent owns site navigation, account,
         notifications, Companion and the full-app action. -->
    <div data-kx="app" data-navigation-owner="host" class="min-h-screen">
      <ReloginModal />
      <div class="mx-auto w-full min-w-0 min-h-0 max-w-6xl">
        {@render children()}
      </div>
    </div>
  {:else if $showInlineTabs}
    <!-- SaaS Embedded Mode: Inline Tab Navigation -->
    <div data-kx="app" class="flex flex-col h-screen">
      <!-- Inline Tab Navigation Header -->
      <div data-onboarding-anchor="techstack-nav">
        <InlineTabNav apiVersion={productIdentity} />
      </div>

      <!-- Main content -->
      <main
        class="flex-1 min-w-0 overflow-auto overflow-x-hidden flex flex-col"
      >
        <ReloginModal />
        <IntroductionHost />
        <div class="flex-1 min-w-0">
          {@render children()}
        </div>
      </main>
    </div>
  {:else}
    <!-- Fallback: Simple layout without navigation -->
    <div data-kx="app" class="min-h-screen">
      <ReloginModal />
      {@render children()}
    </div>
  {/if}
{:else}
  <!-- Unauthenticated Layout (Login/Register pages) -->
  <div data-kx="app" class="min-h-screen">
    {@render children()}
  </div>
{/if}

<InAppDialogHost />

<!--
  Standalone owns Companion. Inside Cloud the portal already mounts one;
  a second in-iframe launcher is the duplicate the Workbench embed already
  retired (owner report 2026-08-26). `ready.chrome.companion: host`.
-->
{#if authStore.isAuthenticated && $deploymentMode.mode !== "saas-embedded"}
  <CompanionHost />
{/if}

<!-- Product analytics: no replay, no persistent anonymous identity -->
<TechstackAnalytics />

<!-- Global Toast Notifications -->
<Toast />

<!-- Global Keyboard Shortcuts Handler -->
<KeyboardShortcuts />
