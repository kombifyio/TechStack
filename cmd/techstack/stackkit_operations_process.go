package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/denial"
	"github.com/kombifyio/techstack/internal/selfhostcontracts/runtimeexecutor"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
)

const (
	stackKitOperationsExecutableName = "techstack-stackkit-operations"
	stackKitOperationsEnrollmentPath = "/etc/techstack/agent-enrollment.json"
	stackKitOperationsRequestSchema  = "stackkit.standard-execution-request/v1"
	stackKitOperationsResponseSchema = "stackkit.standard-execution-result/v1"
	stackKitOperationsChannelRef     = "host-channel-cloud-main"
	stackKitOperationsSiteRef        = "cloud"
	stackKitOperationsNodeRef        = "cloud-main"
	maxStackKitOperationsPayload     = 16 << 20
)

type stackKitOperationsRequestEnvelope struct {
	SchemaVersion string                           `json:"schemaVersion"`
	ChannelRef    string                           `json:"channelRef"`
	Request       runtimeexecutor.ExecutionRequest `json:"request"`
}

type stackKitOperationsResponseEnvelope struct {
	SchemaVersion string                           `json:"schemaVersion"`
	ChannelRef    string                           `json:"channelRef"`
	Outcome       runtimeexecutor.ExecutionOutcome `json:"outcome"`
}

func isStackKitOperationsProcessMode(args []string) bool {
	if len(args) != 1 {
		return false
	}
	name := strings.ToLower(filepath.Base(args[0]))
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(name, ".exe")
	}
	return name == stackKitOperationsExecutableName
}

func runStackKitOperationsProcess(ctx context.Context) error {
	// In the privilege-split layout this process runs as a child of the root
	// executor and must not see the Guard's credential: it hands the request to
	// the unprivileged daemon, which owns the enrollment and the host key.
	if socket := stackKitOperationsSocket(); socket != "" {
		return proxyStackKitOperationsProcess(ctx, os.Stdin, os.Stdout, socket)
	}
	enrollmentPath := hostidentity.LocateEnrollment(stackKitOperationsEnrollmentPath, resolveGuardStateDir(""))
	if enrollment, err := loadAgentEnrollment(enrollmentPath); err == nil {
		if err := pinPrivateLAN(enrollment); err != nil {
			return fmt.Errorf("pin the private LAN bridge: %w", err)
		}
	}
	client := &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("StackKits operations endpoint redirects are forbidden")
		},
	}
	if identity := stackKitOperationsIdentity(enrollmentPath); identity != nil {
		client.Transport = identity.Transport(http.DefaultTransport)
	}
	return executeStackKitOperationsProcess(ctx, os.Stdin, os.Stdout, enrollmentPath, client)
}

// stackKitOperationsIdentity returns the host-key identity of the Guard that
// spawned this process, or nil while the Guard is still on the bearer
// credential. The main Guard process establishes and maintains the identity;
// this short-lived helper only signs its own request with the same key.
func stackKitOperationsIdentity(enrollmentPath string) *hostidentity.Client {
	manager, err := hostidentity.Open(resolveGuardStateDir(""))
	if err != nil || manager.Record().State != hostidentity.StateBound {
		return nil
	}
	enrollment, err := loadAgentEnrollment(enrollmentPath)
	if err != nil {
		return nil
	}
	endpoints := hostidentity.Endpoints{
		TokenURL:  firstCLIValue(enrollment.IdentityTokenURL, enrollment.ChannelBootstrap.IdentityTokenURL),
		BindURL:   firstCLIValue(enrollment.IdentityBindURL, enrollment.ChannelBootstrap.IdentityBindURL),
		RotateURL: firstCLIValue(enrollment.IdentityRotateURL, enrollment.ChannelBootstrap.IdentityRotateURL),
	}
	if endpoints.TokenURL == "" {
		endpoints = hostidentity.DeriveEndpoints(enrollment.HeartbeatURL)
	}
	client, err := hostidentity.NewClient(hostidentity.ClientConfig{
		Manager: manager, Endpoints: endpoints, AgentToken: enrollment.AgentToken,
		TenantID: firstCLIValue(enrollment.TenantID, enrollment.ChannelBootstrap.TenantID),
	})
	if err != nil || client.UseHostKey() != nil {
		return nil
	}
	return client
}

