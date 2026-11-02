package tunnel

import (
	"context"
	"fmt"
)

// GenerateGuardReinstallCommand generates the command that brings the Guard of
// an already enrolled Linux server forward in place: install.sh --reinstall-guard
// downloads the current Guard with the download capability in token, swaps the
// binary and keeps the enrollment and identity key unchanged. It runs as root.
// The capability reaches sudo through its environment, never as an argument
// that other local users could read from the process list or sudo would log.
func (r *RegistryURLResolver) GenerateGuardReinstallCommand(ctx context.Context, token string) (string, error) {
	if err := validateInstallToken(token); err != nil {
		return "", err
	}
	url, err := r.GetRegistryURL(ctx)
	if err != nil {
		return "", fmt.Errorf("cannot generate reinstall command: %w", err)
	}
	if pin := r.LocalBridgePin(url); pin != "" {
		return fmt.Sprintf(`curl -fsSL --insecure --pinnedpubkey %s %s | KOMBI_SERVER=%s KOMBI_TOKEN=%s KOMBI_PRIVATE_LAN_PIN=%s sudo --preserve-env=KOMBI_SERVER,KOMBI_TOKEN,KOMBI_PRIVATE_LAN_PIN bash -s -- --reinstall-guard`,
			shQuote(pin), shQuote(url+"/install.sh"), shQuote(url), shQuote(token), shQuote(pin)), nil
	}
	return fmt.Sprintf(`curl -fsSL %s | KOMBI_SERVER=%s KOMBI_TOKEN=%s sudo --preserve-env=KOMBI_SERVER,KOMBI_TOKEN bash -s -- --reinstall-guard`,
		shQuote(url+"/install.sh"), shQuote(url), shQuote(token)), nil
}
