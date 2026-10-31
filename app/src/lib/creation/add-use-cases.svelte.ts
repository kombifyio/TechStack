/**
 * Submitting more use cases to an existing StackKit deployment: the extend run,
 * what a refusal means for the person, and the state a page shows afterwards.
 * The run accepts the recommended distribution that was shown; if the homelab
 * changed since, nothing is changed and the updated recommendation is shown to
 * be confirmed again.
 */
import { parseApiError } from "#lib/api/errors.js";
import type { WizardRunResponse } from "#lib/api/wizardRuns.js";
import {
  layoutErrorReasonCode,
  layoutReplacementFromError,
  type LayoutRecommendationAcceptance,
  type LayoutRecommendationView,
} from "#lib/api/layout.js";
import { WizardInputError, type StackConfig } from "#lib/wizard/index.js";
import { tr } from "#lib/i18n.svelte.js";
import { CREATION_PROCESSES } from "./core/index";
import { emitCreationEvent } from "./host";
import { creationScreenHref, submitCreationRun } from "./submit";

export interface AddUseCasesNotice {
  tone: "warning" | "error" | "info";
  title: string;
  body: string;
  details?: string;
}

export class AddUseCasesRun {
  readonly process = CREATION_PROCESSES["add-use-case"];
  submitting = $state(false);
  result = $state<WizardRunResponse | null>(null);
  notice = $state<AddUseCasesNotice | null>(null);
  /** A replacement a superseded run handed back; shown to be confirmed again. */
  replacement = $state<LayoutRecommendationView | undefined>();
  /** Changes when the shown recommendation must be asked for again. */
  refresh = $state(0);

  async submit(
    target: { stackId: string; stackName: string },
    config: StackConfig,
    layoutRecommendation?: LayoutRecommendationAcceptance,
  ): Promise<void> {
    this.submitting = true;
    this.notice = null;
    try {
      const { run } = await submitCreationRun(this.process, config, {
        ...target,
        layoutRecommendation,
      });
      this.result = run;
      // Saved without a rollout: nothing further will report to the host.
      if (run.state === "accepted")
        emitCreationEvent({
          event: "completed",
          runId: run.run_id,
          stackId: target.stackId,
          outcome: "accepted",
        });
    } catch (err) {
      this.notice = this.#noticeFor(err);
    } finally {
      this.submitting = false;
    }
  }

  /** The Creation Screen for a run that follows one provisioning job. */
  progressHref(stackId: string, name: string): string {
    const result = this.result;
    // The inline group view follows every incremental node operation.
    if (!result?.job_id || result.job_ids?.length) return "";
    return creationScreenHref(
      this.process,
      { ...result, kit_deployment_id: stackId },
      result.name || name,
    );
  }

  #noticeFor(err: unknown): AddUseCasesNotice {
    if (err instanceof WizardInputError) {
      return {
        tone: "warning",
        title: tr("layout.addUseCases.failed.title"),
        body: err.message,
      };
    }
    switch (layoutErrorReasonCode(err)) {
      case "layout_recommendation_superseded":
        this.replacement = layoutReplacementFromError(err);
        // Without a replacement in the answer, ask for the recommendation again.
        if (!this.replacement) this.refresh += 1;
        return {
          tone: "warning",
          title: tr("layout.addUseCases.superseded.title"),
          body: tr("layout.addUseCases.superseded.body"),
        };
      case "layout_recommendation_stale":
      case "layout_recommendation_unverifiable":
        this.replacement = undefined;
        this.refresh += 1;
        return {
          tone: "warning",
          title: tr("layout.addUseCases.stale.title"),
          body: tr("layout.addUseCases.stale.body"),
        };
      case "layout_recommendation_unavailable":
      case "layout_recommendations_unavailable":
        return {
          tone: "info",
          title: tr("layout.addUseCases.unavailable.title"),
          body: tr("layout.addUseCases.unavailable.body"),
        };
      default:
        return {
          tone: "error",
          title: tr("layout.addUseCases.failed.title"),
          body: tr("layout.addUseCases.failed.body"),
          details: parseApiError(err).message,
        };
    }
  }
}
