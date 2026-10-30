// Package guardlayout installs and migrates the privilege-split layout of a
// kombify Guard host: a dedicated unprivileged system user for the daemon, a
// socket-activated privileged executor, and the protected state directories
// between them. The unit files are embedded here so the installer, the
// migration and the documentation share one source.
package guardlayout

import (
	"fmt"
	"strings"
)

// Unit and file names.
const (
	AgentUnit          = "techstack-agent.service"
	ExecutorSocketUnit = "techstack-guard-executor.socket"
	ExecutorUnit       = "techstack-guard-executor@.service"
	LegacyUnitSuffix   = ".legacy"
	ExecutorEnvFile    = "/etc/techstack/executor.env"
	// EnrollmentFile is where the Guard keeps the enrollment response with its
	// worker credential; the daemon owns it, the executor cannot see it.
	EnrollmentFile = "/etc/techstack/agent-enrollment.json"
)

// ExecutorInstancePrefix starts the name of every instance systemd spawns
// from ExecutorUnit: the socket accepts each connection into its own unit
// (techstack-guard-executor@<n>-<pid>-<uid>.service), one per Guard command.
const ExecutorInstancePrefix = "techstack-guard-executor@"

// IsExecutorInstance reports whether a systemd unit name is one per-connection
// executor instance. Such an instance is Guard plumbing that lives for one
// command, never a service of the host.
func IsExecutorInstance(unit string) bool {
	unit = strings.ToLower(strings.TrimSpace(unit))
	return strings.HasPrefix(unit, ExecutorInstancePrefix) && strings.HasSuffix(unit, ".service") &&
		len(unit) > len(ExecutorInstancePrefix)+len(".service")
}

// UnitParams parameterize the embedded units.
type UnitParams struct {
	BinaryPath     string
	OperationsPath string
	GuardUser      string
	// SocketPath is the privileged executor's socket.
	SocketPath string
	// TypeLines are the Type/notify/watchdog lines of the daemon; an older
	// binary that cannot signal readiness keeps Type=simple.
	NotifyReady bool
}

// AgentUnitText renders the unprivileged daemon's unit. It holds the network,
// the host identity, the access token and the journal, and nothing else: it
// cannot gain privileges, cannot write outside its own state, and has no
// capabilities. Every host effect goes through the executor socket.
func AgentUnitText(p UnitParams) string {
	typeLines := "Type=simple"
	if p.NotifyReady {
		typeLines = "Type=notify\nNotifyAccess=main\nWatchdogSec=120"
	}
	return strings.TrimLeft(fmt.Sprintf(`
[Unit]
Description=kombify Guard (unprivileged node daemon)
Documentation=https://docs.kombify.io
After=network-online.target techstack-guard-executor.socket
Wants=network-online.target techstack-guard-executor.socket

[Service]
%[1]s
User=%[2]s
Group=%[2]s
EnvironmentFile=-/etc/techstack/agent.env
ExecStart=%[3]s agent --transport=https --enrollment-file=%[6]s --wait-for-enrollment
Restart=always
RestartSec=15
TimeoutStopSec=20
# Stopping the Guard stops everything it started, and (through PartOf on the
# executor) the privileged executions it requested.
KillMode=control-group
Environment=TECHSTACK_ACCESS_MANIFEST=/opt/stackkit/.stackkit/access.json,/root/my-homelab/.stackkit/access.json
Environment=TECHSTACK_AGENT_EXECUTABLE_PATH=%[3]s
Environment=TECHSTACK_OPERATIONS_EXECUTABLE_PATH=%[4]s
Environment=TECHSTACK_GUARD_EXECUTOR_SOCKET=%[5]s

# Its own protected state: identity key, journal, enrollment copy (0700) and the
# handoff directory shared with the executor.
StateDirectory=techstack/guard techstack/guard-handoff
StateDirectoryMode=0700
# The enrollment file is the one path outside its state the daemon may write: a
# revoked Guard has to empty it (the file lives in the root-owned /etc/techstack,
# so it is bound read-write as a file; the directory stays read-only and the file
# can be emptied but not replaced or unlinked). "-" ignores a host that has none.
ReadWritePaths=-%[6]s
RuntimeDirectory=techstack-guard
RuntimeDirectoryMode=0750
UMask=0077

# Hardening. The daemon holds credentials and talks to the network, so it gets
# no privileges at all.
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictSUIDSGID=yes
RestrictRealtime=yes
RestrictNamespaces=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK

[Install]
WantedBy=multi-user.target
`, typeLines, p.GuardUser, p.BinaryPath, p.OperationsPath, p.SocketPath, EnrollmentFile), "\n")
}

