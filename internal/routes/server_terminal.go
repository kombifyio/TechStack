package routes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/authsession"
	"github.com/kombifyio/techstack/internal/executionchannel"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

const (
	terminalIdleLimit     = 10 * time.Minute
	terminalHardLimit     = 60 * time.Minute
	terminalReauthPurpose = "kombify.cloud.server-terminal.v1"
	terminalReauthSkew    = 30 * time.Second
)

var shellIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9._:@\[\]-]+$`)

type ServerTerminalRouteConfig struct {
	Servers  controlplane.ServerRuntimeStore
	Targets  jobs.ManagedRuntimeTargetResolver
	Wallet   controlplane.WalletStore
	Activity controlplane.ActivityStore
	Now      func() time.Time
}

type serverTerminalHandlers struct {
	servers    controlplane.ServerRuntimeStore
	targets    jobs.ManagedRuntimeTargetResolver
	wallet     controlplane.WalletStore
	activity   controlplane.ActivityStore
	now        func() time.Time
	installKey func(context.Context, *jobs.ManagedRuntimeTarget, string, string) error
	channel    *executionchannel.Channel
	mu         sync.Mutex
	sessions   map[string]*serverTerminalSession
	active     map[string]string
}

type serverTerminalSession struct {
	id, tenantID, ownerID, serverID, stackID string
	target                                   *jobs.ManagedRuntimeTarget
	hostKeyFingerprint                       string
	createdAt, expiresAt                     time.Time
	attached                                 bool
	bytesIn, bytesOut                        atomic.Int64
}

type serverAccessResponse struct {
	ServerID               string `json:"server_id"`
	Host                   string `json:"host,omitempty"`
	User                   string `json:"user,omitempty"`
	Port                   int    `json:"port,omitempty"`
	HostKeyFingerprint     string `json:"host_key_fingerprint,omitempty"`
	UserKeyFingerprint     string `json:"user_key_fingerprint,omitempty"`
	SSHCommand             string `json:"ssh_command,omitempty"`
	TerminalEnabled        bool   `json:"terminal_enabled"`
	ManualCommandAvailable bool   `json:"manual_command_available"`
	ConnectReady           bool   `json:"connect_ready"`
	// OwnerSSHAccess is the generation-bound owner SSH grant: "enabled" or
	// "disabled". Disabled revokes direct owner SSH; the Kombify-mediated
	// browser terminal is governed separately by fresh re-authentication.
	OwnerSSHAccess string `json:"owner_ssh_access"`
	Reason         string `json:"reason,omitempty"`
}

type createTerminalSessionRequest struct {
	ReauthTimestamp string `json:"reauth_timestamp"`
	ReauthSignature string `json:"reauth_signature"`
}

type authorizeServerKeyRequest struct {
	WalletItemID    string `json:"wallet_item_id"`
	Confirm         bool   `json:"confirm"`
	ReauthTimestamp string `json:"reauth_timestamp"`
	ReauthSignature string `json:"reauth_signature"`
}

type createTerminalSessionResponse struct {
	SessionID        string    `json:"session_id"`
	StreamURL        string    `json:"stream_url"`
	ExpiresAt        time.Time `json:"expires_at"`
	IdleLimitSeconds int64     `json:"idle_limit_seconds"`
}

type terminalWireMessage struct {
	Type   string `json:"type"`
	Data   string `json:"data,omitempty"`
	Cols   int    `json:"cols,omitempty"`
	Rows   int    `json:"rows,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func RegisterServerTerminalRoutes(r *httpx.Router, cfg ServerTerminalRouteConfig) {
	if cfg.Servers == nil || cfg.Targets == nil {
		return
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	h := &serverTerminalHandlers{
		servers: cfg.Servers, targets: cfg.Targets, wallet: cfg.Wallet, activity: cfg.Activity, now: cfg.Now,
		installKey: installAuthorizedKey,
		channel:    &executionchannel.Channel{Servers: cfg.Servers, Targets: cfg.Targets, Now: cfg.Now},
		sessions:   map[string]*serverTerminalSession{}, active: map[string]string{},
	}
	r.GET("/api/v1/servers/{serverId}/access", h.access)
	r.POST("/api/v1/servers/{serverId}/access/authorized-key", h.authorizeUserKey)
	r.POST("/api/v1/servers/{serverId}/terminal-sessions", h.createSession)
	r.GET("/api/v1/terminal-sessions/{sessionId}/stream", h.stream)
}

