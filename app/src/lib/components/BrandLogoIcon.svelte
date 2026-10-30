<script module lang="ts">
  // Keep failed logo URLs in memory for this tab so remounting cards does not
  // spend another request on a logo that already failed.
  const failedLogoUrls = new Set<string>();
</script>

<script lang="ts">
  import { getContext } from "svelte";
  import { Box } from "@lucide/svelte";
  import {
    BRAND_LOGO_CONTEXT,
    localBrandLogoUrl,
    type BrandLogoContext,
  } from "#lib/brand-logo.js";

  let {
    class: className = "",
    fallbackLabel = "",
    circular = false,
    flush = false,
    fallbackClass = "",
  }: {
    class?: string;
    fallbackLabel?: string;
    circular?: boolean;
    /** The logo fills its box edge to edge, without the inner padding. */
    flush?: boolean;
    /** Classes for the neutral glyph instead of `class`, when no logo loads. */
    fallbackClass?: string;
  } = $props();

  const brandLogo = getContext<BrandLogoContext>(BRAND_LOGO_CONTEXT);
  // The logo the caller resolved (recognition or viewer override), else the
  // mirrored icon of the vendor domain; none draws the monogram or glyph.
  const src = $derived(
    brandLogo?.logoUrl || localBrandLogoUrl(brandLogo?.domain),
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
    class="{className} brand-logo object-contain"
    class:circular
    class:flush
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
  <Box class={fallbackClass || className} aria-hidden="true" />
{/if}

<style>
  .brand-logo {
    padding: 2px;
    border-radius: 22%;
    background: #f6f7f9;
  }
  .brand-monogram {
    display: grid;
    place-items: center;
    font-size: 0.7em;
    font-weight: 700;
    letter-spacing: -0.04em;
    color: var(--primary);
  }
  .brand-logo.flush {
    padding: 0;
  }
  .brand-logo.circular {
    padding: 3px;
    border-radius: 50%;
    object-fit: contain;
  }
</style>
