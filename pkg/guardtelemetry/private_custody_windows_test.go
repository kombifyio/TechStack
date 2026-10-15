package guardtelemetry_test

import (
	"fmt"
	"os/exec"
)

func makeCustodyLeafShared(path string) error {
	// POSIX chmod does not grant NTFS access. Everyone's SID avoids localized
	// account names and grants real access to this previously private leaf.
	output, err := exec.Command("icacls.exe", path, "/grant", "*S-1-1-0:(F)").CombinedOutput()
	if err != nil {
		return fmt.Errorf("grant untrusted Windows leaf access: %w: %s", err, output)
	}
	return nil
}
