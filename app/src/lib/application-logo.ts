import type { CanonicalService } from "#lib/api/services.js";
import { appLogoKey } from "#lib/app-logo-overrides.svelte.js";
import type { LogoTheme } from "#lib/brand-logo.js";
import { serviceCardLogo } from "#lib/service-card-adapter.js";

/** The key of the application a service belongs to; viewer overrides use it. */
export function canonicalServiceLogoKey(service: CanonicalService): string {
  return appLogoKey(service.application_key, service.service_key);
}

/**
 * The logo of a running workload: observed services arrive with runtime names
 * (`hermes-gateway.service`, `stack-immich-server-1`) and the runtime identity
 * adds the compose service, project and image when reported, so all of them
 * feed the domain lookup. Reactive on the viewer's override and the theme.
 */
export function canonicalServiceLogo(
  service: CanonicalService,
  theme?: LogoTheme,
): { domain: string; src: string | undefined } {
  return serviceCardLogo(
    service.catalog,
    service.application_key || service.service_key,
    [
      service.application_key,
      service.service_key,
      service.application_display_name,
      service.name,
      service.runtime_identity?.image,
      service.runtime_identity?.service,
      service.runtime_identity?.project,
    ],
    theme,
  );
}
