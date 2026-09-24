/**
 * What Techstack tells the Companion SDK.
 *
 * Separated from the component that mounts it because *what* we ask for is a
 * contract with `@kombify/embed`, while *when* we mount is a lifecycle detail.
 * The contract is the part worth pinning: three of its fields are the ones that
 * decide whether this host stays shareable.
 *
 *   - `launcher: "builtin"` — the SDK renders the real Kubi, with its avatar
 *     states and the account's placement preference. A host-owned launcher is a
 *     second mascot to keep in step; the Workbench has one and it is exactly
 *     what this host must not copy.
 *   - `transport: token`, carrying the USER's own Auth0 access token for the
 *     Gateway audience. This is not interchangeable with `host-relay`: the
 *     Gateway edge accepts a service token in place of a user JWT on exactly
 *     four paths (kombify-Gateway `auth/service-auth.ts`), and none of the
 *     Companion's are among them — deliberately, because `/v1/ai/*` entitles
 *     and bills against the real user. A relay signing with a service secret
 *     gets 401 from production; a live probe against the Gateway proved it.
 *   - `theme` resolved to light or dark. `system` is a user preference, not an
 *     appearance, and the panel cannot resolve it from inside an iframe.
 */

import type {
  CompanionAppearance,
  CompanionFinish,
  MountCompanionOptions,
} from "@kombiverselabs/embed";
import type { CompanionLauncherStackIdentity } from "#lib/companion/stack-identity.js";

/** The five launcher strings the SDK never ships — packages carry no prose. */
export interface CompanionMessages {
  frameTitle: string;
  openLauncher: string;
  closeLauncher: string;
  startVoiceAgent: string;
  launcherLabel: string;
}

export interface CompanionHostContext {
  /**
   * Mints the user's Auth0 access token for the Gateway audience. Called per
   * request rather than once, so a refreshed token reaches the panel without
   * replacing the iframe and discarding the conversation.
   */
  getToken: () => Promise<string>;
  /** Resolved appearance, never the `system` preference. */
  appearance: CompanionAppearance;
  finish: CompanionFinish;
  locale: string;
  messages: CompanionMessages;
  stackIdentity?: CompanionLauncherStackIdentity;
}

export function companionMountOptions(
  context: CompanionHostContext,
): MountCompanionOptions {
  return {
    surface: "techstack",
    aiWorkload: "assistant",
    layout: "floating",
    launcher: "builtin",
    launcherDraggable: true,
    // Switchable rather than fixed: Techstack users reach several harnesses
    // from one shell, and the Gateway decides which of them they may see.
    harness: { mode: "switchable" },
    theme: context.appearance,
    design: { finish: context.finish },
    locale: context.locale,
    transport: { mode: "token", getToken: context.getToken },
    messages: context.messages,
    ...(context.stackIdentity
      ? {
          stackIdentity: context.stackIdentity,
        } as Pick<MountCompanionOptions, "stackIdentity">
      : {}),
  };
}

/**
 * Resolve the appearance the panel needs from the document the theme store
 * already wrote it to.
 *
 * Reading the resolved value rather than resolving it again matters: a second
 * resolution can disagree with the first — `system` at a moment the OS is
 * switching — and then the app and the panel disagree about the theme on the
 * same screen.
 */
export function resolveCompanionAppearance(
  documentTheme: string | undefined,
): CompanionAppearance {
  return documentTheme === "light" ? "light" : "dark";
}

const FINISHES: readonly CompanionFinish[] = [
  "kombify",
  "liquid",
  "frost",
  "expressive",
  "aurora",
];

/**
 * Resolve the finish, falling back to the baseline for anything unknown.
 *
 * Closed enum at the boundary (KOMBIFY-DESIGN-SYSTEM-STANDARD §5.2): the value
 * arrives from a DOM attribute anyone can set, so it is validated here rather
 * than forwarded and trusted.
 */
export function resolveCompanionFinish(
  documentFinish: string | undefined,
): CompanionFinish {
  return FINISHES.includes(documentFinish as CompanionFinish)
    ? (documentFinish as CompanionFinish)
    : "kombify";
}
