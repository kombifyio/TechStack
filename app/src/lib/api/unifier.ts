/**
 * kombify-TechStack API - Unifier & StackKits
 *
 * API functions for interacting with the Unifier engine, StackKits, and Add-Ons.
 * The Unifier validates kombination specs, resolves defaults, and analyzes requirements.
 */

import { fetchApi } from "./client";

// ============================================================================
// StackKit & Add-On Types
// ============================================================================

/**
 * Information about a StackKit
 */
export interface StackKitModeInfo {
  description?: string;
  templateDir?: string;
  engine?: string;
  requires?: string[];
  recommendedFor?: string[];
}

export interface StackKitServicesInfo {
  required?: string[];
  recommended?: string[];
  available?: string[];
}

export interface StackKitInfo {
  name: string;
  displayName: string;
  version: string;
  description: string;
  author?: string;
  license?: string;
  tags: string[];
  deprecated: boolean;
  features?: string[];
  supportedOS?: string[];
  services?: StackKitServicesInfo;
  modes?: Record<string, StackKitModeInfo>;
}

/**
 * Information about an Add-On
 */
export interface AddonInfo {
  name: string;
  displayName: string;
  description: string;
  priority?: number;
}

// ============================================================================
// Validation Types
// ============================================================================

/**
 * Validation error from the Unifier
 */
export interface ValidationError {
  path: string;
  code: string;
  message: string;
}

/**
 * Result of spec validation
 */
export interface ValidationResult {
  valid: boolean;
  errors: ValidationError[];
}

export interface EnvironmentGap {
  code: string;
  message: string;
  severity?: string;
  blocking?: boolean;
  source?: string;
}

export interface DecisionGuidance {
  code: string;
  level?: string;
  message: string;
  surface?: string;
}

export interface DecisionContext {
  apiVersion?: string;
  kind?: string;
  intentRef?: {
    name?: string;
    stackId?: string;
    sourceFile?: string;
    sourceHash?: string;
  };
  channel?: string;
  source?: string;
  constraints?: Array<{
    key: string;
    value?: string;
    source?: string;
    required?: boolean;
  }>;
  environment?: {
    snapshotId?: string;
    computeNodes?: unknown[];
    accessEndpoints?: unknown[];
    assets?: unknown[];
    networkSignals?: unknown[];
    tlsSignals?: unknown[];
    reachability?: unknown[];
    preCheckResults?: unknown[];
    source?: string;
  };
  operator?: {
    score: number;
    band?: string;
    confidence?: number;
    source?: string;
    evidence?: Array<{ signal: string; weight?: number; source?: string }>;
    updatedAt?: string;
  };
}

export interface DecisionTrace {
  selectedStackKit?: string;
  stackKitProfile?: string;
  foundation?: string;
  addons?: string[];
  deploymentMode?: string;
  computeTier?: string;
  paas?: string;
  reasons?: Array<{ code: string; message: string; source?: string }>;
  blockingGaps?: EnvironmentGap[];
  guidance?: DecisionGuidance[];
  decisionContextHash?: string;
}

// ============================================================================
// Pipeline Types
// ============================================================================

/**
 * Pipeline stage information
 */
export interface PipelineStage {
  name: string;
  status:
    | "pending"
    | "running"
    | "success"
    | "completed"
    | "failed"
    | "skipped";
  duration_ms?: number;
  error?: string;
}

/**
 * Result of pipeline validation
 */
export interface PipelineValidationResult {
  valid: boolean;
  resolved_stackkit: string;
  detected_addons: string[];
  stages: PipelineStage[];
  errors?: ValidationError[];
}

/**
 * Result of pipeline preview
 */
export interface PipelinePreviewResult {
  valid: boolean;
  resolved_stackkit: string;
  detected_addons: string[];
  stages: PipelineStage[];
  config?: unknown;
  errors?: ValidationError[];
  warnings?: string[];
  requirements?: RequirementsSpec;
  environment_gaps?: EnvironmentGap[];
  guidance?: DecisionGuidance[];
  decision_context?: DecisionContext;
  decision_context_hash?: string;
  decision_trace?: DecisionTrace;
  decision_trace_hash?: string;
}

export class PipelinePreflightError extends Error {
  result: PipelinePreviewResult;

  constructor(result: PipelinePreviewResult) {
    super(formatPipelinePreflightError(result));
    this.name = "PipelinePreflightError";
    this.result = result;
  }
}

// ============================================================================
// Requirements Analysis Types (from Unifier)
// ============================================================================

/**
 * Hardware/node requirements for a StackKit
 */
export interface WorkerRequirements {
  minCloudServers: number;
  minLocalServers: number;
  minRAM: number;
  minCPU: number;
  specialRequirements?: string[];
}

/**
 * A credential that must be provided before provisioning
 */
