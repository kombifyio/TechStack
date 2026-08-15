export type TechStackTestUserRole =
  | "admin"
  | "superuser"
  | "developer"
  | "oneliner"
  | "remote"
  | "cloud";

export interface TechStackTestUser {
  email: string;
  password: string;
}

export interface TechStackTestUserReadiness {
  emailConfigured: boolean;
  passwordConfigured: boolean;
  missingSecrets: string[];
}

type EnvSource = Record<string, string | undefined>;

const LOOPBACK_HOSTS = new Set(["localhost", "127.0.0.1", "::1"]);

const LOOPBACK_TARGET_ENV_KEYS = [
  "PLAYWRIGHT_BASE_URL",
  "TECHSTACK_API_URL",
  "API_URL",
  "TECHSTACK_URL",
  "TECHSTACK_APP_URL",
];

const LOCAL_BOOTSTRAP_FALLBACKS: Partial<
  Record<
    TechStackTestUserRole,
    {
      emailEnv: string;
      passwordEnv: string;
      defaultEmail: string;
      defaultPassword: string;
    }
  >
> = {
  admin: {
    emailEnv: "TECHSTACK_ADMIN_EMAIL",
    passwordEnv: "TECHSTACK_ADMIN_PASSWORD",
    defaultEmail: "admin@techstack.local",
    defaultPassword: "dev-admin-password-change-me",
  },
  superuser: {
    emailEnv: "TECHSTACK_SUPERUSER_EMAIL",
    passwordEnv: "TECHSTACK_SUPERUSER_PASSWORD",
    defaultEmail: "superuser@techstack.local",
    defaultPassword: "dev-superuser-password-change-me",
  },
  developer: {
    emailEnv: "TECHSTACK_DEVELOPER_EMAIL",
    passwordEnv: "TECHSTACK_DEVELOPER_PASSWORD",
    defaultEmail: "developer@techstack.local",
    defaultPassword: "dev-developer-password-change-me",
  },
};

const ROLE_SECRET_CANDIDATES: Record<
  TechStackTestUserRole,
  { email: string; password: string }[]
> = {
  admin: [
    {
      email: "TEST_PRO_PLUS_USER_EMAIL",
      password: "TEST_PRO_PLUS_USER_PASSWORD",
    },
    { email: "TEST_ADMIN_EMAIL", password: "TEST_ADMIN_PASSWORD" },
    {
      email: "TECHSTACK_E2E_ADMIN_EMAIL",
      password: "TECHSTACK_E2E_ADMIN_PASSWORD",
    },
    { email: "TECHSTACK_ADMIN_EMAIL", password: "TECHSTACK_ADMIN_PASSWORD" },
  ],
  superuser: [
    {
      email: "TEST_GLOBAL_ADMIN_EMAIL",
      password: "TEST_GLOBAL_ADMIN_PASSWORD",
    },
    { email: "TEST_ADMIN_EMAIL", password: "TEST_ADMIN_PASSWORD" },
    {
      email: "TECHSTACK_E2E_SUPERUSER_EMAIL",
      password: "TECHSTACK_E2E_SUPERUSER_PASSWORD",
    },
    {
      email: "TECHSTACK_SUPERUSER_EMAIL",
      password: "TECHSTACK_SUPERUSER_PASSWORD",
    },
  ],
  developer: [
    {
      email: "TEST_DEVELOPER_EMAIL",
      password: "TEST_DEVELOPER_PASSWORD",
    },
    {
      email: "TEST_PRO_PLUS_USER_EMAIL",
      password: "TEST_PRO_PLUS_USER_PASSWORD",
    },
    {
      email: "TECHSTACK_E2E_DEVELOPER_EMAIL",
      password: "TECHSTACK_E2E_DEVELOPER_PASSWORD",
    },
    {
      email: "TECHSTACK_DEVELOPER_EMAIL",
      password: "TECHSTACK_DEVELOPER_PASSWORD",
    },
  ],
  oneliner: [
    {
      email: "TECHSTACK_E2E_ONELINER_EMAIL",
      password: "TECHSTACK_E2E_ONELINER_PASSWORD",
    },
  ],
  remote: [
    {
      email: "TECHSTACK_E2E_REMOTE_EMAIL",
      password: "TECHSTACK_E2E_REMOTE_PASSWORD",
    },
  ],
  cloud: [
    {
      email: "TECHSTACK_E2E_CLOUD_EMAIL",
      password: "TECHSTACK_E2E_CLOUD_PASSWORD",
    },
  ],
};

