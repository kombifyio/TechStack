/**
 * Refusals of Techstack's own API that concern the kombify Wallet. The backend
 * answers them with a structured denial (`reason_code`, `retryable`,
 * `user_guidance`, plus `required_right`, `item_id` or `wallet_reason` where
 * they apply). This module recognises them wherever they come from, turns each
 * into a localized message, and raises the one prompt a person can act on: let
 * Techstack save credentials in the Wallet, or let it use one item.
 * `WalletPromptHost` (mounted once in the layout) shows the prompt.
 */
import { tr } from "#lib/i18n.svelte.js";
import { WALLET_ITEM_ID } from "./item-types.js";

export const WALLET_CREATE_RIGHT_MISSING = "wallet_create_right_missing";
export const WALLET_RIGHT_MISSING = "wallet_right_missing";

/** Reason codes with a localized message, by message id. */
const MESSAGES: Record<string, string> = {
  wallet_item_required: "kwallet.api.wallet_item_required",
  wallet_item_invalid: "kwallet.api.wallet_item_invalid",
  wallet_item_unsupported: "kwallet.api.wallet_item_unsupported",
  wallet_create_right_missing: "kwallet.api.wallet_create_right_missing",
  wallet_right_missing: "kwallet.api.wallet_right_missing",
  wallet_item_not_found: "kwallet.api.wallet_item_not_found",
  wallet_item_unusable: "kwallet.api.wallet_item_unusable",
  wallet_item_not_node_access: "kwallet.api.wallet_item_not_node_access",
  wallet_item_no_public_key: "kwallet.api.wallet_item_no_public_key",
  wallet_item_host_mismatch: "kwallet.api.wallet_item_host_mismatch",
  wallet_item_credential_mismatch:
    "kwallet.api.wallet_item_credential_mismatch",
  wallet_unavailable: "kwallet.api.wallet_unavailable",
  wallet_response_invalid: "kwallet.api.wallet_response_invalid",
  wallet_owner_ineligible: "kwallet.api.wallet_owner_ineligible",
  wallet_self_hosted_only: "kwallet.api.wallet_self_hosted_only",
};

export interface Refusal {
  status?: number;
  code?: string;
  details?: unknown;
}

export interface WalletDenial {
  reason: string;
  requiredRight?: string;
  itemId?: string;
  /** The backend's own guidance text, shown when no localized message exists. */
  guidance?: string;
}

function text(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() !== "" ? value : undefined;
}

/** The Wallet denial an API refusal carries, or null when it is another refusal. */
export function walletDenialOf(refusal: Refusal): WalletDenial | null {
  const details =
    refusal.details && typeof refusal.details === "object"
      ? (refusal.details as Record<string, unknown>)
      : {};
  const reason = [
    refusal.code,
    details.reason_code,
    details.error_code,
    details.code,
  ].find((candidate): candidate is string =>
    Boolean(
      typeof candidate === "string" && Object.hasOwn(MESSAGES, candidate),
    ),
  );
  if (!reason) return null;
  const guidance = details.user_guidance;
  return {
    reason,
    requiredRight: text(details.required_right),
    itemId: text(details.item_id),
    guidance:
      text(guidance) ??
      (guidance && typeof guidance === "object"
        ? text((guidance as { body?: unknown }).body)
        : undefined),
  };
}

/** The localized wording of a Wallet denial; the right in question refines `wallet_right_missing`. */
export function walletDenialMessage(denial: WalletDenial): string {
  if (denial.reason === WALLET_RIGHT_MISSING && denial.requiredRight === "use")
    return tr("kwallet.api.wallet_right_missing_use");
  return tr(MESSAGES[denial.reason]);
}

export type WalletPromptRequest =
  { kind: "create" } | { kind: "use"; itemId: string };

class WalletPrompt {
  /** What the owner is asked, or null; cleared when the owner answers. */
  request = $state<WalletPromptRequest | null>(null);

  dismiss(): void {
    this.request = null;
  }
}

export const walletPrompt = new WalletPrompt();

/** Ask the owner for the right a refusal names, when one grant resolves it. */
export function promptForWalletDenial(denial: WalletDenial): void {
  if (
    denial.reason === WALLET_CREATE_RIGHT_MISSING ||
    (denial.reason === WALLET_RIGHT_MISSING &&
      denial.requiredRight === "create")
  ) {
    walletPrompt.request = { kind: "create" };
  } else if (
    denial.reason === WALLET_RIGHT_MISSING &&
    denial.requiredRight === "use" &&
    denial.itemId &&
    WALLET_ITEM_ID.test(denial.itemId)
  ) {
    walletPrompt.request = { kind: "use", itemId: denial.itemId };
  }
}
