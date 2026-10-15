import type {
  UseCaseSetupResult,
  UseCaseSetupAnswers,
  UseCaseSetupChoice,
  UseCaseSetupCondition,
  UseCaseSetupFlow,
} from "@kombiverselabs/ui/usecase";
import type {
  UseCaseBundle,
  UseCaseCapability,
  UseCaseCatalogView,
  UseCaseSetting,
  UseCaseSettingOption,
} from "#lib/api/useCaseCatalog.js";
import { backendChoices } from "./creationGoals.js";
import { settingLevel, type TieredQuestion } from "./experience.js";

type Translate = (key: string, params?: Record<string, string>) => string;

/** What Techstack already knows about the user's hardware. */
export interface SetupHardwareHints {
  /** GPU model a registered server reported, e.g. "NVIDIA GeForce RTX 4070". */
  gpu?: string;
  /** Memory of the server the setup most likely installs on, in MB. */
  ramMb?: number;
  /** Name of that server, for the recommendation's reason. */
  server?: string;
}

/**
 * The option of a choice setting that fits the server: the one with the
 * largest memory need the server still covers, never one that needs a GPU the
 * server lacks. Undefined when the catalog declares no memory needs or the
 * server's memory is unknown, so the catalog default stays recommended.
 */
export function optionFittingMemory(
  options: readonly UseCaseSettingOption[] | undefined,
  hardware: SetupHardwareHints,
): string | undefined {
  const ramMb = hardware.ramMb;
  if (!ramMb || !options?.some((option) => option.minMemoryGb))
    return undefined;
  const gpu = acceleratorFromGpu(hardware.gpu) !== undefined;
  let best: UseCaseSettingOption | undefined;
  for (const option of options) {
    const need = option.minMemoryGb ?? 0;
    if (need * 1024 > ramMb || (option.needsAccelerator && !gpu)) continue;
    if (!best || need > (best.minMemoryGb ?? 0)) best = option;
  }
  return best?.id;
}

/**
 * The accelerator option a reported GPU model calls for. A model string the
 * agent reported is free text, so only unambiguous vendor names count.
 */
export function acceleratorFromGpu(gpu?: string): "nvidia" | "amd" | undefined {
  const model = gpu?.toLowerCase() ?? "";
  if (/nvidia|geforce|quadro|tesla|\brtx\b|\bgtx\b/.test(model))
    return "nvidia";
  if (/\bamd\b|radeon|instinct/.test(model)) return "amd";
  return undefined;
}

/**
 * Questions that only make sense under an earlier answer. Everything else in a
 * setup flow is derived from the release catalog, so a new use case, setting
 * or capability module appears in the guided setup without code here.
 */
const ONLY_WHEN: Record<string, Record<string, UseCaseSetupCondition[]>> = {
  ai: Object.fromEntries(
    [
      "accelerator",
      "model-size",
      "model-pack-general",
      "model-pack-coding",
      "model-pack-vision",
    ].map((id) => [id, [{ question: "inference-location", anyOf: ["local"] }]]),
  ),
};

const STEP_ORDER: UseCaseSetting["group"][] = [
  "profile",
  "hardware",
  "storage",
  "access",
  "features",
];

/**
 * The guided setup of one use case, built from its release catalog entry:
 * which app, what it should do (capability modules), which tool per module,
 * then every choice setting, then the add-ons as one multi-choice step. The
 * recommendation of every question is the catalog default, so applying the
 * setup without a single answer installs exactly the catalog standard; only a
 * GPU a registered server reported moves the accelerator recommendation.
 */
