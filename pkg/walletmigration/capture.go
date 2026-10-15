package walletmigration

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/walletservice"

	"github.com/kombifyio/techstack/pkg/kombifywallet"
)

// Execution failures of building a capture. None wraps a cause: a decryption
// or encoding error could name a value, so only the class survives.
var (
	errDecrypt    = errors.New("walletmigration: a stored value could not be decrypted")
	errScope      = errors.New("walletmigration: the stack id is not a Wallet project reference")
	errEmptyValue = errors.New("walletmigration: the row has no value to capture")
)

// nodeTarget is the server a node access item is bound to, taken from the
// stacks that reference the item.
type nodeTarget struct {
	host string
	user string
	port int
}

func (t nodeTarget) known() bool { return t.host != "" && t.user != "" }

// capture is one Wallet item as the executor writes it.
type capture struct {
	spec kombifywallet.ItemSpec
	// nodeAccess says the value is a node_access_profile a stack can log in
	// with, which is what the caller switch requires.
	nodeAccess bool
}

// buildCapture opens the row's stored values and shapes them into the Wallet
// item the mapping names. The Wallet service API admits no oauth_connection,
// so an OAuth token is kept as a generic secret. Node access items need the
// target server: without one an SSH key or password is kept as a plain secret
// (an SSH password with a username as a login) instead of a profile no stack
// could use. The caller wipes spec.Value.
func buildCapture(plan ItemPlan, target nodeTarget, decrypt func(string) (string, error)) (capture, error) {
	scope := plan.Target.Scope
	if scope.Kind == ScopeProject {
		if _, ok := kombifywallet.ProjectScope(scope.ProjectRef); !ok {
			return capture{}, errScope
		}
	}
	itemType, node := executionType(plan, target)
	spec := kombifywallet.ItemSpec{
		Type:        itemType,
		DisplayName: plan.Target.Metadata.DisplayName,
		Description: plan.Target.Metadata.Description,
		Scope:       scope.String(),
	}
	if itemType == walletservice.ItemTypeConfiguration {
		spec.Name = strings.ToLower(plan.Target.MigrationKey)
	}
	value, err := captureValue(plan, itemType, target, decrypt)
	if err != nil {
		return capture{}, err
	}
	if len(value) == 0 {
		return capture{}, errEmptyValue
	}
	spec.Value = value
	return capture{spec: spec, nodeAccess: node}, nil
}

// executionType is the Wallet item type written for a plan, and whether it is
// a node access profile.
func executionType(plan ItemPlan, target nodeTarget) (string, bool) {
	switch plan.Target.ResourceType {
	case ResourceLogin:
		return walletservice.ItemTypeLogin, false
	case ResourceConfiguration:
		return walletservice.ItemTypeConfiguration, false
	case ResourceAICredential:
		return walletservice.ItemTypeAICredential, false
	case ResourceNodeAccessProfile:
		switch {
		case target.known() || plan.Target.Values.Form == FormPublicKeyOnly:
			return walletservice.ItemTypeNodeAccessProfile, true
		case plan.Source.Kind == kindSSHPassword && plan.Target.Values.Username.Present():
			return walletservice.ItemTypeLogin, false
		}
		return walletservice.ItemTypeGenericSecret, false
	default:
		// generic_secret, and oauth_connection, which the service API cannot create.
		return walletservice.ItemTypeGenericSecret, false
	}
}

func captureValue(plan ItemPlan, itemType string, target nodeTarget, decrypt func(string) (string, error)) ([]byte, error) {
	values := plan.Target.Values
	secret, totp, err := openPair(values, plan.Source.Kind, decrypt)
	if err != nil {
		return nil, err
	}
	username, _ := values.Username.Open(nil)
	notes, _ := values.Notes.Open(nil)
	switch itemType {
	case walletservice.ItemTypeLogin:
		return json.Marshal(struct {
			Username string `json:"username,omitempty"`
			Password string `json:"password,omitempty"`
			TOTP     string `json:"totp,omitempty"`
			URL      string `json:"url,omitempty"`
			Notes    string `json:"notes,omitempty"`
		}{username, secret, totp, plan.Target.Metadata.URL, notes})
	case walletservice.ItemTypeNodeAccessProfile:
		publicKey, _ := values.PublicKey.Open(nil)
		profile := nodeProfile{Host: target.host, User: target.user, PublicKey: publicKey}
		if target.port > 0 && target.port <= 65535 {
			profile.Port = target.port
		}
		if plan.Source.Kind == kindSSHPassword {
			profile.Password = secret
		} else {
			profile.PrivateKey = secret
		}
		return json.Marshal(profile)
	case walletservice.ItemTypeConfiguration:
		fields := struct {
			URL        string `json:"url,omitempty"`
			Username   string `json:"username,omitempty"`
			Notes      string `json:"notes,omitempty"`
			ItemClass  string `json:"item_class,omitempty"`
			AccessMode string `json:"access_mode,omitempty"`
			SecretRef  string `json:"secret_ref,omitempty"`
		}{plan.Target.Metadata.URL, username, notes, plan.Target.Metadata.ItemClass, plan.Target.Metadata.AccessMode, ""}
		if values.Form == FormSecretReference {
			// The value is a pointer to a secret StackKits keeps, not a secret.
			fields.SecretRef = secret
		}
		return json.Marshal(fields)
	default:
		return []byte(secret), nil
	}
}

// nodeProfile is the node_access_profile value shared with Cloud and the
// Techstack UI.
type nodeProfile struct {
	Host       string `json:"host,omitempty"`
	User       string `json:"user,omitempty"`
	Port       int    `json:"port,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
	Password   string `json:"password,omitempty"`
	PublicKey  string `json:"publicKey,omitempty"`
}

// openPair opens the secret and the TOTP. SSH secrets are trimmed the way the
// legacy remote-connection readers trim them; every other secret is kept
// exactly.
func openPair(values ItemValues, kind string, decrypt func(string) (string, error)) (secret, totp string, err error) {
	if secret, err = values.Secret.Open(decrypt); err != nil {
		return "", "", errDecrypt
	}
	if totp, err = values.TOTP.Open(decrypt); err != nil {
		return "", "", errDecrypt
	}
	if kind == kindSSHKey || kind == kindSSHPassword {
		secret = strings.TrimSpace(secret)
	}
	return secret, strings.TrimSpace(totp), nil
}
