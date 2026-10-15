package guardbootstrap

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
)

// RenderInstallerCommand returns the remote enrollment command the control
// plane runs over SSH: curl the control plane's install.sh and run it with the
// pairing capability in KOMBI_TOKEN. The capability is not part of the command:
// the command reads it from standard input, which the SSH session feeds, so it
// never appears in the argument list of the remote shell or of sudo (where any
// local user could read it from /proc/<pid>/cmdline). The result is safe to
// execute over SSH on a Linux target.
func RenderInstallerCommand(serverURL string) (string, error) {
	origin, err := enrollmentOrigin(serverURL)
	if err != nil {
		return "", err
	}
	command := fmt.Sprintf(
		"set -o pipefail; IFS= read -r KOMBI_TOKEN || { echo 'the pairing capability was not provided' >&2; exit 1; }; export KOMBI_TOKEN; "+
			"curl -fsSL --retry 5 --retry-connrefused --retry-delay 10 %s | KOMBI_SERVER=%s bash",
		shellQuote(origin+"/install.sh"), shellQuote(origin),
	)
	if strings.ContainsAny(command, "\"\\\n\r") {
		return "", ErrUnrenderablePayload
	}
	return command, nil
}

// InstallerStdin is what the SSH session sends to the command of
// RenderInstallerCommand: the validated pairing capability and a newline.
func InstallerStdin(pairingToken string) (string, error) {
	token := strings.TrimSpace(pairingToken)
	parsed, parseErr := pairingtoken.Parse(token)
	if parseErr != nil || parsed.Legacy || parsed.TenantID == "" {
		return "", ErrInvalidPairingToken
	}
	return token + "\n", nil
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
