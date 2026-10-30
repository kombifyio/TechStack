package stackkitcommand

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// MinWorkloadRelocationRelease is the first published machine relocation CLI.
// Verified-receive support is separately admitted from the destination check.
const MinWorkloadRelocationRelease = "v0.53.9"

const (
	// RelocationPlanFormat names the secret-free plan `relocate plan` returns.
	RelocationPlanFormat = "kombify.io/coolify-relocation-plan/v1"
	// RelocationDrainFormat names the drain record `relocate drain` returns.
	RelocationDrainFormat = "kombify.io/coolify-relocation-drain/v1"
	// RelocationUndrainFormat names the result of `relocate drain --undo`.
	RelocationUndrainFormat = "kombify.io/coolify-relocation-undrain/v1"
	// MaxRelocationPlanBytes bounds the plan Core carries between the nodes.
	MaxRelocationPlanBytes = 1 << 20
	maxRelocationProjects  = 32
	maxRelocationDomains   = 64
	// RelocationShareWaitSeconds is how long receive waits for the source's
	// share: the source backs up first and shares only then.
	RelocationShareWaitSeconds = 600
	// RelocationShareTTL bounds how long the source shares without a complete
	// download.
	RelocationShareTTL = "20m"
)

var (
	relocationUUIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	relocationProjectPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._/-]{0,127}$`)
	relocationHostPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
)

// IsWorkloadRelocationOperation reports the operations of a PaaS workload
// move between two servers (StackKits `stackkit relocate`). Each acts only on
// the node it runs on; Core carries the secret-free plan between them.
func IsWorkloadRelocationOperation(operation agentpb.StackKitOperation) bool {
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_PLAN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_CHECK,
		agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_DRAIN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_UNDRAIN,
		agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_SEND,
		agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE,
		agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_STATUS, agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_ABORT:
		return true
	default:
		return false
	}
}

// RelocationPlanOnStdin reports the operations that read the plan on stdin.
func RelocationPlanOnStdin(operation agentpb.StackKitOperation) bool {
	return operation == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_ABORT || operation == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_UNDRAIN || operation == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_CHECK ||
		operation == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_DRAIN ||
		operation == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE || operation == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_SEND || operation == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_STATUS
}

// NormalizeRelocationHost admits a bare host name or IP address of the source
// server, without port or scheme.
func NormalizeRelocationHost(raw string) (string, error) {
	host := strings.TrimSpace(raw)
	if net.ParseIP(host) != nil {
		return host, nil
	}
	if !relocationHostPattern.MatchString(host) || strings.Contains(host, "..") {
		return "", fmt.Errorf("the source address must be a bare host name or IP address")
	}
	return strings.ToLower(host), nil
}

// ParseRelocationDomainSwitch splits one OLD=NEW domain switch.
func ParseRelocationDomainSwitch(value string) (oldHost, newHost string, err error) {
	parts := strings.Split(value, "=")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("a domain switch is OLD=NEW")
	}
	if oldHost, err = NormalizeRelocationHost(parts[0]); err != nil || net.ParseIP(oldHost) != nil {
		return "", "", fmt.Errorf("domain switch %q needs a host name on the left", value)
	}
	if newHost, err = NormalizeRelocationHost(parts[1]); err != nil || net.ParseIP(newHost) != nil {
		return "", "", fmt.Errorf("domain switch %q needs a host name on the right", value)
	}
	return oldHost, newHost, nil
}

func relocationFieldsPresent(command *agentpb.StackKitCommand) bool {
	return len(command.RelocationProjects) != 0 || len(command.RelocationResourceUuids) != 0 || command.RelocationTransferId != "" || len(command.RelocationPlanJson) != 0 ||
		command.RelocationSourceAddress != "" || len(command.RelocationSetDomains) != 0
}

// validateWorkloadRelocationFields keeps each relocation input on the
// operation that consumes it.
func validateWorkloadRelocationFields(command *agentpb.StackKitCommand) error {
	op := command.Operation
	if !IsWorkloadRelocationOperation(op) {
		if relocationFieldsPresent(command) {
			return fmt.Errorf("StackKit relocation fields are only valid for a relocation operation")
		}
		return nil
	}
	plan := op == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_PLAN || op == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_SEND
	if !plan && (len(command.RelocationProjects) != 0 || len(command.RelocationResourceUuids) != 0) {
		return fmt.Errorf("StackKit relocation projects are only valid for relocate plan and send")
	}
	if !RelocationPlanOnStdin(op) && len(command.RelocationPlanJson) != 0 {
		return fmt.Errorf("StackKit relocation plan is only valid for relocate check and drain")
	}
	transfer := op == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_SEND || op == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE
	if !transfer && command.RelocationSourceAddress != "" {
		return fmt.Errorf("StackKit relocation source address is only valid for relocate send and receive")
	}
	if !transfer && op != agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_DRAIN && op != agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_UNDRAIN && op != agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_STATUS && op != agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_ABORT && command.RelocationTransferId != "" {
		return fmt.Errorf("transfer identity belongs only to relocate send and receive")
	}
	if op != agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE && len(command.RelocationSetDomains) != 0 {
		return fmt.Errorf("StackKit relocation domain switches are only valid for relocate receive")
	}
	return nil
}

func validateWorkloadRelocationCommand(command *agentpb.StackKitCommand) error {
	if !ReleaseAtLeast(command.Release.Version, MinWorkloadRelocationRelease) {
		return fmt.Errorf("StackKit workload relocation requires StackKits %s or newer", MinWorkloadRelocationRelease)
	}
	if len(command.InventoryJson) != 0 || len(command.CandidateSpecJson) != 0 || len(command.AdvancedCapability) != 0 || command.DryRun || command.Offline {
		return fmt.Errorf("StackKit workload relocation takes no Inventory, candidate, capability, dry run or offline mode")
	}
	op := command.Operation
	readOnly := op == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_PLAN || op == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_CHECK || op == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_STATUS
	if readOnly && command.OwnerApproved {
		return fmt.Errorf("StackKit relocation read must not carry Owner approval")
	}
	if !readOnly && !command.OwnerApproved {
		return fmt.Errorf("StackKit relocation %s requires Owner approval", ResultCommandName(op))
	}
	switch op {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_PLAN, agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_SEND:
		if err := ValidateRelocationSelection(command.RelocationProjects, command.RelocationResourceUuids); err != nil {
			return err
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_CHECK, agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_DRAIN, agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE, agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_STATUS:
		if err := ValidateRelocationPlan(command.RelocationPlanJson); err != nil {
			return err
		}
	}
	if op == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_UNDRAIN {
		if err := ValidateRelocationResume(command.RelocationPlanJson, command.RelocationTransferId); err != nil {
			return err
		}
	} else if RelocationPlanOnStdin(op) {
		if err := ValidateRelocationPlan(command.RelocationPlanJson); err != nil {
			return err
		}
	}
	if op != agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_PLAN && op != agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_CHECK && !relocationUUIDPattern.MatchString(command.RelocationTransferId) {
		return fmt.Errorf("relocation requires the semantic transfer identity")
	}
	transfer := op == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_SEND || op == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE
	if transfer {
		if !relocationUUIDPattern.MatchString(command.RelocationTransferId) {
			return fmt.Errorf("relocation transfer requires one durable operation identity")
		}
		if normalized, err := NormalizeRelocationHost(command.RelocationSourceAddress); err != nil || normalized != command.RelocationSourceAddress {
			return fmt.Errorf("StackKit relocation transfer requires the normalized source address")
		}
		tenant := command.DeliveryTenantRef
		if !ValidWalletItemRef(command.WalletItemRef) || tenant == "" || tenant != strings.TrimSpace(tenant) || len(tenant) > MaxDeliveryTenantRefBytes {
			return fmt.Errorf("StackKit relocation transfer requires the move's Wallet item and its tenant")
		}
	} else if command.WalletItemRef != "" || command.DeliveryTenantRef != "" {
		return fmt.Errorf("StackKit relocation %s takes no Wallet item", ResultCommandName(op))
	}
	if len(command.RelocationSetDomains) > maxRelocationDomains {
		return fmt.Errorf("StackKit relocation takes at most %d domain switches", maxRelocationDomains)
	}
	for _, value := range command.RelocationSetDomains {
		oldHost, newHost, err := ParseRelocationDomainSwitch(value)
		if err != nil || oldHost+"="+newHost != value {
			return fmt.Errorf("StackKit relocation domain switch %q is not a normalized OLD=NEW", value)
		}
	}
	return nil
}

func validateRelocationProjects(projects []string) error {
	if len(projects) == 0 || len(projects) > maxRelocationProjects {
		return fmt.Errorf("StackKit relocation selects between 1 and %d Coolify projects", maxRelocationProjects)
	}
	seen := map[string]bool{}
	for _, project := range projects {
		if !relocationProjectPattern.MatchString(project) || strings.Contains(project, ",") || seen[project] {
			return fmt.Errorf("StackKit relocation project %q is invalid or repeated", project)
		}
		seen[project] = true
	}
	return nil
}

// ValidateRelocationSelection keeps one explicit selection mode and always
// includes dependencies in StackKits' existing platform closure.
func ValidateRelocationSelection(projects, uuids []string) error {
	if len(uuids) == 0 {
		return validateRelocationProjects(projects)
	}
	if len(projects) != 0 || len(uuids) > maxRelocationProjects {
		return fmt.Errorf("select either projects or at most %d resource UUIDs", maxRelocationProjects)
	}
	seen := map[string]bool{}
	for _, uuid := range uuids {
		if !relocationUUIDPattern.MatchString(uuid) || seen[uuid] {
			return fmt.Errorf("relocation resource UUID %q is invalid or repeated", uuid)
		}
		seen[uuid] = true
	}
	return nil
}

// ValidateRelocationPlan admits one bounded, secret-free relocation plan.
func ValidateRelocationPlan(plan []byte) error {
	if len(plan) == 0 || len(plan) > MaxRelocationPlanBytes {
		return fmt.Errorf("StackKit relocation plan must be within %d bytes", MaxRelocationPlanBytes)
	}
	var head struct {
		Format string `json:"format"`
	}
	if json.Unmarshal(plan, &head) != nil || head.Format != RelocationPlanFormat {
		return fmt.Errorf("StackKit relocation plan is not a %s document", RelocationPlanFormat)
	}
	return nil
}

// WorkloadRelocationArgs is the fixed argv of one relocation operation. The
// plan travels on stdin, the transfer secret stays in the node's Wallet item
// custody: argv names only the item.
func WorkloadRelocationArgs(command *agentpb.StackKitCommand) ([]string, error) {
	if err := validateWorkloadRelocationCommand(command); err != nil {
		return nil, err
	}
	projects := func(args []string) []string {
		for _, project := range command.RelocationProjects {
			args = append(args, "--project", project)
		}
		for _, uuid := range command.RelocationResourceUuids {
			args = append(args, "--uuid", uuid)
		}
		return args
	}
	transfer := []string{"--tenant-ref", command.DeliveryTenantRef, "--secret-ref", command.WalletItemRef, "--transfer-id", command.RelocationTransferId}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_PLAN:
		return append(projects([]string{"relocate", "plan"}), "--json"), nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_CHECK:
		return []string{"relocate", "check", "--plan", "-", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_DRAIN:
		return []string{"relocate", "drain", "--plan", "-", "--transfer-id", command.RelocationTransferId, "--owner-approve", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_UNDRAIN:
		return []string{"relocate", "drain", "--undo", "--managed-resume", "-", "--transfer-id", command.RelocationTransferId, "--owner-approve", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_SEND:
		args := projects([]string{"relocate", "send", "--plan", "-"})
		args = append(args, transfer...)
		return append(args, "--host", command.RelocationSourceAddress, "--ttl", RelocationShareTTL, "--owner-approve", "--json"), nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE:
		args := append([]string{"relocate", "receive", "--plan", "-", "--from", command.RelocationSourceAddress}, transfer...)
		for _, value := range command.RelocationSetDomains {
			args = append(args, "--set-domain", value)
		}
		return append(args, "--wait", fmt.Sprintf("%ds", RelocationShareWaitSeconds), "--owner-approve", "--json"), nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_ABORT:
		return []string{"relocate", "abort", "--plan", "-", "--transfer-id", command.RelocationTransferId, "--owner-approve", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_STATUS:
		return []string{"relocate", "status", "--plan", "-", "--transfer-id", command.RelocationTransferId, "--json"}, nil
	default:
		return nil, fmt.Errorf("unsupported StackKit relocation operation %s", command.Operation.String())
	}
}

func workloadRelocationCommandName(operation agentpb.StackKitOperation) string {
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_PLAN:
		return "stackkit relocate plan"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_CHECK:
		return "stackkit relocate check"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_DRAIN, agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_UNDRAIN:
		return "stackkit relocate drain"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_SEND:
		return "stackkit relocate send"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE:
		return "stackkit relocate receive"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_STATUS:
		return "stackkit relocate status"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_ABORT:
		return "stackkit relocate abort"
	default:
		return ""
	}
}

// RelocationCheck is the destination's verdict on a plan.
type RelocationCheck struct {
	RecoveryFormat       string   `json:"recoveryFormat"`
	RestoreReceiptFormat string   `json:"restoreReceiptFormat"`
	PlanSHA256           string   `json:"planSha256"`
	Ready                bool     `json:"ready"`
	Blockers             []string `json:"blockers"`
	Warnings             []string `json:"warnings"`
}

// RelocationDrain is the source's drain record, reduced to what Core shows.
type RelocationDrain struct {
	Complete  bool
	Resources []RelocationDrainedResource
}

// RelocationDrainedResource is one resource a drain stopped or found stopped.
type RelocationDrainedResource struct {
	UUID       string `json:"uuid"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	WasRunning bool   `json:"was_running"`
	Stopped    bool   `json:"stopped"`
	Error      string `json:"error,omitempty"`
}

