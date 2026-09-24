<script module lang="ts">
  // Keep failed Logo Link URLs in memory for this tab so remounting cards does
  // not spend another request on a logo that already failed.
  const failedLogoUrls = new Set<string>();
</script>

<script lang="ts">
  import { getContext } from "svelte";
  import { Box } from "@lucide/svelte";
  import { clientBootstrap } from "#lib/client/bootstrap.js";
  import { theme } from "#lib/stores/theme.js";
  import {
    BRAND_LOGO_CONTEXT,
    logoLinkUrl,
    type BrandLogoContext,
  } from "#lib/brand-logo.js";

  let {
    class: className = "",
    fallbackLabel = "",
  }: { class?: string; fallbackLabel?: string } = $props();

  const brandLogo = getContext<BrandLogoContext>(BRAND_LOGO_CONTEXT);
  let logoTheme = $state<"light" | "dark">("dark");

  $effect(() => {
    // The store can contain `system`; the root attribute is the resolved theme
    // and also follows host-pushed theme changes in embedded Techstack.
    void $theme;
    if (typeof document === "undefined") return;

    const syncTheme = () => {
      logoTheme =
        document.documentElement.dataset.theme === "light" ? "light" : "dark";
    };
    syncTheme();

    const observer = new MutationObserver(syncTheme);
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });
    return () => observer.disconnect();
  });

  const src = $derived(
    logoLinkUrl(brandLogo?.domain, $clientBootstrap.contextDevLogolinkId, {
      theme: logoTheme,
      type: "icon",
    }),
  );
  let failed = $state(false);
  const imageFailed = $derived(failed || failedLogoUrls.has(src));

  $effect(() => {
    void src;
    failed = false;
  });
</script>

{#if src && !imageFailed}
  <img
    {src}
    alt=""
    class="{className} object-contain"
    aria-hidden="true"
    loading="lazy"
    decoding="async"
    onerror={() => {
      failedLogoUrls.add(src);
      failed = true;
    }}
  />
{:else if fallbackLabel}
  <span class="{className} brand-monogram" aria-hidden="true"
    >{fallbackLabel.slice(0, 2).toUpperCase()}</span
  >
{:else}
  <Box class={className} aria-hidden="true" />
{/if}

<style>
  .brand-monogram {
    display: grid;
    place-items: center;
    font-size: 0.7em;
    font-weight: 700;
    letter-spacing: -0.04em;
    color: var(--primary);
  }
</style>
