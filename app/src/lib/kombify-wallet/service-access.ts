/**
 * Product-service rights on a Wallet item (Gateway
 * `services/wallet-store/AGENT-ACCESS.md`, "Product services"). A kombify
 * product the owner assigns an item to uses it without the owner present; it
 * never gets the owner's other items. Each right is granted on its own.
 *
 * Techstack asks for two things: the `use` right on the items it consumes
 * (a server access profile, a stack credential) and the right to create items
 * in the owner's Wallet (credentials it provisions).
 */

export const WALLET_RIGHTS = ["use", "read", "manage"] as const;
export type WalletRight = (typeof WALLET_RIGHTS)[number];

/** The kombify product services the Wallet admits. */
export const WALLET_SERVICES = [
  "techstack",
  "paperwork",
  "ai",
  "simulate",
  "cloud",
] as const;
export type WalletService = (typeof WALLET_SERVICES)[number];

/** The service this app acts for. */
export const TECHSTACK_SERVICE: WalletService = "techstack";

export function isWalletService(value: unknown): value is WalletService {
  return (
    typeof value === "string" &&
    (WALLET_SERVICES as readonly string[]).includes(value)
  );
}

function isWalletRight(value: unknown): value is WalletRight {
  return (
    typeof value === "string" &&
    (WALLET_RIGHTS as readonly string[]).includes(value)
  );
}

export interface ServiceRightsGrant {
  serviceId: WalletService;
  rights: WalletRight[];
}

/** The grants of a `GET /items/{id}/services` answer; anything malformed is dropped. */
export function serviceGrantsFrom(body: unknown): ServiceRightsGrant[] | null {
  const grants = (body as { grants?: unknown } | null)?.grants;
  if (!Array.isArray(grants)) return null;
  const out: ServiceRightsGrant[] = [];
  for (const grant of grants) {
    if (!grant || typeof grant !== "object") continue;
    const { serviceId, rights } = grant as Record<string, unknown>;
    if (!isWalletService(serviceId) || !Array.isArray(rights)) continue;
    out.push({ serviceId, rights: rights.filter(isWalletRight) });
  }
  return out;
}

/** Whether Techstack holds the `use` right according to a grants answer. */
export function techstackMayUse(
  grants: readonly ServiceRightsGrant[],
): boolean {
  return grants.some(
    (grant) =>
      grant.serviceId === TECHSTACK_SERVICE && grant.rights.includes("use"),
  );
}

/** The services of a `GET /services/creators` answer; null when malformed. */
export function creatorServicesFrom(body: unknown): WalletService[] | null {
  const services = (body as { services?: unknown } | null)?.services;
  return Array.isArray(services) ? services.filter(isWalletService) : null;
}
