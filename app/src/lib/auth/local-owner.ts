import { fetchApi } from "$lib/api/client";

interface CreateLocalOwnerAccountInput {
  email: string;
  password: string;
  passwordConfirm: string;
  isFirstRun: boolean;
}

function deriveLocalOwnerName(email: string): string {
  return email.split("@")[0]?.trim() || email;
}

export async function createLocalOwnerAccount({
  email,
  password,
  passwordConfirm,
  isFirstRun,
}: CreateLocalOwnerAccountInput): Promise<string> {
  const normalizedEmail = email.trim();
  const derivedOwnerName = deriveLocalOwnerName(normalizedEmail);

  if (isFirstRun) {
    await fetchApi("/api/v1/auth/setup", {
      method: "POST",
      body: JSON.stringify({
        mode: "local",
        name: derivedOwnerName,
        email: normalizedEmail,
        password,
      }),
    });

    return normalizedEmail;
  }

  void password;
  void passwordConfirm;
  void derivedOwnerName;
  throw Object.assign(
    new Error("Local owner creation is only available during first-run setup."),
    { status: 409 },
  );
}
