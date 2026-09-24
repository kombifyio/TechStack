import type { ManagedProviderID } from "./types.js";

/** Provider presentation only. Availability comes from effective account features. */
export const managedProviders = [
  {
    value: "ionos",
    label: "IONOS",
    domain: "ionos.com",
    website: "https://www.ionos.de/server/vps",
    featureKey: "monthly_runtime_ionos",
  },
  {
    value: "centron",
    label: "centron",
    domain: "centron.de",
    website: "https://www.centron.de/",
    featureKey: "monthly_runtime_centron",
  },
] as const satisfies ReadonlyArray<{
  value: ManagedProviderID;
  label: string;
  domain: string;
  website: string;
  featureKey: string;
}>;
