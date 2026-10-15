/**
 * The SSH key label a remote-server connection carries, or none. Hosted
 * Techstack keeps the SSH credential in the person's kombify Wallet and names
 * it by Wallet item id; its backend refuses a key label there, so no label is
 * sent in kombify mode. A self-hosted install keeps sending the label of the
 * key in its built-in Wallet.
 */
import { authStore } from "#lib/stores/auth.svelte.js";

export function remoteSshKeyLabel(remote: {
  sshKeyLabel: string;
}): string | undefined {
  if (authStore.walletSource === "kombify") return undefined;
  return remote.sshKeyLabel.trim() || undefined;
}
