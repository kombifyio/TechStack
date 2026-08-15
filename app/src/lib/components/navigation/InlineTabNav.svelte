<script lang="ts">
  /**
   * InlineTabNav - Horizontal tab navigation for SaaS embedded mode
   * Shows when kombify-TechStack is embedded within the kombify Cloud Portal
   * Uses the shared navigation config from lib/navigation.ts
   */
  import { page } from "$app/stores";
  import { fly } from "svelte/transition";
  import { tr } from "$lib/i18n.svelte";
  import {
    embeddedNavItems,
    isNavItemActive,
    type NavItem,
  } from "$lib/navigation";
  import { ExternalLink } from "@lucide/svelte";
  import { stackIdentity } from "$lib/stores/stackIdentity";
  import { StackIdentityBadge } from "$lib/components/open-core";
  import { NotificationBell } from "$lib/components/open-core";

  interface Props {
    onOpenFullApp?: () => void;
    apiVersion?: string;
    class?: string;
  }

  let {
    onOpenFullApp,
    apiVersion = "",
    class: className = "",
  }: Props = $props();

  const currentPath = $derived($page.url.pathname);

  function getLabel(item: NavItem): string {
    const translated = tr(item.labelKey);
    return translated !== item.labelKey ? translated : item.labelFallback;
  }

  function handleOpenFullApp() {
    if (onOpenFullApp) {
      onOpenFullApp();
    } else {
      // Default behavior: open current path in new window without embedded param
      const url = new URL(window.location.href);
      url.searchParams.delete("embedded");
      window.open(url.toString(), "_blank");
    }
  }
</script>

<div
  class="inline-tab-nav flex items-center justify-between gap-3 border-b border-border bg-background px-4 {className}"
>
  <!-- Tab Navigation -->
  <div class="flex min-w-0 items-center gap-3">
    <a href="/" class="hidden shrink-0 items-center gap-2 sm:flex">
      <img
        src="/kombify-techstack-kombi3d.png"
        alt="kombify TechStack"
        class="h-8 w-auto max-w-[10rem] object-contain"
      />
    </a>

    <nav class="flex h-12 min-w-0 items-center gap-1 overflow-x-auto">
      {#each embeddedNavItems as item}
        {@const Icon = item.icon}
        {@const isActive = isNavItemActive(item, currentPath)}

        <a
          href={item.href}
          class="relative flex items-center gap-2 px-4 h-full text-sm font-medium transition-colors {isActive
            ? 'text-primary'
            : 'text-muted-foreground hover:text-foreground'}"
          in:fly={{ y: -5, duration: 150 }}
        >
          <Icon class="w-4 h-4" />
          <span>{getLabel(item)}</span>

          <!-- Active indicator bar -->
          {#if isActive}
            <div
              class="absolute bottom-0 left-2 right-2 h-0.5 bg-primary rounded-t-full"
              in:fly={{ y: 5, duration: 200 }}
            ></div>
          {/if}
        </a>
      {/each}
    </nav>
  </div>

  <!-- Right Actions -->
  <div class="flex shrink-0 items-center gap-3">
    {#if apiVersion}
      <span
        data-testid="product-version-identity"
        class="hidden sm:inline-flex items-center rounded-md border border-border bg-muted/40 px-2 py-1 font-mono text-xs text-muted-foreground"
      >
        {apiVersion}
      </span>
    {/if}

    <div class="hidden min-w-0 max-w-xs md:block">
      {#if $stackIdentity}
        <StackIdentityBadge identity={$stackIdentity} />
      {/if}
    </div>

    <NotificationBell
      apiBase="/api/v1/notifications"
      settingsHref="/settings"
    />

    <!-- Open Full App Button -->
    <button
      onclick={handleOpenFullApp}
      class="flex items-center gap-2 px-3 py-1.5 text-sm font-medium text-muted-foreground hover:text-primary border border-border rounded-lg hover:border-primary/50 transition-all hover:shadow-sm hover:shadow-primary/10"
    >
      <span>Full App</span>
      <ExternalLink class="w-4 h-4" />
    </button>
  </div>
</div>
