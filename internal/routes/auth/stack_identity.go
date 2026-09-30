package auth

import (
	"strings"

	"github.com/kombifyio/techstack/pkg/auth/sso"
)

// launchStackIdentity projects the Stack Identity a kombify Cloud launch
// token carries. Only the name is required: the character and animation are
// presentation details the UI defaults. The persisted Stack Identity lives on
// the homelab row (internal/routes/stacks/stack_identity.go), never on the
// user record.
func launchStackIdentity(identity *sso.StackIdentity) *sso.StackIdentity {
	if identity == nil {
		return nil
	}
	name := strings.TrimSpace(identity.Name)
	if name == "" || len(name) > 80 {
		return nil
	}
	characterID := strings.TrimSpace(identity.CharacterID)
	if len(characterID) > 40 {
		characterID = ""
	}
	animationStyle := strings.TrimSpace(identity.AnimationStyle)
	if len(animationStyle) > 60 {
		animationStyle = ""
	}
	return &sso.StackIdentity{Name: name, CharacterID: characterID, AnimationStyle: animationStyle}
}
