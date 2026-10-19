// Package agent diagnostics_compose.go: docker-compose-flavored diagnostic
// handlers — get_logs and health_check — that operate against an existing
// compose project on the agent host.
//
// These remain in the IaC-First architecture as observability paths
// (operators can still pull logs and inspect health for compose-style
// stacks). Provisioning paths via compose are gone; see executor.go for
// the IaC-First command allowlist.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// ComposeCommand carries the parameters used by the compose-style diagnostic
// handlers (get_logs, health_check). Marshaled JSON-style from the gRPC
// command's first arg or built up from environment variables.
type ComposeCommand struct {
	ProjectName    string `json:"project_name"`
	ComposeContent string `json:"compose_content,omitempty"`
	ServiceName    string `json:"service_name,omitempty"`
	Lines          int    `json:"lines,omitempty"`
}

// Compose names reach the host as a directory below workDir, as `-p` and as a
// docker argv entry, so only plain names pass: a leading letter or digit keeps
// them from being read as a flag, and no separator or dot-dot can leave
// workDir. The control plane applies the same rules before it queues a command
// (pkg/grpcserver/diagnostic_args.go).
var (
	composeProjectNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	composeServiceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)
)

const maxComposeLogLines = 10000

// validateComposeCommand rejects names that could escape workDir or inject
// docker flags, and an out-of-range line count.
func validateComposeCommand(c *ComposeCommand) error {
	if c.ProjectName != "" && !composeProjectNamePattern.MatchString(c.ProjectName) {
		return fmt.Errorf("invalid project_name %q", c.ProjectName)
	}
	if c.ServiceName != "" && !composeServiceNamePattern.MatchString(c.ServiceName) {
		return fmt.Errorf("invalid service_name %q", c.ServiceName)
	}
	if c.Lines < 0 || c.Lines > maxComposeLogLines {
		return fmt.Errorf("lines must be between 0 and %d", maxComposeLogLines)
	}
	return nil
}

// composeProjectDir resolves the project directory and confirms that it stays
// a direct child of workDir.
func (e *CommandExecutor) composeProjectDir(projectName string) (string, error) {
	if !composeProjectNamePattern.MatchString(projectName) {
		return "", fmt.Errorf("invalid project_name %q", projectName)
	}
	root, err := filepath.Abs(e.workDir)
	if err != nil {
		return "", fmt.Errorf("resolve work dir: %w", err)
	}
	projectDir := filepath.Join(root, projectName)
	if rel, err := filepath.Rel(root, projectDir); err != nil || rel != projectName {
		return "", fmt.Errorf("project_name %q leaves the work dir", projectName)
	}
	return projectDir, nil
}

// handleGetLogs handles get_logs commands.
func (e *CommandExecutor) handleGetLogs(ctx context.Context, cmd *agentpb.ExecuteCommand) (string, string, int, error) {
	composeCmd, err := e.parseComposeCommand(cmd)
	if err != nil {
		return "", "", 1, fmt.Errorf("failed to parse compose command: %w", err)
	}
	if composeCmd.ProjectName == "" {
		return "", "", 1, fmt.Errorf("project_name is required for get_logs")
	}
	lines := composeCmd.Lines
	if lines <= 0 {
		lines = 100 // Default to last 100 lines.
	}
	logs, stderr, exitCode, err := e.getServiceLogs(ctx, composeCmd.ProjectName, composeCmd.ServiceName, lines)
	return logs, stderr, exitCode, err
}

// handleHealthCheck handles health_check commands.
func (e *CommandExecutor) handleHealthCheck(ctx context.Context, cmd *agentpb.ExecuteCommand) (string, string, int, error) {
	composeCmd, err := e.parseComposeCommand(cmd)
	if err != nil {
		return "", "", 1, fmt.Errorf("failed to parse compose command: %w", err)
	}
	if composeCmd.ProjectName == "" {
		return "", "", 1, fmt.Errorf("project_name is required for health_check")
	}
	health, stderr, exitCode, err := e.checkHealth(ctx, composeCmd.ProjectName)
	if err != nil {
		return "", stderr, exitCode, err
	}
	healthJSON, err := json.Marshal(health)
	if err != nil {
		return "", "", 1, fmt.Errorf("failed to marshal health status: %w", err)
	}
	return string(healthJSON), stderr, exitCode, nil
}

