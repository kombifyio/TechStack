package nodeproof

import (
	"encoding/json"
	"errors"
	"github.com/kombifyio/techstack/internal/privatefs"
	"os"
	"strings"
)

const LocalIdentityFile = "ril-local-identity.json"

type localIdentity struct {
	TenantID string `json:"tenantId"`
	OwnerID  string `json:"ownerId,omitempty"`
	ServerID string `json:"serverId"`
	StackID  string `json:"stackId,omitempty"`
}

// PinLocalIdentity is called only by the privileged installer after it has
// admitted the root-owned enrollment response. Empty identity is a tombstone.
func PinLocalIdentity(root *os.Root, id Identity) error {
	raw, err := json.Marshal(localIdentity{id.TenantID, id.OwnerID, id.ServerID, id.StackID})
	if err != nil {
		return err
	}
	return privatefs.Replace(root, LocalIdentityFile, raw, 0o600)
}
func LoadLocalIdentity(dir string) (Identity, error) {
	root, err := privatefs.Open(dir)
	if err != nil {
		return Identity{}, err
	}
	defer root.Close()
	raw, err := privatefs.ReadFile(root, LocalIdentityFile)
	if err != nil {
		return Identity{}, err
	}
	var id localIdentity
	if strict(raw, &id) != nil || strings.TrimSpace(id.TenantID) != id.TenantID || strings.TrimSpace(id.ServerID) != id.ServerID || id.TenantID == "" || id.ServerID == "" {
		return Identity{}, errors.New("RIL enrolled identity unavailable")
	}
	return Identity{TenantID: id.TenantID, OwnerID: id.OwnerID, ServerID: id.ServerID, StackID: id.StackID}, nil
}
