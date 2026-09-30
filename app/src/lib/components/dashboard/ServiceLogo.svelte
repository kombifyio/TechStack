<script lang="ts">
  import BrandLogoScope from "#lib/components/BrandLogoScope.svelte";
  import BrandLogoIcon from "#lib/components/BrandLogoIcon.svelte";
  import { brandDomainForTool } from "#lib/brand-logo.js";
  import type { CanonicalService } from "#lib/api/services.js";

  let {
    service,
    class: className = "h-7 w-7",
  }: { service: CanonicalService; class?: string } = $props();

  // Observed services arrive with runtime names (`hermes-gateway.service`,
  // `stack-immich-server-1`); brandDomainForTool normalizes them, and the
  // runtime identity adds the compose service/project and image when reported.
  const domain = $derived(
    brandDomainForTool(
      service.application_key,
      service.service_key,
      service.application_display_name,
      service.name,
      service.runtime_identity?.image,
      service.runtime_identity?.service,
      service.runtime_identity?.project,
    ),
  );
</script>

<span
  class="inline-flex shrink-0 items-center justify-center text-muted-foreground {className}"
  aria-hidden="true"
>
  <BrandLogoScope {domain}>
    <!-- No monogram: an unknown app gets the neutral app glyph. -->
    <BrandLogoIcon class="h-full w-full object-contain" />
  </BrandLogoScope>
</span>
