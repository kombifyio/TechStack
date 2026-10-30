<script lang="ts">
  import { setContext, type Snippet } from "svelte";
  import {
    BRAND_LOGO_CONTEXT,
    type BrandLogoContext,
  } from "#lib/brand-logo.js";

  let {
    domain = "",
    logoUrl = undefined,
    children,
  }: {
    domain?: string;
    /** A logo the caller already resolved; it wins over the domain's icon. */
    logoUrl?: string;
    children: Snippet;
  } = $props();

  setContext<BrandLogoContext>(BRAND_LOGO_CONTEXT, {
    get domain() {
      return domain;
    },
    get logoUrl() {
      return logoUrl;
    },
  });
</script>

{@render children()}
