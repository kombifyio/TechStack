/**
 * Layout recommendations (POST /api/v1/unifier/layout/recommendations).
 *
 * The Unifier's Layout Planner answers one question: which of the owner's
 * servers should run which app. The answer is explained in reason codes, checked
 * against the owner's hardware by StackKits, and recorded as a Recommendation.
 * It is advice: the owner accepts it by finishing the wizard run that carries
 * it out (the run request names the record and its proposal digest).
 *
 * Types mirror the backend view (`internal/layoutadvice` View, reason codes in
 * `pkg/layout/explain.go`). Reason text from the server is English and is only
 * the fallback for a code this client has no message for.
 */
import { fetchApi } from "./client";

/** The surfaces that ask for a recommendation. */
export type LayoutFlow = "add-server" | "add-use-case";

/** A server whose size is known before it enrolls. */
export interface LayoutPlannedNode {
  name: string;
  cpu_cores: number;
  ram_gb: number;
  storage_gb: number;
  arch?: string;
  source: "guest-sizing" | "offering";
  roles?: string[];
  transport?: string;
}

/** The owner's question; the backend rejects unknown fields. */
export interface LayoutSelection {
  flow: LayoutFlow;
  stack_id: string;
  goals: string[];
  compute_profile?: string;
  use_case_settings?: Record<string, Record<string, string | boolean>>;
  use_case_capabilities?: Record<string, Record<string, string>>;
  planned_node?: LayoutPlannedNode;
}

export type LayoutOutcome =
  "feasible" | "proven-infeasible" | "unknown" | "budget-exhausted";

/** "open" when recorded, "unverified" when not recorded; the rest are record states. */
export type LayoutViewState =
  | "open"
  | "unverified"
  | "accepted"
  | "rejected"
  | "snoozed"
  | "expired"
  | "superseded"
  | "executing"
  | "done"
  | "failed";

export type LayoutBand = "important" | "standard" | "tolerant";

export interface LayoutReason {
  code: string;
  params?: Record<string, string>;
  /** Deterministic English rendering of the same fact; fallback only. */
  text: string;
}

export interface LayoutAvailability {
  class: "always-on" | "part-time" | "unknown";
  source: string;
  uptimeRatio?: number;
  confirmed?: boolean;
}

export interface LayoutResources {
  cpuCores: number;
  ramGB: number;
  storageGB: number;
}

export interface LayoutNodeView {
  id: string;
  spec_node_ref?: string;
  name: string;
  primary?: boolean;
  planned?: boolean;
  state: string;
  availability: LayoutAvailability;
  capacity: LayoutResources;
  reserved: LayoutResources;
  workloads: string[];
}

export interface LayoutAssignmentView {
  workload: string;
  title: string;
  goal: string;
  node_id: string;
  node_name: string;
  spec_node_ref?: string;
  band: LayoutBand;
  provenance: string;
  reasons: LayoutReason[];
}

export interface LayoutExistingView {
  workload: string;
  node_id: string;
}

export interface LayoutConflictView {
  workload: string;
  reasons: LayoutReason[];
}

export type LayoutVerificationStatus =
  "verified" | "infeasible" | "unknown" | "unavailable" | "skipped";

export interface LayoutVerification {
  status: LayoutVerificationStatus;
  outcome?: string;
  cli_version?: string;
  reasons?: LayoutReason[];
}

export interface LayoutRecommendationView {
  id?: string;
  api_version: string;
  state: LayoutViewState;
  scope: string;
  stack_id: string;
  flow: LayoutFlow;
  outcome: LayoutOutcome;
  proposal_digest?: string;
  snapshot_digest: string;
  material_digest?: string;
  expires_at?: string;
  superseded_by?: string;
  catalog_release: string;
  planner: { name: string; version: string };
  nodes: LayoutNodeView[];
  assignments: LayoutAssignmentView[];
  existing?: LayoutExistingView[];
  notes?: LayoutReason[];
  conflicts?: LayoutConflictView[];
  verification: LayoutVerification;
}

/** What a wizard run names to accept the shown recommendation. */
export interface LayoutRecommendationAcceptance {
  id: string;
  proposal_digest: string;
}

/**
 * The recommendation a run may accept: recorded, still open and verified
 * against the owner's hardware. Anything else is shown for information only,
 * so unverified advice is never accepted.
 */
export function acceptableLayoutRecommendation(
  view: LayoutRecommendationView | null | undefined,
): LayoutRecommendationAcceptance | undefined {
  if (!view?.id || !view.proposal_digest) return undefined;
  if (view.state !== "open" || view.verification?.status !== "verified")
    return undefined;
  return { id: view.id, proposal_digest: view.proposal_digest };
}

/**
 * Ask for a recommendation. A recorded one comes back with an id; the same
 * answer for advice that could not be verified comes back without one.
 */
export async function previewLayoutRecommendation(
  selection: LayoutSelection,
  options: { signal?: AbortSignal } = {},
): Promise<LayoutRecommendationView> {
  const res = await fetchApi<LayoutRecommendationView>(
    "/api/v1/unifier/layout/recommendations",
    {
      method: "POST",
      body: JSON.stringify(selection),
      signal: options.signal,
      // The backend projects the selection, plans and runs the StackKits
      // feasibility check before it answers.
      timeoutMs: 60_000,
    },
  );
  return res.data;
}

/** The reason code the backend attached to a refused layout or run request. */
export function layoutErrorReasonCode(error: unknown): string | undefined {
  const details = (error as { details?: unknown } | null)?.details;
  if (typeof details !== "object" || details === null) return undefined;
  const code = (details as { reason_code?: unknown }).reason_code;
  return typeof code === "string" ? code : undefined;
}

/** The replacement recommendation a superseded run carries, when present. */
export function layoutReplacementFromError(
  error: unknown,
): LayoutRecommendationView | undefined {
  const details = (error as { details?: unknown } | null)?.details;
  if (typeof details !== "object" || details === null) return undefined;
  const view = (details as { layout_recommendation?: unknown })
    .layout_recommendation;
  if (typeof view !== "object" || view === null) return undefined;
  const candidate = view as Partial<LayoutRecommendationView>;
  return Array.isArray(candidate.assignments) &&
    Array.isArray(candidate.nodes) &&
    typeof candidate.verification === "object"
    ? (candidate as LayoutRecommendationView)
    : undefined;
}