// parseComposeCommand extracts ComposeCommand from the ExecuteCommand. Args
// take precedence over environment variables for the same field.
func (e *CommandExecutor) parseComposeCommand(cmd *agentpb.ExecuteCommand) (*ComposeCommand, error) {
	composeCmd := &ComposeCommand{}
	if len(cmd.Args) > 0 {
		if err := json.Unmarshal([]byte(cmd.Args[0]), composeCmd); err == nil {
			if err := validateComposeCommand(composeCmd); err != nil {
				return nil, err
			}
			return composeCmd, nil
		}
		// Not JSON: treat args as positional [project_name, service_name].
		composeCmd.ProjectName = cmd.Args[0]
		if len(cmd.Args) > 1 {
			composeCmd.ServiceName = cmd.Args[1]
		}
	}
	if v, ok := cmd.Environment["PROJECT_NAME"]; ok && composeCmd.ProjectName == "" {
		composeCmd.ProjectName = v
	}
	if v, ok := cmd.Environment["COMPOSE_CONTENT"]; ok && composeCmd.ComposeContent == "" {
		composeCmd.ComposeContent = v
	}
	if v, ok := cmd.Environment["SERVICE_NAME"]; ok && composeCmd.ServiceName == "" {
		composeCmd.ServiceName = v
	}
	if err := validateComposeCommand(composeCmd); err != nil {
		return nil, err
	}
	return composeCmd, nil
}

// getServiceLogs runs `docker compose logs` for the project, optionally
// scoped to a single service.
func (e *CommandExecutor) getServiceLogs(ctx context.Context, projectName, serviceName string, lines int) (string, string, int, error) {
	projectDir, err := e.composeProjectDir(projectName)
	if err != nil {
		return "", "", 1, err
	}
	composeFile := filepath.Join(projectDir, "docker-compose.yml")
	args := []string{"compose", "-p", projectName}
	if _, err := os.Stat(composeFile); err == nil {
		args = append(args, "-f", composeFile)
	}
	args = append(args, "logs", "--no-color", "--tail", fmt.Sprintf("%d", lines))
	if serviceName != "" {
		if !composeServiceNamePattern.MatchString(serviceName) {
			return "", "", 1, fmt.Errorf("invalid service_name %q", serviceName)
		}
		args = append(args, "--", serviceName)
	}
	return e.runCommand(ctx, e.dockerComposePath, args, projectDir, nil)
}

// checkHealth runs `docker compose ps --format json` and aggregates per-service
// state into a name → "<state>[ (<health>)]" map.
func (e *CommandExecutor) checkHealth(ctx context.Context, projectName string) (map[string]string, string, int, error) {
	projectDir, err := e.composeProjectDir(projectName)
	if err != nil {
		return nil, "", 1, err
	}
	composeFile := filepath.Join(projectDir, "docker-compose.yml")
	args := []string{"compose", "-p", projectName}
	if _, err := os.Stat(composeFile); err == nil {
		args = append(args, "-f", composeFile)
	}
	args = append(args, "ps", "--format", "json", "-a")
	stdout, stderr, exitCode, err := e.runCommand(ctx, e.dockerComposePath, args, projectDir, nil)
	if err != nil {
		return nil, stderr, exitCode, err
	}
	health := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var c struct {
			Name    string `json:"Name"`
			Service string `json:"Service"`
			State   string `json:"State"`
			Health  string `json:"Health"`
			Status  string `json:"Status"`
		}
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			e.log.Debug("failed_to_parse_container_json", "line", line, "error", err.Error())
			continue
		}
		status := c.State
		if c.Health != "" {
			status = fmt.Sprintf("%s (%s)", status, c.Health)
		}
		serviceName := c.Service
		if serviceName == "" {
			serviceName = c.Name
		}
		health[serviceName] = status
	}
	return health, stderr, exitCode, nil
}
