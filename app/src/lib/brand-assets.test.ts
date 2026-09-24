import { describe, expect, it } from "vitest";
import {
  KOMBIFY_MAIN_WORDMARK,
  TECHSTACK_ROSETTE,
  TECHSTACK_ROSETTE_DARK,
  TECHSTACK_TOOL_LOCKUP_DARK_SRCSET,
  TECHSTACK_TOOL_LOCKUP_SRCSET,
} from "./brand-assets.js";

describe("Techstack brand asset roles", () => {
  it("uses the Techstack lockup web tier with the SVG master for larger renders", () => {
    expect(TECHSTACK_TOOL_LOCKUP_SRCSET).toMatch(
      /\/techstack-tool-lockup-96\.webp\S* 513w, \/brand\/techstack-tool-lockup\.svg\S* 1710w$/,
    );
    expect(TECHSTACK_TOOL_LOCKUP_DARK_SRCSET).toMatch(
      /\/techstack-tool-lockup-96-dark\.webp\S* 513w, \/brand\/techstack-tool-lockup-dark\.svg\S* 1710w$/,
    );
  });

  it("uses only the Techstack rosette in collapsed product navigation", () => {
    expect(TECHSTACK_ROSETTE).toContain("/techstack-rosette-128.webp");
    expect(TECHSTACK_ROSETTE_DARK).toContain("/techstack-rosette-128-dark.webp");
  });

  it("keeps the umbrella footer on the underlined wordmark without a rosette", () => {
    expect(KOMBIFY_MAIN_WORDMARK).toMatch(/^\/brand\/kombify-brand-wordmark\.png/);
    expect(KOMBIFY_MAIN_WORDMARK).not.toContain("rosette");
  });
});
