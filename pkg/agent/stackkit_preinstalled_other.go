//go:build !linux

package agent

import "fmt"

func defaultStackKitPreinstalledManifest() string { return "" }

func stackKitPreinstalledManifest(path string) (string, error) {
	if path != "" {
		return "", fmt.Errorf("preinstalled image admission requires a Linux host")
	}
	return "", nil
}
