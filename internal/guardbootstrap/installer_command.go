package guardbootstrap

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
)

// RenderInstallerOneLiner returns the canonical remote enrollment command:
// curl the control plane's install.sh and run it with KOMBI_SERVER/KOMBI_TOKEN.
// The result is safe to execute over SSH on a Linux target.
func RenderInstallerOneLiner(serverURL, pairingToken string) (string, error) {
	origin, err := enrollmentOrigin(serverURL)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(pairingToken)
	parsed, parseErr := pairingtoken.Parse(token)
	if parseErr != nil || parsed.Legacy || parsed.TenantID == "" {
		return "", ErrInvalidPairingToken
	}
	command := fmt.Sprintf(
		"set -o pipefail; curl -fsSL --retry 5 --retry-connrefused --retry-delay 10 %s | KOMBI_SERVER=%s KOMBI_TOKEN=%s bash",
		shellQuote(origin+"/install.sh"), shellQuote(origin), shellQuote(token),
	)
	if strings.ContainsAny(command, "\"\\\n\r") {
		return "", ErrUnrenderablePayload
	}
	return command, nil
}

func enrollmentOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrInsecureServerURL
	}
	if origin, err := normalizeOrigin(raw); err == nil {
		return origin, nil
	}
	if origin := config.SanitizeOrigin(raw); origin != "" {
		return strings.TrimSuffix(origin, "/"), nil
	}
	return "", ErrInsecureServerURL
}
