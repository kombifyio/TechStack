package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	apicli "github.com/kombifyio/techstack/internal/gocommon/apisurface/cli"
	"github.com/kombifyio/techstack/internal/gocommon/toolauth"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/kombifyio/techstack/api/surface"
)

// API client environment keys. The default base URL is the canonical Gateway
// projection; a local instance is reached with TECHSTACK_API_URL=http://localhost:5260.
const (
	envAPIURL   = "TECHSTACK_API_URL"
	envAPIToken = "TECHSTACK_API_TOKEN"

	defaultAPIURL = "https://api.kombify.io/v1/techstack"
	contractPath  = "/api/v1"
)

// isAPIMode reports whether the binary was invoked as `techstack api ...`,
// the generated client for every published contract operation.
func isAPIMode(args []string) bool {
	return len(args) > 1 && strings.TrimSpace(args[1]) == "api"
}

// runAPIMode executes one generated contract command and exits with the
// generated CLI's stable exit code.
func runAPIMode(ctx context.Context, args []string) error {
	s, err := apicli.LoadSurface(surface.Raw)
	if err != nil {
		return fmt.Errorf("load api surface: %w", err)
	}
	root := &cobra.Command{Use: "techstack", SilenceErrors: true, SilenceUsage: true}
	api := &cobra.Command{
		Use:           "api",
		Short:         "Call the Techstack API (generated from the OpenAPI contract)",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(api)
	base := apiBaseURL()
	err = apicli.Mount(api, s, apicli.Config{
		BaseURL:     func(context.Context) (string, error) { return base, nil },
		ResolvePath: apiPathResolver(base),
		HTTPClient:  &http.Client{Timeout: 60 * time.Second},
		Authorize:   authorizeAPIRequest,
		Interactive: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
	})
	if err != nil {
		return fmt.Errorf("mount api commands: %w", err)
	}
	root.SetArgs(append([]string{"api"}, args...))
	if err := root.ExecuteContext(ctx); err != nil {
		var exitErr *apicli.ExitError
		if !errors.As(err, &exitErr) {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		os.Exit(apicli.ExitCode(err))
	}
	return nil
}

func apiBaseURL() string {
	if v := strings.TrimSpace(os.Getenv(envAPIURL)); v != "" {
		return v
	}
	return defaultAPIURL
}

// apiPathResolver maps contract paths (/api/v1/...) onto the base URL. The
// Gateway projection strips its /v1/techstack prefix and re-adds /api/v1 at the
// origin (x-kombify-route-projection), so a base URL with a path drops the
// contract prefix; a bare origin keeps it.
func apiPathResolver(base string) func(string) string {
	parsed, err := url.Parse(base)
	if err != nil || strings.Trim(parsed.Path, "/") == "" {
		return func(p string) string { return p }
	}
	return func(p string) string { return strings.TrimPrefix(p, contractPath) }
}

// authorizeAPIRequest attaches TECHSTACK_API_TOKEN or the session stored by
// `techstack login`. It never refreshes or prints credentials.
func authorizeAPIRequest(req *http.Request) error {
	token := strings.TrimSpace(os.Getenv(envAPIToken))
	if token == "" {
		pair, err := toolauth.DefaultTokenStore().Load(cloudLoginToolName)
		if err != nil {
			return fmt.Errorf("read stored session: %w", err)
		}
		if pair == nil || pair.AccessToken == "" {
			return errors.New("not signed in: run `techstack login` or set " + envAPIToken)
		}
		if !pair.ExpiresAt.IsZero() && time.Now().After(pair.ExpiresAt) {
			return errors.New("stored session expired: run `techstack login` again")
		}
		token = pair.AccessToken
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}
