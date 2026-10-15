import { readFileSync } from "node:fs";
import { createDefaultConfig } from "./types.js";
import {
  buildFoundRunRequest,
  buildJoinRunRequest,
} from "./wizardRunRequest.js";
import { expect, it } from "vitest";
import {
  resolveSetup,
  setupAnswersFor,
  setupChanges,
  setupFlowFromCatalog,
  type UseCaseCapability,
} from "@kombiverselabs/ui/usecase";

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
  const flow = setupFlowFromCatalog("ai", catalog)!;
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
  const gameFlow = setupFlowFromCatalog("game", game)!;
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
