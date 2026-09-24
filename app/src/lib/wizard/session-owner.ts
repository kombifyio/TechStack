import type { OwnerConfig } from "./types";

export type SignedInOwnerAccount = {
  email: string;
  displayName?: string;
  emailVerified?: boolean;
};

export function ownerUsernameFromEmail(email: string): string {
  const local = email.trim().toLowerCase().split("@")[0] ?? "";
  const cleaned = local
    .replace(/[^a-z0-9_-]+/g, "-")
    .replace(/^[-_]+|[-_]+$/g, "");
  return cleaned || "owner";
}

export function seedLocalOwnerFromSignedInAccount(
  owner: OwnerConfig,
  account: SignedInOwnerAccount,
): OwnerConfig {
  if (owner.bootstrapMode !== "custom" || owner.source !== "local") {
    return owner;
  }
  const sessionEmail = account.email.trim();
  const fillingFromSession = !owner.email.trim() && sessionEmail !== "";
  if (fillingFromSession) {
    owner.email = sessionEmail;
  }
  if (!owner.username.trim() && owner.email.trim()) {
    owner.username = ownerUsernameFromEmail(owner.email);
  }
  if (fillingFromSession && !owner.displayName.trim()) {
    const name = account.displayName?.trim();
    if (name) owner.displayName = name;
  }
  return owner;
}
