package routes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/devicereadiness"
	"github.com/kombifyio/techstack/pkg/identity"
	"golang.org/x/crypto/ssh"
)

// Exercise the HTTP boundary over real loopback SSH, without executing any
// host commands: the repair succeeds, but the subsequent observation fails.
func TestDevicePrepareDoesNotReturnStaleReportAfterObservationFailure(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		probes := 0
		for incoming := range channels {
			channel, requests, err := incoming.Accept()
			if err != nil {
				return
			}
			for request := range requests {
				if request.Type != "exec" {
					_ = request.Reply(false, nil)
					continue
				}
				var command struct{ Command string }
				if ssh.Unmarshal(request.Payload, &command) != nil {
					return
				}
				_ = request.Reply(true, nil)
				status := uint32(0)
				if strings.HasSuffix(command.Command, "sh -s") {
					_, _ = io.Copy(io.Discard, channel)
					probes++
					if probes == 1 {
						_, _ = io.WriteString(channel, `{}`)
					} else {
						status = 1
					}
				}
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
				_ = channel.Close()
				break
			}
		}
	}()
	router := readinessRouter(t, DeviceReadinessRouteConfig{LocalDeployment: true,
		Entitled: func(context.Context, string, string) (bool, error) { return true, nil }})
	body, err := json.Marshal(devicePrepareRequest{Connection: deviceConnection{
		Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "fixture", Password: "fixture",
		HostKeyFingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
	}, Resolutions: []string{devicereadiness.ResolutionClockSet}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/device-readiness/prepare", strings.NewReader(string(body)))
	request = request.WithContext(identity.NewContext(request.Context(), &identity.Identity{UserID: "fixture", OrgID: "fixture"}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	_ = listener.Close()
	<-finished
	if response.Code != http.StatusBadGateway {
		t.Fatalf("failed observation returned %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Error struct {
			Details struct {
				Receipts []devicereadiness.Receipt `json:"receipts"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Error.Details.Receipts) == 0 || !result.Error.Details.Receipts[0].Succeeded {
		t.Fatalf("completed repair receipt was lost: %s", response.Body.String())
	}
}
