package kombifywallet_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
	"github.com/kombifyio/techstack/pkg/kombifywallet/walletfake"
)

const owner = "auth0|synthetic-owner"

func nodeProfile(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	value, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func openSSHPrivateKey(t *testing.T) (pemText string, publicLine string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	sshPublic, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPublic)))
}

func denialOf(t *testing.T, err error) *kombifywallet.Denial {
	t.Helper()
	denial, ok := kombifywallet.AsDenial(err)
	if !ok {
		t.Fatalf("expected a Wallet denial, got %v", err)
	}
	return denial
}

// A kombify source must fail the boot, never run on a legacy fallback.
func TestBootFailsClosedWhenTheKombifyWalletIsNotFullyConfigured(t *testing.T) {
	fake := walletfake.New(t)
	complete := fake.Env()
	boot := func(mode config.DeploymentMode, mutate func(map[string]string)) error {
		env := map[string]string{}
		for name, value := range complete {
			env[name] = value
		}
		mutate(env)
		client, _, err := kombifywallet.Boot(mode, func(name string) string { return env[name] })
		if err == nil && client == nil {
			t.Fatal("a kombify source booted without a client")
		}
		return err
	}
	cases := map[string]struct {
		mode   config.DeploymentMode
		mutate func(map[string]string)
		names  string
	}{
		"self-hosted":       {config.ModeSelfHosted, func(map[string]string) {}, kombifywallet.EnvSource},
		"no recipient key":  {config.ModeSaaS, func(env map[string]string) { delete(env, kombifywallet.EnvRecipientKey) }, kombifywallet.EnvRecipientKey},
		"no generation":     {config.ModeSaaS, func(env map[string]string) { delete(env, kombifywallet.EnvRecipientGeneration) }, kombifywallet.EnvRecipientGeneration},
		"no service secret": {config.ModeSaaS, func(env map[string]string) { delete(env, kombifywallet.EnvServiceSecret) }, kombifywallet.EnvServiceSecret},
		"shared secret": {config.ModeSaaS, func(env map[string]string) {
			env["SERVICE_AUTH_SECRET"] = env[kombifywallet.EnvServiceSecret]
		}, "SERVICE_AUTH_SECRET"},
		"unknown source": {config.ModeSaaS, func(env map[string]string) { env[kombifywallet.EnvSource] = "wallet" }, kombifywallet.EnvSource},
	}
	for name, tc := range cases {
		err := boot(tc.mode, tc.mutate)
		if err == nil {
			t.Fatalf("%s: boot succeeded", name)
		}
		if !strings.Contains(err.Error(), tc.names) {
			t.Fatalf("%s: error %q does not name %s", name, err, tc.names)
		}
		for _, secret := range []string{walletfake.ServiceSecret, complete[kombifywallet.EnvRecipientKey]} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("%s: error leaks configuration", name)
			}
		}
	}
	if err := boot(config.ModeSaaS, func(map[string]string) {}); err != nil {
		t.Fatalf("complete configuration: %v", err)
	}
	client, source, err := kombifywallet.Boot(config.ModeSelfHosted, func(string) string { return "" })
	if err != nil || client != nil || source != kombifywallet.SourceLegacy {
		t.Fatalf("an unset source must stay legacy, got source=%q client=%v err=%v", source, client, err)
	}
}

// The item id resolves through deliver into the profile the owner saved, and
// the secrets never print.
func TestNodeAccessDeliversTheProfileTheOwnerSaved(t *testing.T) {
	fake := walletfake.New(t)
	client := fake.Client()
	id := fake.Grant(owner, kombifywallet.ItemTypeNodeAccess, "Prod box", "account",
		nodeProfile(t, map[string]any{"host": "10.0.0.5", "user": "root", "port": 2222, "privateKey": "-----KEY-MATERIAL-----", "unknownFutureField": true}), "use")

	access, err := client.NodeAccess(context.Background(), owner, id)
	if err != nil {
		t.Fatalf("NodeAccess: %v", err)
	}
	defer access.Wipe()
	if access.Host != "10.0.0.5" || access.User != "root" || access.Port != 2222 || string(access.PrivateKey.Bytes()) != "-----KEY-MATERIAL-----" {
		t.Fatalf("parsed profile is wrong: host=%q user=%q port=%d", access.Host, access.User, access.Port)
	}
	if rendered := fmt.Sprintf("%v %+v %s", access, access, access.PrivateKey); strings.Contains(rendered, "KEY-MATERIAL") {
		t.Fatalf("a secret reached a formatted string: %s", rendered)
	}
	access.Wipe()
	if access.HasPrivateKey() {
		t.Fatal("Wipe left the private key readable")
	}
}

// Techstack uses only what the owner shared with it.
func TestNodeAccessWithoutTheUseRightNamesTheMissingRight(t *testing.T) {
	fake := walletfake.New(t)
	client := fake.Client()
	id := fake.Grant(owner, kombifywallet.ItemTypeNodeAccess, "Prod box", "account", nodeProfile(t, map[string]any{"host": "h", "user": "u", "password": "p"}), "read")

	_, err := client.NodeAccess(context.Background(), owner, id)
	denial := denialOf(t, err)
	if denial.Status != http.StatusForbidden || denial.Reason != kombifywallet.ReasonRightMissing || denial.RequiredRight != "use" || denial.ItemID != id {
		t.Fatalf("denial = %+v", denial)
	}
	details := denial.Details()
	if details["reason_code"] != "wallet_right_missing" || details["required_right"] != "use" || details["item_id"] != id {
		t.Fatalf("details = %v", details)
	}
}

