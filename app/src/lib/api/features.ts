/**
 * Feature Flags API Module
 *
 * Read-only access to the feature flags backend. Flag management is
 * central (Cloudflare Edge/OpenFeature entitlements); this client only lists effective flags.
 */

import { fetchApi, ApiRequestError } from "./client";

// Types
export type RiskLevel = "low" | "medium" | "high";
export type FeatureCategory = "security" | "beta" | "ux";

export interface FeatureFlag {
  key: string;
  name: string;
  enabled: boolean;
  locked: boolean;
  requires_consent: boolean;
  has_consent: boolean;
  risk_level: RiskLevel;
  description: string;
  category: FeatureCategory;
}

export interface FeaturesResponse {
  security: FeatureFlag[];
  beta: FeatureFlag[];
  ux: FeatureFlag[];
}

/**
 * Retry wrapper for transient failures
 */
async function withRetry<T>(
  fn: () => Promise<T>,
  maxRetries = 2,
  delayMs = 500,
): Promise<T> {
  let lastError: unknown;
  for (let attempt = 0; attempt <= maxRetries; attempt++) {
    try {
      return await fn();
    } catch (err) {
      lastError = err;
      // Only retry on network errors or 5xx server errors
      const shouldRetry =
        err instanceof ApiRequestError &&
        (err.status === undefined || (err.status >= 500 && err.status < 600));
      if (!shouldRetry || attempt === maxRetries) {
        throw err;
      }
      await new Promise((r) => setTimeout(r, delayMs * (attempt + 1)));
    }
  }
  throw lastError;
}

/**
 * Fetch all features with their current state for the logged-in user
 * Includes retry logic for transient failures
 */
export async function listFeatures(): Promise<FeaturesResponse> {
  return withRetry(async () => {
    const res = await fetchApi<FeaturesResponse>("/api/v1/features");
    return res.data;
  });
}
