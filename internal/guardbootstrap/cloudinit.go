// Package guardbootstrap renders the first-boot payload that installs and
// enrols the kombify Guard on a freshly provisioned managed node.
//
// It is deliberately provider-neutral: IONOS and Centron both accept a
// base64 cloud-init document on volume create, and both must produce the same
// node-side result. The payload only invokes the installer that the control
// plane already serves at /install.sh, so the install logic itself has exactly
// one home.
package guardbootstrap

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/kombifyio/techstack/pkg/guardcompat"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
)

// BootstrapLogPath keeps first-boot output on the node. Without it a node that
// fails to enrol reports nothing at all, which is precisely the ghost-server
// state this payload exists to prevent.
const BootstrapLogPath = "/var/log/kombify-bootstrap.log"

var (
	// ErrInsecureServerURL means the control-plane origin is not a plain https
	// origin. A managed node reaches the control plane across the public
	// internet, so the pairing token must never travel unencrypted.
	ErrInsecureServerURL = errors.New("guardbootstrap: control-plane origin must be an https origin")
	// ErrInvalidPairingToken means the supplied capability is not a canonical
	// tenant-routable pairing token.
	ErrInvalidPairingToken = errors.New("guardbootstrap: pairing token is not canonical")
	// ErrUnrenderablePayload means the rendered command contained a character
	// that would change the meaning of the emitted document.
	ErrUnrenderablePayload = errors.New("guardbootstrap: rendered payload is not safely quotable")
	// ErrInvalidHostname means the requested first-boot hostname is not a safe
	// RFC-1123 label and could change the meaning of the emitted document.
	ErrInvalidHostname = errors.New("guardbootstrap: hostname is not a valid RFC-1123 label")
	// ErrInvalidAuthorizedKey means a first-boot key is not one single-line
	// OpenSSH public key, or was supplied without a Golden Image profile.
	ErrInvalidAuthorizedKey = errors.New("guardbootstrap: authorized key is not a single OpenSSH public key for a Golden Image")
)

// CloudInitInput is the complete first-boot contract.
type CloudInitInput struct {
	// ServerURL is the control-plane origin the node installs from and calls
	// home to. It becomes KOMBI_SERVER.
	ServerURL string
	// PairingToken is the one-time enrolment capability. It becomes KOMBI_TOKEN
	// and must never be logged, wrapped into an error, or returned to a caller
	// that did not already hold it.
	PairingToken string
	// Hostname, when set, names the node at first boot. Provider images
	// otherwise default to a generic hostname (e.g. "ubuntu"), which the agent
	// then reports as the server display name. The value stays part of the
	// deterministic render input.
	Hostname string
	// HostPrepProfile opts a provider image into a declared first-boot host
	// preparation contract. The empty value remains provider-neutral.
	HostPrepProfile HostPrepProfile
	// AuthorizedKeys are installed by cloud-init for root and the image's
	// default user. Only a Golden Image profile accepts them: a provider's own
	// key injection works for its public images alone (IONOS sshKeys), so a
	// private image receives the execution-channel key here instead.
	AuthorizedKeys []string
}

type HostPrepProfile string

const (
	HostPrepProfileStackKitReadyUbuntu2604V1 HostPrepProfile = "stackkit-ready-" + guardcompat.ProfileReleaseID + "-v1"
	// HostPrepProfileIONOSUbuntu2404DockerV1 is the IONOS Ubuntu image
	// contract. It now prepares the same StackKit-ready host as
	// HostPrepProfileStackKitReadyUbuntu2404V1 so Cloud host-security can
	// enable nftables, fail2ban, and unattended-upgrades.
	HostPrepProfileIONOSUbuntu2404DockerV1 HostPrepProfile = "ionos-" + guardcompat.ProfileCompatibilityReleaseID + "-docker-v1"
	// HostPrepProfileStackKitReadyUbuntu2404V1 is the provider-neutral
	// compatibility contract used by existing explicit image choices
	// so apply does not start without host-security packages.
	HostPrepProfileStackKitReadyUbuntu2404V1 HostPrepProfile = "stackkit-ready-" + guardcompat.ProfileCompatibilityReleaseID + "-v1"
	// HostPrepProfileGoldenImageUbuntu2604V1 boots an admitted Guard Golden
	// Image (G09, scripts/os-compat/golden-README.md): the image already holds
	// the enabled unbound Guard and the neutral baseline, so first boot redeems
	// the capability with the baked Guard instead of the served installer,
	// exactly as the candidate's two-clone enrollment does. Host preparation v2
	// still runs and records its status, so readiness has one authority.
	HostPrepProfileGoldenImageUbuntu2604V1 HostPrepProfile = "golden-image-" + guardcompat.ProfileReleaseID + "-v1"
	// HostPrepProfileGoldenImageUbuntu2404V1 is the 24.04 compatibility image.
	HostPrepProfileGoldenImageUbuntu2404V1 HostPrepProfile = "golden-image-" + guardcompat.ProfileCompatibilityReleaseID + "-v1"
)

