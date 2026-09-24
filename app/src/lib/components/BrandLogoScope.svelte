<script lang="ts">
  import { setContext, type Snippet } from "svelte";
  import {
    BRAND_LOGO_CONTEXT,
    type BrandLogoContext,
  } from "#lib/brand-logo.js";

  let {
    domain = "",
    children,
  }: {
    domain?: string;
    children: Snippet;
  } = $props();

  setContext<BrandLogoContext>(BRAND_LOGO_CONTEXT, {
    get domain() {
      return domain;
    },
  });
</script>

{@render children()}
