// Journeys that target Early Access reach it the way a person does: through the
// Stable address with the opt-in cookie `kombify_release_channel=early-access`.
// Without the cookie the edge serves Stable.
export function earlyAccessUse():
  | Record<string, never>
  | {
      storageState: {
        cookies: Array<{
          name: string;
          value: string;
          domain: string;
          path: string;
          expires: number;
          httpOnly: boolean;
          secure: boolean;
          sameSite: "Lax";
        }>;
        origins: [];
      };
    } {
  const selected =
    process.env.TECHSTACK_E2E_EXPECTED_CHANNEL?.trim() === "early-access" ||
    process.env.TECHSTACK_RUNTIME_E2E_EXISTING_STACK_ID !== undefined;
  if (!selected) return {};
  return {
    storageState: {
      cookies: [
        {
          name: "kombify_release_channel",
          value: "early-access",
          domain: "techstack.kombify.io",
          path: "/",
          expires: -1,
          httpOnly: false,
          secure: true,
          sameSite: "Lax",
        },
      ],
      origins: [],
    },
  };
}
