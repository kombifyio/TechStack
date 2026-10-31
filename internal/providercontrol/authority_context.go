package providercontrol

import (
	"context"

	"github.com/kombifyio/techstack/internal/gocommon/tenantdb"
)

// SystemAuthorityContext removes a caller's signed database context token
// before work enters the provider-control pool.
//
// The token is minted for the application login (audience
// techstack_application). Provider Control runs under its own login,
// techstack_provider_control_runtime, which is never a token audience and has
// no kombify_ctx authority: boot refuses any execute grant outside its own
// schema. Its tables are fenced by the app.tenant_id setting that each
// transaction here sets itself. A forwarded request token would only make the
// shared tenant scope call kombify_ctx.verified_context() under the wrong
// login and fail the transaction (kombify-Techstack-ia4a). The empty token
// still masks any session-level token left on a pooled connection.
func SystemAuthorityContext(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return tenantdb.NewContextWithToken(ctx, "")
}
