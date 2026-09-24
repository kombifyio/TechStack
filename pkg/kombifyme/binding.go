package kombifyme

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"regexp"
	"strings"
)

// ErrManagedAddressIdentity reports a stack without the tenant, owner, or ID
// that a managed kombify.me address must be bound to.
var ErrManagedAddressIdentity = errors.New("managed kombify.me addresses require the stack's tenant, owner and ID")

// InstallZonesEnv switches new managed installs from flat hosts
// (<base>-<service>.kombify.me) to one install zone each
// (<service>.<base>.kombify.me), decision D-72. It stays off until Cloudflare
// edge certificates cover *.<base>.kombify.me (Total TLS, owner action O-16).
// An install keeps the layout kombify.me reports for it either way.
const InstallZonesEnv = "KOMBIFY_ME_INSTALL_ZONES"

// InstallZonesEnabled reports whether new managed installs get an install zone.
func InstallZonesEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(InstallZonesEnv))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// bindingRefPattern mirrors kombify.me's accepted binding reference format.
var bindingRefPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{2,127}$`)

// ManagedAddress identifies the kombify.me addresses Techstack registers for
// one stack with its shared platform identity (platform-7rni.10.7, D-37).
//
// kombify.me binds every route to OwnerRef and StackRef and refuses to hand a
// bound route to any other binding. Label and OwnerRef are non-reversible, so
// public hostnames and Certificate Transparency logs never carry a stack name
// or an account subject; the stack's display name stays in Techstack.
type ManagedAddress struct {
	// Label is the homelab_name sent to kombify.me, which derives the public
	// base name sh-<Label>-<device fingerprint> from it.
	Label    string
	OwnerRef string
	StackRef string
}

// NewManagedAddress derives the tenant-unique managed address identity of a
// stack from stable identifiers only, never from the free-text stack name.
func NewManagedAddress(tenantID, ownerID, stackID string) (ManagedAddress, error) {
	tenantID = strings.TrimSpace(tenantID)
	stackID = strings.TrimSpace(stackID)
	ownerRef, err := OwnerRef(tenantID, ownerID)
	if err != nil || stackID == "" {
		return ManagedAddress{}, ErrManagedAddressIdentity
	}
	stackRef := strings.ToLower(stackID)
	if !bindingRefPattern.MatchString(stackRef) {
		stackRef = "s-" + digestHex("kombify.me/managed-stack/v1", tenantID, stackID)[:32]
	}
	return ManagedAddress{
		Label:    "m" + digestHex("kombify.me/managed-address/v1", tenantID, stackID)[:12],
		OwnerRef: ownerRef,
		StackRef: stackRef,
	}, nil
}

// OwnerRef is the non-reversible kombify.me binding reference of one Techstack
// owner inside one tenant.
func OwnerRef(tenantID, ownerID string) (string, error) {
	tenantID = strings.TrimSpace(tenantID)
	ownerID = strings.TrimSpace(ownerID)
	if tenantID == "" || ownerID == "" {
		return "", ErrManagedAddressIdentity
	}
	return "o-" + digestHex("kombify.me/managed-owner/v1", tenantID, ownerID)[:32], nil
}

// Binding is the kombify.me request field that binds a route to this stack.
func (a ManagedAddress) Binding() map[string]string {
	return map[string]string{"owner_ref": a.OwnerRef, "stack_ref": a.StackRef}
}

func digestHex(domain string, parts ...string) string {
	hash := sha256.New()
	hash.Write([]byte(domain))
	for _, part := range parts {
		hash.Write([]byte{0})
		hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
