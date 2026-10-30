//go:build kombify_full_local

package grpcserver

// The Full Local build (tag kombify_full_local) has no error-reporting client.
func captureRuntimeLogIfError(AgentLogEntry) {}
