package guardlayout

import (
	"strings"
	"testing"
)

// Under ProtectSystem=strict the daemon can write only what the unit names. A
// revoked Guard has to empty the enrollment file it was started with (it
// carries the worker credential), so that exact path must be writable, and the
// hardening must stay on so nothing else is.
func TestDaemonUnitLetsARevokedGuardEmptyItsEnrollmentFileAndNothingElseUnderEtc(t *testing.T) {
	unit := AgentUnitText(UnitParams{BinaryPath: "/usr/local/bin/techstack", OperationsPath: "/usr/local/bin/ops", GuardUser: "kombify-guard", SocketPath: "/run/x.sock"})
	var enrollment string
	var writable []string
	protected := false
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "ExecStart="):
			for _, field := range strings.Fields(line) {
				if value, ok := strings.CutPrefix(field, "--enrollment-file="); ok {
					enrollment = value
				}
			}
		case strings.HasPrefix(line, "ReadWritePaths="):
			for _, path := range strings.Fields(strings.TrimPrefix(line, "ReadWritePaths=")) {
				writable = append(writable, strings.TrimPrefix(path, "-"))
			}
		case line == "ProtectSystem=strict":
			protected = true
		}
	}
	if enrollment == "" || !protected {
		t.Fatalf("unit has no enrollment file argument (%q) or no ProtectSystem=strict (%t)", enrollment, protected)
	}
	if len(writable) != 1 || writable[0] != enrollment {
		t.Fatalf("daemon may write %v, want exactly its enrollment file %s", writable, enrollment)
	}
}
