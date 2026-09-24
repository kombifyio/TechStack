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
}

type HostPrepProfile string

const (
	// HostPrepProfileIONOSUbuntu2404DockerV1 is the IONOS Ubuntu image
	// contract. It now prepares the same StackKit-ready host as
	// HostPrepProfileStackKitReadyUbuntu2404V1 so Cloud host-security can
	// enable nftables, fail2ban, and unattended-upgrades.
	HostPrepProfileIONOSUbuntu2404DockerV1 HostPrepProfile = "ionos-ubuntu-24.04-docker-v1"
	// HostPrepProfileStackKitReadyUbuntu2404V1 is the provider-neutral
	// Ubuntu 24.04 contract used by Centron (and any later Ubuntu image)
	// so apply does not start without host-security packages.
	HostPrepProfileStackKitReadyUbuntu2404V1 HostPrepProfile = "stackkit-ready-ubuntu-24.04-v1"
)

func (p HostPrepProfile) stackKitReady() bool {
	return p == HostPrepProfileIONOSUbuntu2404DockerV1 || p == HostPrepProfileStackKitReadyUbuntu2404V1
}

// ExecutionChannelUser is the login the control plane keeps on a managed node.
// Cloud host-security disables root SSH as part of the hardening it must
// enforce, so the execution channel cannot be root and survive its own kit.
const ExecutionChannelUser = "kombify"

const executionChannelSudoersPath = "/etc/sudoers.d/60-kombify-execution-channel"

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

	installerCommand := fmt.Sprintf(
		"curl -fsSL --retry 5 --retry-connrefused --retry-delay 10 %s | KOMBI_SERVER=%s KOMBI_TOKEN=%s bash",
		shellQuote(origin+"/install.sh"), shellQuote(origin), shellQuote(token),
	)
	failureReport := fmt.Sprintf(
		"status=$?; curl -sS --max-time 5 --connect-timeout 2 -X POST %s -H %s -H %s -H %s --data-binary %s >/dev/null 2>&1 || true; exit $status",
		shellQuote(origin+"/api/v1/workers/bootstrap/logs"),
		shellQuote("Authorization: Bearer "+token),
		shellQuote("X-Kombify-Log-Level: error"),
		shellQuote("X-Kombify-Log-Phase: cloud-init"),
		shellQuote("Cloud-init could not download or execute the Techstack installer."),
	)
	command := "set -o pipefail; " + installerCommand + " || { " + failureReport + "; }"
	// The command is embedded in a YAML double-quoted scalar below. Shell
	// quoting uses single quotes only, and both the origin and the token are
	// already restricted to characters that exclude these, so a hit here means
	// an upstream validator regressed rather than an input needing escaping.
	if strings.ContainsAny(command, "\"\\\n\r") {
		return nil, ErrUnrenderablePayload
	}

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
	document.WriteString("package_update: true\n")
	document.WriteString("packages:\n")
	document.WriteString("  - ufw\n")
	if in.HostPrepProfile.stackKitReady() {
		document.WriteString("write_files:\n")
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
	}
	document.WriteString("output:\n")
	document.WriteString("  all: '| tee -a " + BootstrapLogPath + "'\n")
	document.WriteString("runcmd:\n")
	// Provider NICs are not a sufficient host policy. Establish an SSH-only
	// first-boot baseline before downloading or running the Guard installer;
	// StackKit rollout must explicitly open any later published service ports.
	document.WriteString("  - [\"/usr/sbin/ufw\", \"default\", \"deny\", \"incoming\"]\n")
	document.WriteString("  - [\"/usr/sbin/ufw\", \"default\", \"allow\", \"outgoing\"]\n")
	document.WriteString("  - [\"/usr/sbin/ufw\", \"allow\", \"22/tcp\"]\n")
	document.WriteString("  - [\"/usr/sbin/ufw\", \"--force\", \"enable\"]\n")
	if in.HostPrepProfile.stackKitReady() {
		document.WriteString("  - [\"/bin/systemctl\", \"enable\", \"--now\", \"--no-block\", \"kombify-host-prep-v2.service\"]\n")
	}
	// List form: cloud-init execs the argv directly, so no second shell parses
	// the document and YAML cannot be escaped out of.
	document.WriteString("  - [\"/bin/bash\", \"-c\", \"" + command + "\"]\n")
	return []byte(document.String()), nil
}

