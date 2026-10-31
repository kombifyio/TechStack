/**
 * What stands in the way on a step. Each issue says what to do, as a message
 * key; a frame translates it. Answers arrive as plain facts, so this holds for
 * every frame and needs no app state.
 */
import type { CreationStepKey } from "./steps";

export type CreationIssueField =
  | "catalog"
  | "server-selection"
  | "server-host"
  | "server-wallet"
  | "hypervisor-guest"
  | "access-mode"
  | "audience"
  | "public-access"
  | "owner-email"
  | "owner-recovery"
  | "step";

export interface CreationIssue {
  field: CreationIssueField;
  /** A blocker stops the step; a hint is only shown. */
  blocking: boolean;
  /** Message key, or `text` where the message is already written. */
  messageKey?: string;
  text?: string;
}

export interface CreationFacts {
  readonly requireGoal: boolean;
  readonly hasSelectedGoal: boolean;
  readonly catalogState: "loading" | "ready" | "error";
  readonly serverSelectionReady: boolean;
  /** One of the person's existing servers is chosen; nothing new is set up. */
  readonly existingServer?: boolean;
  readonly serverMode: string;
  readonly hypervisorGuestValid: boolean;
  readonly remoteHost: string;
  /** Hosted Techstack reaches a server with a Wallet item instead of a typed credential. */
  readonly usesWallet: boolean;
  readonly walletItemChosen: boolean;
  readonly accessMode: string | undefined;
  readonly publicAccess: boolean;
  readonly inheritsAccess: boolean;
  readonly hasUsers: boolean;
  readonly ownerValid: boolean;
  readonly ownerIssues: readonly { field: CreationIssueField; text: string }[];
}

export function creationStepIssues(
  key: CreationStepKey,
  facts: CreationFacts,
): CreationIssue[] {
  const issues: CreationIssue[] = [];
  const blocker = (field: CreationIssueField, messageKey: string) =>
    issues.push({ field, blocking: true, messageKey });
  const hint = (field: CreationIssueField, messageKey: string) =>
    issues.push({ field, blocking: false, messageKey });

  switch (key) {
    case "goals":
      if (facts.requireGoal && !facts.hasSelectedGoal)
        blocker("step", "wizard.validation.goalsRequired");
      // A failed or unfinished catalog read must not read as "nothing to install".
      if (facts.catalogState === "loading")
        blocker("catalog", "wizard.catalog.blockedLoading");
      else if (facts.catalogState === "error")
        blocker("catalog", "wizard.catalog.blockedError");
      break;
    case "server":
      if (facts.existingServer) break;
      if (!facts.serverSelectionReady)
        blocker("server-selection", "wizard.validation.serverSelection");
      if (facts.serverMode === "hypervisor" && !facts.hypervisorGuestValid)
        blocker("hypervisor-guest", "wizard.validation.hypervisorGuest");
      if (facts.serverMode === "connect-remote") {
        if (!facts.remoteHost.trim())
          blocker("server-host", "ui.easyWizard.serverHostOrIpIs");
        // A run without the item would only be refused (`wallet_item_required`).
        if (facts.usesWallet && !facts.walletItemChosen)
          blocker("server-wallet", "kwallet.wizard.item_required");
      }
      break;
    case "access":
      if (!facts.accessMode)
        hint("access-mode", "ui.easyWizard.pleaseSelectAnAccessMode");
      if (
        !facts.existingServer &&
        facts.serverMode === "kombify-cloud" &&
        facts.accessMode === "home"
      )
        blocker("access-mode", "wizard.access.home.managedReason");
      break;
    case "users":
      if (!facts.inheritsAccess && facts.publicAccess)
        blocker("public-access", "wizard.validation.publicUsers");
      if (!facts.hasUsers)
        hint("audience", "ui.easyWizard.pleaseSelectWhoWillUse");
      break;
    case "login":
      for (const issue of facts.ownerIssues)
        issues.push({
          field: issue.field,
          blocking: !facts.ownerValid,
          text: issue.text,
        });
      break;
  }

  return [
    ...issues.filter((issue) => issue.blocking),
    ...issues.filter((issue) => !issue.blocking),
  ];
}

/** Whether the person can continue past a step. */
export function isCreationStepComplete(
  key: CreationStepKey,
  facts: CreationFacts,
): boolean {
  if (key === "login") return facts.ownerValid;
  return !creationStepIssues(key, facts).some((issue) => issue.blocking);
}
