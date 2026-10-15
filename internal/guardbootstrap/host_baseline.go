package guardbootstrap

import (
	"errors"
	"strings"
)

const ubuntuHostPackages = "ca-certificates curl docker.io docker-compose-v2 nftables fail2ban unattended-upgrades openssh-server ufw"

func ubuntuSSHFirewallCommands() [][]string {
	return [][]string{
		{"/usr/sbin/ufw", "default", "deny", "incoming"},
		{"/usr/sbin/ufw", "default", "allow", "outgoing"},
		{"/usr/sbin/ufw", "allow", "22/tcp"},
		{"/usr/sbin/ufw", "--force", "enable"},
	}
}

func ubuntuSSHFirewallSetup() string {
	var script strings.Builder
	// Stop nftables before applying UFW: nftables.service's stop can flush rules.
	script.WriteString("systemctl disable --now nftables >/dev/null 2>&1 || true\n")
	for _, command := range ubuntuSSHFirewallCommands() {
		script.WriteString(strings.Join(command, " ") + "\n")
	}
	return script.String()
}

// UbuntuHostBaselinePackages is the package authority for installation and
// version evidence; callers must not maintain a separate Golden Image list.
func UbuntuHostBaselinePackages() []string {
	return strings.Fields(ubuntuHostPackages)
}

// RenderNeutralUbuntuHostBaseline reuses the provider host preparation without
// creating an execution-channel account, copying keys, or enrolling a Guard.
// The caller supplies the immutable OS choice and executes the script as root.
func RenderNeutralUbuntuHostBaseline(version, operation string) (string, error) {
	if UbuntuHostPrepProfile(version) == "" {
		return "", errors.New("host baseline requires Ubuntu 26.04 or 24.04")
	}
	if operation != "prepare" && operation != "verify" && operation != "check-os" {
		return "", errors.New("host baseline operation must be prepare, verify or check-os")
	}
	script := "#!/bin/bash\nset -Eeuo pipefail\n" +
		"trap 'printf \"KOMBIFY_HOST_BASELINE_FAILURE_LINE:%s\\n\" \"$LINENO\" >&2' ERR\n" +
		". /etc/os-release\n" +
		"test \"$ID\" = ubuntu\n" +
		"test \"$VERSION_ID\" = " + shellQuote(version) + "\n"
	if operation == "check-os" {
		return script, nil
	}
	script += "export LC_ALL=C\nstackkit_ready() {\n" + ubuntuHostReadyTest() + ` &&
    ufw status | grep -q '^Status: active' &&
    ufw status verbose | grep -Eq '^Default: deny \(incoming\), allow \(outgoing\), (deny|disabled) \(routed\)$' &&
    ufw status | awk '
      {
        line=$0
        gsub(/[[:space:]]+/, " ", line)
        sub(/^ /, "", line); sub(/ $/, "", line)
        if (line=="Status: active" || line=="To Action From" || line ~ /^[ -]*$/) next
        if (line ~ /(^| )FWD( |$)/) { unsafe=1; next }
        if (line ~ /^22\/tcp ALLOW( IN)? Anywhere$/ ||
            line ~ /^22\/tcp \(v6\) ALLOW( IN)? Anywhere \(v6\)$/) { ssh=1; next }
        if (line ~ /(^| )(ALLOW|LIMIT)( |$)/) { unsafe=1; next }
        if (line ~ /(^| )(DENY|REJECT)( |$)/) next
        unsafe=1
      }
      END { exit unsafe || !ssh }
    ' || { printf 'KOMBIFY_HOST_BASELINE_FAILURE_CHECK:neutral_firewall\n' >&2; return 1; }
}
`
	if operation == "prepare" {
		script += ubuntuHostPreparation("", ubuntuSSHFirewallSetup())
	}
	script += "stackkit_ready\n"
	return script, nil
}

// These two fragments are the single host-security/package baseline shared by
// neutral image baking and provider first boot. Enrollment/channel state is
// supplied only by the provider wrapper.
func ubuntuHostReadyTest() string {
	checks := []struct{ name, command string }{
		{"docker", "docker info >/dev/null 2>&1"},
		{"nft", "command -v nft >/dev/null 2>&1"},
		{"fail2ban_enabled", "systemctl is-enabled --quiet fail2ban"},
		{"fail2ban_active", "systemctl is-active --quiet fail2ban"},
		{"updates_enabled", "systemctl is-enabled --quiet unattended-upgrades"},
		{"updates_active", "systemctl is-active --quiet unattended-upgrades"},
		{"sshd_syntax", "sshd -t >/dev/null 2>&1"},
		// Drain sshd output: grep -q can close the pipe early and make a valid
		// configuration fail under pipefail when sshd receives SIGPIPE.
		{"sshd_password", "sshd -T | grep -x 'passwordauthentication no' >/dev/null"},
		{"sshd_keyboard", "sshd -T | grep -x 'kbdinteractiveauthentication no' >/dev/null"},
		{"firewall_owner", "! command -v ufw >/dev/null 2>&1 || ! ufw status 2>/dev/null | grep -q '^Status: active' || { ! systemctl is-active --quiet nftables && ! systemctl is-enabled --quiet nftables; }"},
	}
	var script strings.Builder
	for _, check := range checks {
		script.WriteString("  { " + check.command + "; } || { printf 'KOMBIFY_HOST_BASELINE_FAILURE_CHECK:" + check.name + "\\n' >&2; return 1; }\n")
	}
	return script.String() + "  true"
}

func ubuntuHostPreparation(channelProvisioning, firewallSetup string) string {
	return `export DEBIAN_FRONTEND=noninteractive
for _ in $(seq 1 15); do
  if ! pgrep -x apt >/dev/null && ! pgrep -x apt-get >/dev/null && ! pgrep -x dpkg >/dev/null && ! pgrep -f unattended-upgrade >/dev/null; then break; fi
  sleep 2
done
timeout 40 apt-get -o DPkg::Lock::Timeout=20 -o Acquire::Retries=1 -o Acquire::http::Timeout=10 -o Acquire::https::Timeout=10 update
timeout 90 apt-get install -y -o DPkg::Lock::Timeout=20 -o Acquire::Retries=1 -o Acquire::http::Timeout=10 -o Acquire::https::Timeout=10 ` + ubuntuHostPackages + `
systemctl enable --now docker
` + firewallSetup + `
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
# sshd needs its privilege-separation directory before the syntax check.
# The provider wrapper also establishes its execution channel here.
` + channelProvisioning + `
printf 'PasswordAuthentication no\nKbdInteractiveAuthentication no\n' >/etc/ssh/sshd_config.d/zz-kombify-host-prep.conf
sshd -t
systemctl reload ssh || systemctl reload sshd || true
`
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
` + ubuntuHostReadyTest() + ` &&
    ` + RenderExecutionChannelUserReadyTest("") + `
}
if grep -qx 'status=ready' "$status_file" 2>/dev/null && stackkit_ready; then exit 0; fi
printf 'status=pending\n' >"$status_file.tmp"
mv -f "$status_file.tmp" "$status_file"
failed() { printf 'status=failed\n' >"$status_file.tmp"; mv -f "$status_file.tmp" "$status_file"; }
trap failed ERR
` + ubuntuHostPreparation(RenderExecutionChannelUserProvisioning(""), "") + `stackkit_ready
printf 'status=ready\n' >"$status_file.tmp"
mv -f "$status_file.tmp" "$status_file"
trap - ERR`
}