// ExecutorSocketText renders the socket only the daemon's group can reach.
func ExecutorSocketText(p UnitParams) string {
	return strings.TrimLeft(fmt.Sprintf(`
[Unit]
Description=kombify Guard privileged executor socket
Documentation=https://docs.kombify.io

[Socket]
ListenStream=%[1]s
SocketMode=0660
SocketUser=root
SocketGroup=%[2]s
Accept=yes
MaxConnections=16
DirectoryMode=0755

[Install]
WantedBy=sockets.target
`, p.SocketPath, p.GuardUser), "\n")
}

// ExecutorUnitText renders the privileged executor, one instance per
// connection. It runs as root because StackKits effects need it (packages under
// /usr, configuration under /etc, containers, reboot); what it gives up is
// everything the daemon's credentials and the kernel do not need to reach.
func ExecutorUnitText(p UnitParams) string {
	return strings.TrimLeft(fmt.Sprintf(`
[Unit]
Description=kombify Guard privileged executor (%%i)
Documentation=https://docs.kombify.io
# Restarting or stopping the Guard ends every execution it requested.
PartOf=techstack-agent.service

[Service]
User=root
StandardInput=socket
StandardOutput=socket
StandardError=journal
EnvironmentFile=-%[2]s
ExecStart=%[1]s guard-executor serve
KillMode=control-group
TimeoutStopSec=20

# Privilege set: root, because StackKits installs packages and converges host
# configuration. NoNewPrivileges stays off for apt's and dpkg's helpers and
# ProtectSystem stays off because the same path cannot be both read-only and
# writable for /usr. The identity key, the journal and the enrollment are hidden
# from everything the executor starts.
NoNewPrivileges=no
PrivateTmp=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectClock=yes
ProtectControlGroups=yes
LockPersonality=yes
RestrictRealtime=yes
SystemCallArchitectures=native
# Neutral images have no enrollment file. Ignore only that missing path;
# each socket-activated instance hides it once enrollment has created it.
InaccessiblePaths=/var/lib/techstack/guard -%[3]s
`, p.BinaryPath, ExecutorEnvFile, EnrollmentFile), "\n")
}

// ExecutorEnvText is the executor's own environment: settings only root may
// change, never taken from a request.
func ExecutorEnvText(p UnitParams) string {
	return strings.TrimLeft(fmt.Sprintf(`
# kombify Guard privileged executor environment. Root-owned; requests cannot change it.
TECHSTACK_ACCESS_MANIFEST=/opt/stackkit/.stackkit/access.json,/root/my-homelab/.stackkit/access.json
TECHSTACK_STACKKIT_RELEASE_PIN=/app/.stackkit/stackkits-release-pin.json
TECHSTACK_STACKKIT_RELEASE_CACHE=/var/lib/techstack/.stackkit/releases
TECHSTACK_AGENT_EXECUTABLE_PATH=%[1]s
TECHSTACK_OPERATIONS_EXECUTABLE_PATH=%[2]s
TECHSTACK_GUARD_USER=%[3]s
# Unsigned local builds are refused unless an operator sets this explicitly:
# TECHSTACK_GUARD_ALLOW_UNSIGNED_UPDATES=1
`, p.BinaryPath, p.OperationsPath, p.GuardUser), "\n")
}
