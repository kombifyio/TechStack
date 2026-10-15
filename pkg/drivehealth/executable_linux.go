//go:build linux

package drivehealth

import (
	"os"
	"path/filepath"
	"syscall"
)

// A privileged probe must not resolve its executable from daemon-controlled
// PATH. Every component of a resolved system executable is root-controlled.
func trustedExecutable(name string) (string, error) {
	for _, dir := range []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin", "/usr/local/sbin", "/usr/local/bin"} {
		candidate := filepath.Join(dir, name)
		resolved, err := filepath.EvalSymlinks(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", &ProbeError{Reason: "tool_untrusted"}
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return "", &ProbeError{Reason: "tool_untrusted"}
		}
		for path := resolved; ; path = filepath.Dir(path) {
			info, err := os.Lstat(path)
			if err != nil || info.Mode()&0022 != 0 {
				return "", &ProbeError{Reason: "tool_untrusted"}
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid != 0 {
				return "", &ProbeError{Reason: "tool_untrusted"}
			}
			if path == "/" {
				break
			}
		}
		return resolved, nil
	}
	return "", &ProbeError{Reason: "tool_missing"}
}
