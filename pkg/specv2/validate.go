package specv2

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

const (
	// StackKitCLIEnv names the development-only CLI override; production
	// resolves the binary through the pinned-release admission instead.
	StackKitCLIEnv = "TECHSTACK_STACKKIT_CLI"

	releasePinEnv   = "TECHSTACK_STACKKIT_RELEASE_PIN"
	releaseCacheEnv = "TECHSTACK_STACKKIT_RELEASE_CACHE"

	defaultValidateTimeout = 2 * time.Minute
	validateOutputTail     = 4000
)

// SpecValidator is the acceptance authority for projected specs: the pinned
// StackKits CLI's own `validate`, never a Techstack reimplementation.
type SpecValidator interface {
	ValidateSpec(ctx context.Context, spec map[string]any) error
}

// CLIValidator runs `stackkit --no-log --chdir <dir> --spec stack-spec.yaml
// validate` against a scratch workspace holding the projected document.
type CLIValidator struct {
	Binary  string
	Timeout time.Duration
	// ReleaseVersion is informational provenance when the binary came from
	// the pinned-release admission.
	ReleaseVersion string
}

// NewCLIValidatorFromEnv resolves the validator binary: the fail-closed
// pinned-release admission when the pin is configured, else the explicit
// development override. It returns (nil, nil) when neither is configured so
// callers can fail closed with a precise reason instead of guessing at PATH.
func NewCLIValidatorFromEnv() (*CLIValidator, error) {
	pinPath := strings.TrimSpace(os.Getenv(releasePinEnv))
	cacheRoot := strings.TrimSpace(os.Getenv(releaseCacheEnv))
	switch {
	case pinPath != "" && cacheRoot != "":
		release, err := (stackkitrelease.Cache{Root: cacheRoot}).ResolvePin(pinPath)
		if err != nil {
			return nil, fmt.Errorf("specv2: admit pinned published StackKits release: %w", err)
		}
		return &CLIValidator{
			Binary:         release.BinaryPath(),
			Timeout:        defaultValidateTimeout,
			ReleaseVersion: release.Receipt().Version,
		}, nil
	case pinPath != "" || cacheRoot != "":
		return nil, fmt.Errorf("specv2: %s and %s must be configured together", releasePinEnv, releaseCacheEnv)
	}
	if binary := strings.TrimSpace(os.Getenv(StackKitCLIEnv)); binary != "" {
		return &CLIValidator{Binary: binary, Timeout: defaultValidateTimeout}, nil
	}
	return nil, nil
}

func (v *CLIValidator) ValidateSpec(ctx context.Context, spec map[string]any) error {
	if v == nil || strings.TrimSpace(v.Binary) == "" {
		return fmt.Errorf("specv2: validator not configured")
	}
	workDir, err := os.MkdirTemp("", "specv2-validate-")
	if err != nil {
		return fmt.Errorf("specv2: create validate workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	// JSON is valid YAML; the seed templates are authored the same way.
	document, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("specv2: encode projected spec: %w", err)
	}
	specPath := filepath.Join(workDir, "stack-spec.yaml")
	if writeErr := os.WriteFile(specPath, document, 0o600); writeErr != nil {
		return fmt.Errorf("specv2: write projected spec: %w", writeErr)
	}

	timeout := v.Timeout
	if timeout <= 0 {
		timeout = defaultValidateTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"--no-log", "--chdir", workDir, "--spec", filepath.Base(specPath), "validate"}
	cmd := exec.CommandContext(runCtx, v.Binary, args...) // #nosec G204 -- binary comes from pinned-release admission or the operator env, args are fixed.
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if runCtx.Err() != nil {
			return fmt.Errorf("specv2: StackKits CLI validate timed out after %s: %w", timeout, runCtx.Err())
		}
		return fmt.Errorf("specv2: StackKits CLI validate failed: %w: %s", err, tailText(output, validateOutputTail))
	}
	return nil
}

func tailText(output []byte, limit int) string {
	text := strings.TrimSpace(string(output))
	if len(text) <= limit {
		return text
	}
	return text[len(text)-limit:]
}

var _ SpecValidator = (*CLIValidator)(nil)
