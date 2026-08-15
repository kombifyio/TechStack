package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/kombifyio/go-common/nativeclient/interactiveauth"
	"github.com/kombifyio/go-common/oidcclient"
	"github.com/kombifyio/go-common/toolauth"

	"github.com/kombifyio/techstack/pkg/config"
)

// cloudLoginToolName keys the stored cloud token pair in the OS credential
// store (Windows Credential Manager target "kombify:techstack/cloud/oidc").
const cloudLoginToolName = "techstack/cloud/oidc"

// Native OIDC configuration environment keys. The client id belongs to the
// dedicated Auth0 NATIVE application registration (public client, token
// endpoint auth "none") — never a web application client id
// (NATIVE-CLIENT-PLATFORM-STANDARD section 4.4/4.5).
const (
	envCloudNativeClientID = "TECHSTACK_CLOUD_NATIVE_CLIENT_ID"
	envCloudNativeAudience = "TECHSTACK_CLOUD_NATIVE_AUDIENCE"
)

// isLoginMode reports whether the binary was invoked as `techstack login` or
// `techstack logout` (CLI-first per ADR-033 D1).
func isLoginMode(args []string) bool {
	if len(args) < 2 {
		return false
	}
	command := strings.TrimSpace(args[1])
	return command == "login" || command == "logout"
}

// runLoginMode dispatches the interactive native auth commands and exits.
func runLoginMode(ctx context.Context, args []string) error {
	switch args[0] {
	case "logout":
		return runCloudLogout()
	case "login":
		return runCloudLogin(ctx, args[1:])
	default:
		return fmt.Errorf("unknown auth command %q", args[0])
	}
}

func runCloudLogin(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("techstack login", flag.ContinueOnError)
	useBrowser := flags.Bool("browser", false, "use the loopback browser flow (RFC 8252) instead of the device code flow")
	issuer := flags.String("issuer", config.DefaultCloudAuthIssuer, "cloud OIDC issuer")
	clientID := flags.String("client-id", strings.TrimSpace(os.Getenv(envCloudNativeClientID)), "native OIDC public client id")
	audience := flags.String("audience", strings.TrimSpace(os.Getenv(envCloudNativeAudience)), "optional API audience")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*clientID) == "" {
		return fmt.Errorf(
			"no native OIDC client id configured: set %s (or --client-id) to the dedicated Auth0 Native application registration for techstack-desktop",
			envCloudNativeClientID,
		)
	}

	provider, err := buildCloudNativeProvider(ctx, *issuer, *clientID, *audience)
	if err != nil {
		return err
	}

	var result *oidcclient.CodeExchangeResult
	if *useBrowser {
		result, err = interactiveauth.LoopbackPKCE(ctx, interactiveauth.LoopbackConfig{
			Provider:    provider,
			OpenBrowser: openSystemBrowser,
		})
	} else {
		result, err = interactiveauth.DeviceGrant(ctx, interactiveauth.DeviceGrantConfig{
			Provider: provider,
			Audience: strings.TrimSpace(*audience),
			Prompt: func(userCode, verificationURI, verificationURIComplete string) {
				fmt.Println("To sign in, open:")
				fmt.Println("  " + verificationURI)
				fmt.Println("and enter the code:")
				fmt.Println("  " + userCode)
				if verificationURIComplete != "" {
					fmt.Println("Or open directly:")
					fmt.Println("  " + verificationURIComplete)
				}
			},
		})
	}
	if err != nil {
		return fmt.Errorf("cloud sign-in failed: %w", err)
	}

	pair := &toolauth.TokenPair{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
	}
	if result.ExpiresIn > 0 {
		pair.ExpiresAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	}
	if err := toolauth.DefaultTokenStore().Save(cloudLoginToolName, pair); err != nil {
		return fmt.Errorf("storing the cloud session in the OS credential store failed: %w", err)
	}
	fmt.Println("Signed in to kombify Cloud. The session is stored in the OS credential store.")
	return nil
}

func runCloudLogout() error {
	if err := toolauth.DefaultTokenStore().Delete(cloudLoginToolName); err != nil {
		return fmt.Errorf("removing the stored cloud session failed: %w", err)
	}
	fmt.Println("Signed out. The stored cloud session was removed from the OS credential store.")
	return nil
}

// buildCloudNativeProvider discovers the issuer endpoints and constructs the
// public-client provider. Discovery failure falls back to the RFC-default
// endpoint composition of oidcclient.NewProvider.
func buildCloudNativeProvider(ctx context.Context, issuer, clientID, audience string) (*oidcclient.Provider, error) {
	cfg := oidcclient.ProviderConfig{
		ID:       "cloud-native",
		Kind:     oidcclient.KindGeneric,
		Issuer:   strings.TrimSpace(issuer),
		ClientID: strings.TrimSpace(clientID),
		Audience: strings.TrimSpace(audience),
		Scopes:   []string{"openid", "profile", "email", "offline_access"},
	}
	if doc, err := oidcclient.Discover(ctx, cfg.Issuer, nil); err == nil {
		oidcclient.ApplyDiscovery(&cfg, doc)
	}
	provider, err := oidcclient.NewProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("invalid cloud OIDC configuration: %w", err)
	}
	return provider, nil
}

// openSystemBrowser opens the URL in the user's default browser. Universal
// Login always happens in the system browser, never in an embedded surface.
func openSystemBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// #nosec G204 -- fixed handler binary; the URL is the provider
		// authorization URL constructed by oidcclient.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		// #nosec G204 -- see above.
		cmd = exec.Command("open", url)
	default:
		// #nosec G204 -- see above.
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return errors.Join(errors.New("could not open the system browser"), err)
	}
	return nil
}
