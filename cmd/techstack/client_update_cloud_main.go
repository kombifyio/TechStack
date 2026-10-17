//go:build cloud_desktop_updater

// Built explicitly with client_update.go, excluding the local product runtime.
// The shared source retains the same embedded key, staging and apply verifier.
package main

import (
	"context"
	"fmt"
	"os"
)

var version = "0.0.0-dev"

func init() { clientUpdateEdition = "cloud" }

func main() {
	if !isClientUpdateMode(os.Args) {
		fmt.Fprintln(os.Stderr, "expected client-update")
		os.Exit(2)
	}
	if err := runClientUpdateMode(context.Background(), os.Args[2:]); err != nil {
		os.Exit(1)
	}
}