func (h *serverTerminalHandlers) access(e *httpx.Event) error {
	server, ownerID, err := h.ownedServer(e, "techstack.servers.access")
	if err != nil || server == nil {
		return err
	}
	access := h.resolveAccess(e.Request.Context(), *server, ownerID)
	return httpx.Success(e, http.StatusOK, access)
}

func (h *serverTerminalHandlers) createSession(e *httpx.Event) error {
	server, ownerID, err := h.ownedServer(e, "techstack.servers.terminal")
	if err != nil || server == nil {
		return err
	}
	var request createTerminalSessionRequest
	if e.Request.Body != nil && e.Request.ContentLength != 0 {
		if bindErr := e.BindBody(&request); bindErr != nil {
			return httpx.BadRequest(e, "Invalid terminal session request", nil)
		}
	}
	timestamp := firstNonEmptyWallet(request.ReauthTimestamp, e.Request.Header.Get("X-Kombify-Reauth-Timestamp"), e.Request.Header.Get("X-TechStack-Reauth-Timestamp"))
	signature := firstNonEmptyWallet(request.ReauthSignature, e.Request.Header.Get("X-Kombify-Reauth-Signature"), e.Request.Header.Get("X-TechStack-Reauth-Signature"))
	if !h.validReauth(e.Request.Context(), ownerID, server.ID, timestamp, signature) {
		h.audit(e.Request.Context(), *server, ownerID, "server_terminal_denied", "warning", map[string]any{"error_class": "reauth_required"})
		return httpx.Forbidden(e, "Fresh server-terminal re-authentication required")
	}
	access := h.resolveAccess(e.Request.Context(), *server, ownerID)
	if !access.TerminalEnabled {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Browser terminal is unavailable", map[string]any{"reason": access.Reason})
	}
	target, resolveErr := h.resolveTarget(e.Request.Context(), *server, ownerID)
	if resolveErr != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Managed server access is unavailable", nil)
	}
	now := h.now().UTC()
	key := ownerID + "\x00" + server.ID
	h.mu.Lock()
	if existingID := h.active[key]; existingID != "" {
		if existing := h.sessions[existingID]; existing != nil && now.Before(existing.expiresAt) {
			h.mu.Unlock()
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "A terminal session is already active for this server", nil)
		}
		delete(h.sessions, existingID)
	}
	session := &serverTerminalSession{
		id: uuid.NewString(), tenantID: server.TenantID, ownerID: ownerID, serverID: server.ID, stackID: server.StackID,
		target: target, hostKeyFingerprint: access.HostKeyFingerprint, createdAt: now, expiresAt: now.Add(terminalHardLimit),
	}
	h.sessions[session.id], h.active[key] = session, session.id
	h.mu.Unlock()
	time.AfterFunc(terminalHardLimit, func() { h.expireUnattached(session.id) })
	h.audit(e.Request.Context(), *server, ownerID, "server_terminal_started", "info", nil)
	return httpx.Success(e, http.StatusCreated, createTerminalSessionResponse{
		SessionID: session.id, StreamURL: terminalStreamURL(e.Request, session.id),
		ExpiresAt: session.expiresAt, IdleLimitSeconds: int64(terminalIdleLimit.Seconds()),
	})
}

