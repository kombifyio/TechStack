import * as Sentry from "@sentry/sveltekit";
import { loadClientBootstrap } from "$lib/client/bootstrap";
import { scrubSentryEvent } from "$lib/observability/sentry-scrub";

declare const __SENTRY_RELEASE__: string | undefined;

// Sentry is initialized from the runtime boot config (ADR-033 OQ2): the static
// bundle carries no baked-in DSN, and selfhost-oss/local deployments stay
// telemetry-free unless the backend explicitly provides one.
export const init = async () => {
  const bootstrap = await loadClientBootstrap();
  const dsn = bootstrap.telemetry.sentry.dsn;
  if (!dsn) {
    return;
  }

  const environment = bootstrap.telemetry.sentry.environment || "prod";
  const release =
    bootstrap.telemetry.sentry.release || __SENTRY_RELEASE__ || undefined;

  Sentry.init({
    dsn,
    environment,
    release,
    sendDefaultPii: false,
    tracesSampleRate:
      environment === "prod" || environment === "production" ? 0.1 : 1.0,
    replaysSessionSampleRate: 0,
    replaysOnErrorSampleRate: 0,
    beforeSend: scrubSentryEvent,
  });

  Sentry.setTag("service", "kombify-techstack-frontend");
};

export const handleError = Sentry.handleErrorWithSentry();