// goldenImageGuardPath is where the Golden Image recipe installs the Guard.
const goldenImageGuardPath = "/usr/local/bin/techstack"

func (p HostPrepProfile) stackKitReady() bool {
	return p == HostPrepProfileIONOSUbuntu2404DockerV1 || p == HostPrepProfileStackKitReadyUbuntu2404V1 || p == HostPrepProfileStackKitReadyUbuntu2604V1 ||
		p.goldenImage()
}

func (p HostPrepProfile) goldenImage() bool {
	return p == HostPrepProfileGoldenImageUbuntu2604V1 || p == HostPrepProfileGoldenImageUbuntu2404V1
}

// GoldenImageHostPrepProfile selects the Golden Image contract of an admitted
// image's declared Ubuntu release; any other release has none.
func GoldenImageHostPrepProfile(version string) HostPrepProfile {
	switch version {
	case guardcompat.ProfileReleaseVersion:
		return HostPrepProfileGoldenImageUbuntu2604V1
	case guardcompat.ProfileCompatibilityReleaseVersion:
		return HostPrepProfileGoldenImageUbuntu2404V1
	default:
		return ""
	}
}

// UbuntuHostPrepProfile preserves the OS version of an explicit image choice.
func UbuntuHostPrepProfile(version string) HostPrepProfile {
	switch version {
	case guardcompat.ProfileReleaseVersion:
		return HostPrepProfileStackKitReadyUbuntu2604V1
	case guardcompat.ProfileCompatibilityReleaseVersion:
		return HostPrepProfileStackKitReadyUbuntu2404V1
	default:
		return ""
	}
}

// ExecutionChannelUser is the login the control plane keeps on a managed node.
// Cloud host-security disables root SSH as part of the hardening it must
// enforce, so the execution channel cannot be root and survive its own kit.
const ExecutionChannelUser = "kombify"

const executionChannelSudoersPath = "/etc/sudoers.d/60-kombify-execution-channel"
const executionChannelBootstrapPath = "/usr/local/lib/kombify/execution-channel-v1"

// RenderExecutionChannelUserProvisioning returns the idempotent shell that
// establishes the execution-channel login from the key the provider already
// injected for root. privilege is the prefix the calling script uses to reach
// root ("" when it already runs as root).
//
// It is deliberately re-runnable: an enrolled node that predates the channel
// gains it on the next preparation instead of needing a new generation.
func RenderExecutionChannelUserProvisioning(privilege string) string {
	if privilege != "" {
		privilege += " "
	}
	return strings.NewReplacer("{S}", privilege, "{USER}", ExecutionChannelUser, "{SUDOERS}", executionChannelSudoersPath).Replace(
		`if ! id -u {USER} >/dev/null 2>&1; then
  {S}useradd --create-home --shell /bin/bash {USER}
fi
{S}install -d -m 0700 -o {USER} -g {USER} /home/{USER}/.ssh
if {S}test -s /root/.ssh/authorized_keys; then
  {S}install -m 0600 -o {USER} -g {USER} /root/.ssh/authorized_keys /home/{USER}/.ssh/authorized_keys
elif {S}test -s /home/ubuntu/.ssh/authorized_keys; then
  {S}install -m 0600 -o {USER} -g {USER} /home/ubuntu/.ssh/authorized_keys /home/{USER}/.ssh/authorized_keys
fi
printf '{USER} ALL=(ALL) NOPASSWD:ALL\n' | {S}tee {SUDOERS} >/dev/null
{S}chmod 0440 {SUDOERS}
{S}visudo -cqf {SUDOERS} || { {S}rm -f {SUDOERS}; exit 1; }
{S}usermod -aG docker {USER} >/dev/null 2>&1 || true`)
}

