import {
  recommendWizard,
  type WizardRecommendationRequest,
  type WizardRecommendationResult,
} from "#lib/api/unifier.js";

export type WizardPreviewState =
  | { phase: "idle" }
  | { phase: "loading"; previous?: WizardRecommendationResult }
  | { phase: "resolved"; result: WizardRecommendationResult }
  | {
      phase: "degraded";
      previous?: WizardRecommendationResult;
      message: string;
    };

export type WizardRecommendationLoader = (
  request: WizardRecommendationRequest,
  signal: AbortSignal,
) => Promise<WizardRecommendationResult>;

export class WizardPreviewController {
  readonly #load: WizardRecommendationLoader;
  readonly #onchange: (state: WizardPreviewState) => void;
  readonly #debounceMs: number;
  readonly #cache = new Map<string, WizardRecommendationResult>();
  #timer: ReturnType<typeof setTimeout> | null = null;
  #request: AbortController | null = null;
  #sequence = 0;
  #current: WizardRecommendationResult | undefined;

  constructor(
    onchange: (state: WizardPreviewState) => void,
    load: WizardRecommendationLoader = recommendWizard,
    debounceMs = 250,
  ) {
    this.#onchange = onchange;
    this.#load = load;
    this.#debounceMs = debounceMs;
  }

  update(request: WizardRecommendationRequest): void {
    const normalized = normalizeRequest(request);
    const key = JSON.stringify(normalized);
    const cached = this.#cache.get(key);
    if (cached) {
      this.#current = cached;
      this.#onchange({ phase: "resolved", result: cached });
      return;
    }

    this.cancelPending();
    const sequence = ++this.#sequence;
    this.#onchange({ phase: "loading", previous: this.#current });
    this.#timer = setTimeout(() => {
      this.#timer = null;
      const controller = new AbortController();
      this.#request = controller;
      void this.#load(normalized, controller.signal)
        .then((result) => {
          if (controller.signal.aborted || sequence !== this.#sequence) return;
          this.#cache.set(key, result);
          this.#current = result;
          this.#onchange({ phase: "resolved", result });
        })
        .catch((error: unknown) => {
          if (controller.signal.aborted || sequence !== this.#sequence) return;
          this.#onchange({
            phase: "degraded",
            previous: this.#current,
            message:
              error instanceof Error
                ? error.message
                : "Recommendation preview is temporarily unavailable.",
          });
        })
        .finally(() => {
          if (this.#request === controller) this.#request = null;
        });
    }, this.#debounceMs);
  }

  destroy(): void {
    this.#sequence++;
    this.cancelPending();
    this.#cache.clear();
    this.#current = undefined;
  }

  private cancelPending(): void {
    if (this.#timer) {
      clearTimeout(this.#timer);
      this.#timer = null;
    }
    this.#request?.abort();
    this.#request = null;
  }
}

function normalizeRequest(
  request: WizardRecommendationRequest,
): WizardRecommendationRequest {
  const goals = normalizeList(request.goals);
  const services = normalizeList(request.services);
  const provider = request.provider_id?.trim();
  return {
    ...(request.smart_home_context
      ? { smart_home_context: request.smart_home_context }
      : {}),
    ...(request.smart_home_settings
      ? { smart_home_settings: request.smart_home_settings }
      : {}),
    goals,
    services,
    deployment_lane: request.deployment_lane,
    ...(provider ? { provider_id: provider } : {}),
    surface: request.surface,
  };
}

function normalizeList(values: string[]): string[] {
  return [
    ...new Set(values.map((value) => value.trim()).filter(Boolean)),
  ].sort();
}
