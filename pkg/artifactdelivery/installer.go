package artifactdelivery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
)

// LinuxInstallerSHA256 pins the authoritative repository install.sh after
// CRLF-to-LF normalization. The runtime image copies that exact artifact and
// the Guard release mirrors it to private R2 under this digest; serving any
// partial, stale, or locally substituted shell fragment fails closed until the
// contract digest is deliberately updated with the installer.
const LinuxInstallerSHA256 = "ee62708db4a2c74b2e52dc8e27865cf345e0e6e05d22674e3933f107e8fbc23e"

// NormalizeLinuxInstaller returns the CRLF-to-LF normalized installer and its
// SHA-256, the form both the pin and the R2 object use.
func NormalizeLinuxInstaller(content []byte) ([]byte, string) {
	normalized := bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	digest := sha256.Sum256(normalized)
	return normalized, hex.EncodeToString(digest[:])
}

// LinuxInstallerObjectKey is the content-addressed private R2 key the Gateway
// Worker streams an installer from after the control plane admits its digest.
func LinuxInstallerObjectKey(digest string) string {
	return "techstack/installer/sha256/" + digest + "/install.sh"
}