// RenderExecutionChannelUserReadyTest returns the test that proves the channel
// login exists and carries an authorized key. A host that lost it is not
// prepared, however complete the rest of the profile looks.
func RenderExecutionChannelUserReadyTest(privilege string) string {
	if privilege != "" {
		privilege += " "
	}
	return strings.NewReplacer("{S}", privilege, "{USER}", ExecutionChannelUser).Replace(
		`id -u {USER} >/dev/null 2>&1 && {S}test -s /home/{USER}/.ssh/authorized_keys`)
}

func executionChannelBootstrapScript() string {
	return `#!/bin/bash
set -euo pipefail
` + RenderExecutionChannelUserProvisioning("") + `
` + RenderExecutionChannelUserReadyTest("") + `
`
}

func guardInstallerCommand(readinessCommand, installerCommand, failureReport string) string {
	pipeline := installerCommand
	if readinessCommand = strings.TrimSpace(readinessCommand); readinessCommand != "" {
		pipeline = readinessCommand + " && " + installerCommand
	}
	return "set -o pipefail; timeout --signal=TERM --kill-after=1 54 /bin/bash -o pipefail -c " + shellQuote(pipeline) + " || { " + failureReport + "; }"
}

// GuardBootstrapScriptPath is the root-only first-boot script that carries the
// pairing capability on a managed node. The capability is never part of a
// process argument list (cloud-init runs the script by path, and the failure
// report reads its header from standard input), so another local user cannot
// read it from /proc/<pid>/cmdline while the installer runs. The script removes
// itself when the run ends; bootstrap residue redaction covers an interrupted
// run.
const GuardBootstrapScriptPath = "/usr/local/lib/kombify/guard-bootstrap"