func (h *serverTerminalHandlers) authorizeUserKey(e *httpx.Event) error {
	server, ownerID, err := h.ownedServer(e, "techstack.servers.access.authorize-key")
	if err != nil || server == nil {
		return err
	}
	if h.wallet == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wallet custody is unavailable", nil)
	}
	if !executionchannel.OwnerSSHAccessFromMetadata(server.Metadata).Enabled() {
		return ownerSSHAccessDisabled(e)
	}
	var request authorizeServerKeyRequest
	if bindErr := e.BindBody(&request); bindErr != nil || !request.Confirm || strings.TrimSpace(request.WalletItemID) == "" {
		return httpx.BadRequest(e, "A confirmed Wallet SSH key is required", nil)
	}
	timestamp := firstNonEmptyWallet(request.ReauthTimestamp, e.Request.Header.Get("X-Kombify-Reauth-Timestamp"), e.Request.Header.Get("X-TechStack-Reauth-Timestamp"))
	signature := firstNonEmptyWallet(request.ReauthSignature, e.Request.Header.Get("X-Kombify-Reauth-Signature"), e.Request.Header.Get("X-TechStack-Reauth-Signature"))
	if !h.validReauth(e.Request.Context(), ownerID, server.ID, timestamp, signature) {
		h.audit(e.Request.Context(), *server, ownerID, "server_ssh_key_denied", "warning", map[string]any{"error_class": "reauth_required"})
		return httpx.Forbidden(e, "Fresh server-access re-authentication required")
	}
	item, itemErr := h.wallet.GetWalletItem(e.Request.Context(), server.TenantID, strings.TrimSpace(request.WalletItemID))
	if itemErr != nil || item == nil || !walletItemOwnedBy(item.Metadata, ownerID) {
		return httpx.NotFound(e, "Wallet SSH key not found")
	}
	publicKey, userFingerprint, keyErr := walletSSHPublicKey(item)
	if keyErr != nil {
		return httpx.BadRequest(e, "Wallet item does not contain a valid SSH public key", nil)
	}
	target, targetErr := h.resolveTarget(e.Request.Context(), *server, ownerID)
	if targetErr != nil || !executionchannel.HasCredential(target) {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Managed server access is unavailable", nil)
	}
	hostFingerprint, _, _, hostErr := executionchannel.PinnedHostKey(target.SSHHostKey)
	if hostErr != nil {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Confirmed server host key is required", nil)
	}
	installCtx, cancel := context.WithTimeout(e.Request.Context(), 30*time.Second)
	defer cancel()
	installer := h.installKey
	if installer == nil {
		installer = installAuthorizedKey
	}
	if installErr := installer(installCtx, target, hostFingerprint, publicKey); installErr != nil {
		h.audit(e.Request.Context(), *server, ownerID, "server_ssh_key_denied", "warning", map[string]any{"error_class": "installation_failed"})
		return httpx.Error(e, http.StatusBadGateway, ksapi.ErrCodeUnavailable, "SSH public key installation failed", nil)
	}
	if persistErr := h.persistUserKeyFingerprint(e.Request.Context(), *server, userFingerprint); persistErr != nil {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "SSH key was installed but access evidence could not be persisted; refresh and retry", nil)
	}
	if h.channel != nil {
		if grantErr := h.channel.RecordAuthorizedOwnerKey(e.Request.Context(), *server, executionchannel.OwnerSSHKey{
			Fingerprint: userFingerprint, PublicKey: publicKey,
		}, ownerID); grantErr != nil {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "SSH key was installed but the owner SSH grant could not be persisted; refresh and retry", nil)
		}
	}
	metadata := make(map[string]any, len(server.Metadata)+1)
	for key, value := range server.Metadata {
		metadata[key] = value
	}
	metadata["user_ssh_key_fingerprint"] = userFingerprint
	server.Metadata = metadata
	h.audit(e.Request.Context(), *server, ownerID, "server_ssh_key_authorized", "info", map[string]any{"user_key_fingerprint": userFingerprint})
	return httpx.Success(e, http.StatusOK, h.resolveAccess(e.Request.Context(), *server, ownerID))
}

