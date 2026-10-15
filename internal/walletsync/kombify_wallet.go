package walletsync

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/kombifyio/techstack/pkg/kombifywallet"
)

// SyncStackKitOutputsToKombifyWallet saves the outputs a StackKit reported in
// the owner's kombify Wallet, one item per output in the stack's project, and
// returns how many it saved. It is the kombify counterpart of
// SyncStackKitOutputsToStore and never touches the legacy wallet store.
//
// Display names are fixed per output class and carry no personal data; the
// username, URL and notes travel inside the sealed item value. Saving is
// idempotent: a repeated sync finds each item by its identity and adds a
// version only when the value changed.
func SyncStackKitOutputsToKombifyWallet(ctx context.Context, wallet *kombifywallet.Client, req SyncRequest) (int, error) {
	owner := strings.TrimSpace(req.OwnerID)
	if wallet == nil || owner == "" || strings.TrimSpace(req.StackID) == "" || req.Result == nil {
		return 0, nil
	}
	items := WalletItemsFromResult(req)
	if len(items) == 0 {
		return 0, nil
	}
	scope, ok := kombifywallet.ProjectScope(req.StackID)
	if !ok {
		return 0, errors.New("stack id is not a valid Wallet project reference")
	}
	saved := 0
	for _, item := range items {
		if strings.TrimSpace(item.ServiceID) == "" || strings.TrimSpace(item.SourceRef) == "" {
			continue
		}
		spec, err := kombifyItemSpec(req.StackID, scope, item)
		if err != nil {
			return saved, err
		}
		_, ensureErr := wallet.Ensure(ctx, owner, spec)
		clear(spec.Value)
		if ensureErr != nil {
			return saved, ensureErr
		}
		saved++
	}
	return saved, nil
}

// outputNameUnsafe replaces everything a Wallet configuration name refuses.
var outputNameUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// secretReferencePrefix marks a pointer to a machine secret that StackKits
// keeps. Such a reference is not a secret of the owner, and the Wallet refuses
// to deliver a bare reference, so it is saved as configuration.
const secretReferencePrefix = "secret://"

// kombifyItemSpec maps one StackKit output to a Wallet item. An output that
// carries a real secret is a generic secret holding just that secret; every
// other output, including a reference to a machine secret, is non-secret
// configuration holding what the output reported.
func kombifyItemSpec(stackID, scope string, item WalletItem) (kombifywallet.ItemSpec, error) {
	displayName := outputDisplayName(item)
	secret := strings.TrimSpace(item.Secret)
	if secret != "" && !strings.HasPrefix(secret, secretReferencePrefix) {
		return kombifywallet.ItemSpec{
			Type: kombifywallet.ItemTypeGenericSecret, DisplayName: displayName, Scope: scope,
			Description: "Saved by kombify Techstack from a StackKit output.", Value: []byte(secret),
		}, nil
	}
	fields := map[string]string{
		"username": item.Username, "url": item.URL, "notes": item.Notes,
		"item_class": item.ItemClass, "access_mode": item.AccessMode, "service_id": item.ServiceID,
	}
	if secret != "" {
		fields["secret_ref"] = secret
	}
	value, err := json.Marshal(fields)
	if err != nil {
		return kombifywallet.ItemSpec{}, err
	}
	name := "techstack." + strings.ToLower(stackID) + "." + strings.Trim(outputNameUnsafe.ReplaceAllString(strings.ToLower(item.ServiceID), "-"), "-.")
	if len(name) > 128 {
		name = name[:128]
	}
	return kombifywallet.ItemSpec{
		Type: kombifywallet.ItemTypeConfiguration, DisplayName: displayName, Name: name, Scope: scope,
		Description: "Saved by kombify Techstack from a StackKit output.", Value: value,
	}, nil
}

// outputDisplayName is the Wallet display name of an output: fixed per class,
// never the reported name, which can be a person's display name.
func outputDisplayName(item WalletItem) string {
	switch item.ItemClass {
	case "user_account":
		return "Pocket ID owner access"
	case "launch":
		return "Login gateway"
	case "recovery":
		return "Stack recovery bundle"
	default:
		return "Stack output"
	}
}
