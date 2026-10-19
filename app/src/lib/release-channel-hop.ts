/**
 * Release channel routing (workspace CI-CD-PLATFORM-STANDARD §4.6.2).
 *
 * A person chooses Stable or Preview in kombify Cloud. Stable Techstack sends
 * someone who chose Preview to Techstack's Preview, so they always use the
 * newest version. Preview serves everyone and never sends anyone back, so the
 * hop cannot loop. An embedded frame stays put: Cloud already opened the
 * channel's Techstack in it.
 */
export function previewHopTarget(input: {
  chosen: string | undefined;
  running: string;
  previewOrigin: string;
  href: string;
  embedded: boolean;
}): string | null {
  if (
    input.embedded ||
    input.chosen !== "preview" ||
    input.running === "preview" ||
    !input.previewOrigin
  ) {
    return null;
  }
  try {
    const current = new URL(input.href);
    // A sign-in return still carries its one-time code; the next navigation hops.
    if (current.searchParams.has("code") || current.searchParams.has("state")) {
      return null;
    }
    // "//host/x" would resolve to another host; never build a target from it.
    if (current.pathname.startsWith("//")) {
      return null;
    }
    const target = new URL(
      current.pathname + current.search + current.hash,
      input.previewOrigin,
    );
    if (target.protocol !== "https:" || target.origin === current.origin) {
      return null;
    }
    return target.toString();
  } catch {
    return null;
  }
}
