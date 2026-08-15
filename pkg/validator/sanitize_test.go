package validator

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidateFilePath(t *testing.T) {
	// Create a temp directory for testing that works on all platforms
	tempDir, err := os.MkdirTemp("", "techstack-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create absolute path references
	workDir := tempDir
	validFile := filepath.Join(workDir, "file.txt")
	nestedFile := filepath.Join(workDir, "subdir", "file.txt")

	// Create the nested directory for valid path testing
	if err := os.MkdirAll(filepath.Join(workDir, "subdir"), 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	tests := []struct {
		name    string
		path    string
		workDir string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid path within workdir",
			path:    validFile,
			workDir: workDir,
			wantErr: false,
		},
		{
			name:    "valid relative path",
			path:    "file.txt",
			workDir: workDir,
			wantErr: false,
		},
		{
			name:    "path escapes with ..",
			path:    filepath.Join(workDir, "..", "..", "..", "etc", "passwd"),
			workDir: workDir,
			wantErr: true,
			errMsg:  "escapes work directory",
		},
		{
			name:    "path escapes with relative ..",
			path:    filepath.Join("..", "..", "etc", "passwd"),
			workDir: workDir,
			wantErr: true,
			errMsg:  "escapes work directory",
		},
		{
			name:    "empty path",
			path:    "",
			workDir: workDir,
			wantErr: true,
			errMsg:  "cannot be empty",
		},
		{
			name:    "nested valid path",
			path:    nestedFile,
			workDir: workDir,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip path escape tests on Windows as they behave differently
			if runtime.GOOS == "windows" && strings.Contains(tt.name, "escapes") {
				t.Skip("Skipping path escape test on Windows - path behavior differs")
			}
			err := ValidateFilePath(tt.path, tt.workDir)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateFilePath() expected error but got nil")
					return
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateFilePath() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateFilePath() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestValidateVarName(t *testing.T) {
	tests := []struct {
		name    string
		varName string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid simple name",
			varName: "myvar",
			wantErr: false,
		},
		{
			name:    "valid with underscore",
			varName: "my_var",
			wantErr: false,
		},
		{
			name:    "valid with hyphen",
			varName: "my-var",
			wantErr: false,
		},
		{
			name:    "valid with numbers",
			varName: "var123",
			wantErr: false,
		},
		{
			name:    "valid mixed",
			varName: "my_var-123",
			wantErr: false,
		},
		{
			name:    "invalid with space",
			varName: "my var",
			wantErr: true,
			errMsg:  "invalid characters",
		},
		{
			name:    "invalid with semicolon",
			varName: "var;",
			wantErr: true,
			errMsg:  "invalid characters",
		},
		{
			name:    "invalid with dollar",
			varName: "$var",
			wantErr: true,
			errMsg:  "invalid characters",
		},
		{
			name:    "invalid with slash",
			varName: "var/name",
			wantErr: true,
			errMsg:  "invalid characters",
		},
		{
			name:    "empty string",
			varName: "",
			wantErr: true,
			errMsg:  "cannot be empty",
		},
		{
			name:    "only hyphens",
			varName: "---",
			wantErr: true,
			errMsg:  "must contain at least one alphanumeric",
		},
		{
			name:    "only underscores",
			varName: "___",
			wantErr: true,
			errMsg:  "must contain at least one alphanumeric",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateVarName(tt.varName)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateVarName() expected error but got nil")
					return
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateVarName() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateVarName() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestValidateShellArg(t *testing.T) {
	tests := []struct {
		name    string
		arg     string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid simple arg",
			arg:     "value",
			wantErr: false,
		},
		{
			name:    "valid with underscore",
			arg:     "my_value",
			wantErr: false,
		},
		{
			name:    "valid with hyphen",
			arg:     "my-value",
			wantErr: false,
		},
		{
			name:    "valid with equals",
			arg:     "key=value",
			wantErr: false,
		},
		{
			name:    "valid path-like",
			arg:     "/path/to/file",
			wantErr: false,
		},
		{
			name:    "invalid with semicolon",
			arg:     "value;rm -rf /",
			wantErr: true,
			errMsg:  "dangerous character",
		},
		{
			name:    "invalid with pipe",
			arg:     "value | cat",
			wantErr: true,
			errMsg:  "dangerous character",
		},
		{
			name:    "invalid with ampersand",
			arg:     "value & echo bad",
			wantErr: true,
			errMsg:  "dangerous character",
		},
		{
			name:    "invalid with dollar",
			arg:     "value$var",
			wantErr: true,
			errMsg:  "dangerous character",
		},
		{
			name:    "invalid with backtick",
			arg:     "value`whoami`",
			wantErr: true,
			errMsg:  "dangerous character",
		},
		{
			name:    "invalid with parenthesis",
			arg:     "value()",
			wantErr: true,
			errMsg:  "dangerous character",
		},
		{
			name:    "invalid with redirect",
			arg:     "value > /tmp/bad",
			wantErr: true,
			errMsg:  "dangerous character",
		},
		{
			name:    "invalid with newline",
			arg:     "value\nrm -rf /",
			wantErr: true,
			errMsg:  "dangerous character",
		},
		{
			name:    "invalid with backslash",
			arg:     "value\\",
			wantErr: true,
			errMsg:  "dangerous character",
		},
		{
			name:    "empty string",
			arg:     "",
			wantErr: true,
			errMsg:  "cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateShellArg(tt.arg)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateShellArg() expected error but got nil")
					return
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateShellArg() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateShellArg() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestValidateCommandName(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid simple command",
			cmd:     "ls",
			wantErr: false,
		},
		{
			name:    "valid command with hyphen",
			cmd:     "git-lfs",
			wantErr: false,
		},
		{
			name:    "valid system binary /bin/sh",
			cmd:     "/bin/sh",
			wantErr: false,
		},
		{
			name:    "valid system binary /bin/bash",
			cmd:     "/bin/bash",
			wantErr: false,
		},
		{
			name:    "invalid arbitrary path",
			cmd:     "/usr/local/malicious",
			wantErr: true,
			errMsg:  "path separators",
		},
		{
			name:    "invalid with backslash",
			cmd:     "C:\\Windows\\cmd.exe",
			wantErr: true,
			errMsg:  "path separators",
		},
		{
			name:    "invalid with semicolon",
			cmd:     "ls;rm",
			wantErr: true,
			errMsg:  "invalid command name",
		},
		{
			name:    "empty command",
			cmd:     "",
			wantErr: true,
			errMsg:  "cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCommandName(tt.cmd)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateCommandName() expected error but got nil")
					return
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateCommandName() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateCommandName() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestSanitizeDNSLabel(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple lowercase",
			input: "test",
			want:  "test",
		},
		{
			name:  "uppercase to lowercase",
			input: "TEST",
			want:  "test",
		},
		{
			name:  "mixed case",
			input: "MyApp",
			want:  "myapp",
		},
		{
			name:  "with spaces",
			input: "my app",
			want:  "my-app",
		},
		{
			name:  "with special chars",
			input: "my_app!@#",
			want:  "my-app",
		},
		{
			name:  "leading/trailing hyphens",
			input: "-test-",
			want:  "test",
		},
		{
			name:  "multiple consecutive hyphens",
			input: "my---app",
			want:  "my-app",
		},
		{
			name:  "long string truncation",
			input: "this-is-a-very-long-dns-label-name-that-exceeds-the-sixty-three-character-limit",
			want:  "this-is-a-very-long-dns-label-name-that-exceeds-the-sixty-three",
		},
		{
			name:  "numbers preserved",
			input: "app123",
			want:  "app123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeDNSLabel(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeDNSLabel() = %v, want %v", got, tt.want)
			}
			// Verify output is valid DNS label
			if len(got) > 63 {
				t.Errorf("SanitizeDNSLabel() result too long: %d characters", len(got))
			}
		})
	}
}

func TestValidatePortNumber(t *testing.T) {
	tests := []struct {
		name    string
		port    int
		wantErr bool
	}{
		{"valid port 80", 80, false},
		{"valid port 443", 443, false},
		{"valid port 8080", 8080, false},
		{"valid port 1", 1, false},
		{"valid port 65535", 65535, false},
		{"invalid port 0", 0, true},
		{"invalid port -1", -1, true},
		{"invalid port 65536", 65536, true},
		{"invalid port 100000", 100000, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePortNumber(tt.port)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePortNumber() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateIPAddress(t *testing.T) {
	tests := []struct {
		name    string
		ip      string
		wantErr bool
	}{
		{"valid IPv4", "192.168.1.1", false},
		{"valid IPv4 localhost", "127.0.0.1", false},
		{"valid IPv4 zeros", "0.0.0.0", false},
		{"valid IPv4 max", "255.255.255.255", false},
		{"valid IPv6 simple", "2001:db8::1", false},
		{"valid IPv6 full", "2001:0db8:0000:0000:0000:0000:0000:0001", false},
		{"valid IPv6 localhost", "::1", false},
		{"invalid IPv4 octet > 255", "192.168.1.256", true},
		{"invalid IPv4 too many parts", "192.168.1.1.1", true},
		{"invalid IPv4 too few parts", "192.168.1", true},
		{"invalid IPv4 letters", "192.168.a.1", true},
		{"empty string", "", true},
		{"invalid format", "not-an-ip", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateIPAddress(tt.ip)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateIPAddress() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
