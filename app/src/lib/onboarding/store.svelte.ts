/**
 * Getting started (phase two) as one reactive store.
 *
 * The server decides state and availability; the core resolves what the user
 * sees. This store is only the seam: load once, send an action, take the
 * server's answer. It deliberately has no optimistic layer and no cache —
 * both were here, and both existed to hide a round trip that nobody had
 * measured as slow. A checklist that briefly shows a tick the server then
 * refuses is worse than one that ticks a moment later.
 *
 * It knows nothing about the introduction (phase one). That is the point.
 */
import {
  isJourneyVisible,
  navBadge,
  resolveJourney,
  type OnboardingJourneyDescriptor,
  type OnboardingStateRecord,
  type ResolvedJourney,
  type StepAvailability,
} from "@kombiverselabs/onboarding-core";

import {
  getOnboarding,
  isOnboardingConflict,
  putOnboardingAction,
  resetOnboarding,
  TECHSTACK_JOURNEY_ID,
  type OnboardingAction,
  type OnboardingPayload,
} from "./api.js";
import { trackOnboardingJourney, trackOnboardingStep } from "./analytics.js";

class OnboardingStore {
  descriptor = $state<OnboardingJourneyDescriptor | null>(null);
  record = $state<OnboardingStateRecord | null>(null);
  availability = $state<Readonly<Record<string, StepAvailability>>>({});
  revision = $state(0);
  loading = $state(false);
  /** Set when the journey could not be loaded. Surfaces stay hidden. */
  error = $state<string | null>(null);

  private loadedFor: string | null = null;
  private busy = false;

  /**
   * The resolved journey, or null while nothing has loaded. Every surface
   * reads this — none of them recompute progress or ordering themselves.
   */
  readonly resolved = $derived.by<ResolvedJourney | null>(() => {
    if (!this.descriptor || !this.record) return null;
    return resolveJourney(this.descriptor, this.record, this.availability);
  });

  /** Open actionable steps, or null when the nav should show no badge. */
  readonly badge = $derived.by<number | null>(() =>
    this.resolved ? navBadge(this.resolved) : null,
  );

  readonly visible = $derived.by<boolean>(() =>
    this.resolved ? isJourneyVisible(this.resolved) : false,
  );

  /** Load once per signed-in identity. */
  async load(identity: string): Promise<void> {
    if (this.loadedFor === identity && this.descriptor) return;
    this.loadedFor = identity;
    this.loading = true;
    this.error = null;
    try {
      this.adopt(await getOnboarding(TECHSTACK_JOURNEY_ID));
    } catch (err) {
      // A failed load is not a reason to show a broken checklist; the surfaces
      // read `visible`, which stays false without a descriptor.
      this.error = err instanceof Error ? err.message : String(err);
    } finally {
      this.loading = false;
    }
  }

  async complete(stepId: string): Promise<void> {
    await this.send("complete", stepId);
  }

  /** A user's own tick. Travels as `manual` and unlocks nothing (§3). */
  async skip(stepId: string): Promise<void> {
    await this.send("skip", stepId);
  }

  async dismiss(): Promise<void> {
    trackOnboardingJourney("dismissed", this.resolved);
    await this.send("dismiss");
  }

  async resume(): Promise<void> {
    trackOnboardingJourney("resumed", this.resolved);
    await this.send("resume");
  }

  async reset(): Promise<void> {
    trackOnboardingJourney("reset", this.resolved);
    if (this.busy) return;
    this.busy = true;
    try {
      this.adopt(await resetOnboarding(TECHSTACK_JOURNEY_ID));
    } catch (err) {
      await this.reloadAfter(err);
    } finally {
      this.busy = false;
    }
  }

  /** Drops everything on sign-out. Progress is per user (§8). */
  clear(): void {
    this.descriptor = null;
    this.record = null;
    this.availability = {};
    this.revision = 0;
    this.error = null;
    this.loadedFor = null;
  }

  private async send(
    action: Exclude<OnboardingAction, "reset" | "coach_seen">,
    stepId?: string,
  ): Promise<void> {
    if (this.busy || !this.descriptor) return;
    this.busy = true;
    try {
      const payload = await putOnboardingAction(action, {
        stepId,
        source: action === "skip" ? "manual" : undefined,
        expectRevision: this.revision,
      });
      this.adopt(payload);
      if (stepId) trackOnboardingStep(action, stepId, this.resolved);
    } catch (err) {
      await this.reloadAfter(err);
    } finally {
      this.busy = false;
    }
  }

  /**
   * A lost compare-and-swap means somebody else moved the record on; anything
   * else means we do not know what the server holds. Both answers are the
   * same: ask it.
   */
  private async reloadAfter(err: unknown): Promise<void> {
    if (!isOnboardingConflict(err) && err instanceof Error) {
      this.error = err.message;
    }
    try {
      this.adopt(await getOnboarding(TECHSTACK_JOURNEY_ID));
      this.error = null;
    } catch {
      // Leave the last good render in place rather than blanking the rail.
    }
  }

  private adopt(payload: OnboardingPayload): void {
    this.descriptor = payload.journey;
    this.record = payload.state;
    this.availability = payload.availability ?? {};
    this.revision = payload.revision ?? payload.state?.revision ?? 0;
  }
}

export const onboardingStore = new OnboardingStore();
