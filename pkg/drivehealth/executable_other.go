//go:build !linux

package drivehealth

func trustedExecutable(string) (string, error) {
	return "", &ProbeError{Reason: "unsupported_platform"}
}