export function setupFlowFor(
  useCase: string,
  catalog: UseCaseCatalogView | undefined,
  t: Translate,
  hardware: SetupHardwareHints = {},
): UseCaseSetupFlow | undefined {
  if (!catalog) return undefined;
  const flow: UseCaseSetupFlow & { questions: TieredQuestion[] } = {
    useCase,
    base: { capabilities: {}, settings: {} },
    questions: [],
  };
  const settings = catalog.settings.filter(
    (setting) => setting.kind !== "text",
  );
  for (const setting of settings) {
    if (setting.kind === "toggle") flow.base.settings[setting.id] = false;
    else flow.base.settings[setting.id] = setting.default;
  }

  const apps = backendChoices(catalog);
  const backend = settings.find((setting) => setting.id === "backend");
  if (backend && apps.length > 1) {
    flow.questions.push({
      id: "backend",
      level: 1,
      step: t("wizard.setup.step.app"),
      title: t("wizard.setup.backend.title"),
      kind: "single",
      default: String(backend.default),
      choices: apps
        .filter((app) =>
          backend.options?.some((option) => option.id === app.id),
        )
        .map((app) => {
          const option = backend.options?.find((entry) => entry.id === app.id);
          return {
            id: app.id,
            label: app.name,
            description: option?.note,
            recommended: app.id === backend.default,
            tools: [app.id],
            realization:
              option?.realization ??
              (app.realization === "recorded"
                ? "recorded"
                : backend.realization),
            effect: { settings: { backend: app.id } },
          };
        }),
    });
  }

  flow.questions.push(
    ...(catalog.bundles?.length
      ? bundleQuestions(
          useCase,
          catalog.bundles,
          catalog.capabilities ?? [],
          flow,
          t,
          hardware,
        )
      : capabilityQuestions(catalog.capabilities ?? [], flow, t)),
  );

  const choiceSettings = settings
    .filter((setting) => setting.kind === "choice" && setting.id !== "backend")
    .sort(
      (a, b) =>
        Number(a.depth === "advanced") - Number(b.depth === "advanced") ||
        STEP_ORDER.indexOf(a.group) - STEP_ORDER.indexOf(b.group),
    );
  // A setting with its own step name keeps the rail readable when several
  // settings share a group (Smart Home has three "profile" settings).
  const stepName = (setting: UseCaseSetting) => {
    const key = `wizard.setup.step.setting.${setting.id}`;
    const own = t(key);
    return own === key ? t(`wizard.setup.step.${setting.group}`) : own;
  };
  for (const setting of choiceSettings) {
    // A GPU a registered server reported decides the hardware recommendation;
    // without one the catalog default (CPU) stays recommended.
    const detected =
      setting.id === "accelerator"
        ? setting.options?.find(
            (option) => option.id === acceleratorFromGpu(hardware.gpu),
          )?.id
        : undefined;
    // Options with a declared memory need follow the server's memory and GPU.
    const fitting = optionFittingMemory(setting.options, hardware);
    const ramGb = Math.round((hardware.ramMb ?? 0) / 1024);
    const serverFacts = t(
      acceleratorFromGpu(hardware.gpu)
        ? "wizard.setup.hardware.serverWithGpu"
        : "wizard.setup.hardware.serverNoGpu",
      {
        server: hardware.server ?? "",
        ram: String(ramGb),
        gpu: hardware.gpu ?? "",
      },
    );
    const fitNote = (option: UseCaseSettingOption): string | undefined => {
      if (fitting === undefined || !option.minMemoryGb) return undefined;
      if (option.id === fitting) return serverFacts;
      if (option.minMemoryGb * 1024 > (hardware.ramMb ?? 0))
        return t("wizard.setup.hardware.needsMemory", {
          ram: String(option.minMemoryGb),
        });
      if (option.needsAccelerator && !acceleratorFromGpu(hardware.gpu))
        return t("wizard.setup.hardware.needsGpu");
      return undefined;
    };
    const recommended = detected ?? fitting ?? String(setting.default);
    flow.questions.push({
      id: setting.id,
      level: settingLevel(setting),
      step: stepName(setting),
      title: setting.name,
      help: setting.help,
      kind: "single",
      default: recommended,
      when: ONLY_WHEN[useCase]?.[setting.id],
      choices: (setting.options ?? []).map((option) => ({
        id: option.id,
        label: option.name,
        description:
          [
            option.note,
            option.id === detected
              ? t("wizard.setup.hardware.detected", { gpu: hardware.gpu ?? "" })
              : undefined,
            fitNote(option),
          ]
            .filter(Boolean)
            .join(" · ") || undefined,
        recommended: option.id === recommended,
        realization: option.realization ?? setting.realization,
        effect: { settings: { [setting.id]: option.id } },
      })),
    });
  }

  const toggles = settings.filter((setting) => setting.kind === "toggle");
  if (toggles.length > 0) {
    // A toggle hidden by an earlier answer must not install anything, so the
    // add-ons split into one step per distinct condition, and into one step
    // per experience level so the everyday add-ons are not buried under the
    // technical ones.
    const byCondition = new Map<string, UseCaseSetting[]>();
    for (const toggle of [...toggles].sort(
      (a, b) => settingLevel(a) - settingLevel(b),
    )) {
      const key = JSON.stringify([
        ONLY_WHEN[useCase]?.[toggle.id] ?? null,
        settingLevel(toggle),
      ]);
      byCondition.set(key, [...(byCondition.get(key) ?? []), toggle]);
    }
    let index = 0;
    for (const [key, group] of byCondition) {
      const [condition, level] = JSON.parse(key) as [
        UseCaseSetupCondition[] | null,
        number,
      ];
      flow.questions.push({
        id: index++ === 0 ? "extras" : `extras-${index}`,
        level,
        step: t("wizard.setup.step.extras"),
        title: t("wizard.setup.extras.title"),
        kind: "multi",
        default: group
          .filter((toggle) => toggle.default === true)
          .map((toggle) => toggle.id),
        when: condition ?? undefined,
        choices: group.map((toggle) => ({
          id: toggle.id,
          label: toggle.name,
          description: toggle.help,
          recommended: toggle.default === true,
          realization: toggle.realization,
          effect: { settings: { [toggle.id]: true } },
        })),
      });
    }
  }

  return flow.questions.length > 0 ? flow : undefined;
}

