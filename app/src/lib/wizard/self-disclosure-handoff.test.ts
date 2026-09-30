import { describe, expect, it } from "vitest";
import {
  readSelfDisclosureHandoff,
  selfDisclosureWizardConfig,
  withoutSelfDisclosureParams,
} from "./self-disclosure-handoff.js";

describe("Cloud self-disclosure handoff", () => {
  it("keeps only closed-vocabulary answers, prefills the wizard and leaves no answers in the URL", () => {
    const url = new URL(
      "https://techstack.example/stacks/new?experience=Guided&goals=photos,media,teleport&placement=home&hardware=mini-pc,quantum&household=household&motivations=privacy&intent=%20two%20kids%20&embedded=true",
    );
    const disclosure = readSelfDisclosureHandoff(url.searchParams);

    expect(disclosure).toEqual({
      experience: "guided",
      goals: ["photos", "media"],
      placement: "home",
      hardware: ["mini-pc"],
      household: "household",
      motivations: ["privacy"],
      notes: "two kids",
    });

    const config = selfDisclosureWizardConfig(disclosure!);
    expect(config.goals?.photos).toBe(true);
    expect(config.goals?.media).toBe(true);
    expect(config.goals?.files).toBe(false);
    expect(config.household?.profile).toBe("shared");

    expect(withoutSelfDisclosureParams(url)).toBe("/stacks/new?embedded=true");
    expect(
      readSelfDisclosureHandoff(new URLSearchParams("experience=wizard")),
    ).toBeNull();
  });
});
