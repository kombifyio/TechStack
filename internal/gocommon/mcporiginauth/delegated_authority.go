package mcporiginauth

import (
	"context"
	"encoding/json"
	"errors"
)

func verifyDelegatedDispatch(ctx context.Context, cfg Config, env envelope, verified Context, canonicalArgs []byte, headers originHeaders, now int64) (Context, error) {
	var authority map[string]json.RawMessage
	if cfg.VerifyDelegatedAuthority == nil || cfg.ReserveNonce == nil ||
		env.Transport.ExpiresAt <= now || env.Transport.ExpiresAt <= headers.timestamp ||
		env.Transport.ExpiresAt-headers.timestamp > 60 ||
		json.Unmarshal(env.DelegatedAuthority, &authority) != nil || len(authority) == 0 {
		return Context{}, errors.New("delegated authority verifier required")
	}
	verified.DelegatedAuthority = append(json.RawMessage(nil), env.DelegatedAuthority...)
	verified.Arguments = append(json.RawMessage(nil), canonicalArgs...)
	// Give the callback its own evidence buffers so validation cannot change
	// the signed evidence delivered to the product handler.
	checkContext := verified
	checkContext.DelegatedAuthority = append(json.RawMessage(nil), verified.DelegatedAuthority...)
	checkContext.Arguments = append(json.RawMessage(nil), verified.Arguments...)
	checkContext.Scopes = append([]string(nil), verified.Scopes...)
	checkContext.OperationIDs = append([]string(nil), verified.OperationIDs...)
	if err := cfg.VerifyDelegatedAuthority(ctx, checkContext); err != nil {
		return Context{}, errors.New("delegated authority rejected")
	}
	if !verified.ExpiresAt.After(cfg.Now()) {
		return Context{}, errors.New("delegated authority expired during verification")
	}
	// The key-id selects trusted custody but is not a signed component.
	// Aliases for an overlapping rotation must share the replay boundary.
	reserved, err := cfg.ReserveNonce(ctx, cfg.Audience+":"+cfg.ServerKey+":"+headers.nonce, verified.ExpiresAt)
	if err != nil || !reserved || !verified.ExpiresAt.After(cfg.Now()) {
		return Context{}, errors.New("delegated nonce reservation rejected")
	}
	return verified, nil
}