/** Optional modules as one "what should it do" step, then a tool question for
 * every chosen module that offers an alternative. Required modules are always
 * on and only ask for their tool. */
function capabilityQuestions(
  capabilities: UseCaseCapability[],
  flow: UseCaseSetupFlow,
  t: Translate,
): TieredQuestion[] {
  const byId = new Map(capabilities.map((entry) => [entry.id, entry]));
  const optional = capabilities.filter((entry) => !entry.required);
  for (const capability of capabilities) {
    flow.base.capabilities[capability.id] = capability.required
      ? capability.default.id
      : "off";
  }
  const questions: TieredQuestion[] = [];
  if (optional.length > 0) {
    questions.push({
      id: "capabilities",
      level: 2,
      step: t("wizard.setup.step.tasks"),
      title: t("wizard.setup.capabilities.title"),
      kind: "multi",
      default: optional
        .filter((entry) => entry.enabledByDefault)
        .map((entry) => entry.id),
      choices: optional.map((capability): UseCaseSetupChoice => ({
        id: capability.id,
        label: capability.name,
        description: capability.requiresAccelerator
          ? [capability.help, t("wizard.setup.capabilities.needsGpu")]
              .filter(Boolean)
              .join(" · ")
          : capability.help,
        recommended: capability.enabledByDefault,
        tools: [capability.default.id],
        realization: capability.default.realization,
        effect: {
          capabilities: Object.fromEntries(
            (capability.default.realization === "install"
              ? [capability, ...requirements(capability, byId)]
              : []
            )
              .filter((entry) => entry.default.realization === "install")
              .map((entry) => [entry.id, entry.default.id]),
          ),
        },
      })),
    });
  }
  for (const capability of capabilities) {
    if (!capability.alternative) continue;
    const options = [capability.default, capability.alternative];
    questions.push({
      id: `tool-${capability.id}`,
      level: 3,
      step: t("wizard.setup.step.tool"),
      title: t("wizard.setup.tool.title", { capability: capability.name }),
      help: capability.help,
      kind: "single",
      default: capability.default.id,
      when: capability.required
        ? undefined
        : [{ question: "capabilities", anyOf: [capability.id] }],
      choices: options.map((option, index) => ({
        id: option.id,
        label: option.name,
        description: option.note ?? option.pending,
        recommended: index === 0,
        tools: [option.id],
        realization: option.realization,
        // Pending intent must not silently execute a different default tool.
        // Required modules remain subject to the release's normal admission.
        effect: {
          capabilities: {
            [capability.id]:
              option.realization === "install" ? option.id : "off",
          },
        },
      })),
    });
  }
  return questions;
}

/** What turning a set of modules on selects: each installing module plus the
 * installing modules it requires. A recorded module selects nothing, so
 * pending intent never executes a different default. */
