//go:build kombify_full_local

package jobs

import "context"

// The Full Local build (tag kombify_full_local) has no error-reporting client:
// jobs run without scopes, breadcrumbs or captured errors.

func withJobSentryScope(ctx context.Context, _ *Job) (context.Context, func(error)) {
	return ctx, func(error) {}
}

func breadcrumbStep(context.Context, string, string, map[string]interface{}) {}

func captureJobError(context.Context, error, map[string]interface{}) {}
