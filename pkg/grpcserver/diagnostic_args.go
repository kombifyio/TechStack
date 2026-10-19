package grpcserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Diagnostic commands (get_logs, health_check) name a compose project and an
// optional service on the agent host. The agent uses the project as a
// directory below its work dir and as `-p`, and the service as a docker argv
// entry, so the control plane admits only plain names and re-encodes the
// arguments in one canonical form instead of forwarding caller input. The agent
// applies the same rules again (pkg/agent/diagnostics_compose.go).
var (
	diagnosticProjectNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	diagnosticServiceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)
)

const maxDiagnosticLogLines = 10000

type diagnosticArgs struct {
	ProjectName string `json:"project_name"`
	ServiceName string `json:"service_name,omitempty"`
	Lines       int    `json:"lines,omitempty"`
}

// normalizeDiagnosticCommand validates the caller arguments of a diagnostic
// command and replaces them with canonical JSON. Environment entries that the
// agent would read as names are validated too.
func normalizeDiagnosticCommand(cmd *AgentCommand) error {
	parsed, err := parseDiagnosticArgs(cmd.Args)
	if err != nil {
		return err
	}
	if parsed != nil {
		if err := validateDiagnosticArgs(parsed); err != nil {
			return err
		}
		encoded, err := json.Marshal(parsed)
		if err != nil {
			return fmt.Errorf("encode diagnostic arguments: %w", err)
		}
		cmd.Args = []string{string(encoded)}
	}
	if v, ok := cmd.Environment["PROJECT_NAME"]; ok && !diagnosticProjectNamePattern.MatchString(v) {
		return fmt.Errorf("invalid project name %q", v)
	}
	if v, ok := cmd.Environment["SERVICE_NAME"]; ok && v != "" && !diagnosticServiceNamePattern.MatchString(v) {
		return fmt.Errorf("invalid service name %q", v)
	}
	return nil
}

func parseDiagnosticArgs(args []string) (*diagnosticArgs, error) {
	if len(args) == 0 {
		return nil, nil
	}
	if strings.HasPrefix(strings.TrimSpace(args[0]), "{") {
		if len(args) > 1 {
			return nil, fmt.Errorf("diagnostic arguments take one JSON object")
		}
		decoder := json.NewDecoder(bytes.NewReader([]byte(args[0])))
		decoder.DisallowUnknownFields()
		var parsed diagnosticArgs
		if err := decoder.Decode(&parsed); err != nil {
			return nil, fmt.Errorf("invalid diagnostic arguments: %w", err)
		}
		if decoder.More() {
			return nil, fmt.Errorf("diagnostic arguments take one JSON object")
		}
		return &parsed, nil
	}
	if len(args) > 2 {
		return nil, fmt.Errorf("diagnostic arguments are [project, service]")
	}
	parsed := diagnosticArgs{ProjectName: args[0]}
	if len(args) == 2 {
		parsed.ServiceName = args[1]
	}
	return &parsed, nil
}

func validateDiagnosticArgs(args *diagnosticArgs) error {
	if !diagnosticProjectNamePattern.MatchString(args.ProjectName) {
		return fmt.Errorf("invalid project name %q", args.ProjectName)
	}
	if args.ServiceName != "" && !diagnosticServiceNamePattern.MatchString(args.ServiceName) {
		return fmt.Errorf("invalid service name %q", args.ServiceName)
	}
	if args.Lines < 0 || args.Lines > maxDiagnosticLogLines {
		return fmt.Errorf("lines must be between 0 and %d", maxDiagnosticLogLines)
	}
	return nil
}
