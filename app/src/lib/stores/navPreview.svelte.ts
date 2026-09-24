/**
 * What the navigation flyouts show: the entries a section would list, loaded
 * ONCE after sign-in and kept as resources.
 *
 * The flyout itself never fetches (Workbench/CMO discipline): hovering a nav
 * item must cost nothing and must not hammer the API. Each section is a
 * `resource` — painted from the session cache on the next visit, revalidated
 * behind it, and cleared with the rest of the session cache on logout.
 */
import { resource } from "#lib/data/resource.svelte.js";
import {
  listCanonicalServices,
  type CanonicalService,
} from "#lib/api/services.js";
import { getActiveAlerts, type AlertState } from "#lib/api/monitoring.js";

const TIMEOUT_MS = 8_000;

class NavPreviewStore {
  readonly services = resource<CanonicalService[]>(
    () => listCanonicalServices(),
    {
      key: "nav:services",
      timeoutMs: TIMEOUT_MS,
    },
  );
  readonly alerts = resource<AlertState[]>(getActiveAlerts, {
    key: "nav:alerts",
    timeoutMs: TIMEOUT_MS,
  });

  private loadedFor: string | null = null;

  /** Load both previews once per signed-in identity; later calls are no-ops. */
  load(identity: string) {
    if (this.loadedFor === identity) return;
    this.loadedFor = identity;
    void this.services.refresh();
    void this.alerts.refresh();
  }

  /** Forget the identity so the next sign-in loads again. Cached values are
   * dropped by clearSectionCache() alongside every other section. */
  clear() {
    this.loadedFor = null;
  }

  get activeAlertCount(): number {
    return (this.alerts.data ?? []).filter((alert) => alert.active).length;
  }
}

export const navPreview = new NavPreviewStore();
