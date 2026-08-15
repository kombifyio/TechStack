//go:build !windows

package agent

import "os"

func replaceStackKitInventory(source, target string) error {
	return os.Rename(source, target)
}
