import { describe, expect, it } from "vitest";
import type { UseCaseCatalogView } from "#lib/api/useCaseCatalog.js";
import { creationGoals } from "./creationGoals.js";
import {
  CREATION_TEMPLATES,
  templateSelection,
  templateActive,
} from "./creationTemplates.js";
import {
  getEasyGoalQuestions,
  createDefaultConfig,
  buildFoundRunRequest,
} from "./index.js";

describe("creation templates", () => {
  it("carries only released selections through Infinity into the wizard intent", () => {
    const app: UseCaseCatalogView = {
      components: [
        {
          id: "immich",
          name: "Immich",
          role: "primary",
          kind: "application",
          maturity: "beta",
        },
      ],
      computeTiers: {},
      settings: [],
    };
    const questions = getEasyGoalQuestions();
    const infinity = CREATION_TEMPLATES.find(
      (template) => template.id === "infinity",
    )!;
    const catalog = new Map([["photos", app]]);
    const apply = (source: Map<string, UseCaseCatalogView>) => {
      const cards = creationGoals(questions, source);
      const selection = templateSelection(infinity, cards);
      const config = createDefaultConfig();
      for (const goal of selection.goals) config.goals![goal] = true;
      return { selection, config, request: buildFoundRunRequest(config) };
    };
    const absent = apply(new Map());
    expect(absent.request.intent.goals).toEqual([]);
    expect(absent.selection.skipped).toContain("marketplace");
    const current = apply(catalog);
    expect(current.request.intent.goals).toEqual(["photos"]);
    expect(current.selection.skipped).toContain("marketplace");
    expect(templateActive(current.selection, current.config.goals)).toBe(true);
    catalog.set("marketplace", {
      ...app,
      components: [
        {
          id: "coolify",
          name: "Coolify",
          role: "primary",
          kind: "application",
        },
      ],
    });
    const planned = apply(catalog);
    expect(planned.request.intent.goals).not.toContain("marketplace");
    expect(planned.selection.skipped).toContain("marketplace");

    catalog.set("marketplace", {
      ...app,
      components: [
        {
          id: "coolify",
          name: "Coolify",
          role: "primary",
          kind: "application",
          maturity: "experimental",
        },
      ],
    });
    const released = apply(catalog);
    expect(released.request.intent.goals).toContain("marketplace");
    expect(released.selection.skipped).not.toContain("marketplace");
    for (const goal of released.selection.goals)
      released.config.goals![goal] = false;
    expect(buildFoundRunRequest(released.config).intent.goals).toEqual([]);
    expect(templateActive(released.selection, released.config.goals)).toBe(
      false,
    );
  });
});
