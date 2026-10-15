package guardreach

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"

	"github.com/kombifyio/techstack/pkg/kombifyme"
)

// Connect registration authenticates as the Techstack service with the exact
// scope of each operation and acts for the owner; every token is signed with the
// dedicated secret, lives under 60 seconds, carries a fresh jti and is bound to
// the exact method, request target and body sent; the revocation cursor is read
// with the machine token; denials surface as structured reasons.
func TestGatewayClientSignsExactScopeAndOwnerAndMapsDenials(t *testing.T) {
	const secret = "service-auth-secret-fixture"
	type seen struct {
		method, path string
		claims       *servicecall.Claims
		body         map[string]any
		jti          string
		bindingOK    bool
	}
	var mu sync.Mutex
	var calls []seen
	respond := func(w http.ResponseWriter, status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	denialFor := map[string]func(http.ResponseWriter){}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := servicecall.VerifyToken(r.Header.Get(servicecall.HeaderServiceAuth), secret, "")
		if err != nil {
			t.Errorf("service token rejected: %v", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		var payload struct {
			JTI string `json:"jti"`
		}
		segments := strings.Split(r.Header.Get(servicecall.HeaderServiceAuth), ".")
		raw, _ := base64.RawURLEncoding.DecodeString(segments[1])
		_ = json.Unmarshal(raw, &payload)
		bound := claims.RequestBinding != nil && claims.RequestBinding.Method == r.Method &&
			claims.RequestBinding.RequestURI == r.RequestURI &&
			claims.RequestBinding.BodySHA256 == fmt.Sprintf("%x", sha256.Sum256(data))
		mu.Lock()
		calls = append(calls, seen{r.Method, r.URL.Path, claims, body, payload.JTI, bound})
		mu.Unlock()
		if deny, ok := denialFor[r.URL.Path]; ok {
			deny(w)
			return
		}
		switch {
		case r.Method == http.MethodPut:
			respond(w, http.StatusCreated, map[string]any{"node_id": "tsn_1", "status": "active", "guard": map[string]any{"binding_generation": 3}})
		case r.URL.Path == gatewayRevokePath:
			respond(w, http.StatusOK, map[string]any{"node_id": "tsn_1", "status": "revoked", "revoked_now": true})
		default:
			respond(w, http.StatusOK, map[string]any{"events": []map[string]any{{"seq": 7, "tenant_id": "org_a", "server_id": "s", "binding_generation": 3, "revoked_by": "subject:x"}}, "next_cursor": 7, "has_more": false})
		}
	}))
	defer gateway.Close()
	client, err := NewGateway(GatewayConfig{BaseURL: gateway.URL, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}

	node, err := client.PutNode(t.Context(), NodeUpsert{
		OwnerSubject: "auth0|owner", ConnectTenantID: "org_a", Roles: []string{"developer"}, HostProof: "proof", KeyThumbprint: "thumb",
		DisplayName: "Basement", StackID: "stack-1", Placement: "byo",
	})
	if err != nil || node.NodeID != "tsn_1" || !node.Created || node.BindingGeneration != 3 {
		t.Fatalf("put = %+v, %v", node, err)
	}
	if revoked, err := client.RevokeNode(t.Context(), "auth0|owner", "org_a", nil, "server-1", "detached"); err != nil || !revoked.RevokedNow {
		t.Fatalf("revoke = %+v, %v", revoked, err)
	}
	page, err := client.Revocations(t.Context(), 4, 100)
	if err != nil || page.NextCursor != 7 || len(page.Events) != 1 || page.Events[0].RevokedBy != "subject:x" {
		t.Fatalf("revocations = %+v, %v", page, err)
	}

	mu.Lock()
	put, revoke, read := calls[0], calls[1], calls[2]
	mu.Unlock()
	for name, call := range map[string]seen{"put": put, "revoke": revoke} {
		c := call.claims
		if c.Svc != "techstack" || c.Iss != "kombify-techstack" || c.Aud != "kombify-gateway" || c.Scope != "connect:techstack-nodes:write" ||
			c.OnBehalfOf == nil || c.OnBehalfOf.Sub != "auth0|owner" || c.OnBehalfOf.OrgID != "org_a" || c.Exp-c.Iat > 60 {
			t.Errorf("%s claims = %+v obo=%+v", name, c, c.OnBehalfOf)
		}
	}
	ids := map[string]bool{}
	for name, call := range map[string]seen{"put": put, "revoke": revoke, "read": read} {
		if !call.bindingOK {
			t.Errorf("%s token is not bound to the request it was sent with: %+v", name, call.claims.RequestBinding)
		}
		if len(call.jti) < 16 || ids[call.jti] {
			t.Errorf("%s jti %q is missing or reused", name, call.jti)
		}
		ids[call.jti] = true
	}
	if put.claims.OnBehalfOf.Roles[0] != "developer" || put.body["host_proof"] != "proof" || put.body["placement"] != "byo" || put.body["stack_id"] != "stack-1" {
		t.Errorf("put body = %v", put.body)
	}
	if read.method != http.MethodGet || read.claims.Scope != "connect:techstack-nodes:read" || read.claims.OnBehalfOf != nil {
		t.Errorf("revocation read = %+v obo=%+v", read.claims, read.claims.OnBehalfOf)
	}

	denialFor[gatewayNodesPath] = func(w http.ResponseWriter) {
		respond(w, http.StatusConflict, map[string]any{"error_code": "connect_techstack_node_denied", "reason_code": "connect_techstack_node_owner_conflict", "retryable": false})
	}
	var denial *GatewayError
	if _, err := client.PutNode(t.Context(), NodeUpsert{OwnerSubject: "o", ConnectTenantID: "t"}); !errors.As(err, &denial) || denial.ReasonCode != "owner_conflict" || denial.Retryable {
		t.Fatalf("conflict = %v", err)
	}
	denialFor[gatewayNodesPath] = func(w http.ResponseWriter) { http.Error(w, "<html>upstream</html>", http.StatusBadGateway) }
	if _, err := client.PutNode(t.Context(), NodeUpsert{OwnerSubject: "o", ConnectTenantID: "t"}); !errors.As(err, &denial) || !denial.Retryable || denial.ReasonCode != "" {
		t.Fatalf("outage = %v", err)
	}
	denialFor[gatewayRevokePath] = func(w http.ResponseWriter) {
		respond(w, http.StatusNotFound, map[string]any{"reason_code": "connect_techstack_node_node_not_found"})
	}
	if node, err := client.RevokeNode(t.Context(), "o", "t", nil, "never-registered", "detached"); err != nil || node != nil {
		t.Fatalf("revoking an unregistered server = %+v, %v", node, err)
	}
	if _, err := NewGateway(GatewayConfig{BaseURL: "http://gateway.example", Secret: secret}); err == nil {
		t.Fatal("a plain-HTTP Gateway URL was accepted")
	}
	if _, err := NewGateway(GatewayConfig{BaseURL: gateway.URL}); err == nil {
		t.Fatal("a Gateway client without the dedicated secret was accepted")
	}
}

// The kombify.me client creates an exposed tunnel route bound to the stack and
// registers the server, and refuses a response that does not prove the binding.
func TestMeClientCreatesBoundTunnelRouteAndRefusesUnprovenBinding(t *testing.T) {
	address, err := kombifyme.NewManagedAddress("org_a", "auth0|owner", "stack-1")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []string
	var registered map[string]any
	unbound := false
	me := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Kombify-API-Key") != "kbi_platform" {
			t.Errorf("platform key = %q", r.Header.Get("X-Kombify-API-Key"))
		}
		var body map[string]any
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		mu.Lock()
		requests = append(requests, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/api/v1/"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		bound := map[string]any{"binding_owner_ref": address.OwnerRef, "binding_stack_ref": address.StackRef}
		if unbound {
			bound = map[string]any{}
		}
		merge := func(m map[string]any) map[string]any {
			for k, v := range bound {
				m[k] = v
			}
			return m
		}
		switch strings.TrimPrefix(r.URL.Path, "/api/v1/") {
		case "auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"device_fingerprint": "abcdef123456"})
		case "subdomains/auto-register":
			if body["homelab_name"] != address.Label || body["kind"] != "self-hosted" {
				t.Errorf("base registration = %v", body)
			}
			_ = json.NewEncoder(w).Encode(merge(map[string]any{"id": "base-1", "name": "sh-lab-abcdef", "fqdn": "sh-lab-abcdef.kombify.me", "subdomain_kind": "base"}))
		case "subdomains/auto-register/service":
			if body["target_type"] != "tunnel" || body["local_addr"] != "http://127.0.0.1:80" || body["service_name"] != "gabc" {
				t.Errorf("route registration = %v", body)
			}
			_ = json.NewEncoder(w).Encode(merge(map[string]any{"id": "route-1", "parent_id": "base-1", "name": "sh-lab-abcdef-gabc", "fqdn": "sh-lab-abcdef-gabc.kombify.me", "target_type": "tunnel"}))
		case "subdomains/base-1/services/route-1/expose":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "route-1", "exposed": true})
		case "guard/servers":
			registered = body
			_ = json.NewEncoder(w).Encode(map[string]any{"generation": body["generation"], "state": "active"})
		case "guard/servers/revoke":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error_code": "guard_relay_denied", "reason_code": "binding_not_found", "retryable": false})
		}
	}))
	defer me.Close()
	client := NewMe(kombifyme.NewClient(kombifyme.Config{DirectAPIBase: me.URL + "/api/v1", APIKey: "kbi_platform"}))

	route, err := client.EnsureRoute(t.Context(), address, "gabc", "http://127.0.0.1:80", "kombify Guard reach")
	if err != nil || route.Name != "sh-lab-abcdef-gabc" || route.FQDN != "sh-lab-abcdef-gabc.kombify.me" {
		t.Fatalf("route = %+v, %v", route, err)
	}
	if err := client.RegisterServer(t.Context(), MeRegistration{
		TenantID: "org_a", ServerID: "server-1", WorkerID: "worker-1", Address: address, KeyThumbprint: "thumb", Generation: 2, Routes: []string{route.Name},
	}); err != nil {
		t.Fatal(err)
	}
	if registered["owner_ref"] != address.OwnerRef || registered["stack_ref"] != address.StackRef || registered["generation"] != float64(2) ||
		registered["tenant_id"] != "org_a" || registered["guard_key_thumbprint"] != "thumb" {
		t.Fatalf("server registration = %v", registered)
	}
	// Revoking a server kombify.me never bound is not an error.
	if err := client.RevokeServer(t.Context(), "org_a", "server-1", "detached"); err != nil {
		t.Fatalf("revoke of an unbound server = %v", err)
	}

	unbound = true
	var denial *MeError
	if _, err := client.EnsureRoute(t.Context(), address, "gabc", "http://127.0.0.1:80", "x"); !errors.As(err, &denial) || denial.ReasonCode != "base_binding_unproven" {
		t.Fatalf("unproven binding = %v", err)
	}
}
