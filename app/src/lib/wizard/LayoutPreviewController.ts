import {
  acceptableLayoutRecommendation,
  layoutErrorReasonCode,
  previewLayoutRecommendation,
  type LayoutRecommendationAcceptance,
  type LayoutRecommendationView,
  type LayoutSelection,
} from "#lib/api/layout.js";

/** Why no recommendation could be shown, in the owner's terms. */
export type LayoutPreviewFailure =
  /** The homelab has no validated first setup to extend yet. */
  | "noSetup"
  /** The selection cannot be distributed (yet). */
  | "invalid"
  /** The service, the network or the check itself is unavailable. */
  | "unavailable";

export type LayoutPreviewState =
  | { phase: "idle" }
  | { phase: "loading"; key: string; previous?: LayoutRecommendationView }
  | { phase: "resolved"; key: string; view: LayoutRecommendationView }
  | { phase: "failed"; key: string; reason: LayoutPreviewFailure };

export type LayoutPreviewLoader = (
  selection: LayoutSelection,
  options: { signal: AbortSignal },
) => Promise<LayoutRecommendationView>;

/** The view to render, including the previous one while a new one loads. */
export function layoutPreviewView(
  state: LayoutPreviewState,
): LayoutRecommendationView | undefined {
  if (state.phase === "resolved") return state.view;
  if (state.phase === "loading") return state.previous;
  return undefined;
}

/**
 * What the run request may carry. Only a resolved answer for the current
 * selection counts: while a changed selection is still being worked out, the
 * earlier answer belongs to a question nobody is asking any more.
 */
export function acceptedLayoutFromPreview(
  state: LayoutPreviewState,
  selection?: LayoutSelection | null,
): LayoutRecommendationAcceptance | undefined {
  if (state.phase !== "resolved") return undefined;
  // Until the controller has seen an edit, the resolved answer may still
  // belong to the previous selection.
  if (selection && state.key !== layoutSelectionKey(selection)) return undefined;
  return acceptableLayoutRecommendation(state.view);
}

export function layoutSelectionKey(selection: LayoutSelection): string {
  return JSON.stringify({
    ...selection,
    stack_id: selection.stack_id.trim(),
    goals: [...new Set(selection.goals.map((goal) => goal.trim()))]
      .filter(Boolean)
      .sort(),
  });
}

function failureOf(error: unknown): LayoutPreviewFailure {
  const status = (error as { status?: unknown } | null)?.status;
  if (status === 422) return "invalid";
  if (
    status === 409 &&
    layoutErrorReasonCode(error) === "layout_spec_unavailable"
  )
    return "noSetup";
  return "unavailable";
}

/**
 * Debounced, abortable preview of the recommended distribution for the
 * wizard's current selection. A response that belongs to an earlier selection
 * is dropped, so the panel and the acceptance never describe a question the
 * owner has already changed.
 */
export class LayoutPreviewController {
  readonly #load: LayoutPreviewLoader;
  readonly #onchange: (state: LayoutPreviewState) => void;
  readonly #debounceMs: number;
  #timer: ReturnType<typeof setTimeout> | null = null;
  #request: AbortController | null = null;
  #sequence = 0;
  #key = "";
  #current: LayoutRecommendationView | undefined;

  constructor(
    onchange: (state: LayoutPreviewState) => void,
    load: LayoutPreviewLoader = previewLayoutRecommendation,
    debounceMs = 600,
  ) {
    this.#onchange = onchange;
    this.#load = load;
    this.#debounceMs = debounceMs;
  }

  /** Ask about a selection; null (no goals, no deployment) clears the preview. */
  update(selection: LayoutSelection | null): void {
    if (!selection) {
      this.#reset();
      this.#onchange({ phase: "idle" });
      return;
    }
    const key = layoutSelectionKey(selection);
    if (key === this.#key) return;
    this.#key = key;
    this.#schedule(key, selection);
  }

  /** Ask the same question again after a failure. */
  retry(selection: LayoutSelection | null): void {
    this.#key = "";
    this.update(selection);
  }

  /**
   * Show a replacement the server handed back (a superseded recommendation)
   * as the answer to this selection, so the owner can confirm it.
   */
  adopt(selection: LayoutSelection, view: LayoutRecommendationView): void {
    const key = layoutSelectionKey(selection);
    this.#cancelPending();
    this.#sequence++;
    this.#key = key;
    this.#current = view;
    this.#onchange({ phase: "resolved", key, view });
  }

  destroy(): void {
    this.#reset();
  }

  #reset(): void {
    this.#sequence++;
    this.#cancelPending();
    this.#key = "";
    this.#current = undefined;
  }

  #schedule(key: string, selection: LayoutSelection): void {
    this.#cancelPending();
    const sequence = ++this.#sequence;
    this.#onchange({ phase: "loading", key, previous: this.#current });
    this.#timer = setTimeout(() => {
      this.#timer = null;
      const controller = new AbortController();
      this.#request = controller;
      void this.#load(selection, { signal: controller.signal })
        .then((view) => {
          if (controller.signal.aborted || sequence !== this.#sequence) return;
          this.#current = view;
          this.#onchange({ phase: "resolved", key, view });
        })
        .catch((error: unknown) => {
          if (controller.signal.aborted || sequence !== this.#sequence) return;
          this.#current = undefined;
          this.#onchange({ phase: "failed", key, reason: failureOf(error) });
        })
        .finally(() => {
          if (this.#request === controller) this.#request = null;
        });
    }, this.#debounceMs);
  }

  #cancelPending(): void {
    if (this.#timer) {
      clearTimeout(this.#timer);
      this.#timer = null;
    }
    this.#request?.abort();
    this.#request = null;
  }
}
