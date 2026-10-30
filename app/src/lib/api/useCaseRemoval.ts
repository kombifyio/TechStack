import { fetchApi } from "./client";

export type UseCaseRemovalSelection =
  | { selector_kind: "workload"; selector_ref: string }
  | { selector_kind: "use_case"; selector_ref: string };
export type UseCaseRemovalRequest = UseCaseRemovalSelection & {
  expected_spec_hash: string;
};
export interface UseCaseRemovalAccepted {
  kit_deployment_id: string;
  run_id: string;
  job_id: string;
  job_ids: string[];
  state: "provisioning";
  operation: "use_case_remove";
  selector_kind: UseCaseRemovalSelection["selector_kind"];
  selector_ref: string;
  removed_workload_refs: string[];
  previous_spec_hash: string;
  spec_hash: string;
  data_disposition: "retain";
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function isNonEmptyStrings(value: unknown): value is string[] {
  return (
    Array.isArray(value) &&
    value.length > 0 &&
    value.every((item: unknown) => typeof item === "string" && item.length > 0)
  );
}

export async function removeInstalledUseCase(options: {
  stackId: string;
  request: UseCaseRemovalRequest;
  expectedWorkloadRefs: string[];
  idempotencyKey: string;
}): Promise<UseCaseRemovalAccepted> {
  const response = await fetchApi<unknown>(
    `/api/v1/stacks/${encodeURIComponent(options.stackId)}/use-cases/remove`,
    {
      method: "POST",
      headers: { "Idempotency-Key": options.idempotencyKey },
      body: JSON.stringify(options.request),
      timeoutMs: 60_000,
    },
  );
  const accepted = response.data;
  if (
    !isRecord(accepted) ||
    accepted.kit_deployment_id !== options.stackId ||
    accepted.state !== "provisioning" ||
    accepted.operation !== "use_case_remove" ||
    accepted.selector_kind !== options.request.selector_kind ||
    accepted.selector_ref !== options.request.selector_ref ||
    typeof accepted.run_id !== "string" ||
    !accepted.run_id ||
    !isNonEmptyStrings(accepted.job_ids) ||
    accepted.job_id !== accepted.job_ids[0] ||
    !isNonEmptyStrings(accepted.removed_workload_refs) ||
    accepted.data_disposition !== "retain" ||
    accepted.previous_spec_hash !== options.request.expected_spec_hash ||
    typeof accepted.spec_hash !== "string" ||
    !/^sha256:[a-f0-9]{64}$/.test(accepted.spec_hash) ||
    accepted.spec_hash === accepted.previous_spec_hash ||
    JSON.stringify([...accepted.removed_workload_refs].sort()) !==
      JSON.stringify([...options.expectedWorkloadRefs].sort())
  ) {
    throw new Error(
      "The removal acceptance does not match this StackKit and its selected applications. Retry the same attempt.",
    );
  }
  return {
    kit_deployment_id: options.stackId,
    run_id: accepted.run_id,
    job_id: accepted.job_ids[0],
    job_ids: accepted.job_ids,
    state: "provisioning",
    operation: "use_case_remove",
    selector_kind: options.request.selector_kind,
    selector_ref: options.request.selector_ref,
    removed_workload_refs: accepted.removed_workload_refs,
    previous_spec_hash: options.request.expected_spec_hash,
    spec_hash: accepted.spec_hash,
    data_disposition: "retain",
  };
}
