/**
 * The five steps as modules. A frame shows a module through `Component` and
 * `props`, without knowing what a step needs; `summary` is the one line a
 * reached step shows in the stepper.
 */
import type { Component } from "svelte";
import AccessStep from "#lib/components/wizard/steps/AccessStep.svelte";
import GoalsStep from "#lib/components/wizard/steps/GoalsStep.svelte";
import LoginStep from "#lib/components/wizard/steps/LoginStep.svelte";
import ServerStep from "#lib/components/wizard/steps/ServerStep.svelte";
import UsersStep from "#lib/components/wizard/steps/UsersStep.svelte";
import { tr, trn } from "#lib/i18n.svelte.js";
import { selectedCanonicalUseCasesFromGoals } from "#lib/wizard/index.js";
import type { StackConfig } from "#lib/wizard/types.js";
import type { CreationStepKey } from "./core/index";
import type { CreationFrame } from "./frame.svelte";

type SliceKey = "goals" | "target" | "access" | "users" | "owner";

export interface CreationStepModule {
  key: CreationStepKey;
  /** The slice this step writes. */
  provides: SliceKey;
  /** Slices it reads besides its own. */
  requires: SliceKey[];
  /** The frame is there for a step whose answer lives outside the config. */
  summary(config: StackConfig, frame?: CreationFrame): string;
  // Each step takes its own props; `props` is what ties the two together.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  Component: Component<any>;
  props(frame: CreationFrame): Record<string, unknown>;
}

const SERVER_SUMMARY: Record<string, string> = {
  "install-command": "wizard.summary.server.installCommand",
  "connect-remote": "wizard.summary.server.connectRemote",
  "kombify-cloud": "wizard.summary.server.managed",
  hypervisor: "wizard.summary.server.hypervisor",
};

/** The answers as a bound prop: the Goals and Server steps write into them. */
function withBoundConfig(frame: CreationFrame, props: Record<string, unknown>) {
  return Object.defineProperty(props, "config", {
    enumerable: true,
    configurable: true,
    get: () => frame.config,
    set: (value: StackConfig) => {
      frame.config = value;
    },
  });
}

export const CREATION_MODULES: Record<CreationStepKey, CreationStepModule> = {
  goals: {
    key: "goals",
    provides: "goals",
    requires: [],
    summary(config) {
      const count = selectedCanonicalUseCasesFromGoals(config.goals).length;
      return count ? trn("wizard.summary.goals", count, { count }) : "";
    },
    Component: GoalsStep,
    props: (frame) =>
      withBoundConfig(frame, {
        primaryGoalQuestions: frame.primaryGoalQuestions,
        advancedGoalQuestions: frame.advancedGoalQuestions,
        onGoalChange: frame.setGoal,
        onSettingChange: frame.setUseCaseSetting,
        recommendationState: frame.recommendationState,
        onRecommendationOpen: frame.options.onRecommendationOpen,
        useCaseCatalog: frame.useCaseCatalog,
        catalogState: frame.catalogState,
        onCatalogReload: frame.reloadCatalog,
        layoutState: frame.layoutState,
        onLayoutRetry: frame.retryLayout,
      }),
  },
  server: {
    key: "server",
    provides: "target",
    requires: [],
    summary(config, frame) {
      const homelab = frame?.options.homelab;
      if (homelab?.selected)
        return (
          homelab.servers.find((server) => server.id === homelab.selected)
            ?.name ?? ""
        );
      const server = config.serverProvisioning;
      if (server.mode === "connect-remote" && server.remote.host.trim())
        return server.remote.host.trim();
      const key = SERVER_SUMMARY[server.mode];
      return key ? tr(key) : "";
    },
    Component: ServerStep,
    props: (frame) =>
      withBoundConfig(frame, {
        onselectionchange: (ready: boolean) => {
          frame.serverSelectionReady = ready;
        },
        nodeOptions: frame.options.nodeOptions,
        nodeAlternative: frame.options.nodeAlternative,
        showRole: frame.process.asksServerRole,
        // Offering existing servers is not joining one deployment: the step
        // stays the full choice of where the use cases run.
        lockFoundation: frame.process.inheritsAccess && !frame.options.homelab,
        joinSurface: frame.process.inheritsAccess && !frame.options.homelab,
        homelab: frame.options.homelab,
        onmanagedproviderselect: frame.options.onmanagedproviderselect,
        recommendedStackKit: frame.recommendedStackKit,
      }),
  },
  access: {
    key: "access",
    provides: "access",
    requires: ["target", "goals"],
    summary(config) {
      const mode = config.network.accessMode;
      return mode === "home" || mode === "anywhere"
        ? tr(`wizard.access.${mode}.label`)
        : "";
    },
    Component: AccessStep,
    props: (frame) => ({
      config: frame.config,
      accessQuestions: frame.accessQuestions,
      vpnQuestions: frame.vpnQuestions,
      onAccessModeSelect: frame.setAccessMode,
      onDomainBaseChange: (domain: string) => {
        frame.config.network.domainBase = domain;
      },
    }),
  },
  users: {
    key: "users",
    provides: "users",
    requires: ["owner"],
    summary(config) {
      const people = config.household?.people?.length ?? 0;
      if (people > 0)
        return trn("wizard.summary.users.people", people + 1, {
          count: people + 1,
        });
      return tr(
        config.audience.onlyMe
          ? "wizard.summary.users.solo"
          : "wizard.summary.users.shared",
      );
    },
    Component: UsersStep,
    props: (frame) => ({
      config: frame.config,
      audienceQuestions: frame.audienceQuestions,
      onAudienceChange: frame.setAudience,
      householdProfile: frame.config.household?.profile,
      peopleDrafts: frame.config.household?.people ?? [],
      onHouseholdProfileChange: frame.setHouseholdProfile,
      onPeopleDraftsChange: frame.setHouseholdPeople,
    }),
  },
  login: {
    key: "login",
    provides: "owner",
    requires: [],
    summary: (config) => config.owner.email.trim(),
    Component: LoginStep,
    props: (frame) => ({
      config: frame.config,
      owner: frame.owner,
      lane: frame.context.auth.lane,
    }),
  },
};
