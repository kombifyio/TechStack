package validator

import "testing"

func TestValidateCommandName(t *testing.T) {
	tests := []struct {
		name    string
		command string
		valid   bool
	}{
		{name: "simple", command: "ls", valid: true},
		{name: "hyphenated", command: "git-lfs", valid: true},
		{name: "approved absolute binary", command: "/bin/bash", valid: true},
		{name: "arbitrary absolute binary", command: "/usr/local/malicious"},
		{name: "Windows path", command: `C:\Windows\cmd.exe`},
		{name: "semicolon", command: "ls;rm"},
		{name: "pipe", command: "ls|cat"},
		{name: "ampersand", command: "ls&cat"},
		{name: "variable expansion", command: "echo$PATH"},
		{name: "command substitution", command: "echo`whoami`"},
		{name: "subshell", command: "echo(foo)"},
		{name: "redirect", command: "echo>file"},
		{name: "newline", command: "echo\nwhoami"},
		{name: "carriage return", command: "echo\rwhoami"},
		{name: "empty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateCommandName(test.command)
			if (err == nil) != test.valid {
				t.Fatalf("ValidateCommandName(%q) valid = %v, want %v", test.command, err == nil, test.valid)
			}
		})
	}
}
