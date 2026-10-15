package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/db"
)

type tenantContextKey struct{}

// WithTenant carries a tenant already authorized by the calling product boundary.
// It does not infer authority from workflow input, an owner id, or system fallback.
func WithTenant(ctx context.Context, tenantID string) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, tenantContextKey{}, strings.TrimSpace(tenantID))
}
func tenantFromContext(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", db.ErrNoTenant
	}
	id, _ := ctx.Value(tenantContextKey{}).(string)
	if id == "" {
		return "", db.ErrNoTenant
	}
	return id, nil
}
func (e *Engine) tenantStore(ctx context.Context) (RunStore, error) {
	id, err := tenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if e == nil || e.store == nil {
		return nil, fmt.Errorf("workflow: store unavailable")
	}
	scoped, ok := e.store.(interface {
		ForTenant(context.Context, string) (RunStore, error)
	})
	if !ok {
		return nil, fmt.Errorf("workflow: tenant store unavailable")
	}
	return scoped.ForTenant(ctx, id)
}