// A node access item is bound to its node: its credential must not be sent to
// a host the owner never saved it for.
func TestNodeAccessProfileBoundToAnotherHostIsRefused(t *testing.T) {
	fake := walletfake.New(t)
	client := fake.Client()
	id := fake.Grant(owner, kombifywallet.ItemTypeNodeAccess, "Prod box", "account", nodeProfile(t, map[string]any{"host": "Prod.example.test", "user": "root", "password": "p"}), "use")

	access, err := client.NodeAccess(context.Background(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Wipe()
	if err := access.RequireHost("prod.example.test"); err != nil {
		t.Fatalf("the bound host was refused: %v", err)
	}
	denial := denialOf(t, access.RequireHost("attacker.example.test"))
	if denial.Reason != kombifywallet.ReasonHostMismatch {
		t.Fatalf("reason = %s", denial.Reason)
	}
}

func TestPublicKeyLineIsDerivedFromThePrivateKey(t *testing.T) {
	fake := walletfake.New(t)
	client := fake.Client()
	privatePEM, publicLine := openSSHPrivateKey(t)
	id := fake.Grant(owner, kombifywallet.ItemTypeNodeAccess, "Key", "account", nodeProfile(t, map[string]any{"host": "h", "user": "u", "privateKey": privatePEM}), "use")

	access, err := client.NodeAccess(context.Background(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Wipe()
	line, fingerprint, err := access.PublicKeyLine()
	if err != nil || line != publicLine || !strings.HasPrefix(fingerprint, "SHA256:") {
		t.Fatalf("line=%q fingerprint=%q err=%v", line, fingerprint, err)
	}

	passwordOnly := fake.Grant(owner, kombifywallet.ItemTypeNodeAccess, "Pw", "account", nodeProfile(t, map[string]any{"host": "h", "user": "u", "password": "p"}), "use")
	password, err := client.NodeAccess(context.Background(), owner, passwordOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer password.Wipe()
	if _, _, err := password.PublicKeyLine(); denialOf(t, err).Reason != kombifywallet.ReasonNoPublicKey {
		t.Fatalf("a password-only profile has no public key, got %v", err)
	}
}

func TestEnsureConvergesOnOneItemAndVersionsOnlyAChangedValue(t *testing.T) {
	fake := walletfake.New(t)
	fake.AllowCreate(owner)
	client := fake.Client()
	spec := kombifywallet.ItemSpec{Type: kombifywallet.ItemTypeGenericSecret, DisplayName: "Stack recovery passphrase", Scope: "project:stack-1", Value: []byte("hash-1")}

	first, err := client.Ensure(context.Background(), owner, spec)
	if err != nil || !first.Created {
		t.Fatalf("first Ensure: %+v %v", first, err)
	}
	again, err := client.Ensure(context.Background(), owner, spec)
	if err != nil || again.ItemID != first.ItemID || again.Created || again.Versioned {
		t.Fatalf("an unchanged value must not write: %+v %v", again, err)
	}
	spec.Value = []byte("hash-2")
	changed, err := client.Ensure(context.Background(), owner, spec)
	if err != nil || changed.ItemID != first.ItemID || !changed.Versioned {
		t.Fatalf("a changed value must add a version: %+v %v", changed, err)
	}
	items := fake.Items(owner)
	if len(items) != 1 || len(items[0].Versions) != 2 || string(items[0].Value()) != "hash-2" || items[0].Scope != "project:stack-1" {
		t.Fatalf("Wallet state = %+v", items)
	}
}

func TestCanCreateRefusesUntilTheOwnerAllowsTechstackToSave(t *testing.T) {
	fake := walletfake.New(t)
	client := fake.Client()

	denial := denialOf(t, client.CanCreate(context.Background(), owner))
	if denial.Status != http.StatusConflict || denial.Reason != kombifywallet.ReasonCreateRightMissing || denial.RequiredRight != "create" {
		t.Fatalf("denial = %+v", denial)
	}
	fake.AllowCreate(owner)
	if err := client.CanCreate(context.Background(), owner); err != nil {
		t.Fatalf("CanCreate after the grant: %v", err)
	}
}

func TestAWalletOutageIsARetryableDenialNotAFallback(t *testing.T) {
	fake := walletfake.New(t)
	client := fake.Client()
	id := fake.Grant(owner, kombifywallet.ItemTypeNodeAccess, "Box", "account", nodeProfile(t, map[string]any{"host": "h", "user": "u", "password": "p"}), "use")
	fake.Outage(true)

	_, err := client.NodeAccess(context.Background(), owner, id)
	denial := denialOf(t, err)
	if denial.Status != http.StatusServiceUnavailable || denial.Reason != kombifywallet.ReasonUnavailable || !denial.Retryable {
		t.Fatalf("denial = %+v", denial)
	}
	var again *kombifywallet.Denial
	if !errors.As(err, &again) {
		t.Fatal("the denial must unwrap with errors.As")
	}
}
