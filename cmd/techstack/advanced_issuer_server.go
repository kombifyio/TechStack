package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/executioncustody"
)

// runExecutionServer composes the canonical issuer and provider authority in a
// privileged process. It starts no user-facing routes, migration or wallet.
// Deployment installs its exact caller grants and retained key custody here;
// the separately supervised HTTP control plane receives neither credential.
func runExecutionServer(ctx context.Context) error {
	if profile := strings.TrimSpace(os.Getenv("TECHSTACK_EXECUTION_PROFILE")); profile == "platform-projection" {
		return runPlatformProjectionServer(ctx)
	} else if profile != "" {
		return errors.New("unknown private Execution profile")
	}
	return errors.New("native Execution ran only on the retired Stage host; no private Execution profile remains")
}

// runExecutionVerification uses the exact authenticated transport used by
// HTTP runtime admission; it needs no database or installation credential.
func runExecutionVerification(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remote, err := executioncustody.OpenRemote(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(remote.Metadata())
}