// ownerSSHAccessDisabled explains why key authorization is refused and how to
// restore it, without dispatching any node change.
func ownerSSHAccessDisabled(e *httpx.Event) error {
	return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Owner SSH access is disabled for this server", map[string]any{
		"error_code":  "managed_ssh_access_disabled",
		"reason_code": "owner_ssh_access_disabled",
		"retryable":   false,
		"user_guidance": map[string]any{
			"title": "SSH access is turned off",
			"body":  "Direct SSH for this managed server was disabled. No key was installed.",
			"next_steps": []string{
				"Turn SSH access on for this server, then authorize your Wallet SSH key again.",
			},
		},
	})
}

func (h *serverTerminalHandlers) validReauth(ctx context.Context, ownerID, resourceID, timestamp, signature string) bool {
	if claims, err := authsession.ClaimsFrom(ctx); err == nil &&
		claims.Subject == ownerID &&
		claims.ReauthPurpose == terminalReauthPurpose &&
		claims.ReauthResource == resourceID {
		authenticatedAt := time.Unix(claims.AuthenticatedAt, 0).UTC()
		age := h.now().UTC().Sub(authenticatedAt)
		if claims.AuthenticatedAt > 0 && age <= walletReauthWindow && age >= -terminalReauthSkew {
			return true
		}
	}
	secret := walletReauthSecret()
	if secret == "" {
		return false
	}
	return verifyWalletSignature(ownerID, resourceID, timestamp, signature, secret, terminalReauthPurpose, h.now()) == nil ||
		verifyWalletSignature(ownerID, resourceID, timestamp, signature, secret, walletPlatformReauthAssertionPurpose, h.now()) == nil
}

