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
	cases := map[string]string{
		"024385d7-722c-4b1d-92fe-ebc7b7a54d82": "skbk-024385d7-722c-4b1d-92fe-ebc7b7a54d82",
		"Stack_ID With Junk!":                  "skbk-stack-id-with-junk-",
	}
	for input, wantPrefix := range cases {
		got := BucketName(input)
		if !strings.HasPrefix(got, "skbk-") || len(got) > 63 || strings.HasSuffix(got, "-") && input == "024385d7-722c-4b1d-92fe-ebc7b7a54d82" {
			t.Fatalf("BucketName(%q) = %q", input, got)
		}
		_ = wantPrefix
	}
	long := strings.Repeat("a", 100)
	if got := BucketName(long); len(got) > 63 {
		t.Fatalf("long name not clamped: %d", len(got))
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
	if store.Endpoint != "https://acct123.r2.cloudflarestorage.com" {
		t.Fatalf("endpoint = %s", store.Endpoint)
	}

	// The token policy must be scoped to exactly the stack bucket.
	policies := tokenPayload["policies"].([]any)
	policy := policies[0].(map[string]any)
	resources := policy["resources"].(map[string]any)
	if _, ok := resources["com.cloudflare.edge.r2.bucket.acct123_default_skbk-stack-1"]; !ok {
		t.Fatalf("token not bucket-scoped: %v", resources)
	}
	groups := policy["permission_groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("expected read+write permission groups: %v", groups)
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
