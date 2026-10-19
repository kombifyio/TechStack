//go:build !windows

package toolauth

// defaultTokenStore returns the platform-preferred token store.
// On non-Windows platforms, this is the machine-bound obfuscated file store (not a boundary against local attackers; see SECURITY.md).
// macOS Keychain support may be added in a future version.
func defaultTokenStore() TokenStore {
	return &FileStore{}
}