function readSecret(env: EnvSource, name: string): string {
  return env[name]?.trim() ?? "";
}

function isLoopbackTarget(env: EnvSource): boolean {
  return LOOPBACK_TARGET_ENV_KEYS.some((key) => {
    const raw = env[key]?.trim();
    if (!raw) return false;

    try {
      return LOOPBACK_HOSTS.has(new URL(raw).hostname.toLowerCase());
    } catch {
      return false;
    }
  });
}

function getLocalBootstrapUser(
  role: TechStackTestUserRole,
  env: EnvSource,
): TechStackTestUser | null {
  if (!isLoopbackTarget(env)) {
    return null;
  }

  const fallback = LOCAL_BOOTSTRAP_FALLBACKS[role];
  if (!fallback) {
    return null;
  }
  return {
    email: readSecret(env, fallback.emailEnv) || fallback.defaultEmail,
    password: readSecret(env, fallback.passwordEnv) || fallback.defaultPassword,
  };
}

function describeCandidates(
  role: TechStackTestUserRole,
  field: "email" | "password",
): string {
  return ROLE_SECRET_CANDIDATES[role]
    .map((candidate) => candidate[field])
    .join(" or ");
}

export function getTechStackTestUser(
  role: TechStackTestUserRole = "admin",
  env: EnvSource = process.env,
): TechStackTestUser {
  for (const candidate of ROLE_SECRET_CANDIDATES[role]) {
    const email = readSecret(env, candidate.email);
    const password = readSecret(env, candidate.password);
    if (email && password) return { email, password };
  }

  const localBootstrapUser = getLocalBootstrapUser(role, env);
  if (localBootstrapUser) {
    return localBootstrapUser;
  }

  return { email: "", password: "" };
}

export function buildTechStackTestUserReadiness(
  env: EnvSource = process.env,
): Record<TechStackTestUserRole, TechStackTestUserReadiness> {
  return Object.fromEntries(
    (Object.keys(ROLE_SECRET_CANDIDATES) as TechStackTestUserRole[]).map(
      (role) => {
        const localBootstrapUser = getLocalBootstrapUser(role, env);
        const emailConfigured =
          !!localBootstrapUser ||
          ROLE_SECRET_CANDIDATES[role].some(
            (candidate) => readSecret(env, candidate.email).length > 0,
          );
        const passwordConfigured =
          !!localBootstrapUser ||
          ROLE_SECRET_CANDIDATES[role].some(
            (candidate) => readSecret(env, candidate.password).length > 0,
          );

        return [
          role,
          {
            emailConfigured,
            passwordConfigured,
            missingSecrets: [
              emailConfigured ? "" : describeCandidates(role, "email"),
              passwordConfigured ? "" : describeCandidates(role, "password"),
            ].filter(Boolean),
          },
        ];
      },
    ),
  ) as Record<TechStackTestUserRole, TechStackTestUserReadiness>;
}

export function requireTechStackTestUser(
  role: TechStackTestUserRole = "admin",
  env: EnvSource = process.env,
): TechStackTestUser {
  const user = getTechStackTestUser(role, env);
  if (user.email && user.password) return user;

  const readiness = buildTechStackTestUserReadiness(env)[role];
  throw new Error(
    [
      `Missing required TechStack E2E test-user secrets for ${role}.`,
      `Configure ${readiness.missingSecrets.join(", ")} via the configured environment before running happy-path tests.`,
      "401/403 responses are not acceptable release-test substitutes.",
    ].join(" "),
  );
}
