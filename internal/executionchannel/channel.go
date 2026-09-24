package executionchannel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

const (
	// OwnerSSHAccessMetadataKey holds the generation-bound owner SSH grant on
	// the canonical RuntimeServer aggregate.
	OwnerSSHAccessMetadataKey = monthlyruntime.OwnerSSHAccessMetadataKey
	ownerSSHAccessSource      = "owner-ssh-access"
	guardInventoryHostKey     = "host"

	OwnerSSHAccessEnabled  = "enabled"
	OwnerSSHAccessDisabled = "disabled"

	guardServiceUnit = "techstack-agent.service"
	maxOwnerKeys     = 16
)

// OwnerSSHKey is one owner-authorized public key. Public keys are not secret;
// they are retained so a re-enabled grant can reinstall exactly the keys the
// owner authorized under fresh re-authentication.
type OwnerSSHKey struct {
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
}

// OwnerSSHAccess is the durable grant projection.
type OwnerSSHAccess struct {
	State                string        `json:"state"`
	ResourceGenerationID string        `json:"resource_generation_id,omitempty"`
	ChangedAt            string        `json:"changed_at,omitempty"`
	ChangedBy            string        `json:"changed_by,omitempty"`
	Keys                 []OwnerSSHKey `json:"keys,omitempty"`
}

// Enabled reports the effective grant. A server without a record keeps the
// historical default: owner keys may be authorized.
func (a OwnerSSHAccess) Enabled() bool { return a.State != OwnerSSHAccessDisabled }

// OwnerSSHAccessFromMetadata decodes the grant from server metadata.
func OwnerSSHAccessFromMetadata(metadata map[string]any) OwnerSSHAccess {
	var access OwnerSSHAccess
	raw, ok := metadata[OwnerSSHAccessMetadataKey]
	if !ok || raw == nil {
		return access
	}
	var payload []byte
	switch value := raw.(type) {
	case string:
		payload = []byte(value)
	case []byte:
		payload = value
	case json.RawMessage:
		payload = value
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return access
		}
		payload = encoded
	}
	_ = json.Unmarshal(payload, &access)
	return access
}

