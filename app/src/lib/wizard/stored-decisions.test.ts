import { expect, it } from "vitest";
import type { HomelabSummary } from "#lib/api/homelab.js";
import { storedBlockedWizardConfig } from "./stored-decisions.js";
import { buildFoundRunRequest } from "./wizardRunRequest.js";

it("reopens canonical blocked wishes without substituting executable defaults", () => {
  const homelab: HomelabSummary = {
    id: "saved-homelab",
    name: "My homelab",
    created: "",
    updated: "",
    intent: {
      wizard: {
        projection_status: "blocked",
        goals: ["ai"],
        use_case_settings: { ai: { "model-pack-general": false } },
        use_case_capabilities: {
          ai: { inference: "off", observability: "off" },
        },
        use_case_setup_answers: {
          ai: { "tool-inference": ["llama-cpp"], extras: [] },
        },
        access: { mode: "local" },
        household: { profile: "solo" },
      },
    },
  };
  const reopened = storedBlockedWizardConfig(homelab);
  expect(reopened).toBeDefined();
  if (!reopened) throw new Error("saved decisions missing");
  const request = buildFoundRunRequest(reopened);
  expect(request.intent.goals).toEqual(["ai"]);
  expect(request.intent.use_case_settings).toEqual({
    ai: { "model-pack-general": false },
  });
  expect(request.intent.use_case_capabilities).toEqual({
    ai: { inference: "off", observability: "off" },
  });
  expect(request.intent.use_case_setup_answers).toEqual({
    ai: { "tool-inference": ["llama-cpp"], extras: [] },
  });
  expect(reopened.network.accessMode).toBe("home");
  expect(reopened.audience.onlyMe).toBe(true);
  expect(storedBlockedWizardConfig({ ...homelab, intent: {} })).toBeUndefined();
  expect(storedBlockedWizardConfig(null)).toBeUndefined();
});

it("roundtrips canonical shared people and remote access, including empty and solo decisions (E85-R3)", () => {
  const homelab: HomelabSummary = {
    id: "saved",
    name: "Home",
    created: "",
    updated: "",
    intent: {
      wizard: {
        projection_status: "blocked",
        goals: ["ai"],
        household: {
          profile: "shared",
          planned_people: [
            {
              client_ref: "friend-1",
              name: "Friend",
              email: "friend@example.invalid",
            },
            { client_ref: "friend-2", email: "second@example.invalid" },
          ],
        },
        access: {
          mode: "remote-private",
          publications: [{ service_id: "own-site", exposure: "public" }],
        },
      },
    },
  };
  const reopened = storedBlockedWizardConfig(homelab);
  if (!reopened) throw new Error("saved decisions missing");
  expect(reopened.network.accessModeSelected).toBe(true);
  expect(reopened.network.accessMode).toBe("anywhere");
  expect(reopened.audience).toEqual({
    onlyMe: false,
    familyFriends: true,
    public: false,
  });
  const request = buildFoundRunRequest(reopened);
  expect(request.intent.access).toEqual({
    mode: "remote-private",
    publications: [{ service_id: "own-site", exposure: "public" }],
  });
  expect(request.intent.household).toEqual({
    profile: "shared",
    planned_people: [
      {
        client_ref: "friend-1",
        name: "Friend",
        email: "friend@example.invalid",
      },
      { client_ref: "friend-2", email: "second@example.invalid" },
    ],
  });
  const wizard = {
    projection_status: "blocked",
    goals: ["ai"],
    access: { mode: "local", publications: [] },
  };
  for (const profile of ["shared", "solo"]) {
    const config = storedBlockedWizardConfig({
      ...homelab,
      intent: {
        wizard: { ...wizard, household: { profile, planned_people: [] } },
      },
    });
    if (!config) throw new Error("saved decisions missing");
    expect(config.household).toEqual({ profile, people: [] });
    const result = buildFoundRunRequest(config).intent;
    expect(result.access).toEqual({ mode: "local", publications: [] });
    expect(result.household?.profile).toBe(profile);
    expect(result.household?.planned_people ?? []).toEqual([]);
  }
});
