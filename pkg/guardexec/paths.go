package guardexec

// Default layout of a kombify Guard host. install.sh and the layout migration
// create it; every path is also overridable through the executor's flags.
const (
	// GuardUser is the dedicated system user of the unprivileged daemon.
	GuardUser = "kombify-guard"
	// AgentUnit is the daemon's systemd service.
	AgentUnit = "techstack-agent.service"
	// DefaultStateDir is the root-only executor state: verified manifest, trial,
	// replay records and the recovery flag.
	DefaultStateDir = "/var/lib/techstack-guard-executor"
	// DefaultHandoffDir is the only directory both sides write: downloaded
	// artifacts, release evidence and non-secret update status.
	DefaultHandoffDir = "/var/lib/techstack/guard-handoff"
	// DefaultAgentPath is the installed Guard executable.
	DefaultAgentPath = "/usr/local/bin/techstack"
	// DefaultOperationsPath is the StackKits operations executable, a copy of the
	// Guard executable.
	DefaultOperationsPath = "/usr/local/libexec/techstack-stackkit-operations"
	// DefaultOperationsSocket is where the daemon serves StackKits operations
	// requests to the operations executable, so the credential never leaves it.
	DefaultOperationsSocket = "/run/techstack-guard/operations.sock"
)
