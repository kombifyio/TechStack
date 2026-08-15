<script lang="ts">
  import "../app.css";
  import { page } from "$app/stores";
  import { derived } from "svelte/store";
  import { onMount } from "svelte";
  import { initI18n } from "$lib/i18n.svelte";
  import { getInfo } from "$lib/api/health";
  import { getInstance } from "$lib/api/instance";
  import { appDeployLabel, productIdentityLabel } from "$lib/config";
  import ReloginModal from "$lib/components/ReloginModal.svelte";
  import InAppDialogHost from "$lib/components/InAppDialogHost.svelte";
  import TechstackAnalytics from "$lib/components/TechstackAnalytics.svelte";
  import { Toast, KeyboardShortcuts, FooterModern } from "$lib/components";
  import { SidebarNav, InlineTabNav } from "$lib/components/navigation";
  import { features, loadFeatures } from "$lib/stores/features";
  import { authStore } from "$lib/stores/auth.svelte";
  import { theme } from "$lib/stores/theme";
  import ThemeToggle from "$lib/components/ui/ThemeToggle.svelte";
  import {
    deploymentMode,
    showSidebar,
    showInlineTabs,
  } from "$lib/stores/deploymentMode";
  import { initBridge, destroyBridge } from "$lib/stores/postMessageBridge";
  import {
    clearStackIdentity,
    hydrateStackIdentityFromBackend,
    initStackIdentity,
  } from "$lib/stores/stackIdentity";
  import { Menu, X } from "@lucide/svelte";

  let { children } = $props();
  let productIdentity = $state("");
  let instanceId = $state("");
  // Start with sidebar closed by default to avoid mobile overlay blocking clicks
  let sidebarOpen = $state(false);
  let sidebarCollapsed = $state(false);
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
      return;
    }

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

    // Close sidebar when clicking outside on mobile
    const handleClickOutside = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (
        window.innerWidth < 1024 &&
        !target.closest(".sidebar") &&
        !target.closest("[aria-label='Toggle menu']")
      ) {
        sidebarOpen = false;
      }
    };
    document.addEventListener("click", handleClickOutside);
    return () => {
      destroyBridge();
      document.removeEventListener("click", handleClickOutside);
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
      await import("$lib/auth/embedded-session");
    await refreshEmbeddedCloudSession();
  }

  // Use derived stores for reactive values in Svelte 5
  const isLoggedIn = $derived(authStore.isAuthenticated);
  const isSaaSDeployment = $derived(authStore.deploymentMode === "saas");
  const isAuthPage = derived(
    page,
    ($p) => $p.url.pathname === "/login" || $p.url.pathname === "/register",
  );

  function toggleSidebar() {
    sidebarOpen = !sidebarOpen;
  }

  function toggleSidebarCollapse() {
    sidebarCollapsed = !sidebarCollapsed;
  }
</script>

<svelte:head>
  <title>kombify-TechStack</title>
  <meta name="description" content="Hybrid Infrastructure Control Plane" />
</svelte:head>

{#if authInitialized && isLoggedIn && !$isAuthPage}
  <!-- Authenticated Layout -->

  {#if $showSidebar}
    <!-- Self-Hosted Mode: Full Sidebar Layout -->
    <div class="flex h-screen overflow-hidden">
      <!-- Mobile menu button -->
      <button
        onclick={toggleSidebar}
        class="lg:hidden fixed top-4 left-4 z-50 rounded-lg border border-border bg-card p-2 text-foreground shadow-lg transition-colors hover:bg-accent"
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
          class="lg:hidden fixed inset-0 z-30 bg-black/65 backdrop-blur-[1px]"
          onclick={toggleSidebar}
          aria-label="Close menu"
        ></button>
      {/if}

      <!-- Sidebar Component -->
      <div
        class="fixed inset-y-0 left-0 z-40 h-full w-[min(18rem,86vw)] max-w-[86vw] overflow-hidden bg-sidebar shadow-2xl transition-transform duration-300 lg:relative lg:w-auto lg:max-w-none lg:shadow-none {sidebarOpen
          ? 'translate-x-0'
          : '-translate-x-full lg:translate-x-0'}"
      >
        <SidebarNav
          collapsed={sidebarCollapsed}
          onToggleCollapse={toggleSidebarCollapse}
          apiVersion={productIdentity}
          onOpenLocalAdmin={undefined}
        />
      </div>

      <!-- Main content -->
      <main
        class="flex-1 min-w-0 overflow-auto overflow-x-hidden bg-(--color-void) flex flex-col"
      >
        <ReloginModal />
        <div class="flex-1 min-w-0">
          {@render children()}
        </div>
        <FooterModern
          class="shrink-0"
          variant="compact"
          apiVersion={productIdentity}
          {instanceId}
        />
      </main>
    </div>
  {:else if $showInlineTabs}
    <!-- SaaS Embedded Mode: Inline Tab Navigation -->
    <div class="flex flex-col h-screen bg-(--color-void)">
      <!-- Inline Tab Navigation Header -->
      <InlineTabNav apiVersion={productIdentity} />

      <!-- Main content -->
      <main
        class="flex-1 min-w-0 overflow-auto overflow-x-hidden bg-(--color-void) flex flex-col"
      >
        <ReloginModal />
        <div class="flex-1 min-w-0">
          {@render children()}
        </div>
      </main>
    </div>
  {:else}
    <!-- Fallback: Simple layout without navigation -->
    <div class="min-h-screen bg-(--color-void)">
      <ReloginModal />
      {@render children()}
    </div>
  {/if}
{:else}
  <!-- Unauthenticated Layout (Login/Register pages) -->
  <div class="min-h-screen bg-(--color-void)">
    {@render children()}
  </div>
{/if}

<InAppDialogHost />

{#if $showSidebar}
  <div class="fixed top-4 right-4 z-50">
    <div
      class="rounded-lg border border-border bg-card/80 backdrop-blur-sm shadow-lg"
    >
      <ThemeToggle />
    </div>
  </div>
{/if}

<!-- Product analytics: no replay, no persistent anonymous identity -->
<TechstackAnalytics />

<!-- Global Toast Notifications -->
<Toast />

<!-- Global Keyboard Shortcuts Handler -->
<KeyboardShortcuts />
