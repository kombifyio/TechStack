package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/updateclient"
	"github.com/kombifyio/techstack/internal/gocommon/updateclient/manifest"
)

// `techstack client-update` is the Go half of the Windows client update
// consumer (NATIVE-CLIENT-PLATFORM-STANDARD section 7). It fetches the channel
// manifest, verifies its signature against the key embedded below, refuses
// downgrades and channels below the supported floor, downloads the package,
// checks its size and SHA-256, and stages it with a descriptor. It never
// swaps files: the Windows shell applies a staged package at its next start,
// before it spawns the runtime.

// clientUpdateKeyID and clientUpdatePublicKeyPEM identify the production
// update-signing key (DESKTOP-CLIENT-DISTRIBUTION-STANDARD section 4). Only
// manifests signed by an embedded key are accepted; there is no runtime
// override, because a user-writable trust setting would let any local process
// have the client install its own package.
const clientUpdateKeyID = "kombify-desktop-update-2026-v1"

const clientUpdatePublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAOLkR9JyjFiPmnm11tQ1f2ghRR7tMZlxBkSJhP1MxQ2w=
-----END PUBLIC KEY-----
`

type clientUpdateResult struct {
	Status     string `json:"status"`
	Version    string `json:"version,omitempty"`
	Installed  string `json:"installed"`
	Descriptor string `json:"descriptor,omitempty"`
	Error      string `json:"error,omitempty"`
}

func isClientUpdateMode(args []string) bool {
	return len(args) > 1 && strings.TrimSpace(args[1]) == "client-update"
}

func clientUpdateKeyring() (manifest.Keyring, error) {
	key, err := manifest.ParseEd25519PublicKeyPEM([]byte(clientUpdatePublicKeyPEM))
	if err != nil {
		return manifest.Keyring{}, fmt.Errorf("embedded update key: %w", err)
	}
	return manifest.Keyring{Ed25519: map[string]ed25519.PublicKey{clientUpdateKeyID: key}}, nil
}

// runClientUpdateMode prints one JSON result line and exits non-zero on any
// failure. Callers treat every failure as "no update this round".
func runClientUpdateMode(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("techstack client-update", flag.ContinueOnError)
	manifestURL := flags.String("manifest-url", "", "HTTPS URL of the channel's update manifest")
	channel := flags.String("channel", "stable", "update channel bound to this installation")
	stagingDir := flags.String("staging-dir", "", "directory that receives verified packages")
	minSupported := flags.String("min-supported-version", "", "refuse a channel below this version")
	timeout := flags.Duration("timeout", 10*time.Minute, "budget for the check and the download")
	if err := flags.Parse(args); err != nil {
		return err
	}

	result := clientUpdateResult{Installed: version}
	err := stageClientUpdate(ctx, *manifestURL, *channel, *stagingDir, *minSupported, *timeout, nil, &result)
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return marshalErr
	}
	fmt.Fprintln(os.Stdout, string(encoded))
	return err
}

func stageClientUpdate(ctx context.Context, manifestURL, channel, stagingDir, minSupported string, timeout time.Duration, httpClient *http.Client, result *clientUpdateResult) error {
	if !strings.HasPrefix(strings.TrimSpace(manifestURL), "https://") {
		return errors.New("--manifest-url must be an HTTPS URL")
	}
	keys, err := clientUpdateKeyring()
	if err != nil {
		return err
	}
	client, err := updateclient.New(updateclient.Config{
		ManifestURL:         strings.TrimSpace(manifestURL),
		Channel:             strings.TrimSpace(channel),
		CurrentVersion:      version,
		MinSupportedVersion: strings.TrimSpace(minSupported),
		Keys:                keys,
		StagingDir:          strings.TrimSpace(stagingDir),
		HTTPClient:          httpClient,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	decision, err := client.Check(ctx)
	if err != nil {
		return err
	}
	if !decision.UpdateAvailable {
		result.Status = "current"
		return nil
	}
	descriptor, err := client.Download(ctx, decision.Manifest)
	if err != nil {
		return err
	}
	result.Status = "staged"
	result.Version = decision.Manifest.Version
	result.Descriptor = descriptor
	return nil
}
