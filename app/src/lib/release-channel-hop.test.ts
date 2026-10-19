import { describe, expect, it } from "vitest";
import { previewHopTarget } from "./release-channel-hop";

const base = {
  chosen: "preview",
  running: "stable",
  previewOrigin: "https://preview.techstack.kombify.io",
  href: "https://techstack.kombify.io/dashboard/stacks?tab=all#top",
  embedded: false,
};

describe("previewHopTarget", () => {
  it("keeps a person who chose Preview on Preview, and nobody else moves", () => {
    expect(previewHopTarget(base)).toBe(
      "https://preview.techstack.kombify.io/dashboard/stacks?tab=all#top",
    );
    expect(previewHopTarget({ ...base, chosen: "stable" })).toBeNull();
    expect(previewHopTarget({ ...base, chosen: undefined })).toBeNull();
    expect(previewHopTarget({ ...base, running: "preview" })).toBeNull();
    expect(previewHopTarget({ ...base, embedded: true })).toBeNull();
    expect(
      previewHopTarget({
        ...base,
        href: "https://techstack.kombify.io/dashboard?code=x&state=y",
      }),
    ).toBeNull();
    expect(
      previewHopTarget({ ...base, previewOrigin: "http://preview.example" }),
    ).toBeNull();
    expect(
      previewHopTarget({
        ...base,
        href: "https://techstack.kombify.io//evil.example/x",
      }),
    ).toBeNull();
  });
});
