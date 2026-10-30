//go:build kombify_full_local

package sessionreauth

import "github.com/kombifyio/techstack/pkg/httpx"

// The Full Local build (tag kombify_full_local) has no error-reporting client.
func captureSentry(*httpx.Event, string, string, map[string]string) {}