func decodeRelocationEnvelope(command *agentpb.StackKitCommand, result *agentpb.StackKitResult, operation agentpb.StackKitOperation, wantSuccess bool) (json.RawMessage, string, error) {
	name := workloadRelocationCommandName(operation)
	if command == nil || result == nil || command.Operation != operation || (wantSuccess && !result.Success) {
		return nil, "", fmt.Errorf("successful StackKit %s evidence is required", name)
	}
	var envelope struct {
		SchemaVersion string          `json:"schemaVersion"`
		Command       string          `json:"command"`
		Status        string          `json:"status"`
		Data          json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return nil, "", fmt.Errorf("decode StackKit %s evidence: %w", name, err)
	}
	if envelope.SchemaVersion != CommandResultVersion || envelope.Command != name || (wantSuccess && envelope.Status != commandResultStatusSuccess) {
		return nil, "", fmt.Errorf("StackKit %s evidence identity is invalid", name)
	}
	return envelope.Data, envelope.Status, nil
}

// ParseRelocationPlan returns the plan the source produced, for check and
// drain on the nodes.
func ParseRelocationPlan(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) ([]byte, error) {
	data, _, err := decodeRelocationEnvelope(command, result, agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_PLAN, true)
	if err != nil {
		return nil, err
	}
	if err := ValidateRelocationPlan(data); err != nil {
		return nil, err
	}
	return append([]byte(nil), data...), nil
}

