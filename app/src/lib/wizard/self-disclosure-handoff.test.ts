import { describe, expect, it } from "vitest";
import {
  creationUseCaseHandoffKey,
  creationUseCaseWizardConfig,
  readSelfDisclosureHandoff,
  selfDisclosureWizardConfig,
  withoutSelfDisclosureParams,
} from "./self-disclosure-handoff.js";

describe("Cloud self-disclosure handoff", () => {
  it("keeps creation selections out of profile imports without bypassing actual answer consent", () => {
    const params = new URLSearchParams("use-case=mail");
    const config = creationUseCaseWizardConfig(params);
    expect(config?.goals?.mail).toBe(true);
    expect(config?.goals?.files).toBe(false);
    expect(config?.goals?.vault).toBe(false);
    expect(readSelfDisclosureHandoff(params)).toBeNull();

    const mailKey = creationUseCaseHandoffKey(params);
    params.set("theme", "dark");
    params.set("host_navigation", "embedded");
    expect(creationUseCaseHandoffKey(params)).toBe(mailKey);

    params.set("goals", "photos");
    expect(readSelfDisclosureHandoff(params)?.goals).toEqual(["photos"]);
    expect(creationUseCaseWizardConfig(params)?.goals?.mail).toBe(true);
    const settledUrl = withoutSelfDisclosureParams(
      new URL(`https://techstack.example/stacks/new?${params}`),
    );
    expect(
      creationUseCaseHandoffKey(
        new URL(settledUrl, "https://techstack.example").searchParams,
      ),
    ).toBe(mailKey);

    params.set("use-case", " Photos ");
    expect(creationUseCaseHandoffKey(params)).not.toBe(mailKey);
    expect(creationUseCaseWizardConfig(params)?.goals?.photos).toBe(true);
    expect(creationUseCaseWizardConfig(params)?.goals?.mail).toBe(false);
    params.set("use-case", "teleport");
    expect(creationUseCaseWizardConfig(params)).toBeUndefined();
  });

  it("keeps only closed-vocabulary answers, ignores free text, prefills the wizard and leaves no answers in the URL", () => {
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
