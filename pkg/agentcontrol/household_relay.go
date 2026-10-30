package agentcontrol

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const HouseholdRelayPath = "/api/v1/internal/household/transient-result"
const HouseholdRelayCaller = "household-worker"
const HouseholdRelayScope = "household:transient-result"
const householdRelayLimit = 128 << 10

type HouseholdRelayClient struct {
	origin string
	config servicecall.Config
	http   *http.Client
}
type householdRelayRequest struct {
	Replica string          `json:"replica"`
	Tenant  string          `json:"tenant"`
	Agent   string          `json:"agent"`
	Nonce   string          `json:"nonce"`
	Digest  string          `json:"digest"`
	Command json.RawMessage `json:"command"`
}
type householdRelayFrame struct {
	Replica    string          `json:"replica,omitempty"`
	Nonce      string          `json:"nonce"`
	Digest     string          `json:"digest"`
	Registered bool            `json:"registered,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
}
type householdRelayStream struct {
	body          io.ReadCloser
	decoder       *json.Decoder
	nonce, digest string
}

func NewHouseholdRelayClient(origin, privateKey string) (*HouseholdRelayClient, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("fixed household relay HTTPS origin required")
	}
	kid, key, err := servicecall.ParseSigningKey(privateKey)
	if err != nil {
		return nil, errors.New("household relay signing custody required")
	}
	return &HouseholdRelayClient{origin: strings.TrimRight(origin, "/"), config: servicecall.Config{ServiceName: HouseholdRelayCaller,
		SigningKeyID: kid, SigningKey: key, Scope: HouseholdRelayScope, TokenTTL: time.Minute}, http: &http.Client{Timeout: transientIdentityLifetime,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func householdCommandDigest(command *agentpb.StackKitCommand) (string, error) {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(command)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (c *HouseholdRelayClient) register(ctx context.Context, tenant, agent string, command *agentpb.StackKitCommand) (*householdRelayStream, error) {
	challengeBinding, err := servicecall.NewRequestBinding(http.MethodGet, HouseholdRelayPath, nil)
	if err != nil {
		return nil, err
	}
	challengeToken, err := servicecall.IssueBoundToken(c.config, "techstack", challengeBinding, "")
	if err != nil {
		return nil, err
	}
	challenge, err := http.NewRequestWithContext(ctx, http.MethodGet, c.origin+HouseholdRelayPath, nil)
	if err != nil {
		return nil, err
	}
	challenge.Header.Set(servicecall.HeaderServiceAuth, challengeToken)
	response, err := c.http.Do(challenge)
	if err != nil {
		return nil, ErrTransientIdentityResultUnavailable
	}
	var replica householdRelayFrame
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&replica)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || len(replica.Replica) != 64 {
		return nil, ErrTransientIdentityResultUnavailable
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	digest, err := householdCommandDigest(command)
	if err != nil {
		return nil, err
	}
	raw, err := protojson.Marshal(command)
	if err != nil {
		return nil, err
	}
	envelope := householdRelayRequest{Replica: replica.Replica, Tenant: tenant, Agent: agent, Nonce: hex.EncodeToString(nonce), Digest: digest, Command: raw}
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	binding, err := servicecall.NewRequestBinding(http.MethodPost, HouseholdRelayPath, body)
	if err != nil {
		return nil, err
	}
	token, err := servicecall.IssueBoundToken(c.config, "techstack", binding, envelope.Nonce)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+HouseholdRelayPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set(servicecall.HeaderServiceAuth, token)
	req.Header.Set("Content-Type", "application/json")
	response, err = c.http.Do(req)
	if err != nil {
		return nil, ErrTransientIdentityResultUnavailable
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, ErrTransientIdentityResultUnavailable
	}
	stream := &householdRelayStream{body: response.Body, decoder: json.NewDecoder(io.LimitReader(response.Body, householdRelayLimit)), nonce: envelope.Nonce, digest: digest}
	var ack householdRelayFrame
	if stream.decoder.Decode(&ack) != nil || !ack.Registered || ack.Nonce != stream.nonce || ack.Digest != stream.digest || ack.Replica != replica.Replica || len(ack.Result) != 0 {
		stream.body.Close()
		return nil, ErrTransientIdentityResultUnavailable
	}
	return stream, nil
}

func (s *householdRelayStream) receive() (*agentpb.StackKitResult, error) {
	var frame householdRelayFrame
	if s.decoder.Decode(&frame) != nil || frame.Registered || frame.Nonce != s.nonce || frame.Digest != s.digest {
		return nil, ErrTransientIdentityResultUnavailable
	}
	defer clear(frame.Result)
	result := &agentpb.StackKitResult{}
	if protojson.Unmarshal(frame.Result, result) != nil {
		return nil, ErrTransientIdentityResultUnavailable
	}
	return result, nil
}

// ConfigureHouseholdRelay is worker-only configuration. The receiving App Hub
// has no worker DB custody and does not configure an outbound relay.
func (h *Hub) ConfigureHouseholdRelay(client *HouseholdRelayClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.householdRelay = client
}

// ServeHouseholdTransient is called ONLY behind the fixed bound-EdDSA route.
// It registers a proxy of exactly one original caller; it performs no SQL,
// command enqueue, owner impersonation, callback, replay or broadcast.
func (h *Hub) ServeHouseholdTransient(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		http.Error(w, "unavailable", 503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), transientIdentityLifetime)
	defer cancel()
	h.mu.Lock()
	if h.householdReplica == "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			h.mu.Unlock()
			http.Error(w, "unavailable", 503)
			return
		}
		h.householdReplica = hex.EncodeToString(raw)
	}
	replica := h.householdReplica
	h.mu.Unlock()
	if r.Method == http.MethodGet {
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(householdRelayFrame{Replica: replica})
		return
	}
	var envelope householdRelayRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, householdRelayLimit))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Replica != replica || envelope.Tenant == "" || envelope.Agent == "" || len(envelope.Nonce) != 64 {
		http.Error(w, "invalid binding", 400)
		return
	}
	if _, err := hex.DecodeString(envelope.Nonce); err != nil {
		http.Error(w, "invalid binding", 400)
		return
	}
	command := &agentpb.StackKitCommand{}
	if protojson.Unmarshal(envelope.Command, command) != nil || stackkitcommand.ValidateCommand(command) != nil ||
		command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_PROJECT || command.HouseholdProjectionAction != "add" || !needsTransientIdentityResult(command) {
		http.Error(w, "invalid command", 400)
		return
	}
	digest, err := householdCommandDigest(command)
	if err != nil || digest != envelope.Digest {
		http.Error(w, "invalid digest", 400)
		return
	}
	// Remember registration through the command authority window, even after
	// disconnect. A captured original request cannot install another waiter.
	key := transientIdentityKey{envelope.Tenant, envelope.Agent, command.CommandId}
	h.mu.Lock()
	if h.householdRelaySeen == nil {
		h.householdRelaySeen = make(map[transientIdentityKey]time.Time)
	}
	now := time.Now()
	for k, expiry := range h.householdRelaySeen {
		if now.After(expiry) {
			delete(h.householdRelaySeen, k)
		}
	}
	_, seen := h.householdRelaySeen[key]
	if seen || len(h.householdRelaySeen) >= 1024 {
		h.mu.Unlock()
		http.Error(w, "registration unavailable", 409)
		return
	}
	h.householdRelaySeen[key] = now.Add(CommandAuthorityLifetime)
	h.mu.Unlock()
	waiter, err := h.registerTransientIdentity(ctx, envelope.Tenant, envelope.Agent, command)
	if err != nil {
		http.Error(w, "original waiter exists", 409)
		return
	}
	defer h.removeTransientIdentity(envelope.Tenant, envelope.Agent, command.CommandId, waiter)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/x-ndjson")
	encoder := json.NewEncoder(w)
	if encoder.Encode(householdRelayFrame{Replica: replica, Nonce: envelope.Nonce, Digest: digest, Registered: true}) != nil {
		return
	}
	if http.NewResponseController(w).Flush() != nil {
		return
	}
	select {
	case result := <-waiter.result:
		defer clear(result.SensitiveResultJson)
		payload, err := protojson.Marshal(result)
		if err != nil {
			return
		}
		defer clear(payload)
		_ = encoder.Encode(householdRelayFrame{Nonce: envelope.Nonce, Digest: digest, Result: payload})
	case <-ctx.Done():
		return
	}
}
