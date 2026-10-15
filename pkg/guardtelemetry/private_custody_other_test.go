//go:build !windows

package guardtelemetry_test

import "os"

func makeCustodyLeafShared(path string) error { return os.Chmod(path, 0o666) }