// RenderCloudInit returns the #cloud-config document that installs and enrols
// the Guard on first boot.
//
// The result is a pure function of its input: the same input renders the same
// bytes. Callers fold this document into an at-most-once provisioning request
// digest, so any nondeterminism here would make a retry look like a different
// request and strand an already-created VM.
//
// No error returned by this function contains the pairing token.
func RenderCloudInit(in CloudInitInput) ([]byte, error) {
	origin, err := normalizeOrigin(in.ServerURL)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(in.PairingToken)
	parsed, parseErr := pairingtoken.Parse(token)
	if parseErr != nil || parsed.Legacy || parsed.TenantID == "" {
		return nil, ErrInvalidPairingToken
	}

	// KOMBI_TOKEN is exported by the script below; it reaches the installer as
	// environment, never as an argument.
	installerCommand := fmt.Sprintf(
		"curl -fsSL --connect-timeout 3 --max-time 10 --retry 2 --retry-max-time 20 --retry-connrefused --retry-delay 1 %s | KOMBI_SERVER=%s bash",
		shellQuote(origin+"/install.sh"), shellQuote(origin),
	)
	if in.HostPrepProfile.goldenImage() {
		// The baked Guard redeems the capability from a root-only tmpfs file
		// and activates its layout; the served installer would replace the
		// qualified binary. agent enroll deletes the file after use; the trap
		// removes it after a transient failure too.
		installerCommand = fmt.Sprintf(
			"capability=$(mktemp /run/kombify-pairing.XXXXXX) && trap 'rm -f \"$capability\"' EXIT && printf '%%s' \"$KOMBI_TOKEN\" > \"$capability\" && %s agent enroll --server %s --token-file \"$capability\" && %s guard layout apply --activate",
			goldenImageGuardPath, shellQuote(origin), goldenImageGuardPath,
		)
	}
	keys, err := authorizedKeyLines(in.AuthorizedKeys, in.HostPrepProfile)
	if err != nil {
		return nil, err
	}
	failureReport := fmt.Sprintf(
		"status=$?; reason=installer_failed; case $status in 42) reason=bootstrap_tools_missing;; 43) reason=ssh_firewall_failed;; 44) reason=execution_channel_failed;; 124|137) reason=first_boot_deadline_exceeded;; esac; printf 'Authorization: Bearer %%s\\n' \"$KOMBI_TOKEN\" | curl -sS --max-time 5 --connect-timeout 2 -X POST %s -H @- -H %s -H %s -H \"X-Kombify-Bootstrap-Reason: $reason\" --data-binary %s >/dev/null 2>&1 || true; exit $status",
		shellQuote(origin+"/api/v1/workers/bootstrap/logs"),
		shellQuote("X-Kombify-Log-Level: error"),
		shellQuote("X-Kombify-Log-Phase: cloud-init"),
		shellQuote("Cloud-init could not establish the execution channel or execute the Techstack installer."),
	)
	readinessCommand := "command -v curl >/dev/null && command -v systemctl >/dev/null || exit 42"
	if in.HostPrepProfile.goldenImage() {
		readinessCommand += "; test -x " + goldenImageGuardPath + " || exit 42"
	}
	// cloud-init's packages module runs before runcmd, outside this deadline.
	// A missing firewall must spend the same bounded bootstrap budget instead.
	readinessCommand += "; if ! command -v ufw >/dev/null; then command -v apt-get >/dev/null || exit 42; timeout --signal=TERM --kill-after=1 12 env DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=3 -o Acquire::Retries=0 -o Acquire::http::Timeout=3 -o Acquire::https::Timeout=3 install -y --no-install-recommends ufw || exit 42; fi; command -v ufw >/dev/null || exit 42"
	for _, firewallCommand := range ubuntuSSHFirewallCommands() {
		readinessCommand += "; " + strings.Join(firewallCommand, " ") + " || exit 43"
	}
	if in.HostPrepProfile.stackKitReady() {
		readinessCommand += "; " + executionChannelBootstrapPath + " || exit 44"
	}
	command := guardInstallerCommand(readinessCommand, installerCommand, failureReport)
	// The script is embedded in a YAML block scalar below, one line per line.
	// Shell quoting uses single quotes only, and both the origin and the token
	// are already restricted to characters that exclude these, so a hit here
	// means an upstream validator regressed rather than an input needing
	// escaping.
	if strings.ContainsAny(command, "\n\r") || strings.ContainsAny(origin+token, "\"\\\n\r'") {
		return nil, ErrUnrenderablePayload
	}
	script := "#!/bin/bash\n" +
		"trap 'rm -f \"$0\"' EXIT\n" +
		"export KOMBI_TOKEN=" + shellQuote(token) + "\n" +
		command + "\n"

	hostname := strings.TrimSpace(in.Hostname)
	if hostname != "" && !validHostnameLabel(hostname) {
		return nil, ErrInvalidHostname
	}

	var document strings.Builder
	document.WriteString("#cloud-config\n")
	if hostname != "" {
		document.WriteString("hostname: " + hostname + "\n")
		document.WriteString("preserve_hostname: false\n")
	}
	document.WriteString("package_update: false\n")
	if len(keys) > 0 {
		// Without disable_root: false cloud-init prefixes root's keys with a
		// forced "login as the default user" command, and the execution channel
		// copies root's keys first.
		document.WriteString("disable_root: false\n")
		document.WriteString("ssh_authorized_keys:\n")
		for _, key := range keys {
			document.WriteString("  - '" + key + "'\n")
		}
	}
	document.WriteString("write_files:\n")
	// The capability file is root-only; it is created before anything runs.
	document.WriteString("  - path: " + GuardBootstrapScriptPath + "\n")
	document.WriteString("    owner: root:root\n")
	document.WriteString("    permissions: '0700'\n")
	document.WriteString("    content: |\n")
	for _, line := range strings.Split(strings.TrimRight(script, "\n"), "\n") {
		document.WriteString("      " + line + "\n")
	}
	if in.HostPrepProfile.stackKitReady() {
		document.WriteString("  - path: /var/lib/kombify/host-prep/v2.status\n")
		document.WriteString("    permissions: '0644'\n")
		document.WriteString("    content: |\n      status=pending\n")
		document.WriteString("  - path: /usr/local/lib/kombify/host-prep-v2\n")
		document.WriteString("    permissions: '0755'\n")
		document.WriteString("    content: |\n")
		for _, line := range strings.Split(stackKitReadyUbuntuHostPrepV2(), "\n") {
			document.WriteString("      " + line + "\n")
		}
		document.WriteString("  - path: /etc/systemd/system/kombify-host-prep-v2.service\n")
		document.WriteString("    permissions: '0644'\n")
		document.WriteString("    content: |\n")
		document.WriteString("      [Unit]\n      Description=Kombify StackKit-ready host preparation v2\n      After=network-online.target\n      Wants=network-online.target\n")
		document.WriteString("      [Service]\n      Type=oneshot\n      ExecStart=/usr/local/lib/kombify/host-prep-v2\n      RemainAfterExit=yes\n")
		document.WriteString("      [Install]\n      WantedBy=multi-user.target\n")
		document.WriteString("  - path: " + executionChannelBootstrapPath + "\n")
		document.WriteString("    permissions: '0755'\n")
		document.WriteString("    content: |\n")
		for _, line := range strings.Split(executionChannelBootstrapScript(), "\n") {
			document.WriteString("      " + line + "\n")
		}
	}
	document.WriteString("output:\n")
	document.WriteString("  all: '| tee -a " + BootstrapLogPath + "'\n")
	document.WriteString("runcmd:\n")
	// List form: cloud-init execs the argv directly, so no second shell parses
	// the document and YAML cannot be escaped out of.
	document.WriteString("  - [\"" + GuardBootstrapScriptPath + "\"]\n")
	if in.HostPrepProfile.stackKitReady() {
		document.WriteString("  - [\"/bin/systemctl\", \"enable\", \"--now\", \"--no-block\", \"kombify-host-prep-v2.service\"]\n")
	}
	return []byte(document.String()), nil
}

