/**
 * Localized text for Wallet refusals and item facts, resolved at render time.
 * A refusal maps by its reason code; a reason without a localized message
 * shows the Gateway's own guidance, and nothing at all falls back to a generic
 * message. Message IDs live in `wallet-messages.ts`.
 */
import { tr } from "#lib/i18n.svelte.js";
import { WalletApiError } from "./wallet-api.js";
import { WalletTransportClientError } from "./transport.js";
import { parseScope } from "./item-types.js";

const REASONS: Record<string, string> = {
  auth_required: "kwallet.denied.auth_required",
  owner_unknown: "kwallet.denied.owner_unknown",
  wallet_owner_required: "kwallet.denied.owner_required",
  item_unknown: "kwallet.denied.item_unknown",
  item_denied: "kwallet.denied.item_denied",
  lease_replayed: "kwallet.denied.replayed",
  capture_invalid: "kwallet.denied.capture_invalid",
  item_conflict: "kwallet.denied.conflict",
  name_conflict: "kwallet.denied.name_conflict",
  version_conflict: "kwallet.denied.conflict",
  generation_conflict: "kwallet.denied.conflict",
  generation_pin_conflict: "kwallet.denied.conflict",
  body_too_large: "kwallet.denied.too_large",
  custody_partition_shredded: "kwallet.denied.shredded",
  network_unavailable: "kwallet.denied.unavailable",
  value_use_only: "kwallet.denied.value_use_only",
  wallet_item_not_owned: "kwallet.denied.item_not_owned",
  wallet_item_not_active: "kwallet.denied.item_not_active",
  sign_policy_invalid: "kwallet.denied.sign_policy_invalid",
  sign_policy_missing: "kwallet.denied.sign_policy_missing",
  sign_outside_policy: "kwallet.denied.sign_outside_policy",
  subject_key_invalid: "kwallet.denied.subject_key_invalid",
  ssh_ca_invalid: "kwallet.denied.ssh_ca_invalid",
  item_action_unsupported: "kwallet.denied.item_action_unsupported",
  sign_response_invalid: "kwallet.denied.sign_mismatch",
  wallet_step_up_required: "kwallet.denied.step_up_required",
};

const TRANSPORT: Record<string, string> = {
  capture_key_invalid: "kwallet.denied.capture_key_untrusted",
  capture_key_untrusted: "kwallet.denied.capture_key_untrusted",
  owner_subject_invalid: "kwallet.denied.owner_unknown",
  value_too_large: "kwallet.error.value_too_large",
  reveal_context_mismatch: "kwallet.denied.reveal_mismatch",
  reveal_response_invalid: "kwallet.denied.reveal_mismatch",
};

export function walletErrorMessage(error: unknown): string {
  if (error instanceof WalletTransportClientError)
    return tr(TRANSPORT[error.code] ?? "kwallet.denied.transport");
  if (error instanceof WalletApiError) {
    const known = REASONS[error.reason];
    if (known) return tr(known);
    if (error.retryable) return tr("kwallet.denied.unavailable");
    return error.guidance ?? tr("kwallet.denied.generic");
  }
  return tr("kwallet.denied.transport");
}

const TYPES: Record<string, string> = {
  generic_secret: "kwallet.type.generic_secret",
  login: "kwallet.type.login",
  variable: "kwallet.type.variable",
  configuration: "kwallet.type.configuration",
  node_access_profile: "kwallet.type.node_access_profile",
  parent_credential: "kwallet.type.parent_credential",
  ssh_ca: "kwallet.type.ssh_ca",
  ai_credential: "kwallet.type.ai_credential",
};

export function itemTypeLabel(itemType: string): string {
  return tr(TYPES[itemType] ?? "kwallet.type.other");
}

const STATES: Record<string, string> = {
  active: "kwallet.state.active",
  revoked: "kwallet.state.revoked",
  deleted: "kwallet.state.deleted",
};

export function itemStateLabel(state: string): string {
  return STATES[state] ? tr(STATES[state]) : state;
}

/**
 * A scope for people: a stack's own name when the project id is one of the
 * stacks Techstack knows, otherwise the Wallet's generic wording.
 */
export function scopeLabel(
  scope: string,
  stackNames?: ReadonlyMap<string, string>,
): string {
  const parsed = parseScope(scope);
  if (!parsed) return scope;
  if (parsed.kind === "account") return tr("kwallet.scope.account");
  const stack = stackNames?.get(parsed.project);
  if (parsed.kind === "project")
    return stack
      ? tr("kwallet.scope.stack", { stack })
      : tr("kwallet.scope.project", { project: parsed.project });
  return tr("kwallet.scope.environment", {
    project: stack ?? parsed.project,
    environment: parsed.environment,
  });
}

const FIELDS: Record<string, string> = {
  secret: "kwallet.field.secret",
  username: "kwallet.field.username",
  password: "kwallet.field.password",
  totp: "kwallet.field.totp",
  value: "kwallet.field.value",
  document: "kwallet.field.document",
  host: "kwallet.field.host",
  port: "kwallet.field.port",
  user: "kwallet.field.user",
  privateKey: "kwallet.field.private_key",
  publicKey: "kwallet.field.public_key",
};

export function fieldLabel(field: string): string {
  return FIELDS[field] ? tr(FIELDS[field]) : field;
}