// GuardInventoryHostKey returns the first SSH host key Guard reported for the
// node, the fallback custody source when the target carries none.
func GuardInventoryHostKey(metadata map[string]any) string {
	host, ok := jsonMap(metadata[guardInventoryHostKey])
	if !ok {
		return ""
	}
	switch values := host["ssh_host_keys"].(type) {
	case []string:
		if len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func jsonMap(value any) (map[string]any, bool) {
	var payload []byte
	switch v := value.(type) {
	case map[string]any:
		return v, true
	case json.RawMessage:
		payload = v
	case []byte:
		payload = v
	case string:
		payload = []byte(v)
	default:
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil || out == nil {
		return nil, false
	}
	return out, true
}

// Channel resolves the exact managed runtime target of a lease and runs the
// Day-2 node operations that have no provider API.
type Channel struct {
	Servers controlplane.ServerRuntimeStore
	Targets jobs.ManagedRuntimeTargetResolver
	Now     func() time.Time
	// run is replaceable for behavior tests; production uses Run.
	run func(ctx context.Context, target *jobs.ManagedRuntimeTarget, fingerprint, command, stdin string) (string, error)
}

func (c *Channel) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Channel) runCommand(ctx context.Context, target *jobs.ManagedRuntimeTarget, fingerprint, command, stdin string) (string, error) {
	if c.run != nil {
		return c.run(ctx, target, fingerprint, command, stdin)
	}
	return Run(ctx, target, fingerprint, command, stdin)
}

// ResolveTarget returns the channel target and its pinned host fingerprint
// for one server. It fails closed without host-key custody or a credential.
func (c *Channel) ResolveTarget(ctx context.Context, server controlplane.ServerRuntime, ownerID string) (*jobs.ManagedRuntimeTarget, string, error) {
	if c == nil || c.Targets == nil || strings.TrimSpace(server.LeaseID) == "" {
		return nil, "", monthlyruntime.ErrNodeChannelUnavailable
	}
	target, err := c.Targets.ResolveManagedRuntimeTarget(ctx, jobs.ManagedRuntimeTargetRequest{
		StackID: server.StackID, StackName: server.Name, TenantID: server.TenantID,
		OwnerID: ownerID, LeaseID: server.LeaseID, Provider: server.ProviderRef,
	})
	if err != nil || target == nil {
		return nil, "", fmt.Errorf("%w: target resolution failed", monthlyruntime.ErrNodeChannelUnavailable)
	}
	resolved := *target
	if strings.TrimSpace(resolved.SSHHostKey) == "" {
		resolved.SSHHostKey = GuardInventoryHostKey(server.Metadata)
	}
	fingerprint, _, _, err := PinnedHostKey(resolved.SSHHostKey)
	if err != nil || !HasCredential(&resolved) || strings.TrimSpace(resolved.Host) == "" {
		return nil, "", fmt.Errorf("%w: host key or channel credential custody is missing", monthlyruntime.ErrNodeChannelUnavailable)
	}
	if resolved.SSHPort <= 0 {
		resolved.SSHPort = 22
	}
	return &resolved, fingerprint, nil
}

func (c *Channel) leaseServer(ctx context.Context, tenantID, leaseID string) (*controlplane.ServerRuntime, error) {
	if c == nil || c.Servers == nil {
		return nil, monthlyruntime.ErrNodeChannelUnavailable
	}
	server, err := c.Servers.GetServerRuntime(ctx, strings.TrimSpace(tenantID), runtimeidentity.LeaseServerID(strings.TrimSpace(leaseID)))
	if err != nil || server == nil || server.LeaseID != strings.TrimSpace(leaseID) {
		return nil, fmt.Errorf("%w: canonical server for the lease is unavailable", monthlyruntime.ErrNodeChannelUnavailable)
	}
	return server, nil
}

// serverOwnedBy accepts the lease subject the service authorized: the owning
// user, or the tenant itself for an organization-subject lease.
func serverOwnedBy(server controlplane.ServerRuntime, subjectID string) bool {
	subjectID = strings.TrimSpace(subjectID)
	return subjectID != "" && (server.OwnerSubjectID == subjectID || server.TenantID == subjectID)
}

// PowerOffGuest schedules a graceful guest shutdown. It refuses a target that
// does not resolve to the provider-observed address of the exact graph.
func (c *Channel) PowerOffGuest(ctx context.Context, req providercontrol.GuestPowerOffRequest) error {
	server, err := c.leaseServer(ctx, req.TenantID, req.LeaseID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(req.RuntimeServerID) != "" && server.ID != strings.TrimSpace(req.RuntimeServerID) {
		return fmt.Errorf("%w: runtime server does not match the lease", monthlyruntime.ErrNodeChannelUnavailable)
	}
	target, fingerprint, err := c.ResolveTarget(ctx, *server, server.OwnerSubjectID)
	if err != nil {
		return err
	}
	if ip := strings.TrimSpace(req.PublicIPv4); ip != "" && target.Host != ip && target.PublicIP != ip {
		return fmt.Errorf("%w: channel target does not match the provider-observed address", monthlyruntime.ErrNodeChannelUnavailable)
	}
	// --no-block returns once the shutdown job is queued, so the session ends
	// cleanly instead of racing the connection teardown.
	if _, err := c.runCommand(ctx, target, fingerprint, "sudo -n systemctl --no-block poweroff", ""); err != nil {
		var exitErr *ssh.ExitMissingError
		if errors.As(err, &exitErr) {
			return nil
		}
		return fmt.Errorf("%w: guest shutdown command failed: %v", monthlyruntime.ErrNodeChannelUnavailable, err)
	}
	slog.Info("managed_guest_poweroff_requested", "tenant_id", req.TenantID, "lease_id", req.LeaseID)
	return nil
}

// RestartAgent restarts the node's Guard service so it re-establishes its
// control-plane session. It never touches provider resources.
func (c *Channel) RestartAgent(ctx context.Context, req monthlyruntime.AgentReconnectRequest) error {
	server, err := c.leaseServer(ctx, req.TenantID, req.LeaseID)
	if err != nil {
		return err
	}
	if !serverOwnedBy(*server, req.OwnerID) {
		return fmt.Errorf("%w: server is not owned by the requester", monthlyruntime.ErrNodeChannelUnavailable)
	}
	target, fingerprint, err := c.ResolveTarget(ctx, *server, server.OwnerSubjectID)
	if err != nil {
		return err
	}
	if _, err := c.runCommand(ctx, target, fingerprint, "sudo -n systemctl restart "+guardServiceUnit, ""); err != nil {
		return fmt.Errorf("%w: guard restart failed: %v", monthlyruntime.ErrNodeChannelUnavailable, err)
	}
	slog.Info("managed_guard_restart_requested", "tenant_id", req.TenantID, "lease_id", req.LeaseID)
	return nil
}

// SetOwnerSSHAccess applies the generation-bound owner SSH grant on the node
// and records it on the canonical server. Disable keeps only the execution
// channel's own keys; enable reinstalls the keys the owner authorized.
func (c *Channel) SetOwnerSSHAccess(ctx context.Context, req monthlyruntime.SSHAccessRequest) (monthlyruntime.SSHAccessResult, error) {
	server, err := c.leaseServer(ctx, req.TenantID, req.LeaseID)
	if err != nil {
		return monthlyruntime.SSHAccessResult{}, err
	}
	if !serverOwnedBy(*server, req.OwnerID) {
		return monthlyruntime.SSHAccessResult{}, fmt.Errorf("%w: server is not owned by the requester", monthlyruntime.ErrNodeChannelUnavailable)
	}
	if server.LifecycleState != string(serverregistry.LifecycleActive) || !serverregistry.MutationsAllowed(server.ConnectionState) {
		return monthlyruntime.SSHAccessResult{}, fmt.Errorf("%w: server is not active and connected", monthlyruntime.ErrNodeChannelUnavailable)
	}
	target, fingerprint, err := c.ResolveTarget(ctx, *server, server.OwnerSubjectID)
	if err != nil {
		return monthlyruntime.SSHAccessResult{}, err
	}
	channelKeys := ChannelKeyBlobs(target)
	if len(channelKeys) == 0 {
		return monthlyruntime.SSHAccessResult{}, fmt.Errorf("%w: execution channel key is unknown", monthlyruntime.ErrNodeChannelUnavailable)
	}
	current := OwnerSSHAccessFromMetadata(server.Metadata)
	var output string
	if req.Enabled {
		output, err = c.runCommand(ctx, target, fingerprint, enableOwnerKeysScript, ownerKeysInput(channelKeys, current.Keys))
	} else {
		output, err = c.runCommand(ctx, target, fingerprint, disableOwnerKeysScript, strings.Join(channelKeys, "\n")+"\n")
	}
	if err != nil {
		return monthlyruntime.SSHAccessResult{}, fmt.Errorf("%w: owner key update failed: %v", monthlyruntime.ErrNodeChannelUnavailable, err)
	}
	counts := parseCounts(output)
	changedAt := c.now()
	state := OwnerSSHAccessDisabled
	if req.Enabled {
		state = OwnerSSHAccessEnabled
	}
	grant := OwnerSSHAccess{
		State: state, ResourceGenerationID: strings.TrimSpace(req.ResourceGenerationID),
		ChangedAt: changedAt.Format(time.RFC3339), ChangedBy: strings.TrimSpace(req.Actor), Keys: current.Keys,
	}
	if err := c.recordGrant(ctx, *server, grant); err != nil {
		return monthlyruntime.SSHAccessResult{}, err
	}
	return monthlyruntime.SSHAccessResult{
		Enabled: req.Enabled, ResourceGenerationID: grant.ResourceGenerationID,
		OwnerKeysInstalled: counts["installed"], OwnerKeysRemoved: counts["removed"],
		OwnerKeysPresent: counts["present"], ChangedAt: changedAt,
	}, nil
}

// RecordAuthorizedOwnerKey adds one owner-authorized public key to the grant
// after the re-authenticated authorize-key route installed it on the node.
func (c *Channel) RecordAuthorizedOwnerKey(ctx context.Context, server controlplane.ServerRuntime, key OwnerSSHKey, actor string) error {
	if c == nil || c.Servers == nil {
		return monthlyruntime.ErrNodeChannelUnavailable
	}
	current, err := c.Servers.GetServerRuntime(ctx, server.TenantID, server.ID)
	if err != nil || current == nil {
		if err == nil {
			err = controlplane.ErrNotFound
		}
		return err
	}
	grant := OwnerSSHAccessFromMetadata(current.Metadata)
	if !grant.Enabled() {
		return ErrOwnerSSHAccessDisabled
	}
	if grant.State == "" {
		grant.State = OwnerSSHAccessEnabled
	}
	keys := make([]OwnerSSHKey, 0, len(grant.Keys)+1)
	for _, existing := range grant.Keys {
		if existing.Fingerprint != key.Fingerprint {
			keys = append(keys, existing)
		}
	}
	keys = append(keys, key)
	if len(keys) > maxOwnerKeys {
		keys = keys[len(keys)-maxOwnerKeys:]
	}
	grant.Keys = keys
	grant.ChangedAt = c.now().Format(time.RFC3339)
	grant.ChangedBy = strings.TrimSpace(actor)
	return c.recordGrant(ctx, server, grant)
}

// ErrOwnerSSHAccessDisabled rejects owner key authorization while the grant
// for the current generation is disabled.
var ErrOwnerSSHAccessDisabled = errors.New("executionchannel: owner SSH access is disabled for this server")

func (c *Channel) recordGrant(ctx context.Context, server controlplane.ServerRuntime, grant OwnerSSHAccess) error {
	events, ok := c.Servers.(controlplane.ServerEventStore)
	if !ok {
		return errors.New("executionchannel: canonical server event store unavailable")
	}
	for attempt := 0; attempt < 2; attempt++ {
		current, err := c.Servers.GetServerRuntime(ctx, server.TenantID, server.ID)
		if err != nil || current == nil {
			if err == nil {
				err = controlplane.ErrNotFound
			}
			return err
		}
		_, err = events.ApplyServerEvent(ctx, controlplane.ServerEvent{
			TenantID: current.TenantID, ServerID: current.ID, ExpectedRevision: current.Revision,
			Generation: current.Generation, Authority: controlplane.ServerEventAuthorityControlPlane,
			Source: ownerSSHAccessSource, SourceID: ownerSSHAccessSource, ObservedAt: c.now(),
			Runtime: controlplane.ServerRuntime{Metadata: map[string]any{OwnerSSHAccessMetadataKey: grant}},
		})
		if !errors.Is(err, controlplane.ErrConflict) {
			return err
		}
	}
	return controlplane.ErrConflict
}

func ownerKeysInput(channelKeys []string, keys []OwnerSSHKey) string {
	var builder strings.Builder
	for _, blob := range channelKeys {
		builder.WriteString("channel " + blob + "\n")
	}
	for _, key := range keys {
		line := strings.TrimSpace(key.PublicKey)
		if line == "" || strings.ContainsAny(line, "\r\n") {
			continue
		}
		if _, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(line)); err != nil || len(strings.TrimSpace(string(rest))) != 0 {
			continue
		}
		builder.WriteString("owner " + line + "\n")
	}
	return builder.String()
}

func parseCounts(output string) map[string]int {
	counts := map[string]int{}
	for _, field := range strings.Fields(output) {
		name, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		if parsed, err := strconv.Atoi(value); err == nil {
			counts[name] = parsed
		}
	}
	return counts
}

// disableOwnerKeysScript keeps only authorized_keys lines whose key blob is
// one of the execution channel's keys (stdin, one blob per line). It refuses
// to write a file that would lock the channel out.
const disableOwnerKeysScript = `set -eu
umask 077
keep="$(cat)"
[ -n "$keep" ] || exit 64
file="$HOME/.ssh/authorized_keys"
[ -f "$file" ] || exit 65
tmp="$(mktemp "$HOME/.ssh/.authorized_keys.XXXXXX")"
removed=0
kept=0
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in ''|'#'*) printf '%s\n' "$line" >>"$tmp"; continue ;; esac
  matched=0
  for blob in $keep; do
    case " $line " in *" $blob "*) matched=1; break ;; esac
  done
  if [ "$matched" = 1 ]; then printf '%s\n' "$line" >>"$tmp"; kept=$((kept+1)); else removed=$((removed+1)); fi
done <"$file"
if [ "$kept" -lt 1 ]; then rm -f "$tmp"; exit 66; fi
chmod 600 "$tmp"
mv -f "$tmp" "$file"
echo "removed=$removed present=0 kept=$kept"`

// enableOwnerKeysScript appends each recorded owner key that is missing and
// reports how many owner keys are present afterwards. Stdin lines are
// "channel <blob>" or "owner <authorized-key line>".
const enableOwnerKeysScript = `set -eu
umask 077
mkdir -p "$HOME/.ssh"
file="$HOME/.ssh/authorized_keys"
touch "$file"
channel=""
installed=0
while IFS= read -r entry || [ -n "$entry" ]; do
  kind="${entry%% *}"
  value="${entry#* }"
  case "$kind" in
    channel) channel="$channel $value" ;;
    owner)
      if ! grep -qxF -e "$value" "$file"; then printf '%s\n' "$value" >>"$file"; installed=$((installed+1)); fi ;;
  esac
done
present=0
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in ''|'#'*) continue ;; esac
  owned=1
  for blob in $channel; do
    case " $line " in *" $blob "*) owned=0; break ;; esac
  done
  if [ "$owned" = 1 ]; then present=$((present+1)); fi
done <"$file"
chmod 700 "$HOME/.ssh"
chmod 600 "$file"
echo "installed=$installed present=$present removed=0"`

var (
	_ providercontrol.GuestPowerOffExecutor = (*Channel)(nil)
	_ monthlyruntime.AgentReconnector       = (*Channel)(nil)
	_ monthlyruntime.SSHAccessController    = (*Channel)(nil)
)