function installEffect(
  modules: UseCaseCapability[],
  byId: Map<string, UseCaseCapability>,
): Record<string, string> {
  return Object.fromEntries(
    modules
      .filter((entry) => entry.default.realization === "install")
      .flatMap((entry) => [entry, ...requirements(entry, byId)])
      .filter((entry) => entry.default.realization === "install")
      .map((entry) => [entry.id, entry.default.id]),
  );
}

/**
 * The guided setup of a use case that declares bundles: first "what do you
 * want to do?" with one card per goal, then at most one question per chosen
 * goal for its optional modules, then the tool of every offered module with
 * an alternative. A module in no bundle is not offered and stays off.
 */
function bundleQuestions(
  useCase: string,
  bundles: UseCaseBundle[],
  capabilities: UseCaseCapability[],
  flow: UseCaseSetupFlow,
  t: Translate,
  hardware: SetupHardwareHints,
): TieredQuestion[] {
  const byId = new Map(capabilities.map((entry) => [entry.id, entry]));
  for (const capability of capabilities) {
    flow.base.capabilities[capability.id] = capability.required
      ? capability.default.id
      : "off";
  }
  const modules = (ids: string[]) => ids.flatMap((id) => byId.get(id) ?? []);
  const gpu = acceleratorFromGpu(hardware.gpu) !== undefined;
  const own = (
    key: string,
    fallback: string,
    params?: Record<string, string>,
  ) => {
    const text = t(`${key}.${useCase}`, params);
    return text === `${key}.${useCase}` ? t(fallback, params) : text;
  };
  const questions: TieredQuestion[] = [
    {
      id: "bundles",
      level: 1,
      step: t("wizard.setup.step.goals"),
      title: own("wizard.setup.bundles.title", "wizard.setup.bundles.title"),
      kind: "multi",
      default: bundles
        .filter((bundle) => bundle.required || bundle.enabledByDefault)
        .map((bundle) => bundle.id),
      choices: bundles.map((bundle): UseCaseSetupChoice => {
        const chosen = modules(bundle.defaults);
        return {
          id: bundle.id,
          label: bundle.name,
          description: [
            bundle.help,
            bundle.examples?.[0] &&
              t("wizard.setup.bundles.example", {
                example: bundle.examples[0],
              }),
            bundle.required && t("wizard.setup.bundles.always"),
            bundle.recommendsAccelerator &&
              t(
                gpu
                  ? "wizard.setup.bundles.gpuFound"
                  : "wizard.setup.bundles.gpuHelps",
              ),
            bundle.advanced && t("wizard.setup.bundles.advanced"),
            bundle.requiresSandbox && t("wizard.setup.bundles.sandbox"),
          ]
            .filter(Boolean)
            .join(" · "),
          recommended:
            bundle.required ||
            bundle.enabledByDefault ||
            (bundle.recommendsAccelerator && gpu),
          tools: chosen.map((entry) => entry.default.id),
          realization: chosen.some(
            (entry) => entry.default.realization === "install",
          )
            ? "install"
            : "recorded",
          effect: { capabilities: installEffect(chosen, byId) },
        };
      }),
    },
  ];

  // Where a module is offered: on with a goal, or as an option inside one.
  const offeredBy = new Map<string, { question: string; anyOf: string[] }>();
  for (const bundle of bundles) {
    for (const id of bundle.defaults) {
      const known = offeredBy.get(id);
      if (known?.question === "bundles") known.anyOf.push(bundle.id);
      else offeredBy.set(id, { question: "bundles", anyOf: [bundle.id] });
    }
  }
  for (const bundle of bundles) {
    const options = modules(bundle.capabilities).filter(
      (entry) => !entry.required && !bundle.defaults.includes(entry.id),
    );
    if (options.length === 0) continue;
    const question = `bundle-${bundle.id}`;
    for (const entry of options) {
      if (!offeredBy.has(entry.id))
        offeredBy.set(entry.id, { question, anyOf: [entry.id] });
    }
    questions.push({
      id: question,
      level: 2,
      step: bundle.name,
      title: t("wizard.setup.bundles.optionsTitle", { bundle: bundle.name }),
      kind: "multi",
      default: options
        .filter((entry) => entry.enabledByDefault)
        .map((entry) => entry.id),
      when: bundle.required
        ? undefined
        : [{ question: "bundles", anyOf: [bundle.id] }],
      choices: options.map((entry): UseCaseSetupChoice => ({
        id: entry.id,
        label: entry.name,
        description: entry.requiresAccelerator
          ? [entry.help, t("wizard.setup.capabilities.needsGpu")]
              .filter(Boolean)
              .join(" · ")
          : entry.help,
        recommended: entry.enabledByDefault,
        tools: [entry.default.id],
        realization: entry.default.realization,
        effect: { capabilities: installEffect([entry], byId) },
      })),
    });
  }

  for (const capability of capabilities) {
    if (!capability.alternative) continue;
    const offered = offeredBy.get(capability.id);
    if (!capability.required && !offered) continue;
    const options = [capability.default, capability.alternative];
    questions.push({
      id: `tool-${capability.id}`,
      level: 3,
      step: t("wizard.setup.step.tool"),
      title: t("wizard.setup.tool.title", { capability: capability.name }),
      help: capability.help,
      kind: "single",
      default: capability.default.id,
      when: capability.required || !offered ? undefined : [offered],
      choices: options.map((option, index) => ({
        id: option.id,
        label: option.name,
        description: option.note ?? option.pending,
        recommended: index === 0,
        tools: [option.id],
        realization: option.realization,
        effect: {
          capabilities: {
            [capability.id]:
              option.realization === "install" ? option.id : "off",
          },
        },
      })),
    });
  }
  return questions;
}

