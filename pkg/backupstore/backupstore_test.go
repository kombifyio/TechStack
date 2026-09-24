package backupstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePurger struct {
	purged []string
	err    error
}

func (f *fakePurger) PurgeBucket(_ context.Context, bucket string) error {
	f.purged = append(f.purged, bucket)
	return f.err
}

func newTestServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Config{
		AccountID:  "acct123",
		APIToken:   "platform-token",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func writeCF(w http.ResponseWriter, success bool, result any, errs ...map[string]any) {
	payload := map[string]any{"success": success, "errors": errs, "result": result}
	_ = json.NewEncoder(w).Encode(payload)
}

func TestBucketName(t *testing.T) {
	cases := []struct {
		stackID string
		want    string
	}{
		{stackID: "024385d7-722c-4b1d-92fe-ebc7b7a54d82", want: "skbk-024385d7-722c-4b1d-92fe-ebc7b7a54d82"},
		{stackID: "Stack_ID With Junk!", want: "skbk-stack-id-with-junk"},
	}
	for _, tc := range cases {
		if got := BucketName(tc.stackID); got != tc.want {
			t.Fatalf("BucketName(%q) = %q, want %q", tc.stackID, got, tc.want)
		}
	}
	long := strings.Repeat("a", 100)
	if got := BucketName(long); len(got) > 63 || strings.HasSuffix(got, "-") {
		t.Fatalf("long name violates R2 constraints: %q", got)
	}
}

func TestFromEnvRequiresDedicatedProvisioningToken(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct123")
	t.Setenv("TECHSTACK_BACKUPSTORE_CF_API_TOKEN", "")
	t.Setenv("R2_API_TOKEN", "s3-only-token")

	if _, err := FromEnv(); err == nil {
		t.Fatal("expected the S3-only R2 token fallback to be rejected")
	}

	t.Setenv("TECHSTACK_BACKUPSTORE_CF_API_TOKEN", "provisioning-token")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.AccountID != "acct123" || cfg.APIToken != "provisioning-token" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestProvisionHappyPath(t *testing.T) {
	var tokenPayload map[string]any
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct123/r2/buckets":
			writeCF(w, true, map[string]any{"name": "skbk-stack-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct123/tokens/permission_groups":
			writeCF(w, true, []map[string]any{
				{"id": "pg-read", "name": "Workers R2 Storage Bucket Item Read"},
				{"id": "pg-write", "name": "Workers R2 Storage Bucket Item Write"},
				{"id": "pg-other", "name": "Something Else"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct123/tokens":
			if err := json.NewDecoder(r.Body).Decode(&tokenPayload); err != nil {
				t.Fatal(err)
			}
			writeCF(w, true, map[string]any{"id": "token-id-1", "value": "token-value-1"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	store, err := client.Provision(context.Background(), "stack-1")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if store.Bucket != "skbk-stack-1" || store.TokenID != "token-id-1" || store.AccessKeyID != "token-id-1" {
		t.Fatalf("store = %+v", store)
	}
	wantSecret := sha256.Sum256([]byte("token-value-1"))
	if store.SecretAccessKey != hex.EncodeToString(wantSecret[:]) {
		t.Fatalf("secret derivation wrong: %s", store.SecretAccessKey)
	}
	if store.Endpoint != "https://acct123.eu.r2.cloudflarestorage.com" {
		t.Fatalf("endpoint = %s", store.Endpoint)
	}

	// The token policy must be scoped to exactly the stack bucket, in the
	// same jurisdiction the bucket was created in.
	policies := tokenPayload["policies"].([]any)
	policy := policies[0].(map[string]any)
	resources := policy["resources"].(map[string]any)
	if _, ok := resources["com.cloudflare.edge.r2.bucket.acct123_eu_skbk-stack-1"]; !ok {
		t.Fatalf("token not bucket-scoped: %v", resources)
	}
	groups := policy["permission_groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("expected read+write permission groups: %v", groups)
	}
}

// TestJurisdictionReachesAllThreeDerivations pins the three places a
// jurisdiction has to appear together. R2 fixes a bucket's jurisdiction at
// creation, so a mismatch between them is not a degraded state that can be
// repaired in place: the bucket is in the wrong place permanently, and the
// token or the endpoint cannot address it.
func TestJurisdictionReachesAllThreeDerivations(t *testing.T) {
	var bucketJurisdiction string
	var tokenPayload map[string]any
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct123/r2/buckets":
			bucketJurisdiction = r.Header.Get("cf-r2-jurisdiction")
			writeCF(w, true, map[string]any{"name": "skbk-stack-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct123/tokens/permission_groups":
			writeCF(w, true, []map[string]any{
				{"id": "pg-read", "name": "Workers R2 Storage Bucket Item Read"},
				{"id": "pg-write", "name": "Workers R2 Storage Bucket Item Write"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct123/tokens":
			if r.Header.Get("cf-r2-jurisdiction") != "" {
				t.Fatalf("account token endpoint must not carry the R2 jurisdiction header")
			}
			if err := json.NewDecoder(r.Body).Decode(&tokenPayload); err != nil {
				t.Fatal(err)
			}
			writeCF(w, true, map[string]any{"id": "token-id-1", "value": "token-value-1"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	store, err := client.Provision(context.Background(), "stack-1")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// 1. The bucket is created in the EU jurisdiction.
	if bucketJurisdiction != JurisdictionEU {
		t.Fatalf("bucket created with jurisdiction %q, want %q", bucketJurisdiction, JurisdictionEU)
	}
	// 2. The persisted endpoint addresses that jurisdiction.
	if store.Endpoint != "https://acct123.eu.r2.cloudflarestorage.com" {
		t.Fatalf("endpoint = %q", store.Endpoint)
	}
	// 3. The bucket-scoped token names the same jurisdiction.
	resources := tokenPayload["policies"].([]any)[0].(map[string]any)["resources"].(map[string]any)
	if _, ok := resources["com.cloudflare.edge.r2.bucket.acct123_eu_skbk-stack-1"]; !ok {
		t.Fatalf("token resource does not name the eu jurisdiction: %v", resources)
	}
}

// TestDeleteBucketCarriesJurisdiction covers the other half: an EU bucket is
// invisible to a delete issued without the header, so the wipe path would
// report success while leaving the bucket and its objects behind.
func TestDeleteBucketCarriesJurisdiction(t *testing.T) {
	var seen string
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/accounts/acct123/r2/buckets/") {
			seen = r.Header.Get("cf-r2-jurisdiction")
			writeCF(w, true, nil)
			return
		}
		writeCF(w, true, nil)
	})

	if err := client.Wipe(context.Background(), "stack-1", "", nil); err != nil {
		t.Fatalf("Wipe: %v", err)
	}
	if seen != JurisdictionEU {
		t.Fatalf("delete bucket jurisdiction = %q, want %q", seen, JurisdictionEU)
	}
}

// TestUnsetJurisdictionResolvesToEU pins the safety direction of the default.
// Forgetting to configure a jurisdiction must place backups inside the EU,
// never outside it.
func TestUnsetJurisdictionResolvesToEU(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct123")
	t.Setenv("TECHSTACK_BACKUPSTORE_CF_API_TOKEN", "platform-token")
	t.Setenv("TECHSTACK_BACKUPSTORE_R2_JURISDICTION", "")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.Jurisdiction != JurisdictionEU {
		t.Fatalf("unset jurisdiction resolved to %q, want %q", cfg.Jurisdiction, JurisdictionEU)
	}

	endpoint, err := S3EndpointFor("acct123", "")
	if err != nil {
		t.Fatalf("S3EndpointFor: %v", err)
	}
	if endpoint != "https://acct123.eu.r2.cloudflarestorage.com" {
		t.Fatalf("endpoint for unset jurisdiction = %q", endpoint)
	}

	// The legacy jurisdiction stays addressable so pre-pin buckets can still
	// be found and cleaned up, but only when it is asked for explicitly.
	legacy, err := S3EndpointFor("acct123", JurisdictionDefault)
	if err != nil {
		t.Fatalf("S3EndpointFor(default): %v", err)
	}
	if legacy != "https://acct123.r2.cloudflarestorage.com" {
		t.Fatalf("legacy endpoint = %q", legacy)
	}

	if _, err := S3EndpointFor("acct123", "atlantis"); err == nil {
		t.Fatal("an unsupported jurisdiction must be rejected, not silently accepted")
	}
}

func TestProvisionToleratesExistingBucket(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct123/r2/buckets":
			w.WriteHeader(http.StatusConflict)
			writeCF(w, false, nil, map[string]any{"code": 10004, "message": "The bucket you tried to create already exists"})
		case r.URL.Path == "/accounts/acct123/tokens/permission_groups":
			writeCF(w, true, []map[string]any{
				{"id": "pg-read", "name": "Workers R2 Storage Bucket Item Read"},
				{"id": "pg-write", "name": "Workers R2 Storage Bucket Item Write"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct123/tokens":
			writeCF(w, true, map[string]any{"id": "token-id-2", "value": "token-value-2"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	store, err := client.Provision(context.Background(), "stack-1")
	if err != nil || store.TokenID != "token-id-2" {
		t.Fatalf("store = %+v, err = %v", store, err)
	}
}

func TestProvisionFailsWithoutPermissionGroups(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct123/r2/buckets":
			writeCF(w, true, nil)
		case r.URL.Path == "/accounts/acct123/tokens/permission_groups":
			writeCF(w, true, []map[string]any{{"id": "x", "name": "Unrelated"}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	if _, err := client.Provision(context.Background(), "stack-1"); err == nil {
		t.Fatal("expected permission group resolution failure")
	}
}

func TestWipeOrderAndIdempotency(t *testing.T) {
	var calls []string
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/accounts/acct123/r2/buckets/skbk-stack-1":
			writeCF(w, true, nil)
		case r.Method == http.MethodDelete && r.URL.Path == "/accounts/acct123/tokens/token-id-1":
			writeCF(w, true, nil)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	purger := &fakePurger{}
	if err := client.Wipe(context.Background(), "stack-1", "token-id-1", purger); err != nil {
		t.Fatalf("Wipe: %v", err)
	}
	if len(purger.purged) != 1 || purger.purged[0] != "skbk-stack-1" {
		t.Fatalf("purger calls = %v", purger.purged)
	}
	want := []string{
		"DELETE /accounts/acct123/r2/buckets/skbk-stack-1",
		"DELETE /accounts/acct123/tokens/token-id-1",
	}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestWipeToleratesMissingBucketAndToken(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		writeCF(w, false, nil, map[string]any{"code": 10006, "message": "The specified bucket does not exist"})
	})
	if err := client.Wipe(context.Background(), "stack-1", "token-gone", &fakePurger{}); err != nil {
		t.Fatalf("missing resources must be idempotent: %v", err)
	}
}

func TestWipeStopsWhenPurgeFails(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("bucket/token deletion must not run after purge failure: %s %s", r.Method, r.URL.Path)
	})
	purger := &fakePurger{err: fmt.Errorf("s3 unavailable")}
	if err := client.Wipe(context.Background(), "stack-1", "token-id-1", purger); err == nil {
		t.Fatal("expected purge failure to abort wipe")
	}
}
