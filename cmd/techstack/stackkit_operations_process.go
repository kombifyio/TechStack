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

	"github.com/kombifyio/go-common/runtimeexecutor"
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
	client := &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("StackKits operations endpoint redirects are forbidden")
		},
	}
	return executeStackKitOperationsProcess(ctx, os.Stdin, os.Stdout, stackKitOperationsEnrollmentPath, client)
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
	request, err := decodeStackKitOperationsRequest(payload)
	if err != nil {
		return err
	}
	enrollment, err := loadAgentEnrollment(enrollmentPath)
	if err != nil {
		return fmt.Errorf("load StackKits operations enrollment: %w", err)
	}
	if err := validateStackKitOperationsBinding(request, enrollment); err != nil {
		return err
	}
	endpoint, err := stackKitOperationsEndpoint(enrollment)
	if err != nil {
		return err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create StackKits operations request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+enrollment.AgentToken)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("X-Kombify-Runtime-Agent-ID", enrollment.RuntimeAgentID)
	httpRequest.Header.Set("X-Kombify-Tenant-ID", enrollment.TenantID)

	response, err := client.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("execute authenticated StackKits operations request: %w", err)
	}
	defer response.Body.Close()
	responsePayload, err := io.ReadAll(io.LimitReader(response.Body, maxStackKitOperationsPayload+1))
	if err != nil {
		return fmt.Errorf("read StackKits operations response: %w", err)
	}
	if len(responsePayload) > maxStackKitOperationsPayload {
		return errors.New("StackKits operations response exceeds the closed limit")
	}
	if response.StatusCode != http.StatusOK {
		return stackKitOperationsHTTPError(response.StatusCode, responsePayload)
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Type"))), "application/json") {
		return errors.New("StackKits operations endpoint returned an unsupported content type")
	}
	result, err := decodeStackKitOperationsResponse(responsePayload, request.ChannelRef)
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

func stackKitOperationsHTTPError(status int, payload []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				ReasonCode   string `json:"reason_code"`
				Retryable    bool   `json:"retryable"`
				UserGuidance string `json:"user_guidance"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err == nil && envelope.Error.Code != "" {
		return fmt.Errorf(
			"StackKits operations endpoint returned HTTP %d: code=%s reason=%s retryable=%t guidance=%s",
			status, envelope.Error.Code, envelope.Error.Details.ReasonCode,
			envelope.Error.Details.Retryable, envelope.Error.Details.UserGuidance,
		)
	}
	return fmt.Errorf("StackKits operations endpoint returned HTTP %d with an invalid error envelope", status)
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
	if request.SchemaVersion != stackKitOperationsRequestSchema || request.ChannelRef != stackKitOperationsChannelRef {
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
	if len(request.RuntimeTargets) != 1 || len(request.BackupTargetBindings) != 1 {
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