export interface CredentialRequirement {
  key: string;
  label: string;
  description: string;
  required: boolean;
  type: string;
  helpUrl?: string;
}

/**
 * A pre-check that must pass before provisioning
 */
export interface PreCheckDefinition {
  type: string;
  description: string;
  minVersion?: string;
  blocking: boolean;
}

/**
 * Full requirements specification returned by the Unifier analyze endpoint.
 * This tells the frontend what the user needs to provide/configure before provisioning.
 */
export interface RequirementsSpec {
  apiVersion?: string;
  kind?: string;
  decisionContext?: DecisionContext;
  decisionContextHash?: string;
  decisionTrace?: DecisionTrace;
  decisionTraceHash?: string;
  stackKit: string;
  detectedAddons?: string[];
  requiredWorkers: WorkerRequirements;
  requiredCredentials?: CredentialRequirement[];
  requiredPreChecks?: PreCheckDefinition[];
  environmentGaps?: EnvironmentGap[];
  guidance?: DecisionGuidance[];
  appliedDefaults?: Record<string, unknown>;
  description: string;
  intentName?: string;
}

// ============================================================================
// StackKit & Add-On API Functions
// ============================================================================

/**
 * List all available StackKits
 */
export async function listStackKits(): Promise<StackKitInfo[]> {
  const res = await fetchApi<StackKitInfo[]>("/api/v1/stackkits");
  return res.data;
}

/**
 * List all available Add-Ons
 */
export async function listAddons(): Promise<AddonInfo[]> {
  const res = await fetchApi<{ addons: AddonInfo[]; count: number }>(
    "/api/v1/addons",
  );
  return res.data.addons;
}

/**
 * Get information about a specific StackKit
 */
export async function getStackKitInfo(name: string): Promise<StackKitInfo> {
  const res = await fetchApi<StackKitInfo>(
    `/api/v1/stackkits/${encodeURIComponent(name)}`,
  );
  return res.data;
}

// ============================================================================
// Unifier Validation API Functions
// ============================================================================

/**
 * Validate a kombination spec against the Unifier
 * @param spec - The kombination spec object (from wizard draft)
 * @param kitName - Optional StackKit name to validate against
 */
export async function validateSpec(
  spec: unknown,
  kitName?: string,
): Promise<ValidationResult> {
  const body: { spec: unknown; kit?: string } = { spec };
  if (kitName) {
    body.kit = kitName;
  }
  const res = await fetchApi<ValidationResult>("/api/v1/unifier/validate", {
    method: "POST",
    body: JSON.stringify(body),
  });
  return res.data;
}

/**
 * Unify/resolve a kombination spec (apply defaults, resolve references)
 * @param spec - The kombination spec object
 */
export async function unifySpec(spec: unknown): Promise<{ unified: unknown }> {
  const res = await fetchApi<{ unified: unknown }>("/api/v1/unifier/unify", {
    method: "POST",
    body: JSON.stringify({ spec }),
  });
  return res.data;
}

// ============================================================================
// Pipeline API Functions
// ============================================================================

function buildPipelineRequest(
  spec: unknown,
  decisionContext?: DecisionContext,
): { body: string; contentType: string } {
  if (decisionContext) {
    return {
      body: JSON.stringify({ spec, decision_context: decisionContext }),
      contentType: "application/json",
    };
  }
  return {
    body: typeof spec === "string" ? spec : JSON.stringify(spec),
    contentType:
      typeof spec === "string" ? "application/yaml" : "application/json",
  };
}

/**
 * Validate spec through the full pipeline (NEW)
 * Returns detailed stage information and auto-resolved StackKit
 * @param spec - The kombination spec (YAML string or object)
 */
export async function validatePipeline(
  spec: unknown,
): Promise<PipelineValidationResult> {
  // If spec is an object, convert to YAML-like JSON for the backend
  const body = typeof spec === "string" ? spec : JSON.stringify(spec);
  const contentType =
    typeof spec === "string" ? "application/yaml" : "application/json";

  const res = await fetchApi<PipelineValidationResult>(
    "/api/v1/unifier/pipeline/validate",
    {
      method: "POST",
      headers: {
        "Content-Type": contentType,
      },
      body,
    },
  );

  return res.data;
}

/**
 * Preview the generated config through the pipeline (NEW)
 * @param spec - The kombination spec (YAML string or object)
 */
export async function previewPipeline(
  spec: unknown,
  decisionContext?: DecisionContext,
): Promise<PipelinePreviewResult> {
  const { body, contentType } = buildPipelineRequest(spec, decisionContext);

  const res = await fetchApi<PipelinePreviewResult>(
    "/api/v1/unifier/pipeline/preview",
    {
      method: "POST",
      headers: {
        "Content-Type": contentType,
      },
      body,
      // The preview performs the full CUE/StackKit resolution pipeline. A
      // cold SaaS process can legitimately exceed the generic 10-second CRUD
      // timeout, but the Wizard must still remain bounded and actionable.
      timeoutMs: 60_000,
    },
  );

  return res.data;
}