func stackKitReadyUbuntuHostPrepV2() string {
	return `#!/bin/bash
set -euo pipefail
state_dir=/var/lib/kombify/host-prep
status_file=$state_dir/v2.status
mkdir -p "$state_dir"
exec 9>"$state_dir/v2.lock"
flock -n 9 || exit 0
stackkit_ready() {
  docker info >/dev/null 2>&1 &&
    command -v nft >/dev/null 2>&1 &&
    systemctl is-enabled --quiet fail2ban &&
    systemctl is-enabled --quiet unattended-upgrades &&
    sshd -t >/dev/null 2>&1 &&
    { ! command -v ufw >/dev/null 2>&1 || ! ufw status 2>/dev/null | grep -q '^Status: active' || { ! systemctl is-active --quiet nftables && ! systemctl is-enabled --quiet nftables; }; } &&
    ` + RenderExecutionChannelUserReadyTest("") + `
}
if grep -qx 'status=ready' "$status_file" 2>/dev/null && stackkit_ready; then exit 0; fi
printf 'status=pending\n' >"$status_file.tmp"
mv -f "$status_file.tmp" "$status_file"
failed() { printf 'status=failed\n' >"$status_file.tmp"; mv -f "$status_file.tmp" "$status_file"; }
trap failed ERR
export DEBIAN_FRONTEND=noninteractive
for _ in $(seq 1 15); do
  if ! pgrep -x apt >/dev/null && ! pgrep -x apt-get >/dev/null && ! pgrep -x dpkg >/dev/null && ! pgrep -f unattended-upgrade >/dev/null; then break; fi
  sleep 2
done
timeout 40 apt-get -o DPkg::Lock::Timeout=20 -o Acquire::Retries=1 -o Acquire::http::Timeout=10 -o Acquire::https::Timeout=10 update
timeout 90 apt-get install -y -o DPkg::Lock::Timeout=20 -o Acquire::Retries=1 -o Acquire::http::Timeout=10 -o Acquire::https::Timeout=10 ca-certificates curl docker.io docker-compose-v2 nftables fail2ban unattended-upgrades openssh-server
systemctl enable --now docker
# Install nftables for the nft binary. Do not start that service: Ubuntu
# cloud-init already owns incoming with ufw (allow 22). Starting the
# service flushes that ruleset and can make 22/443/ICMP silent.
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q '^Status: active'; then
  ufw allow 22/tcp || true
  # apt install nftables enables the unit even though we never start it.
  # That flushes the ufw SSH-only baseline and breaks Docker Compose Apply.
  systemctl disable --now nftables >/dev/null 2>&1 || true
fi
systemctl enable --now fail2ban
systemctl enable --now unattended-upgrades
mkdir -p /etc/ssh/sshd_config.d /run/sshd
chmod 0755 /run/sshd
# Establish the execution channel before sshd -t. Ubuntu's sshd refuses
# to test its config until the privilege-separation directory exists, and
# a first-boot that dies there never copies the provider key onto kombify.
` + RenderExecutionChannelUserProvisioning("") + `
printf 'PasswordAuthentication no\nKbdInteractiveAuthentication no\n' >/etc/ssh/sshd_config.d/zz-kombify-host-prep.conf
sshd -t
systemctl reload ssh || systemctl reload sshd || true
stackkit_ready
printf 'status=ready\n' >"$status_file.tmp"
mv -f "$status_file.tmp" "$status_file"
trap - ERR`
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
