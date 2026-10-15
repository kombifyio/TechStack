import { readFileSync } from "node:fs";
import { createDefaultConfig } from "./types.js";
import {
  buildFoundRunRequest,
  buildJoinRunRequest,
} from "./wizardRunRequest.js";
import { describe, expect, it } from "vitest";
import { resolveSetup } from "@kombiverselabs/ui/usecase";
import type {
  UseCaseCapability,
  UseCaseCatalogView,
  UseCaseSetting,
} from "#lib/api/useCaseCatalog.js";
import { setupAnswersFor, setupChanges, setupFlowFor } from "./setupFlows.js";

function toggle(id: string, value: boolean): UseCaseSetting {
  return {
    id,
    name: id,
    kind: "toggle",
    group: "features",
    depth: "summary",
    default: value,
    realization: "install",
  };
}

function capability(id: string, requires: string[] = []): UseCaseCapability {
  return {
    id,
    name: id,
    required: false,
    enabledByDefault: false,
    requires,
    requiresAccelerator: false,
    default: { id: `${id}-tool`, name: id, realization: "install" },
  };
}

const catalog: UseCaseCatalogView = {
  components: [],
  computeTiers: {},
  settings: [toggle("public-sharing", false), toggle("power-tools", true)],
  capabilities: [capability("search", ["index"]), capability("index")],
};

describe("guided setup derived from the release catalog", () => {
  it("turns answers into catalog decisions and reads them back", () => {
    const flow = setupFlowFor("photos", catalog, (key) => key)!;
    const answers = { capabilities: ["search"], extras: ["public-sharing"] };
    const resolved = resolveSetup(flow, answers);
    const { selection } = resolved;
    const changes = setupChanges(catalog, resolved);

    // The chosen module brings what it requires; the unchosen add-on that is
    // on by default is turned off explicitly.
    expect(changes.capabilities).toEqual({
      search: "search-tool",
      index: "index-tool",
    });
    expect(Object.fromEntries(changes.settings)).toEqual({
      "public-sharing": true,
      "power-tools": false,
    });
    // Reopening the card shows answers that decide exactly the same.
    const reopened = changes.answers;
    expect(resolveSetup(flow, reopened).selection).toEqual(selection);
  });
});

describe("guided setup by customer goal", () => {
  it("installs the catalog default through the goals and never offers an unbundled module", () => {
    const required = { ...capability("engine"), required: true };
    const alternative = {
      ...capability("chat", ["engine"]),
      enabledByDefault: true,
      alternative: {
        id: "other-chat",
        name: "Other",
        realization: "install" as const,
      },
    };
    const goals: UseCaseCatalogView = {
      components: [],
      computeTiers: {},
      settings: [],
      capabilities: [
        required,
        alternative,
        capability("search", ["engine"]),
        capability("helper", ["engine"]),
        capability("legacy", ["engine"]),
      ],
      bundles: [
        {
          id: "own",
          name: "Own",
          capabilities: ["engine"],
          defaults: ["engine"],
          required: true,
          enabledByDefault: true,
          advanced: false,
          recommendsAccelerator: false,
          requiresSandbox: false,
        },
        {
          id: "talk",
          name: "Talk",
          capabilities: ["chat", "search"],
          defaults: ["chat"],
          required: false,
          enabledByDefault: true,
          advanced: false,
          recommendsAccelerator: false,
          requiresSandbox: false,
        },
        {
          id: "aide",
          name: "Aide",
          capabilities: ["helper", "search"],
          defaults: ["helper"],
          required: false,
          enabledByDefault: false,
          advanced: true,
          recommendsAccelerator: false,
          requiresSandbox: false,
        },
      ],
    };
    const flow = setupFlowFor("lab", goals, (key) => key)!;
    const pick = (answers = {}) =>
      resolveSetup(flow, answers).selection.capabilities;

    expect(pick()).toEqual({
      engine: "engine-tool",
      chat: "chat-tool",
      search: "off",
      helper: "off",
      legacy: "off",
    });
    // A module two goals offer installs once when either asks for it.
    expect(
      pick({
        bundles: ["own", "talk", "aide"],
        "bundle-talk": [],
        "bundle-aide": ["search"],
      }),
    ).toMatchObject({ helper: "helper-tool", search: "search-tool" });
    // The tool of a goal's module is asked only while that goal is chosen.
    const askedFor = (bundles: string[]) =>
      resolveSetup(flow, { bundles }).questions.map((question) => question.id);
    expect(askedFor(["own", "talk"])).toContain("tool-chat");
    expect(askedFor(["own"])).not.toContain("tool-chat");
    expect(
      flow.questions.flatMap((question) =>
        question.choices.map((choice) => choice.id),
      ),
    ).not.toContain("legacy");
  });
});