func terminalStreamURL(request *http.Request, sessionID string) string {
	host := firstNonEmptyString(strings.TrimSpace(request.Header.Get("X-Forwarded-Host")), request.Host)
	defaultProto := "http"
	if request.TLS != nil {
		defaultProto = "https"
	}
	proto := strings.ToLower(firstNonEmptyString(strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")), defaultProto))
	scheme := "wss"
	if proto == "http" {
		scheme = "ws"
	}
	return scheme + "://" + host + "/api/v1/terminal-sessions/" + url.PathEscape(sessionID) + "/stream"
}

func (h *serverTerminalHandlers) stream(e *httpx.Event) error {
	ownerID, _, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	sessionID := strings.TrimSpace(e.Request.PathValue("sessionId"))
	h.mu.Lock()
	session := h.sessions[sessionID]
	if session == nil || session.ownerID != ownerID || session.attached || !h.now().Before(session.expiresAt) {
		h.mu.Unlock()
		return httpx.NotFound(e, "Terminal session not found")
	}
	session.attached = true
	h.mu.Unlock()
	upgrader := websocket.Upgrader{CheckOrigin: terminalOriginAllowed, EnableCompression: false}
	e.MarkResponseStreamed()
	conn, err := upgrader.Upgrade(e.Response, e.Request, nil)
	if err != nil {
		h.finish(session, "upgrade_failed")
		return nil
	}
	defer conn.Close()
	reason := h.runTerminal(e.Request.Context(), conn, session)
	h.finish(session, reason)
	return nil
}

func (h *serverTerminalHandlers) runTerminal(ctx context.Context, conn *websocket.Conn, terminal *serverTerminalSession) string {
	client, sshSession, stdin, stdout, err := openPinnedSSH(ctx, terminal.target, terminal.hostKeyFingerprint)
	if err != nil {
		_ = conn.WriteJSON(terminalWireMessage{Type: "error", Reason: "ssh_connection_failed"})
		return "ssh_connection_failed"
	}
	defer client.Close()
	defer sshSession.Close()
	if err := sshSession.RequestPty("xterm-256color", 40, 120, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		_ = conn.WriteJSON(terminalWireMessage{Type: "error", Reason: "pty_unavailable"})
		return "pty_unavailable"
	}
	if err := sshSession.Shell(); err != nil {
		_ = conn.WriteJSON(terminalWireMessage{Type: "error", Reason: "shell_unavailable"})
		return "shell_unavailable"
	}
	_ = conn.WriteJSON(terminalWireMessage{Type: "status", Data: "connected"})
	incoming := make(chan terminalWireMessage, 8)
	readErr := make(chan error, 1)
	go func() {
		for {
			var message terminalWireMessage
			if err := conn.ReadJSON(&message); err != nil {
				readErr <- err
				return
			}
			incoming <- message
		}
	}()
	output := make(chan []byte, 16)
	outputErr := make(chan error, 2)
	go func() {
		buffer := make([]byte, 32*1024)
		for {
			count, err := stdout.Read(buffer)
			if count > 0 {
				output <- append([]byte(nil), buffer[:count]...)
			}
			if err != nil {
				outputErr <- err
				return
			}
		}
	}()
	go func() { outputErr <- sshSession.Wait() }()
	idle := time.NewTimer(terminalIdleLimit)
	defer idle.Stop()
	hard := time.NewTimer(time.Until(terminal.expiresAt))
	defer hard.Stop()
	resetIdle := func() {
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(terminalIdleLimit)
	}
	for {
		select {
		case <-ctx.Done():
			return "request_cancelled"
		case <-hard.C:
			_ = conn.WriteJSON(terminalWireMessage{Type: "status", Data: "closed", Reason: "hard_limit"})
			return "hard_limit"
		case <-idle.C:
			_ = conn.WriteJSON(terminalWireMessage{Type: "status", Data: "closed", Reason: "idle_limit"})
			return "idle_limit"
		case message := <-incoming:
			resetIdle()
			switch message.Type {
			case "input":
				count, writeErr := io.WriteString(stdin, message.Data)
				terminal.bytesIn.Add(int64(count))
				if writeErr != nil {
					return "ssh_write_failed"
				}
			case "resize":
				if message.Cols >= 20 && message.Cols <= 1000 && message.Rows >= 5 && message.Rows <= 500 {
					_ = sshSession.WindowChange(message.Rows, message.Cols)
				}
			case "close":
				return "client_closed"
			}
		case bytes := <-output:
			resetIdle()
			terminal.bytesOut.Add(int64(len(bytes)))
			if err := conn.WriteJSON(terminalWireMessage{Type: "output", Data: string(bytes)}); err != nil {
				return "websocket_write_failed"
			}
		case <-readErr:
			return "websocket_closed"
		case <-outputErr:
			return "ssh_closed"
		}
	}
}

func (h *serverTerminalHandlers) resolveAccess(ctx context.Context, server controlplane.ServerRuntime, ownerID string) serverAccessResponse {
	result := serverAccessResponse{ServerID: server.ID}
	connection := strings.ToLower(strings.TrimSpace(server.ConnectionState))
	if connection != string(serverregistry.ConnectionConnected) && connection != string(serverregistry.ConnectionDegraded) {
		result.Reason = "server_" + firstNonEmptyString(connection, "unavailable")
		return result
	}
	target, err := h.resolveTarget(ctx, server, ownerID)
	if err != nil {
		result.Reason = "managed_access_unavailable"
		return result
	}
	result.Host, result.User, result.Port = safeShellIdentity(target.Host), safeShellIdentity(target.SSHUser), target.SSHPort
	if result.Host == "" || result.User == "" {
		result.Reason = "invalid_access_identity"
		return result
	}
	commandHost := result.Host
	if strings.Contains(commandHost, ":") && !strings.HasPrefix(commandHost, "[") {
		commandHost = "[" + commandHost + "]"
	}
	result.SSHCommand = fmt.Sprintf("ssh -o StrictHostKeyChecking=ask -p %d %s@%s", result.Port, result.User, commandHost)
	result.ManualCommandAvailable = true
	result.UserKeyFingerprint = strings.TrimSpace(stringFromAnyMap(server.Metadata, "user_ssh_key_fingerprint"))
	result.ConnectReady = result.UserKeyFingerprint != ""
	result.OwnerSSHAccess = executionchannel.OwnerSSHAccessEnabled
	if !executionchannel.OwnerSSHAccessFromMetadata(server.Metadata).Enabled() {
		result.OwnerSSHAccess = executionchannel.OwnerSSHAccessDisabled
		result.ManualCommandAvailable = false
		result.ConnectReady = false
	}
	fingerprint, _, _, keyErr := executionchannel.PinnedHostKey(target.SSHHostKey)
	if keyErr != nil {
		result.Reason = "confirmed_host_key_required"
		return result
	}
	result.HostKeyFingerprint = fingerprint
	if !executionchannel.HasCredential(target) {
		result.Reason = "managed_ssh_custody_unavailable"
		return result
	}
	result.TerminalEnabled = true
	return result
}

func (h *serverTerminalHandlers) resolveTarget(ctx context.Context, server controlplane.ServerRuntime, ownerID string) (*jobs.ManagedRuntimeTarget, error) {
	if strings.TrimSpace(server.LeaseID) == "" || h.targets == nil {
		return nil, errors.New("managed lease access unavailable")
	}
	target, err := h.targets.ResolveManagedRuntimeTarget(ctx, jobs.ManagedRuntimeTargetRequest{
		StackID: server.StackID, StackName: server.Name, TenantID: server.TenantID,
		OwnerID: ownerID, LeaseID: server.LeaseID, Provider: server.ProviderRef,
	})
	if err != nil || target == nil {
		return target, err
	}
	copy := *target
	if strings.TrimSpace(copy.SSHHostKey) == "" {
		copy.SSHHostKey = executionchannel.GuardInventoryHostKey(server.Metadata)
	}
	return &copy, nil
}


func (h *serverTerminalHandlers) ownedServer(e *httpx.Event, capability string) (*controlplane.ServerRuntime, string, error) {
	ownerID, _, ok := authenticatedUser(e)
	if !ok {
		return nil, "", httpx.Unauthorized(e, "Authentication required")
	}
	tenantID, err := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, capability)
	if err != nil {
		return nil, "", err
	}
	serverID := strings.TrimSpace(e.Request.PathValue("serverId"))
	server, err := h.servers.GetServerRuntime(e.Request.Context(), tenantID, serverID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && (server == nil || !serverRuntimeOwnedBy(*server, ownerID))) {
		return nil, "", httpx.NotFound(e, "Server not found")
	}
	if err != nil {
		return nil, "", httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}
	return server, ownerID, nil
}

func (h *serverTerminalHandlers) finish(session *serverTerminalSession, reason string) {
	h.mu.Lock()
	delete(h.sessions, session.id)
	delete(h.active, session.ownerID+"\x00"+session.serverID)
	h.mu.Unlock()
	h.audit(context.Background(), controlplane.ServerRuntime{ID: session.serverID, TenantID: session.tenantID, StackID: session.stackID}, session.ownerID, "server_terminal_ended", "info", map[string]any{
		"duration_seconds": int64(h.now().Sub(session.createdAt).Seconds()),
		"bytes_in":         session.bytesIn.Load(), "bytes_out": session.bytesOut.Load(), "error_class": reason,
	})
}

func (h *serverTerminalHandlers) expireUnattached(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	session := h.sessions[sessionID]
	if session == nil || session.attached || h.now().Before(session.expiresAt) {
		return
	}
	delete(h.sessions, sessionID)
	delete(h.active, session.ownerID+"\x00"+session.serverID)
}

func (h *serverTerminalHandlers) audit(ctx context.Context, server controlplane.ServerRuntime, ownerID, action, severity string, details map[string]any) {
	if h.activity == nil {
		return
	}
	_, _ = h.activity.AppendActivity(ctx, controlplane.ActivityEvent{
		ID: uuid.NewString(), TenantID: server.TenantID, StackID: server.StackID, ServerScopeKey: server.ID,
		ActorSubjectID: ownerID, Action: action, Category: "server_access", Severity: severity,
		Message: strings.ReplaceAll(action, "_", " "), Details: details, CreatedAt: h.now().UTC(),
	})
}

func terminalOriginAllowed(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	host := firstNonEmptyString(strings.TrimSpace(request.Header.Get("X-Forwarded-Host")), request.Host)
	return strings.EqualFold(parsed.Host, host)
}


func openPinnedSSH(ctx context.Context, target *jobs.ManagedRuntimeTarget, fingerprint string) (*ssh.Client, *ssh.Session, io.WriteCloser, io.Reader, error) {
	client, err := executionchannel.Dial(ctx, target, fingerprint)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, nil, nil, nil, err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		client.Close()
		return nil, nil, nil, nil, err
	}
	stdout, outputWriter := io.Pipe()
	session.Stdout, session.Stderr = outputWriter, outputWriter
	return client, session, stdin, stdout, nil
}