/**
 * Run the side-effect-free wizard preflight against the preview endpoint.
 * Invalid backend validation or StackKit resolution results block stack
 * creation so the Wizard cannot silently continue with a different rollout
 * truth than the backend.
 */
export async function preflightPipeline(
  spec: unknown,
  decisionContext?: DecisionContext,
): Promise<PipelinePreviewResult> {
  const result = await previewPipeline(spec, decisionContext);
  if (!result.valid) {
    throw new PipelinePreflightError(result);
  }
  return result;
}

function formatPipelinePreflightError(result: PipelinePreviewResult): string {
  const messages = result.errors?.map((error) => error.message).filter(Boolean);
  if (messages?.length) {
    return `Pipeline preflight failed: ${messages.join(", ")}`;
  }
  return "Pipeline preflight failed. Please review the selected StackKit, server, and service settings.";
}

// ============================================================================
// Requirements Analysis API
// ============================================================================

/**
 * Analyze a kombination spec to determine requirements before provisioning.
 * Returns what workers, credentials, and pre-checks are needed.
 *
 * @param spec - The kombination spec (YAML string or object)
 * @returns RequirementsSpec with all information needed for the checklist UI
 */
export async function analyzeRequirements(
  spec: unknown,
): Promise<RequirementsSpec> {
  const body = typeof spec === "string" ? spec : JSON.stringify(spec);
  const contentType =
    typeof spec === "string" ? "application/yaml" : "application/json";

  const res = await fetchApi<RequirementsSpec>("/api/v1/unifier/analyze", {
    method: "POST",
    headers: {
      "Content-Type": contentType,
    },
    body,
  });

  return res.data;
}

// ============================================================================
// IaC Generation Types (Phase 6)
// ============================================================================

/**
 * A generated IaC file
 */
export interface GeneratedFile {
  name: string;
  content: string;
  size: number;
  language: "hcl" | "json" | "yaml";
}

/**
 * Result of IaC generation
 */
export interface IaCGenerationResult {
  success: boolean;
  mode: "simple" | "advanced";
  outputDir?: string;
  steps: PipelineStage[];
  warnings?: string[];
  failedStep?: string;
  errorMessage?: string;
  generatedFiles?: GeneratedFile[];
  unifiedSpec?: unknown;
}

/**
 * Result of IaC preview (without writing to disk)
 */
export interface IaCPreviewResult {
  success: boolean;
  mode: "simple" | "advanced";
  steps: PipelineStage[];
  warnings?: string[];
  outputDir?: string;
  files?: GeneratedFile[];
  stackKit?: string;
  addons?: string[];
}

// ============================================================================
// IaC Generation API Functions (Phase 6)
// ============================================================================

/**
 * Generate IaC files from a kombination spec.
 * Runs the complete extended pipeline including Phase 6 (IaC generation).
 *
 * @param spec - The kombination spec (YAML string or object)
 * @param mode - Deployment mode: "simple" (OpenTofu only) or "advanced" (Terramate)
 * @returns IaCGenerationResult with generated files and pipeline status
 */
export async function generateIaC(
  spec: unknown,
  mode: "simple" | "advanced" = "simple",
): Promise<IaCGenerationResult> {
  const body = typeof spec === "string" ? spec : JSON.stringify(spec);
  const contentType =
    typeof spec === "string" ? "application/yaml" : "application/json";

  const response = await fetch(`/api/v1/unifier/iac?mode=${mode}`, {
    method: "POST",
    credentials: "include",
    headers: {
      "Content-Type": contentType,
    },
    body,
  });

  if (!response.ok) {
    const error = await response.json();
    throw new Error(error.error || `HTTP ${response.status}`);
  }

  return response.json();
}

/**
 * Preview IaC files without writing to disk.
 * Useful for showing users what will be generated before deployment.
 *
 * @param spec - The kombination spec (YAML string or object)
 * @param mode - Deployment mode: "simple" or "advanced"
 * @returns IaCPreviewResult with file content previews
 */
export async function previewIaC(
  spec: unknown,
  mode: "simple" | "advanced" = "simple",
): Promise<IaCPreviewResult> {
  const body = typeof spec === "string" ? spec : JSON.stringify(spec);
  const contentType =
    typeof spec === "string" ? "application/yaml" : "application/json";

  const response = await fetch(`/api/v1/unifier/iac/preview?mode=${mode}`, {
    method: "POST",
    credentials: "include",
    headers: {
      "Content-Type": contentType,
    },
    body,
  });

  if (!response.ok) {
    const error = await response.json();
    throw new Error(error.error || `HTTP ${response.status}`);
  }

  return response.json();
}