// ParseRelocationCheck reads the destination's verdict; a blocked check is a
// failed command that still carries its blockers.
func ParseRelocationCheck(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (RelocationCheck, error) {
	data, status, err := decodeRelocationEnvelope(command, result, agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_CHECK, false)
	if err != nil {
		return RelocationCheck{}, err
	}
	var check RelocationCheck
	if json.Unmarshal(data, &check) != nil {
		return RelocationCheck{}, fmt.Errorf("StackKit relocate check evidence is invalid")
	}
	if check.Ready && (check.RestoreReceiptFormat != "kombify.io/coolify-relocation-restore/v1" || check.RecoveryFormat != "stackkit.relocation-recovery/v1") {
		return RelocationCheck{}, fmt.Errorf("the pinned destination CLI does not advertise checked-plan restore and permanent no-effects recovery verification; use an admitted artifact supporting kombify.io/coolify-relocation-restore/v1")
	}
	if check.Ready && check.PlanSHA256 != fmt.Sprintf("sha256:%x", sha256.Sum256(command.RelocationPlanJson)) {
		return RelocationCheck{}, fmt.Errorf("destination check differs from the exact source plan")
	}
	if check.Ready != (status == commandResultStatusSuccess && result.Success) || (!check.Ready && len(check.Blockers) == 0) {
		return RelocationCheck{}, fmt.Errorf("StackKit relocate check evidence conflicts with its status")
	}
	return check, nil
}

// ParseRelocationDrain reads the drain record of a drain that returned one.
func ParseRelocationDrain(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (RelocationDrain, error) {
	data, _, err := decodeRelocationEnvelope(command, result, agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_DRAIN, true)
	if err != nil {
		return RelocationDrain{}, err
	}
	var record struct {
		Format    string                      `json:"format"`
		Resources []RelocationDrainedResource `json:"resources"`
	}
	if json.Unmarshal(data, &record) != nil || record.Format != RelocationDrainFormat {
		return RelocationDrain{}, fmt.Errorf("StackKit relocate drain evidence is not a %s record", RelocationDrainFormat)
	}
	drain := RelocationDrain{Complete: true, Resources: record.Resources}
	for _, resource := range record.Resources {
		if resource.WasRunning && !resource.Stopped {
			drain.Complete = false
		}
	}
	return drain, nil
}

// ValidateRelocationStep admits the success evidence of undrain, send and
// receive: the command named, and for undrain the engine's undrain result.
func ValidateRelocationStep(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) error {
	data, _, err := decodeRelocationEnvelope(command, result, command.GetOperation(), true)
	if err != nil {
		return err
	}
	var outcome struct {
		Format    string `json:"format"`
		Delivered bool   `json:"delivered"`
		Restored  bool   `json:"restored"`
	}
	if json.Unmarshal(data, &outcome) != nil {
		return fmt.Errorf("StackKit %s evidence is invalid", ResultCommandName(command.Operation))
	}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_UNDRAIN:
		if outcome.Format != RelocationUndrainFormat {
			return fmt.Errorf("StackKit relocate drain --undo evidence is not a %s result", RelocationUndrainFormat)
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_SEND:
		if !outcome.Delivered {
			return fmt.Errorf("StackKit relocate send evidence does not confirm the delivery")
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE:
		if !outcome.Restored {
			return fmt.Errorf("StackKit relocate receive evidence does not confirm the restore")
		}
		_, err = ParseRelocationRestore(command, result)
		return err
	}
	return nil
}

// ParseRelocationRestore binds the node's real post-restore observations to
// every resource in the checked source plan, including dependency closure.
func ParseRelocationRestore(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (json.RawMessage, error) {
	data, _, err := decodeRelocationEnvelope(command, result, agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_RECEIVE, true)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Verification json.RawMessage `json:"verification"`
		Started      bool            `json:"started"`
	}
	if json.Unmarshal(data, &envelope) != nil || !envelope.Started {
		return nil, fmt.Errorf("relocation requires destination resource verification")
	}
	return parseRelocationEngineRestore(command, envelope.Verification)
}

func parseRelocationEngineRestore(command *agentpb.StackKitCommand, raw json.RawMessage) (json.RawMessage, error) {
	var receipt struct {
		TransferID string    `json:"transfer_id"`
		PlanSHA256 string    `json:"plan_sha256"`
		Format     string    `json:"format"`
		VerifiedAt time.Time `json:"verified_at"`
		Resources  []struct {
			SourceUUID    string `json:"source_uuid"`
			UUID          string `json:"uuid"`
			Kind          string `json:"kind"`
			WasRunning    bool   `json:"was_running"`
			ObservedState string `json:"observed_state"`
		} `json:"resources"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Format != "kombify.io/coolify-relocation-restore/v1" || receipt.VerifiedAt.IsZero() {
		return nil, fmt.Errorf("relocation requires destination resource verification")
	}
	if receipt.TransferID != command.RelocationTransferId || receipt.PlanSHA256 != fmt.Sprintf("sha256:%x", sha256.Sum256(command.RelocationPlanJson)) {
		return nil, fmt.Errorf("destination verification belongs to another plan or transfer")
	}
	var plan struct {
		Manifest struct {
			Resources []struct {
				UUID     string `json:"uuid"`
				Table    string `json:"table"`
				ServerID int64  `json:"server_id"`
			} `json:"resources"`
		} `json:"manifest"`
	}
	if err := ValidateRelocationPlan(command.RelocationPlanJson); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(command.RelocationPlanJson, &plan); err != nil {
		return nil, err
	}
	expected := map[string]string{}
	for _, resource := range plan.Manifest.Resources {
		if resource.ServerID == 0 {
			expected[resource.UUID] = resource.Table
		}
	}
	if len(expected) == 0 || len(receipt.Resources) != len(expected) {
		return nil, fmt.Errorf("relocation verification omits the checked resource closure")
	}
	seenDest := map[string]bool{}
	for _, resource := range receipt.Resources {
		kind, found := expected[resource.SourceUUID]
		if !found || kind != resource.Kind || resource.UUID == "" || seenDest[resource.UUID] || (resource.ObservedState != "running" && resource.ObservedState != "stopped") || (resource.WasRunning && resource.ObservedState != "running") {
			return nil, fmt.Errorf("destination verification differs from checked relocation resource")
		}
		delete(expected, resource.SourceUUID)
		seenDest[resource.UUID] = true
	}
	return append(json.RawMessage(nil), raw...), nil
}

type RelocationNodeReceipt struct {
	APIVersion string `json:"apiVersion"`
	TransferID string `json:"transferId"`
	Phase      string `json:"phase"`
	PlanSHA256 string `json:"planSha256"`
	Binding    struct {
		SiteRef    string `json:"siteRef"`
		NodeRef    string `json:"nodeRef"`
		ChannelRef string `json:"executionChannelRef"`
	} `json:"binding"`
	RecordedAt time.Time       `json:"recordedAt"`
	Data       json.RawMessage `json:"data"`
	Signature  struct {
		OwnerRef string `json:"ownerRef"`
		KeyID    string `json:"keyId"`
		Value    string `json:"value"`
	} `json:"signature"`
}

type RelocationStatusEvidence struct {
	APIVersion string                `json:"apiVersion"`
	Receipt    RelocationNodeReceipt `json:"receipt"`
	VerifiedAt time.Time             `json:"verifiedAt"`
	Signature  struct {
		OwnerRef string `json:"ownerRef"`
		KeyID    string `json:"keyId"`
		Value    string `json:"value"`
	} `json:"signature"`
}

func ParseRelocationStatus(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (RelocationStatusEvidence, error) {
	var status RelocationStatusEvidence
	data, _, err := decodeRelocationEnvelope(command, result, command.Operation, true)
	if err != nil {
		return status, err
	}
	if json.Unmarshal(data, &status) != nil || status.APIVersion != "stackkit.relocation-status/v1" || status.VerifiedAt.IsZero() || time.Since(status.VerifiedAt) > 5*time.Minute || status.VerifiedAt.After(time.Now().Add(time.Minute)) || status.Signature.OwnerRef == "" || status.Signature.KeyID == "" || status.Signature.Value == "" {
		return status, fmt.Errorf("current signed relocation observation is unavailable")
	}
	r := status.Receipt
	if r.APIVersion != "stackkit.relocation-receipt/v1" || r.TransferID != command.RelocationTransferId || r.PlanSHA256 != relocationCommandPlanDigest(command) || r.Signature != status.Signature && (r.Signature.OwnerRef != status.Signature.OwnerRef || r.Signature.KeyID != status.Signature.KeyID) || r.Signature.Value == "" {
		return status, fmt.Errorf("relocation status differs from node custody, transfer or checked plan")
	}
	if r.Phase == "received" {
		_, err = parseRelocationEngineRestore(command, r.Data)
	} else if r.Phase != "sent" && r.Phase != "prepared" && r.Phase != "aborted" && r.Phase != "resumed" {
		err = fmt.Errorf("unknown relocation receipt phase")
	}
	var head struct {
		TransferID    string `json:"transfer_id"`
		PlanSHA256    string `json:"plan_sha256"`
		ArchiveSHA256 string `json:"archive_sha256"`
		Delivered     bool   `json:"delivered"`
	}
	if json.Unmarshal(r.Data, &head) != nil || head.TransferID != r.TransferID || head.PlanSHA256 != r.PlanSHA256 || ((r.Phase == "sent" || r.Phase == "prepared" || r.Phase == "received") && !stackKitRelocationDigest(head.ArchiveSHA256)) || (r.Phase == "sent" && !head.Delivered) {
		err = fmt.Errorf("node receipt lacks exact transfer/archive verification")
	}
	if err == nil && r.Phase == "aborted" {
		err = ValidateRelocationAbortEvidence(status, command.RelocationTransferId, r.PlanSHA256, r.Signature.OwnerRef, r.Binding.NodeRef)
	}
	if err == nil && r.Phase == "resumed" {
		var v struct {
			Resumed bool `json:"source_resumed"`
		}
		if json.Unmarshal(r.Data, &v) != nil || !v.Resumed {
			err = fmt.Errorf("source resume observation is unavailable")
		}
	}
	return status, err
}

func stackKitRelocationDigest(value string) bool {
	return len(value) == 71 && strings.HasPrefix(value, "sha256:") && sha256Pattern.MatchString(strings.TrimPrefix(value, "sha256:"))
}

func validateWorkloadRelocationResult(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) error {
	var err error
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_PLAN:
		_, err = ParseRelocationPlan(command, result)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_CHECK:
		_, err = ParseRelocationCheck(command, result)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_DRAIN:
		_, err = ParseRelocationDrain(command, result)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_STATUS, agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_ABORT:
		_, err = ParseRelocationStatus(command, result)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_UNDRAIN:
		if len(command.RelocationPlanJson) > 0 {
			_, err = ParseRelocationStatus(command, result)
		} else {
			err = ValidateRelocationStep(command, result)
		}
	default:
		err = ValidateRelocationStep(command, result)
	}
	return err
}

type RelocationResumeAuthority struct {
	APIVersion          string                   `json:"apiVersion"`
	TransferID          string                   `json:"transferId"`
	PlanJSON            string                   `json:"planJson"`
	Destination         RelocationStatusEvidence `json:"destination"`
	SourceOwnerRef      string                   `json:"sourceOwnerRef"`
	SourceNodeRef       string                   `json:"sourceNodeRef"`
	DestinationOwnerRef string                   `json:"destinationOwnerRef"`
	DestinationNodeRef  string                   `json:"destinationNodeRef"`
	ExpiresAt           time.Time                `json:"expiresAt"`
	ArchiveSHA256       string                   `json:"archiveSha256,omitempty"`
}

func ValidateRelocationResume(raw []byte, transfer string) error {
	var v RelocationResumeAuthority
	if len(raw) > MaxRelocationPlanBytes || json.Unmarshal(raw, &v) != nil || v.APIVersion != "stackkit.relocation-resume/v1" || v.TransferID != transfer || v.SourceOwnerRef == "" || v.SourceNodeRef == "" || v.DestinationOwnerRef == "" || v.DestinationNodeRef == "" || v.ExpiresAt.Before(time.Now()) || v.ExpiresAt.After(time.Now().Add(5*time.Minute)) {
		return fmt.Errorf("invalid managed source resume authority")
	}
	if err := ValidateRelocationPlan([]byte(v.PlanJSON)); err != nil {
		return err
	}
	return ValidateRelocationAbortEvidence(v.Destination, transfer, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(v.PlanJSON))), v.DestinationOwnerRef, v.DestinationNodeRef)
}
func ValidateRelocationAbortEvidence(v RelocationStatusEvidence, transfer, digest, owner, node string) error {
	var d struct {
		TransferID string `json:"transfer_id"`
		PlanSHA256 string `json:"plan_sha256"`
		State      string `json:"state"`
		NoEffects  bool   `json:"no_destination_effects"`
	}
	if v.Receipt.Phase != "aborted" || v.Receipt.TransferID != transfer || v.Receipt.PlanSHA256 != digest || v.Receipt.Binding.NodeRef != node || v.Receipt.Signature.OwnerRef != owner || v.Signature.OwnerRef != owner || time.Since(v.VerifiedAt) > 5*time.Minute || v.VerifiedAt.After(time.Now().Add(time.Minute)) || v.Signature.Value == "" || v.Receipt.Signature.Value == "" || json.Unmarshal(v.Receipt.Data, &d) != nil || d.TransferID != transfer || d.PlanSHA256 != digest || d.State != "aborted" || !d.NoEffects {
		return fmt.Errorf("destination has no current permanent no-effects abort observation")
	}
	return nil
}
func relocationCommandPlanDigest(command *agentpb.StackKitCommand) string {
	raw := command.RelocationPlanJson
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_RELOCATE_UNDRAIN {
		var v RelocationResumeAuthority
		if json.Unmarshal(raw, &v) == nil {
			raw = []byte(v.PlanJSON)
		}
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}