func installAuthorizedKey(ctx context.Context, target *jobs.ManagedRuntimeTarget, hostFingerprint, publicKey string) error {
	client, err := executionchannel.Dial(ctx, target, hostFingerprint)
	if err != nil {
		return err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(publicKey + "\n")
	done := make(chan error, 1)
	go func() {
		_, runErr := session.CombinedOutput(`set -eu; umask 077; mkdir -p "$HOME/.ssh"; touch "$HOME/.ssh/authorized_keys"; IFS= read -r key; grep -qxF -e "$key" "$HOME/.ssh/authorized_keys" || printf '%s\n' "$key" >> "$HOME/.ssh/authorized_keys"; chmod 700 "$HOME/.ssh"; chmod 600 "$HOME/.ssh/authorized_keys"`)
		done <- runErr
	}()
	select {
	case <-ctx.Done():
		_ = session.Close()
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func walletSSHPublicKey(item *controlplane.WalletItem) (string, string, error) {
	if item == nil {
		return "", "", errors.New("wallet item missing")
	}
	candidates := []string{walletString(item.Metadata["public_key"])}
	for _, line := range strings.Split(walletString(item.Metadata["notes"]), "\n") {
		candidates = append(candidates, strings.TrimSpace(line))
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || len(candidate) > 16*1024 {
			continue
		}
		key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(candidate))
		if err == nil && len(strings.TrimSpace(string(rest))) == 0 {
			return candidate, ssh.FingerprintSHA256(key), nil
		}
	}
	return "", "", errors.New("wallet SSH public key missing")
}

func (h *serverTerminalHandlers) persistUserKeyFingerprint(ctx context.Context, server controlplane.ServerRuntime, fingerprint string) error {
	events, ok := h.servers.(controlplane.ServerEventStore)
	if !ok {
		return errors.New("canonical server event store unavailable")
	}
	for attempt := 0; attempt < 2; attempt++ {
		current, err := h.servers.GetServerRuntime(ctx, server.TenantID, server.ID)
		if err != nil || current == nil {
			return firstNonNilError(err, controlplane.ErrNotFound)
		}
		_, err = events.ApplyServerEvent(ctx, controlplane.ServerEvent{
			TenantID: current.TenantID, ServerID: current.ID, ExpectedRevision: current.Revision,
			Generation: current.Generation, Authority: controlplane.ServerEventAuthorityControlPlane,
			Source: "server-access", SourceID: "server-access", ObservedAt: h.now().UTC(),
			Runtime: controlplane.ServerRuntime{Metadata: map[string]any{"user_ssh_key_fingerprint": fingerprint}},
		})
		if !errors.Is(err, controlplane.ErrConflict) {
			return err
		}
	}
	return controlplane.ErrConflict
}

func firstNonNilError(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}


func safeShellIdentity(value string) string {
	value = strings.TrimSpace(value)
	if !shellIdentityPattern.MatchString(value) {
		return ""
	}
	return value
}
