package walletmigration

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/auth"
	"golang.org/x/crypto/ssh"
)

const testTenant = "org_acme"

func testEncryptor(t *testing.T) *auth.SecretEncryptor {
	t.Helper()
	encryptor, err := auth.NewSecretEncryptor([]byte("walletmigration-test-key-32-byte"))
	if err != nil {
		t.Fatal(err)
	}
	return encryptor
}

func sealed(t *testing.T, plaintext string) string {
	t.Helper()
	value, err := testEncryptor(t).Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func authorizedKey(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func row(id, kind string, metadata map[string]any) SourceRow {
	if _, ok := metadata["owner_id"]; !ok {
		metadata["owner_id"] = "auth0|owner"
	}
	return SourceRow{ID: id, TenantID: testTenant, ItemType: kind, Metadata: metadata, CreatedAt: time.Unix(1_700_000_000, 0)}
}

func TestPlanItemMapsEachTechstackKindToItsWalletItem(t *testing.T) {
	publicKey := authorizedKey(t)
	type want struct {
		target  ResourceType
		form    CredentialForm
		scope   string
		writer  Writer
		values  []string // value fields that must be present
		flags   []Flag
		noFlags []Flag
	}
	cases := []struct {
		name string
		row  SourceRow
		want want
	}{
		{
			name: "password with username becomes a login with its TOTP",
			row:  row("w1", "password", map[string]any{"name": "Grafana", "username": "admin", "secret": sealed(t, "pw"), "totp": sealed(t, "JBSWY3DP"), "source_type": "manual"}),
			want: want{target: ResourceLogin, form: FormPassword, scope: "account", writer: WriterOwner, values: []string{"secret", "totp", "username"}},
		},
		{
			name: "password with a TOTP but no username is still a login, so the TOTP is kept",
			row:  row("w1b", "password", map[string]any{"secret": sealed(t, "pw"), "totp": sealed(t, "JBSWY3DP")}),
			want: want{target: ResourceLogin, form: FormPassword, scope: "account", writer: WriterOwner, values: []string{"secret", "totp"}},
		},
		{
			name: "password without username becomes a generic secret",
			row:  row("w2", "password", map[string]any{"secret": sealed(t, "pw")}),
			want: want{target: ResourceGenericSecret, form: FormPassword, scope: "account", writer: WriterOwner, values: []string{"secret"}},
		},
		{
			name: "manual api key becomes a generic secret",
			row:  row("w3", "api_key", map[string]any{"secret": sealed(t, "key"), "source_type": "service"}),
			want: want{target: ResourceGenericSecret, form: FormAPIKey, scope: "account", writer: WriterOwner, values: []string{"secret"}},
		},
		{
			name: "api key of an AI provider becomes an ai credential",
			row:  SourceRow{ID: "w4", TenantID: testTenant, ItemType: "api_key", Provider: "OpenAI", Metadata: map[string]any{"owner_id": "auth0|owner", "secret": sealed(t, "sk")}},
			want: want{target: ResourceAICredential, form: FormAPIKey, scope: "account", writer: WriterOwner, values: []string{"secret"}},
		},
		{
			name: "pasted oauth token without a provider becomes a generic secret",
			row:  row("w5", "oauth_token", map[string]any{"secret": sealed(t, "tok"), "source_type": "manual"}),
			want: want{target: ResourceGenericSecret, form: FormToken, scope: "account", writer: WriterOwner, values: []string{"secret"}},
		},
		{
			name: "oauth token of an OAuth provider becomes a connection",
			row:  row("w6", "oauth_token", map[string]any{"secret": sealed(t, "gho"), "provider": "github"}),
			want: want{target: ResourceOAuthConnection, form: FormOAuthToken, scope: "account", writer: WriterOwner, values: []string{"secret"}},
		},
		{
			name: "certificate becomes a generic secret in its stack project",
			row:  SourceRow{ID: "w7", TenantID: testTenant, StackID: "stack-1", ItemType: "certificate", Metadata: map[string]any{"owner_id": "auth0|owner", "secret": sealed(t, "pem"), "stack_id": "stack-1"}},
			want: want{target: ResourceGenericSecret, form: FormCertificate, scope: "project:stack-1", writer: WriterOwner, values: []string{"secret"}},
		},
		{
			name: "ssh key carries the private key and the public key from the notes",
			row:  row("w8", "ssh_key", map[string]any{"name": "Prod Key", "secret": sealed(t, "-----BEGIN OPENSSH PRIVATE KEY-----"), "notes": "deploy key\n" + publicKey}),
			want: want{target: ResourceNodeAccessProfile, form: FormPrivateKey, scope: "account", writer: WriterOwner, values: []string{"secret", "public_key", "notes"}, flags: []Flag{FlagLabelResolved}},
		},
		{
			name: "ssh public key alone is a node access profile",
			row:  row("w9", "ssh_key", map[string]any{"public_key": publicKey}),
			want: want{target: ResourceNodeAccessProfile, form: FormPublicKeyOnly, scope: "account", writer: WriterOwner, values: []string{"public_key"}, noFlags: []Flag{FlagNoSecretValue}},
		},
		{
			name: "wizard ssh password is a derived node access profile in its stack",
			row: SourceRow{ID: "stack-2:remote-ssh", TenantID: testTenant, StackID: "stack-2", ItemType: "ssh_password", Provider: "remote_ssh",
				Metadata: map[string]any{"owner_id": "auth0|owner", "owner_subject_id": "auth0|owner", "kind": "ssh_password", "name": "remote-ssh:stack-2", "stack_id": "stack-2", "secret": sealed(t, "pw")}},
			want: want{target: ResourceNodeAccessProfile, form: FormPassword, scope: "project:stack-2", writer: WriterRemoteSSHWizard, values: []string{"secret"}, flags: []Flag{FlagDerivedProjection, FlagLabelResolved}},
		},
		{
			name: "owner bootstrap recovery hash is recovery material",
			row: SourceRow{ID: "stack-3:stackkit:recovery", TenantID: testTenant, StackID: "stack-3", ItemType: "other", Provider: "stackkit",
				Metadata: map[string]any{"owner_id": "auth0|owner", "stack_id": "stack-3", "service_id": "stackkit:recovery", "item_class": "recovery", "auto_generated": true, "secret": sealed(t, "$argon2id$v=19$m=65536")}},
			want: want{target: ResourceGenericSecret, form: FormRecoveryMaterial, scope: "project:stack-3", writer: WriterOwnerBootstrap, values: []string{"secret"}, flags: []Flag{FlagDerivedProjection}},
		},
		{
			name: "launch entry without a value becomes configuration",
			row: SourceRow{ID: "wallet_0123456789abcdef01234567", TenantID: testTenant, StackID: "stack-4", ItemType: "other", Provider: "stackkit",
				Metadata: map[string]any{"owner_id": "auth0|owner", "stack_id": "stack-4", "service_id": "stackkit:login-gateway", "item_class": "launch", "url": "https://login.example.test", "auto_generated": true, "secret": ""}},
			want: want{target: ResourceConfiguration, form: FormNone, scope: "project:stack-4", writer: WriterStackKitOutputs, flags: []Flag{FlagNoSecretValue, FlagDerivedProjection}},
		},
		{
			name: "system account recovery password is a login in account scope",
			row: SourceRow{ID: "wallet_aaaaaaaaaaaaaaaaaaaaaaaa", TenantID: testTenant, ItemType: "password", Provider: "system_account",
				Metadata: map[string]any{"owner_id": "auth0|owner", "service_id": "system:pocketbase-admin", "username": "admin@local.host", "item_class": "recovery", "auto_generated": true, "secret": sealed(t, "pw")}},
			want: want{target: ResourceLogin, form: FormPassword, scope: "account", writer: WriterSystemAccount, values: []string{"secret", "username"}, flags: []Flag{FlagDerivedProjection}},
		},
		{
			name: "row of a deleted stack falls back to account scope",
			row:  row("w10", "api_key", map[string]any{"secret": sealed(t, "key"), "stack_id": "gone"}),
			want: want{target: ResourceGenericSecret, form: FormAPIKey, scope: "account", writer: WriterOwner, values: []string{"secret"}, flags: []Flag{FlagStackOrphaned}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanItem(tc.row, testTenant)
			if !plan.Eligible() {
				t.Fatalf("plan quarantined with %q", plan.Quarantine)
			}
			target := plan.Target
			if target.ResourceType != tc.want.target || target.Values.Form != tc.want.form || target.Scope.String() != tc.want.scope {
				t.Fatalf("target = %s/%s/%s, want %s/%s/%s", target.ResourceType, target.Values.Form, target.Scope, tc.want.target, tc.want.form, tc.want.scope)
			}
			if target.Custodian != CustodianWalletStore || target.MigrationKey == "" {
				t.Fatalf("target custodian %q key %q", target.Custodian, target.MigrationKey)
			}
			if plan.Source.Writer != tc.want.writer || plan.Source.ItemID != tc.row.ID {
				t.Fatalf("source = %+v, want writer %s", plan.Source, tc.want.writer)
			}
			present := map[string]bool{
				"secret": target.Values.Secret.Present(), "totp": target.Values.TOTP.Present(),
				"username": target.Values.Username.Present(), "public_key": target.Values.PublicKey.Present(),
				"notes": target.Values.Notes.Present(),
			}
			for _, field := range tc.want.values {
				if !present[field] {
					t.Errorf("value %s missing", field)
				}
			}
			for _, flag := range tc.want.flags {
				if !plan.HasFlag(flag) {
					t.Errorf("flag %s missing from %v", flag, plan.Flags)
				}
			}
			for _, flag := range tc.want.noFlags {
				if plan.HasFlag(flag) {
					t.Errorf("unexpected flag %s", flag)
				}
			}
		})
	}
}

func TestPlanItemQuarantinesRowsWithoutASoundMapping(t *testing.T) {
	truncated := auth.EncryptedPrefix + "c2hvcnQ="
	cases := map[string]struct {
		row  SourceRow
		want QuarantineReason
	}{
		"unknown kind":                {row("q1", "vpn_profile", map[string]any{"secret": "x"}), ReasonUnsupportedKind},
		"kind alias disagrees":        {row("q2", "api_key", map[string]any{"kind": "password", "secret": "x"}), ReasonKindConflict},
		"truncated enc:v1 envelope":   {row("q3", "api_key", map[string]any{"secret": truncated}), ReasonValueMalformed},
		"unknown envelope version":    {row("q4", "api_key", map[string]any{"secret": "enc:v2:AAAA"}), ReasonValueMalformed}, // #nosec G101 -- malformed envelope fixture, not a credential.
		"non-string totp":             {row("q5", "password", map[string]any{"username": "u", "secret": "x", "totp": 123456}), ReasonValueMalformed},
		"owner outranks value defect": {row("q6", "api_key", map[string]any{"owner_id": nil, "secret": "enc:v9:"}), ReasonOwnerMissing},
		// An owner the Wallet cannot keep items for is never guessed into a Wallet.
		"local account id owner": {row("q7", "api_key", map[string]any{"owner_id": "k3j2h1g0f9e8d7c", "secret": "x"}), ReasonOwnerUnlinked},
		"e-mail address owner":   {row("q8", "api_key", map[string]any{"owner_id": "alice@example.test", "secret": "x"}), ReasonOwnerUnlinked},
		"machine client owner":   {row("q9", "api_key", map[string]any{"owner_id": "auth0|abc@clients", "secret": "x"}), ReasonOwnerUnlinked},
		// A TOTP secret on a kind that cannot hold it would be dropped silently.
		"totp without a login home": {row("q10", "api_key", map[string]any{"secret": "x", "totp": "JBSWY3DP"}), ReasonValueUnmappable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			plan := PlanItem(tc.row, testTenant)
			if plan.Eligible() || plan.Quarantine != tc.want {
				t.Fatalf("quarantine = %q, want %q", plan.Quarantine, tc.want)
			}
		})
	}
}
