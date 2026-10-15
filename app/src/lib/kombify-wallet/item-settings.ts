/**
 * Item value policy (Gateway `services/wallet-store/AGENT-ACCESS.md`, "Item
 * policy and use targets"): who may receive an item's value. The Wallet Store
 * stays the authority; the editor only offers what the contract allows.
 */

/** `read_right_holders`: holders of the read right may receive the value.
 * `use_only`: nobody receives it, the owner included; it can only be used. */
export const VALUE_ACCESS = ["read_right_holders", "use_only"] as const;
export type ValueAccess = (typeof VALUE_ACCESS)[number];
export const DEFAULT_VALUE_ACCESS: ValueAccess = "read_right_holders";

export interface ItemPolicy {
  valueAccess: ValueAccess;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

/** Item settings as the owner list reports them; a missing member is the default. */
export function itemValueAccess(item: { policy?: unknown }): ValueAccess {
  const policy = item.policy;
  return isRecord(policy) && policy.valueAccess === "use_only"
    ? "use_only"
    : DEFAULT_VALUE_ACCESS;
}

/** How often the value was released to someone other than the owner since its last version. */
export function disclosuresSinceRotation(item: {
  disclosure?: unknown;
}): number {
  const disclosure = item.disclosure;
  if (!isRecord(disclosure)) return 0;
  const count = disclosure.sinceRotation;
  return typeof count === "number" && Number.isSafeInteger(count) && count > 0
    ? count
    : 0;
}
