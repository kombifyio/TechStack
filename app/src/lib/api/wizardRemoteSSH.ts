import { fetchApi, ApiRequestError } from "./client";

export interface WizardRemoteSSHTestRequest {
  host: string;
  port?: number;
  user: string;
  auth_method?: "ssh-key" | "password";
  password?: string;
  ssh_key_label?: string;
  /** Hosted: the Wallet item with the SSH credential; replaces label and password. */
  ssh_wallet_item_id?: string;
  private_key?: string;
}

export interface WizardRemoteSSHTestResult {
  success: boolean;
  error?: string;
  message?: string;
}

export async function testWizardRemoteSSH(
  request: WizardRemoteSSHTestRequest,
): Promise<WizardRemoteSSHTestResult> {
  try {
    const res = await fetchApi<WizardRemoteSSHTestResult>(
      "/api/v1/wizard/remote/test-ssh",
      {
        method: "POST",
        body: JSON.stringify(request),
        timeoutMs: 30_000,
      },
    );
    return res.data;
  } catch (error) {
    if (error instanceof ApiRequestError) {
      throw new Error(error.message);
    }
    throw error;
  }
}
