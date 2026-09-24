package guardbootstrap

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/pairingtoken"
)

func testToken(t *testing.T, tenant, scope string) string {
	t.Helper()
	raw, _, err := pairingtoken.Derive(bytes.Repeat([]byte{0x5a}, 32), tenant, scope)
	if err != nil {
		t.Fatalf("derive test token: %v", err)
	}
	return raw
}

// The document is folded into the at-most-once provisioning request digest, so
// two renders of the same input must be byte-identical or a retry looks like a
// different request and strands an already-created VM.
func TestRenderIsByteIdentical(t *testing.T) {
	in := CloudInitInput{ServerURL: "https://techstack.kombify.io", PairingToken: testToken(t, "tenant-demo", "op_1")}
	first, err := RenderCloudInit(in)
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	second, err := RenderCloudInit(in)
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("render is not deterministic:\n%s\n---\n%s", first, second)
	}
}

func TestRenderInvokesTheServedInstaller(t *testing.T) {
	token := testToken(t, "tenant-demo", "op_1")
	document, err := RenderCloudInit(CloudInitInput{
		ServerURL: "https://techstack.kombify.io", PairingToken: token,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	rendered := string(document)
	if !strings.HasPrefix(rendered, "#cloud-config\n") {
		t.Fatalf("document is not a cloud-config:\n%s", rendered)
	}
	for _, want := range []string{
		"https://techstack.kombify.io/install.sh",
		"KOMBI_SERVER='https://techstack.kombify.io'",
		"KOMBI_TOKEN='" + token + "'",
		BootstrapLogPath,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("document is missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderEnablesSSHOnlyHostFirewallBeforeInstaller(t *testing.T) {
	document, err := RenderCloudInit(CloudInitInput{
		ServerURL: "https://techstack.kombify.io", PairingToken: testToken(t, "tenant-demo", "op_1"),
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	rendered := string(document)
	ordered := []string{
		"  - ufw\n",
		`["/usr/sbin/ufw", "default", "deny", "incoming"]`,
		`["/usr/sbin/ufw", "default", "allow", "outgoing"]`,
		`["/usr/sbin/ufw", "allow", "22/tcp"]`,
		`["/usr/sbin/ufw", "--force", "enable"]`,
		"install.sh",
	}
	position := -1
	for _, fragment := range ordered {
		next := strings.Index(rendered[position+1:], fragment)
		if next < 0 {
			t.Fatalf("firewall document missing %q:\n%s", fragment, rendered)
		}
		position += next + 1
	}
	for _, forbidden := range []string{"allow any", "allow 0.0.0.0/0", "disable"} {
		if strings.Contains(strings.ToLower(rendered), forbidden) {
			t.Fatalf("firewall baseline contains broad/disabled rule %q:\n%s", forbidden, rendered)
		}
	}
}

func TestRenderIONOSUbuntuDockerHostPrepIsExplicitAndVersioned(t *testing.T) {
	document, err := RenderCloudInit(CloudInitInput{
		ServerURL:       "https://techstack.kombify.io",
		PairingToken:    testToken(t, "tenant-demo", "op_1"),
		HostPrepProfile: HostPrepProfileIONOSUbuntu2404DockerV1,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	rendered := string(document)
	for _, want := range []string{
		"/usr/local/lib/kombify/host-prep-v2",
		"/var/lib/kombify/host-prep/v2.status",
		"status=pending",
		"flock -n 9",
		"set -euo pipefail",
		"apt-get install -y",
		"timeout 40 apt-get",
		"timeout 90 apt-get install",
		"nftables fail2ban unattended-upgrades openssh-server",
		"systemctl enable --now docker",
		"systemctl enable --now fail2ban",
		"systemctl enable --now unattended-upgrades",
		"PasswordAuthentication no",
		"/run/sshd",
		"status=ready",
		"kombify",
		"/home/kombify/.ssh/authorized_keys",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("IONOS host-prep cloud-init is missing %q:\n%s", want, rendered)
		}
	}
	if strings.Index(rendered, "trap failed ERR") > strings.LastIndex(rendered, "status=ready") {
		t.Fatal("failure trap must be active before any command can publish ready")
	}
	if strings.Contains(rendered, "DPkg::Lock::Timeout=120") || strings.Contains(rendered, "seq 1 90") {
		t.Fatal("host preparation exceeds the bounded bootstrap observation contract")
	}
	if strings.Contains(rendered, "systemctl enable --now nftables") {
		t.Fatal("host-prep must not start nftables.service on a ufw-owned Ubuntu host")
	}
	if !strings.Contains(rendered, "systemctl disable --now nftables") {
		t.Fatal("host-prep must disable nftables.service after apt enables it")
	}
	if !strings.Contains(rendered, "systemctl is-enabled --quiet nftables") {
		t.Fatal("host-prep ready must fail while nftables.service stays enabled on a ufw-owned host")
	}
	// The provider-neutral default must not silently make every managed image
	// an Ubuntu/Docker image.
	baseline, err := RenderCloudInit(CloudInitInput{
		ServerURL: "https://techstack.kombify.io", PairingToken: testToken(t, "tenant-demo", "op_1"),
	})
	if err != nil {
		t.Fatalf("render baseline: %v", err)
	}
	if strings.Contains(string(baseline), "/usr/local/lib/kombify/host-prep-v2") {
		t.Fatalf("default cloud-init unexpectedly includes StackKit-ready host prep:\n%s", baseline)
	}
}

func TestStackKitReadyHostPrepCreatesSSHPrivilegeDirBeforeConfigCheck(t *testing.T) {
	document, err := RenderCloudInit(CloudInitInput{
		ServerURL:       "https://techstack.kombify.io",
		PairingToken:    testToken(t, "tenant-demo", "op_1"),
		HostPrepProfile: HostPrepProfileStackKitReadyUbuntu2404V1,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	rendered := string(document)
	mkdirAt := strings.Index(rendered, "mkdir -p /etc/ssh/sshd_config.d /run/sshd")
	useraddAt := strings.Index(rendered, "useradd --create-home --shell /bin/bash "+ExecutionChannelUser)
	sshdTestAt := strings.LastIndex(rendered, "sshd -t")
	if mkdirAt < 0 || useraddAt < 0 || sshdTestAt < 0 {
		t.Fatalf("host-prep is missing the SSH privilege dir, execution channel, or sshd -t:\n%s", rendered)
	}
	if !(mkdirAt < useraddAt && useraddAt < sshdTestAt) {
		t.Fatalf("first-boot must create /run/sshd and the kombify login before sshd -t; order mkdir=%d useradd=%d sshd-t=%d", mkdirAt, useraddAt, sshdTestAt)
	}
}

func TestExecutionChannelUserProvisioningKeepsANonRootLogin(t *testing.T) {
	got := RenderExecutionChannelUserProvisioning("")
	if !strings.Contains(got, ExecutionChannelUser) || !strings.Contains(got, "authorized_keys") {
		t.Fatalf("provisioning does not establish the execution-channel login:\n%s", got)
	}
	if strings.Contains(got, "PermitRootLogin yes") {
		t.Fatal("provisioning must not re-enable root SSH")
	}
	ready := RenderExecutionChannelUserReadyTest("")
	if !strings.Contains(ready, ExecutionChannelUser) || !strings.Contains(ready, "authorized_keys") {
		t.Fatalf("ready test does not observe the execution-channel login:\n%s", ready)
	}
}

// Cloud host-security disables root SSH. A host preparation that reports ready
// without leaving a non-root login behind hands back a node the control plane
// can no longer reach, so the channel is part of readiness rather than a later
// convenience.
func TestStackKitReadyHostPrepKeepsANonRootExecutionChannel(t *testing.T) {
	document, err := RenderCloudInit(CloudInitInput{
		ServerURL: "https://techstack.kombify.io", PairingToken: testToken(t, "tenant-demo", "op_1"),
		HostPrepProfile: HostPrepProfileStackKitReadyUbuntu2404V1,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	rendered := string(document)
	if ExecutionChannelUser == "root" {
		t.Fatal("the execution channel must not be the login host-security disables")
	}
	for _, want := range []string{
		"useradd --create-home --shell /bin/bash " + ExecutionChannelUser,
		"/root/.ssh/authorized_keys /home/" + ExecutionChannelUser + "/.ssh/authorized_keys",
		"visudo -cqf " + executionChannelSudoersPath,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("host prep does not establish the execution channel (%q):\n%s", want, rendered)
		}
	}
	// An unvalidated privilege grant must not survive the render.
	grant := strings.Index(rendered, "tee "+executionChannelSudoersPath)
	validation := strings.Index(rendered, "visudo -cqf "+executionChannelSudoersPath)
	if grant < 0 || validation < grant {
		t.Fatalf("the sudoers grant is not validated after it is written:\n%s", rendered)
	}
	readyTest := RenderExecutionChannelUserReadyTest("")
	if !strings.Contains(rendered, readyTest) {
		t.Fatalf("readiness does not prove the execution channel (%q):\n%s", readyTest, rendered)
	}
}

func TestRenderStackKitReadyProfileMatchesIONOSHostPrep(t *testing.T) {
	token := testToken(t, "tenant-demo", "op_1")
	ionos, err := RenderCloudInit(CloudInitInput{
		ServerURL: "https://techstack.kombify.io", PairingToken: token,
		HostPrepProfile: HostPrepProfileIONOSUbuntu2404DockerV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	centron, err := RenderCloudInit(CloudInitInput{
		ServerURL: "https://techstack.kombify.io", PairingToken: token,
		HostPrepProfile: HostPrepProfileStackKitReadyUbuntu2404V1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ionos, centron) {
		t.Fatal("IONOS and Centron StackKit-ready host-prep documents must be identical")
	}
}

// A path, query or fragment on the configured origin must not be able to point
// the installer download somewhere else.
func TestRenderPinsTheOriginAndDropsAnyPath(t *testing.T) {
	document, err := RenderCloudInit(CloudInitInput{
		ServerURL:    "https://techstack.kombify.io/evil?x=1#y",
		PairingToken: testToken(t, "tenant-demo", "op_1"),
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	rendered := string(document)
	if strings.Contains(rendered, "evil") || strings.Contains(rendered, "x=1") {
		t.Fatalf("origin path/query survived into the payload:\n%s", rendered)
	}
	if !strings.Contains(rendered, "'https://techstack.kombify.io/install.sh'") {
		t.Fatalf("installer URL was not pinned to the bare origin:\n%s", rendered)
	}
}

// A managed node reaches the control plane across the public internet; the
// pairing token must never travel unencrypted, and a non-canonical token must
// not reach a VM that would then sit forever unenrolled.
func TestRenderRefusesUnsafeInput(t *testing.T) {
	valid := testToken(t, "tenant-demo", "op_1")
	for name, tc := range map[string]struct {
		in      CloudInitInput
		wantErr error
	}{
		"http origin":      {CloudInitInput{ServerURL: "http://techstack.kombify.io", PairingToken: valid}, ErrInsecureServerURL},
		"empty origin":     {CloudInitInput{ServerURL: "", PairingToken: valid}, ErrInsecureServerURL},
		"origin with user": {CloudInitInput{ServerURL: "https://user:pw@techstack.kombify.io", PairingToken: valid}, ErrInsecureServerURL}, //nolint:gosec // fixture: userinfo origins must be refused
		"quote in origin":  {CloudInitInput{ServerURL: "https://tech'stack.kombify.io", PairingToken: valid}, ErrInsecureServerURL},
		"empty token":      {CloudInitInput{ServerURL: "https://techstack.kombify.io", PairingToken: ""}, ErrInvalidPairingToken},
		"garbage token":    {CloudInitInput{ServerURL: "https://techstack.kombify.io", PairingToken: "not-a-token"}, ErrInvalidPairingToken},
		"legacy token":     {CloudInitInput{ServerURL: "https://techstack.kombify.io", PairingToken: strings.Repeat("A", 32)}, ErrInvalidPairingToken},
		"uppercase hostname": {CloudInitInput{
			ServerURL: "https://techstack.kombify.io", PairingToken: valid, Hostname: "Demo-Cloud",
		}, ErrInvalidHostname},
		"hostname with dot": {CloudInitInput{
			ServerURL: "https://techstack.kombify.io", PairingToken: valid, Hostname: "demo.cloud",
		}, ErrInvalidHostname},
	} {
		t.Run(name, func(t *testing.T) {
			document, err := RenderCloudInit(tc.in)
			if err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if document != nil {
				t.Fatalf("a refused render still returned a document:\n%s", document)
			}
		})
	}
}

func TestRenderSetsFirstBootHostname(t *testing.T) {
	token := testToken(t, "tenant-demo", "op_1")
	document, err := RenderCloudInit(CloudInitInput{
		ServerURL: "https://techstack.kombify.io", PairingToken: token, Hostname: "demo-cloud-3f9a2b",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(document), "hostname: demo-cloud-3f9a2b\n") ||
		!strings.Contains(string(document), "preserve_hostname: false\n") {
		t.Fatalf("hostname directives missing:\n%s", document)
	}
	// Without a hostname the directives must be absent so existing digests of
	// hostname-less documents stay reproducible.
	document, err = RenderCloudInit(CloudInitInput{ServerURL: "https://techstack.kombify.io", PairingToken: token})
	if err != nil {
		t.Fatalf("render without hostname: %v", err)
	}
	if strings.Contains(string(document), "hostname:") {
		t.Fatalf("unexpected hostname directive:\n%s", document)
	}
}

func TestDeriveNodeHostnameIsProviderNeutralAndStable(t *testing.T) {
	first := DeriveNodeHostname("Demo Homelab", "lease-abc")
	second := DeriveNodeHostname("Demo Homelab", "lease-abc")
	if first != second {
		t.Fatalf("derivation is not stable: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "demo-homelab-") || !validHostnameLabel(first) {
		t.Fatalf("hostname %q should be a sanitized stack-derived label", first)
	}
	if other := DeriveNodeHostname("Demo Homelab", "lease-xyz"); other == first {
		t.Fatalf("two leases derived the same hostname %q", other)
	}
	if fallback := DeriveNodeHostname("", "lease-abc"); !strings.HasPrefix(fallback, "kombify-") {
		t.Fatalf("empty stack name should fall back to kombify prefix, got %q", fallback)
	}
}

// Errors are surfaced into provider logs and ledger receipts, so none of them
// may carry the capability itself.
func TestRenderErrorsNeverCarryTheToken(t *testing.T) {
	token := testToken(t, "tenant-demo", "op_1")
	_, err := RenderCloudInit(CloudInitInput{ServerURL: "http://techstack.kombify.io", PairingToken: token})
	if err == nil {
		t.Fatal("expected the http origin to be refused")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked the pairing token: %v", err)
	}
	secret := strings.Split(token, ".")[2]
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the token secret: %v", err)
	}
}