// normalizeOrigin accepts only a scheme+host https origin and strips any path,
// query or fragment so the derived /install.sh URL cannot be redirected.
func normalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrInsecureServerURL
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", ErrInsecureServerURL
	}
	if strings.ContainsAny(parsed.Host, "'\"\\ ") {
		return "", ErrInsecureServerURL
	}
	return "https://" + parsed.Host, nil
}

// authorizedKeyLines admits only single-line OpenSSH public keys, and only for
// a Golden Image profile. Each is emitted inside YAML single quotes, so a key
// may not contain one.
func authorizedKeyLines(keys []string, profile HostPrepProfile) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if !profile.goldenImage() {
		return nil, ErrInvalidAuthorizedKey
	}
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		fields := strings.Fields(key)
		if len(key) == 0 || len(key) > 16<<10 || len(fields) < 2 || !printableASCII(key) || strings.ContainsAny(key, "'\\\"") ||
			!(strings.HasPrefix(fields[0], "ssh-") || strings.HasPrefix(fields[0], "ecdsa-sha2-") || strings.HasPrefix(fields[0], "sk-")) {
			return nil, ErrInvalidAuthorizedKey
		}
		lines = append(lines, key)
	}
	return lines, nil
}

// printableASCII admits a key line made of visible ASCII and spaces only, so
// no line break of any encoding can split it inside the YAML document.
func printableASCII(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

// shellQuote wraps a value in POSIX single quotes, which suppress every form of
// expansion. An embedded single quote is closed, escaped and reopened.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// validHostnameLabel accepts a single RFC-1123 label: lowercase alphanumerics
// and dashes, no leading/trailing dash, at most 63 characters. The restriction
// also guarantees the value is YAML-safe without quoting.
func validHostnameLabel(value string) bool {
	if len(value) == 0 || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}
