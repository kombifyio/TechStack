/** Installed, catalog-bound configuration. The backend owns native authoring and dispatch. */
import { fetchApi } from "./client";

export type PublicSettingValue = string | number | boolean;
export interface ConfigurationProfile {
  current?: string;
  options: string[];
}
export interface PublicConfigurationSetting {
  key: string;
  type: "string" | "number" | "boolean";
  label: string;
  current?: PublicSettingValue;
  options?: PublicSettingValue[];
}
export interface ExistingSecretReference {
  key: string;
  current?: string;
  options: string[];
}
export interface InstalledWorkloadConfiguration {
  workload_ref: string;
  alternative: string;
  module_ref: string;
  profiles: {
    compute: ConfigurationProfile;
    storage: ConfigurationProfile;
    accelerator: ConfigurationProfile;
  };
  settings: PublicConfigurationSetting[];
  secret_refs: ExistingSecretReference[];
  eligible: boolean;
  removal_eligible?: boolean;
  reason_code?: string;
}
export interface InstalledUseCaseConfiguration {
  id: string;
  name: string;
  workloads: InstalledWorkloadConfiguration[];
}
export interface UseCaseConfigurationView {
  retained_removal_available?: boolean;
  kit_deployment_id: string;
  expected_spec_hash: string;
  release_version: string;
  use_cases: InstalledUseCaseConfiguration[];
}
export interface ConfigureInstalledWorkloadRequest {
  expected_spec_hash: string;
  compute_profile?: string;
  storage_profile?: string;
  accelerator_profile?: string;
  settings?: Record<string, PublicSettingValue>;
  secret_refs?: Record<string, string>;
}
export interface ConfigureInstalledWorkloadAccepted {
  kit_deployment_id: string;
  run_id: string;
  previous_spec_hash: string;
  spec_hash: string;
  job_id: string;
  job_ids: string[];
  state: "provisioning";
}

export async function getUseCaseConfiguration(
  stackId: string,
  signal?: AbortSignal,
): Promise<UseCaseConfigurationView> {
  const response = await fetchApi<UseCaseConfigurationView>(
    `/api/v1/stacks/${encodeURIComponent(stackId)}/use-cases/configuration`,
    { signal },
  );
  if (
    response.data.kit_deployment_id !== stackId ||
    !Array.isArray(response.data.use_cases) ||
    (!response.data.expected_spec_hash && response.data.use_cases.some((entry) =>
      entry.workloads.some((workload) => workload.eligible),
    ))
  ) {
    throw new Error("The configuration response does not match this StackKit.");
  }
  return response.data;
}

export async function configureInstalledWorkload(
  stackId: string,
  workloadRef: string,
  request: ConfigureInstalledWorkloadRequest,
  idempotencyKey: string,
): Promise<ConfigureInstalledWorkloadAccepted> {
  const response = await fetchApi<ConfigureInstalledWorkloadAccepted>(
    `/api/v1/stacks/${encodeURIComponent(stackId)}/use-cases/${encodeURIComponent(workloadRef)}/configure`,
    {
      method: "POST",
      headers: { "Idempotency-Key": idempotencyKey },
      body: JSON.stringify(request),
      timeoutMs: 60_000,
    },
  );
  const accepted = response.data;
  if (
    accepted.kit_deployment_id !== stackId ||
    accepted.state !== "provisioning" ||
    !accepted.run_id ||
    accepted.previous_spec_hash !== request.expected_spec_hash ||
    typeof accepted.spec_hash !== "string" ||
    !accepted.spec_hash ||
    !Array.isArray(accepted.job_ids) ||
    !accepted.job_ids.length ||
    accepted.job_ids.some((id) => typeof id !== "string" || !id)
  ) {
    throw new Error(
      "The accepted operation did not return matching StackKit jobs. Retry the same attempt.",
    );
  }
  return accepted;
}
