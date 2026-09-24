package validator

import (
	"fmt"
	"strings"
)

var allowedSystemCommands = map[string]struct{}{
	"/bin/sh":       {},
	"/bin/bash":     {},
	"/bin/zsh":      {},
	"/usr/bin/sh":   {},
	"/usr/bin/bash": {},
	"/usr/bin/zsh":  {},
	"/bin/echo":     {},
	"/usr/bin/echo": {},
	"/bin/cat":      {},
	"/usr/bin/cat":  {},
	"/bin/ls":       {},
	"/usr/bin/ls":   {},
}

// ValidateCommandName restricts agent process execution to simple command names
// and an explicit set of system binaries.
func ValidateCommandName(command string) error {
	if command == "" {
		return fmt.Errorf("command name cannot be empty")
	}
	if _, allowed := allowedSystemCommands[command]; allowed {
		return nil
	}
	if strings.ContainsAny(command, `/\`) {
		return fmt.Errorf("command name cannot contain path separators: %q", command)
	}
	if strings.ContainsAny(command, ";|&$`()<>\n\r") {
		return fmt.Errorf("command name contains shell metacharacters: %q", command)
	}
	return nil
}