func executeStackKitOperationsProcess(ctx context.Context, input io.Reader, output io.Writer, enrollmentPath string, client *http.Client) error {
	if ctx == nil || input == nil || output == nil || client == nil {
		return errors.New("StackKits operations process is not configured")
	}
	payload, err := io.ReadAll(io.LimitReader(input, maxStackKitOperationsPayload+1))
	if err != nil {
		return fmt.Errorf("read StackKits operations request: %w", err)
	}
	if len(payload) == 0 || len(payload) > maxStackKitOperationsPayload {
		return errors.New("StackKits operations request is empty or exceeds the closed limit")
	}
	enrollment, err := loadAgentEnrollment(enrollmentPath)
	if err != nil {
		return fmt.Errorf("load StackKits operations enrollment: %w", err)
	}
	result, err := executeStackKitOperationsRequest(ctx, payload, enrollment, client)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("write StackKits operations result: %w", err)
	}
	return nil
}

// executeStackKitOperationsRequest validates one operations request against the
// enrolled stack and channel, sends it to the control plane with the Guard's
// credential and returns the decoded outcome. It is the only code that holds the
// credential for this seam.
func executeStackKitOperationsRequest(ctx context.Context, payload []byte, enrollment agentEnrollment, client *http.Client) (stackKitOperationsResponseEnvelope, error) {
	request, err := decodeStackKitOperationsRequest(payload)
	if err != nil {
		return stackKitOperationsResponseEnvelope{}, err
	}
	if err := validateStackKitOperationsBinding(request, enrollment); err != nil {
		return stackKitOperationsResponseEnvelope{}, err
	}
	endpoint, err := stackKitOperationsEndpoint(enrollment)
	if err != nil {
		return stackKitOperationsResponseEnvelope{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return stackKitOperationsResponseEnvelope{}, fmt.Errorf("create StackKits operations request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+enrollment.AgentToken)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("X-Kombify-Runtime-Agent-ID", enrollment.RuntimeAgentID)
	httpRequest.Header.Set("X-Kombify-Tenant-ID", enrollment.TenantID)

	response, err := client.Do(httpRequest)
	if err != nil {
		return stackKitOperationsResponseEnvelope{}, fmt.Errorf("execute authenticated StackKits operations request: %w", err)
	}
	defer response.Body.Close()
	responsePayload, err := io.ReadAll(io.LimitReader(response.Body, maxStackKitOperationsPayload+1))
	if err != nil {
		return stackKitOperationsResponseEnvelope{}, fmt.Errorf("read StackKits operations response: %w", err)
	}
	if len(responsePayload) > maxStackKitOperationsPayload {
		return stackKitOperationsResponseEnvelope{}, errors.New("StackKits operations response exceeds the closed limit")
	}
	if response.StatusCode != http.StatusOK {
		return stackKitOperationsResponseEnvelope{}, stackKitOperationsHTTPError(response.StatusCode, responsePayload)
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Type"))), "application/json") {
		return stackKitOperationsResponseEnvelope{}, errors.New("StackKits operations endpoint returned an unsupported content type")
	}
	result, err := decodeStackKitOperationsResponse(responsePayload, request.ChannelRef)
	if err != nil {
		return stackKitOperationsResponseEnvelope{}, err
	}
	return result, nil
}

func stackKitOperationsHTTPError(status int, payload []byte) error {
	if env, err := denial.Parse(payload); err == nil {
		return fmt.Errorf(
			"StackKits operations endpoint returned HTTP %d: code=%s reason=%s retryable=%t guidance=%s",
			status, env.ErrorCode, env.ReasonCode, env.Retryable, env.UserGuidance.Title,
		)
	}
	var envelope struct {
		Error struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Error.Code == "" {
		return fmt.Errorf("StackKits operations endpoint returned HTTP %d with an invalid error envelope", status)
	}
	reason, retryable, guidance, ok := stackKitOperationsDenialDetails(envelope.Error.Details)
	if !ok {
		return fmt.Errorf("StackKits operations endpoint returned HTTP %d with an invalid error envelope", status)
	}
	return fmt.Errorf(
		"StackKits operations endpoint returned HTTP %d: code=%s reason=%s retryable=%t guidance=%s",
		status, envelope.Error.Code, reason, retryable, guidance,
	)
}

func stackKitOperationsDenialDetails(raw json.RawMessage) (reason string, retryable bool, guidance string, ok bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", false, "", false
	}
	var details struct {
		ReasonCode   string          `json:"reason_code"`
		Retryable    bool            `json:"retryable"`
		UserGuidance json.RawMessage `json:"user_guidance"`
	}
	if err := json.Unmarshal(trimmed, &details); err != nil {
		return "", false, "", false
	}
	guidance, ok = stackKitOperationsGuidanceTitle(details.UserGuidance)
	if !ok {
		return "", false, "", false
	}
	return details.ReasonCode, details.Retryable, guidance, true
}

func stackKitOperationsGuidanceTitle(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", false
	}
	var guidance denial.UserGuidance
	if err := json.Unmarshal(trimmed, &guidance); err != nil {
		return "", false
	}
	if strings.TrimSpace(guidance.Title) == "" || strings.TrimSpace(guidance.Body) == "" || len(guidance.NextSteps) == 0 {
		return "", false
	}
	return guidance.Title, true
}

func decodeStackKitOperationsRequest(payload []byte) (stackKitOperationsRequestEnvelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var request stackKitOperationsRequestEnvelope
	if err := decoder.Decode(&request); err != nil {
		return request, fmt.Errorf("decode StackKits operations request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return request, errors.New("StackKits operations request contains trailing JSON")
	}
	if request.SchemaVersion != stackKitOperationsRequestSchema || request.ChannelRef == "" {
		return request, errors.New("StackKits operations request is not bound to the managed Cloud channel")
	}
	return request, nil
}

func validateStackKitOperationsBinding(envelope stackKitOperationsRequestEnvelope, enrollment agentEnrollment) error {
	if strings.TrimSpace(enrollment.AgentToken) == "" || strings.TrimSpace(enrollment.RuntimeAgentID) == "" ||
		strings.TrimSpace(enrollment.TenantID) == "" || strings.TrimSpace(enrollment.StackID) == "" {
		return errors.New("StackKits operations enrollment is incomplete")
	}
	request := envelope.Request
	if len(request.RuntimeTargets) == 1 {
		target := request.RuntimeTargets[0]
		if target.ProviderRef == "stackkits-home-assistant-appliance" && target.RuntimeKind == "external" && target.RuntimeDelivery == "external-control-plane" && target.RuntimeEngine == "api" && target.ExecutionChannelRef == envelope.ChannelRef && len(request.BackupTargetBindings) == 0 && len(request.AccessBindings) == 0 {
			// Backend authorization binds this sealed target to the authenticated
			// tenant, stack and runtime agent. The shim cannot select an endpoint.
			return request.Validate()
		}
	}
	if envelope.ChannelRef != stackKitOperationsChannelRef || len(request.RuntimeTargets) != 1 || len(request.BackupTargetBindings) != 1 {
		return errors.New("StackKits operations request must carry one exact runtime and backup target binding")
	}
	target := request.RuntimeTargets[0]
	binding := request.BackupTargetBindings[0]
	if target.ExecutionChannelRef != stackKitOperationsChannelRef ||
		len(target.SiteRefs) != 1 || target.SiteRefs[0] != stackKitOperationsSiteRef ||
		len(target.NodeRefs) != 1 || target.NodeRefs[0] != stackKitOperationsNodeRef ||
		binding.StackID != enrollment.StackID || binding.SiteRef != stackKitOperationsSiteRef ||
		len(binding.TargetNodeRefs) != 1 || binding.TargetNodeRefs[0] != stackKitOperationsNodeRef ||
		binding.RuntimeRequirementID != target.RequirementID || binding.ID == "" ||
		len(target.BackupTargetBindingRefs) != 1 || target.BackupTargetBindingRefs[0] != binding.ID {
		return errors.New("StackKits operations request escaped the enrolled stack or managed Cloud channel")
	}
	return nil
}

func stackKitOperationsEndpoint(enrollment agentEnrollment) (string, error) {
	heartbeat, err := url.Parse(strings.TrimSpace(enrollment.HeartbeatURL))
	if err != nil || heartbeat.Host == "" || heartbeat.User != nil || heartbeat.RawQuery != "" || heartbeat.Fragment != "" {
		return "", errors.New("StackKits operations enrollment has an invalid heartbeat URL")
	}
	if heartbeat.Scheme != "https" && !(heartbeat.Scheme == "http" && isLoopbackHost(heartbeat.Hostname())) {
		return "", errors.New("StackKits operations endpoint requires HTTPS outside loopback")
	}
	wantSuffix := "/api/v1/workers/" + url.PathEscape(enrollment.RuntimeAgentID) + "/heartbeat"
	if heartbeat.EscapedPath() != wantSuffix {
		return "", errors.New("StackKits operations heartbeat URL is not bound to the enrolled runtime agent")
	}
	heartbeat.Path = strings.TrimSuffix(heartbeat.Path, "/heartbeat") + "/stackkit/operations"
	heartbeat.RawPath = ""
	return heartbeat.String(), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	return net.ParseIP(strings.TrimSpace(host)).IsLoopback()
}

func decodeStackKitOperationsResponse(payload []byte, channelRef string) (stackKitOperationsResponseEnvelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var response stackKitOperationsResponseEnvelope
	if err := decoder.Decode(&response); err != nil {
		return response, fmt.Errorf("decode StackKits operations response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return response, errors.New("StackKits operations response contains trailing JSON")
	}
	if response.SchemaVersion != stackKitOperationsResponseSchema || response.ChannelRef != channelRef {
		return response, errors.New("StackKits operations response is not bound to the admitted channel")
	}
	return response, nil
}
