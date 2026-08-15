// Auto-Install API client
import { get, post } from "./client";

export interface MissingDependency {
  name: string;
  version: string;
  executable_name: string;
  required: boolean;
  reason: string;
}

export interface InstallResult {
  dependency: {
    name: string;
    version: string;
    executable_name: string;
  };
  success: boolean;
  version?: string;
  installed_path?: string;
  strategy: string;
  error?: string;
  message?: string;
}

export interface AutoInstallStatus {
  missing: MissingDependency[];
  count: number;
}

export interface InstallRequest {
  dependencies?: string[];
  strategy?: "auto" | "package_manager" | "download";
}

export interface InstallResponse {
  message: string;
  results: InstallResult[];
  failed?: InstallResult[];
}

/**
 * Get auto-install status (missing dependencies).
 */
export async function getAutoInstallStatus(): Promise<AutoInstallStatus> {
  return get<AutoInstallStatus>("/api/v1/autoinstall/status");
}

/**
 * Install missing dependencies.
 */
export async function installDependencies(
  request?: InstallRequest,
): Promise<InstallResponse> {
  return post<InstallResponse>("/api/v1/autoinstall/install", request || {});
}
