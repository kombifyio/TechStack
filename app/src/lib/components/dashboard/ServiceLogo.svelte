<script lang="ts">
  import BrandLogoScope from "#lib/components/BrandLogoScope.svelte";
  import BrandLogoIcon from "#lib/components/BrandLogoIcon.svelte";
  import { canonicalServiceLogo } from "#lib/application-logo.js";
  import type { CanonicalService } from "#lib/api/services.js";

  let {
    service,
    class: className = "h-7 w-7",
  }: { service: CanonicalService; class?: string } = $props();

  // The logo sits on a light plate in every theme, so it uses the light
  // variant of the recognition's logo. Viewer override first, then the
  // recognition's logo, then the mirrored icon of the vendor domain.
  const logo = $derived(canonicalServiceLogo(service, "light"));
</script>

<span
  class="inline-flex shrink-0 items-center justify-center text-muted-foreground {className}"
  aria-hidden="true"
>
  <BrandLogoScope domain={logo.domain} logoUrl={logo.src}>
    <!-- No monogram: an unknown app gets the neutral app glyph. -->
    <BrandLogoIcon class="h-full w-full object-contain" />
  </BrandLogoScope>
</span>
