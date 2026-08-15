// Package validator provides input validation and sanitization utilities.
package validator

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ValidateFilePath ensures path is safe and doesn't escape the working directory.
// It validates that the resolved absolute path stays within the workDir.
func ValidateFilePath(path string, workDir string) error {
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}

	// Clean the paths to normalize them
	cleanPath := filepath.Clean(path)
	cleanWorkDir := filepath.Clean(workDir)

	// Convert to absolute paths for comparison
	// For relative paths, join with workDir first
	var absPath string
	if filepath.IsAbs(cleanPath) {
		absPath = cleanPath
	} else {
		absPath = filepath.Join(cleanWorkDir, cleanPath)
	}

	absWorkDir, err := filepath.Abs(cleanWorkDir)
	if err != nil {
		return fmt.Errorf("invalid work directory: %w", err)
	}

	// Ensure the path is within the work directory
	// Use filepath.Rel to check if path is under workDir
	rel, err := filepath.Rel(absWorkDir, absPath)
	if err != nil {
		return fmt.Errorf("path resolution error: %w", err)
	}

	// If the relative path starts with "..", it's trying to escape
	if strings.HasPrefix(rel, "..") {
		return fmt.Errorf("path escapes work directory: %s", path)
	}

	return nil
}

// ValidateVarName ensures variable name contains only safe characters.
// Allows: letters, numbers, underscores, hyphens.
func ValidateVarName(name string) error {
	if name == "" {
		return fmt.Errorf("variable name cannot be empty")
	}

	// Allow alphanumeric, underscore, and hyphen
	validPattern := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !validPattern.MatchString(name) {
		return fmt.Errorf("variable name contains invalid characters: %q (allowed: a-z, A-Z, 0-9, _, -)", name)
	}

	// Additional check: don't allow names that are just hyphens or underscores
	if strings.Trim(name, "-_") == "" {
		return fmt.Errorf("variable name must contain at least one alphanumeric character: %q", name)
	}

	return nil
}

// ValidateShellArg ensures argument doesn't contain shell metacharacters.
// This prevents command injection through shell expansion.
func ValidateShellArg(arg string) error {
	if arg == "" {
		return fmt.Errorf("argument cannot be empty")
	}

	// List of dangerous shell metacharacters
	dangerous := []string{
		";",  // Command separator
		"|",  // Pipe
		"&",  // Background/and
		"$",  // Variable expansion
		"`",  // Command substitution
		"(",  // Subshell
		")",  // Subshell
		"<",  // Redirect
		">",  // Redirect
		"\n", // Newline
		"\r", // Carriage return
		"\\", // Escape character (can be used maliciously)
	}

	for _, char := range dangerous {
		if strings.Contains(arg, char) {
			return fmt.Errorf("argument contains dangerous character %q: %s", char, arg)
		}
	}

	return nil
}

// ValidateCommandName ensures the command name is safe.
// Allows simple command names and absolute paths to common system binaries.
func ValidateCommandName(cmd string) error {
	if cmd == "" {
		return fmt.Errorf("command name cannot be empty")
	}

	// Allow absolute paths to known system binaries
	allowedSystemBinaries := []string{
		"/bin/sh", "/bin/bash", "/bin/zsh",
		"/usr/bin/sh", "/usr/bin/bash", "/usr/bin/zsh",
		"/bin/echo", "/usr/bin/echo",
		"/bin/cat", "/usr/bin/cat",
		"/bin/ls", "/usr/bin/ls",
	}
	for _, allowed := range allowedSystemBinaries {
		if cmd == allowed {
			return nil // Allow this specific path
		}
	}

	// Don't allow path separators (prevents running arbitrary executables)
	if strings.Contains(cmd, "/") || strings.Contains(cmd, "\\") {
		return fmt.Errorf("command name cannot contain path separators: %q", cmd)
	}

	// Don't allow shell metacharacters in command name
	if err := ValidateShellArg(cmd); err != nil {
		return fmt.Errorf("invalid command name: %w", err)
	}

	return nil
}

// SanitizeDNSLabel sanitizes a string to be a valid DNS label.
// DNS labels must:
// - Start and end with alphanumeric
// - Contain only alphanumeric and hyphens
// - Be 63 characters or less
func SanitizeDNSLabel(s string) string {
	// Convert to lowercase
	s = strings.ToLower(s)

	// Replace invalid characters with hyphens
	re := regexp.MustCompile(`[^a-z0-9-]`)
	s = re.ReplaceAllString(s, "-")

	// Remove leading/trailing hyphens
	s = strings.Trim(s, "-")

	// Collapse multiple consecutive hyphens
	re = regexp.MustCompile(`-+`)
	s = re.ReplaceAllString(s, "-")

	// Truncate to 63 characters
	if len(s) > 63 {
		s = s[:63]
		// Remove trailing hyphen if truncation created one
		s = strings.TrimRight(s, "-")
	}

	return s
}

// ValidatePortNumber ensures the port is in valid range (1-65535).
func ValidatePortNumber(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port number must be between 1 and 65535, got: %d", port)
	}
	return nil
}

// ValidateIPAddress performs basic IP address validation.
// Supports both IPv4 and IPv6 format validation.
func ValidateIPAddress(ip string) error {
	if ip == "" {
		return fmt.Errorf("IP address cannot be empty")
	}

	// Basic IPv4 pattern
	ipv4Pattern := regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}$`)
	if ipv4Pattern.MatchString(ip) {
		// Validate each octet is 0-255
		parts := strings.Split(ip, ".")
		for _, part := range parts {
			var octet int
			if _, err := fmt.Sscanf(part, "%d", &octet); err != nil {
				return fmt.Errorf("invalid IPv4 address: %s", ip)
			}
			if octet < 0 || octet > 255 {
				return fmt.Errorf("invalid IPv4 octet value: %d in %s", octet, ip)
			}
		}
		return nil
	}

	// Basic IPv6 pattern (simplified - full validation is complex)
	ipv6Pattern := regexp.MustCompile(`^([0-9a-fA-F]{0,4}:){2,7}[0-9a-fA-F]{0,4}$`)
	if ipv6Pattern.MatchString(ip) {
		return nil
	}

	return fmt.Errorf("invalid IP address format: %s", ip)
}