describe("guided setup hardware recommendation", () => {
  it("installs the accelerator of a GPU a server reported when the recommendation is taken", () => {
    const ai: UseCaseCatalogView = {
      components: [],
      computeTiers: {},
      settings: [
        {
          id: "accelerator",
          name: "Accelerator",
          kind: "choice",
          group: "hardware",
          depth: "summary",
          default: "cpu",
          realization: "install",
          options: [
            { id: "cpu", name: "CPU only" },
            { id: "nvidia", name: "NVIDIA GPU" },
            { id: "amd", name: "AMD GPU" },
          ],
        },
      ],
    };
    const recommended = (gpu?: string) => {
      const flow = setupFlowFor("lab", ai, (key) => key, { gpu })!;
      return resolveSetup(flow, {}).selection.settings.accelerator;
    };

    expect(recommended("NVIDIA GeForce RTX 4070")).toBe("nvidia");
    expect(recommended("AMD Radeon RX 7900 XTX")).toBe("amd");
    expect(recommended("Intel UHD Graphics 770")).toBe("cpu");
    expect(recommended(undefined)).toBe("cpu");
  });

  it("installs the largest model size the server's memory and GPU fit", () => {
    const ai: UseCaseCatalogView = {
      components: [],
      computeTiers: {},
      settings: [
        {
          id: "model-size",
          name: "Model size",
          kind: "choice",
          group: "features",
          depth: "advanced",
          default: "small",
          realization: "install",
          options: [
            { id: "small", name: "Small", minMemoryGb: 8 },
            { id: "medium", name: "Medium", minMemoryGb: 16 },
            {
              id: "large",
              name: "Large",
              minMemoryGb: 32,
              needsAccelerator: true,
            },
          ],
        },
      ],
    };
    const size = (ramGb?: number, gpu?: string) => {
      const flow = setupFlowFor("lab", ai, (key) => key, {
        ramMb: ramGb && ramGb * 1024,
        gpu,
      })!;
      return resolveSetup(flow, {}).selection.settings["model-size"];
    };

    expect(size(undefined)).toBe("small");
    expect(size(4)).toBe("small");
    expect(size(16)).toBe("medium");
    expect(size(64)).toBe("medium");
    expect(size(64, "NVIDIA GeForce RTX 4090")).toBe("large");
  });
});

it("preserves desired pending and off decisions through draft and typed creation roundtrip", () => {
  const pinned = JSON.parse(
    readFileSync(
      new URL(
        "../../../../internal/stackkitrelease/pinned-use-case-catalog.json",
        import.meta.url,
      ),
      "utf8",
    ),
  );
  const catalog = pinned.catalog.useCases.find((c: any) => c.id === "ai");
  const flow = setupFlowFor("ai", catalog, (k) => k)!;
  // The question that turns chat on: the module step, or the goal step of a
  // release that groups its modules into bundles.
  const turnsOn = (module: string) => {
    const question = flow.questions.find(
      (entry) =>
        entry.kind === "multi" &&
        entry.choices.some((choice) => choice.effect?.capabilities?.[module]),
    )!;
    const chosen = question.choices
      .filter(
        (choice) =>
          choice.effect?.capabilities?.[module] || choice.id === "speech",
      )
      .map((choice) => choice.id);
    return [question.id, chosen] as const;
  };
  const [chatQuestion, chatAnswer] = turnsOn("chat");
  const requested = {
    "tool-inference": "llama-cpp",
    [chatQuestion]: chatAnswer,
    extras: [],
  };
  const first = resolveSetup(flow, requested);
  const changed = setupChanges(catalog, first);
  const config = createDefaultConfig();
  config.goals!.ai = true;
  config.useCaseSetupAnswers = { ai: new Proxy(changed.answers, {}) };
  config.useCaseSettings = { ai: Object.fromEntries(changed.settings) };
  config.useCaseCapabilities = { ai: changed.capabilities };
  // Reactive drafts expose proxy-backed answer maps at the same interface.
  expect(
    setupAnswersFor(flow, {}, config.useCaseSetupAnswers.ai)["tool-inference"],
  ).toEqual(["llama-cpp"]);
  expect(buildFoundRunRequest(config).intent.use_case_setup_answers).toEqual({
    ai: changed.answers,
  });
  const draft = JSON.parse(JSON.stringify(config));
  const reopened = resolveSetup(flow, draft.useCaseSetupAnswers.ai);
  const found = buildFoundRunRequest(draft);
  const join = buildJoinRunRequest(
    draft,
    "existing-stack",
    "Existing stack",
    [],
  );

  expect(reopened.answers["tool-inference"]).toBe("llama-cpp");
  expect(reopened.answers[chatQuestion]).toEqual(chatAnswer);
  expect(reopened.answers.extras).toEqual([]);
  const inference = catalog.capabilities.find(
    (entry: UseCaseCapability) => entry.id === "inference",
  )!;
  const wanted = [inference.default, inference.alternative].find(
    (option) => option?.id === requested["tool-inference"],
  );
  expect(changed.capabilities.inference).toBe(
    wanted?.realization === "install" ? wanted.id : "off",
  );
  expect(changed.capabilities.observability).toBe("off");
  expect(found.intent.use_case_setup_answers).toEqual(
    draft.useCaseSetupAnswers,
  );
  expect(join.intent.use_case_setup_answers).toEqual(draft.useCaseSetupAnswers);
  const supported = setupChanges(
    catalog,
    resolveSetup(flow, {
      ...requested,
      "tool-inference": inference.default.id,
    }),
  );
  expect(supported.capabilities.inference).toBe(inference.default.id);
  expect(supported.capabilities.chat).toBe("open-webui");
  // Earlier drafts contain direct setting decisions without setup answers.
  const game = pinned.catalog.useCases.find(
    (entry: { id: string }) => entry.id === "game",
  );
  const gameFlow = setupFlowFor("game", game, (key) => key)!;
  const legacySettings = { edition: "bedrock" };
  const legacy = resolveSetup(
    gameFlow,
    setupAnswersFor(gameFlow, legacySettings),
  );
  expect(legacy.answers.edition).toBe("bedrock");
  expect(
    Object.fromEntries(setupChanges(game, legacy, legacySettings).settings)
      .edition,
  ).toBeUndefined();
});