function requirements(
  capability: UseCaseCapability,
  byId: Map<string, UseCaseCapability>,
  seen = new Set<string>(),
): UseCaseCapability[] {
  const found: UseCaseCapability[] = [];
  for (const id of capability.requires ?? []) {
    const required = byId.get(id);
    if (!required || seen.has(id)) continue;
    seen.add(id);
    found.push(required, ...requirements(required, byId, seen));
  }
  return found;
}

/** Forward actual setting decisions into their catalog questions. Legacy
 * executable capability effects are deliberately not used to infer wishes. */
export function setupAnswersFor(
  flow: UseCaseSetupFlow | undefined,
  settings: Record<string, string | boolean> = {},
  desired: UseCaseSetupAnswers = {},
): UseCaseSetupAnswers {
  const direct: UseCaseSetupAnswers = {};
  for (const question of flow?.questions ?? []) {
    const value = settings[question.id];
    if (question.kind === "single" && typeof value === "string") {
      direct[question.id] = [value];
    } else if (
      question.kind === "multi" &&
      question.choices.some(
        (choice) =>
          choice.id in (choice.effect?.settings ?? {}) &&
          typeof settings[choice.id] === "boolean",
      )
    ) {
      const defaults = [question.default].flat();
      direct[question.id] = question.choices
        .filter(
          (choice) =>
            settings[choice.id] === true ||
            (settings[choice.id] === undefined && defaults.includes(choice.id)),
        )
        .map((choice) => choice.id);
    }
  }
  return {
    ...direct,
    ...Object.fromEntries(
      Object.entries(desired).map(([id, value]) => [
        id,
        Array.isArray(value) ? [...value] : value,
      ]),
    ),
  };
}

/**
 * What applying a setup changes for one use case: the settings whose value
 * differs from what is in effect now (stored value, else the catalog
 * default), and the complete capability selection.
 */
export function setupChanges(
  catalog: UseCaseCatalogView | undefined,
  result: UseCaseSetupResult,
  current: Record<string, string | boolean> = {},
): {
  settings: [string, string | boolean][];
  capabilities: Record<string, string>;
  answers: Record<string, string[]>;
} {
  const settings: [string, string | boolean][] = [];
  const askedSettings = new Set(
    result.questions.flatMap((question) =>
      question.choices.flatMap((choice) =>
        Object.keys(choice.effect?.settings ?? {}),
      ),
    ),
  );
  for (const [id, value] of Object.entries(result.selection.settings)) {
    const setting = catalog?.settings.find((entry) => entry.id === id);
    if (!setting || (id in current && !askedSettings.has(id))) continue;
    const effective = current[id] ?? setting.default;
    if (effective !== value) settings.push([id, value]);
  }
  return {
    settings,
    capabilities: { ...result.selection.capabilities },
    answers: Object.fromEntries(
      Object.entries(result.answers).map(([id, answer]) => [
        id,
        Array.isArray(answer) ? [...answer] : [answer],
      ]),
    ),
  };
}
