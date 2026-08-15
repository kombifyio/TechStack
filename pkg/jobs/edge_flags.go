package jobs

import (
	"context"
	"strings"

	"github.com/kombifyio/go-common/edgeauth"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/middleware"
)

const payloadKeyEdgeFlags = "edge_flags"

type requestAuthoritySnapshot struct {
	tenantID     string
	ownerID      string
	authorityCtx context.Context
	entitlements []string
}

type requestAuthorityContext struct {
	context.Context
	authority context.Context
}

func (c requestAuthorityContext) Value(key any) any {
	if value := c.authority.Value(key); value != nil {
		return value
	}
	return c.Context.Value(key)
}

// CopyEdgeFlagsFromContext snapshots the signed Edge flag decisions into a job
// payload. Jobs run asynchronously, so they cannot rely on the original HTTP
// request context still being present when managed runtime actions continue.
func CopyEdgeFlagsFromContext(ctx context.Context, payload map[string]interface{}) {
	if payload == nil {
		return
	}
	flags, ok := edgeauth.FlagsFromContext(ctx)
	if !ok || len(flags.Flags) == 0 {
		return
	}
	payload[payloadKeyEdgeFlags] = cloneBoolMap(flags.Flags)
}

// CaptureRequestAuthority stores an in-process-only authority context containing
// only the already verified v2 commercial entitlements and signed budget
// decision. Re-attaching the exact verified FlagSet preserves the unexported
// cryptographic provenance that edgeauth intentionally refuses to reconstruct
// from application data, while context.Background detaches HTTP cancellation
// and every unrelated request value. The snapshot is deliberately not
// serialized: after a restart, fresh cost-bearing work without a newly verified
// request must fail closed. Exact NativeAdmission replay remains available from
// its durable database records.
func CaptureRequestAuthority(ctx context.Context, job *Job, tenantID, ownerID string) {
	if ctx == nil || job == nil {
		return
	}
	tenantID = strings.TrimSpace(tenantID)
	ownerID = strings.TrimSpace(ownerID)
	principal := identity.FromContext(ctx)
	entitlements, hasEntitlements := middleware.SignedEntitlementsFromContext(ctx)
	decisions, hasDecisions := edgeauth.FlagsFromContext(ctx)
	if tenantID == "" || ownerID == "" || principal == nil || principal.UserID != ownerID ||
		(strings.TrimSpace(principal.OrgID) != "" && strings.TrimSpace(principal.OrgID) != tenantID) ||
		!hasEntitlements || !hasDecisions || len(decisions.Budgets) == 0 {
		return
	}
	snapshotEntitlements := entitlements.Values()
	authorityCtx := edgeauth.FlagsToContext(context.Background(), decisions)
	authorityCtx = middleware.WithSignedEntitlements(authorityCtx, snapshotEntitlements...)
	snapshot := &requestAuthoritySnapshot{
		tenantID: tenantID, ownerID: ownerID,
		authorityCtx: authorityCtx,
		entitlements: snapshotEntitlements,
	}
	if len(snapshot.entitlements) == 0 {
		return
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if firstJobString(job.Payload, job.Result, "tenant_id") != tenantID ||
		firstJobString(job.Payload, job.Result, "owner_id") != ownerID {
		return
	}
	job.requestAuthority = snapshot
}

func contextWithJobEdgeFlags(ctx context.Context, job *Job) context.Context {
	if job == nil || len(job.Payload) == 0 {
		return ctx
	}
	flags := boolMapFromAny(job.Payload[payloadKeyEdgeFlags])
	if len(flags) == 0 {
		return ctx
	}
	return edgeauth.FlagsToContext(ctx, edgeauth.FlagSet{Flags: flags})
}

func contextWithJobRequestAuthority(ctx context.Context, job *Job) context.Context {
	if job == nil {
		return ctx
	}
	job.mu.RLock()
	snapshot := cloneRequestAuthoritySnapshot(job.requestAuthority)
	tenantID := firstJobString(job.Payload, job.Result, "tenant_id")
	ownerID := firstJobString(job.Payload, job.Result, "owner_id")
	job.mu.RUnlock()
	if snapshot == nil || snapshot.tenantID != tenantID || snapshot.ownerID != ownerID {
		return ctx
	}
	if snapshot.authorityCtx == nil {
		return ctx
	}
	return requestAuthorityContext{Context: ctx, authority: snapshot.authorityCtx}
}

func cloneBoolMap(in map[string]bool) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneRequestAuthoritySnapshot(in *requestAuthoritySnapshot) *requestAuthoritySnapshot {
	if in == nil {
		return nil
	}
	return &requestAuthoritySnapshot{
		tenantID: in.tenantID, ownerID: in.ownerID,
		authorityCtx: in.authorityCtx,
		entitlements: append([]string(nil), in.entitlements...),
	}
}

func boolMapFromAny(raw interface{}) map[string]bool {
	switch typed := raw.(type) {
	case map[string]bool:
		return cloneBoolMap(typed)
	case map[string]interface{}:
		out := make(map[string]bool, len(typed))
		for key, value := range typed {
			if boolValue, ok := value.(bool); ok {
				out[key] = boolValue
			}
		}
		return out
	default:
		return nil
	}
}
