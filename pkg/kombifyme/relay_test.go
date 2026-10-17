package kombifyme

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRelaySessionForwardsAuthorizedPrivateTarget(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/base/status" || r.URL.RawQuery != "full=1" {
			t.Errorf("target path = %q?%s", r.URL.Path, r.URL.RawQuery)
		}
		if got := r.Header.Get("X-Forwarded-Host"); got != "lab.kombify.me" {
			t.Errorf("X-Forwarded-Host = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer local-service-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "private=secret; Domain=127.0.0.1; Path=/")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer target.Close()

	result := make(chan relayHTTPResponse, 1)
	upgrader := websocket.Upgrader{}
	relayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Kombify-API-Key"); got != "kbi_test" {
			t.Errorf("API key = %q", got)
		}
		if got := r.URL.Query().Get("agent_id"); got != "node-1" {
			t.Errorf("agent_id = %q", got)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]any{"type": "handshake_ack", "payload": map[string]any{"success": true}})
		_ = conn.WriteJSON(map[string]any{
			"type":       "http_request",
			"request_id": "req-1",
			"payload": map[string]any{
				"method":      http.MethodGet,
				"path":        "/status?full=1",
				"headers":     map[string]string{"authorization": "Bearer local-service-token"},
				"host":        "lab.kombify.me",
				"target_addr": target.URL + "/base",
			},
		})

		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read response: %v", err)
			return
		}
		var message struct {
			Type      string            `json:"type"`
			RequestID string            `json:"request_id"`
			Payload   relayHTTPResponse `json:"payload"`
		}
		if err := json.Unmarshal(data, &message); err != nil {
			t.Errorf("decode response: %v", err)
			return
		}
		if message.Type != "http_response" || message.RequestID != "req-1" {
			t.Errorf("response envelope = %#v", message)
		}
		result <- message.Payload
	}))
	defer relayServer.Close()

	relay, err := NewRelay(RelayConfig{
		URL:     "ws" + strings.TrimPrefix(relayServer.URL, "http"),
		APIKey:  "kbi_test",
		AgentID: "node-1",
		Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = relay.RunSession(ctx)
	var sessionErr *RelaySessionError
	if !errors.As(err, &sessionErr) || sessionErr.ConnectedFor <= 0 {
		t.Fatalf("RunSession error = %v", err)
	}

	select {
	case response := <-result:
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d", response.StatusCode)
		}
		if response.Headers["Set-Cookie"] != "private=secret; Domain=127.0.0.1; Path=/" {
			t.Fatalf("Set-Cookie missing: %#v", response.Headers)
		}
		body, decodeErr := base64.StdEncoding.DecodeString(response.Body)
		if decodeErr != nil || string(body) != `{"ok":true}` {
			t.Fatalf("body = %q, err = %v", body, decodeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("relay response not received")
	}
}

func TestResolveRelayTargetDeniesMetadataAndAbsoluteRequestPath(t *testing.T) {
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://169.254.1.1/internal",
		"http://100.100.100.200/latest/meta-data",
		"http://168.63.129.16/machine",
		"http://metadata.google.internal",
		"http://[fd00:ec2::254]",
	} {
		if _, err := resolveRelayTarget(target, "/"); err == nil {
			t.Fatalf("metadata target accepted: %s", target)
		}
	}
	if _, err := resolveRelayTarget("http://127.0.0.1:5260", "https://attacker.example/"); err == nil {
		t.Fatal("absolute request path accepted")
	}
}

func TestDialRelayTargetDeniesMetadataAfterResolution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if _, err := dialRelayTarget(ctx, "tcp", "169.254.169.254:80"); err == nil ||
		!strings.Contains(err.Error(), "cloud metadata targets are denied") {
		t.Fatalf("metadata dial error = %v", err)
	}
}

func TestNewRelayFailsClosedWithoutCredentialOrIdentity(t *testing.T) {
	if _, err := NewRelay(RelayConfig{APIKey: "", AgentID: "node-1"}); err == nil {
		t.Fatal("missing API key accepted")
	}
	if _, err := NewRelay(RelayConfig{APIKey: "kbi_test", AgentID: ""}); err == nil {
		t.Fatal("missing agent ID accepted")
	}
}

// A relay with a proof provider authenticates with a fresh host proof per
// connection and no static key, and tells a permanent refusal of the binding
// (stop and report) from a retryable one.
func TestRelayHostProofModeSendsAFreshProofAndClassifiesRefusals(t *testing.T) {
	var proofs []string
	var mu sync.Mutex
	refusal := `{"error_code":"guard_relay_denied","reason_code":"binding_unknown","retryable":false}`
	status := http.StatusUnauthorized
	upgrader := websocket.Upgrader{}
	relayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proofs = append(proofs, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("X-Kombify-API-Key") != "" || r.URL.Query().Get("agent_id") != "" {
			t.Errorf("host proof mode sent a static credential: %q agent_id=%q", r.Header.Get("X-Kombify-API-Key"), r.URL.Query().Get("agent_id"))
		}
		if refusal != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(refusal))
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer relayServer.Close()

	minted := 0
	relay, err := NewRelay(RelayConfig{
		URL: "ws" + strings.TrimPrefix(relayServer.URL, "http"), Version: "test",
		ProofProvider: func(context.Context) (string, error) { minted++; return "proof-" + string(rune('a'+minted)), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	denial := AsRelayDenial(relay.RunSession(t.Context()))
	if denial == nil || denial.Status != http.StatusUnauthorized || denial.ReasonCode != "binding_unknown" || !denial.Permanent() {
		t.Fatalf("permanent refusal = %+v", denial)
	}

	refusal, status = `{"reason_code":"host_proof_expired","retryable":true}`, http.StatusUnauthorized
	if denial := AsRelayDenial(relay.RunSession(t.Context())); denial == nil || denial.Permanent() {
		t.Fatalf("retryable refusal = %+v", denial)
	}
	// An outage without an envelope is never read as a revoked binding.
	refusal, status = "<html>bad gateway</html>", http.StatusBadGateway
	if denial := AsRelayDenial(relay.RunSession(t.Context())); denial == nil || denial.Permanent() {
		t.Fatalf("proxy error = %+v", denial)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(proofs) != 3 || proofs[0] != "KombifyGuard proof-b" || proofs[0] == proofs[1] {
		t.Fatalf("Authorization headers = %v, want a fresh KombifyGuard proof per connection", proofs)
	}
}

func TestLoopbackOnlyRelayRefusesNonLoopbackTargets(t *testing.T) {
	hit := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	relay, err := NewRelay(RelayConfig{
		URL:           "wss://relay.example/ws",
		ProofProvider: func(context.Context) (string, error) { return "proof", nil },
		LoopbackOnly:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"http://10.0.0.5:9000", "http://192.168.1.10", "http://relay.example", "http://[fd00::1]:80"} {
		if _, _, _, err := relay.forwardHTTPRequest(context.Background(), relayHTTPRequest{Method: "GET", Path: "/", TargetAddr: addr}); err == nil {
			t.Fatalf("non-loopback target dialed: %s", addr)
		}
	}
	status, _, _, err := relay.forwardHTTPRequest(context.Background(), relayHTTPRequest{Method: "GET", Path: "/", TargetAddr: target.URL})
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("loopback target = %d, %v", status, err)
	}
	<-hit
}
