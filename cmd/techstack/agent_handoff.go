package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/agent/journal"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardidentity"
)

// Cross-tenant handoff on the server (Guard G04, guardbinding/handoff.go).
const (
	// handoffReceiptsFile holds the receipts of handoffs completed since the
	// last binding, in the Guard state directory. It carries digests and the
	// one-time custodian codes the next binding consumes, never a payload.
	handoffReceiptsFile = "tenant-handoff-receipts.json"
	// handoffArchiveVersion names the archive a handoff writes.
	handoffArchiveVersion = "guard.tenant-handoff-archive/v1"
)

// isAgentHandoff reports whether the arguments after `agent` select a handoff.
func isAgentHandoff(args []string) bool { return len(args) > 0 && args[0] == "handoff" }

type handoffArchive struct {
	ContractVersion string          `json:"contract_version"`
	PriorTenantID   string          `json:"prior_tenant_id"`
	HandoffID       string          `json:"handoff_id"`
	Disposition     string          `json:"disposition"`
	CreatedAt       time.Time       `json:"created_at"`
	Entries         []journal.Entry `json:"entries"`
}

// runAgentHandoff implements `techstack agent handoff`: under the prior owner's
// one-time confirmation it moves that tenant's journal entries and outcomes
// out of the Guard state into an export file or a root-only quarantine
// directory, destroys the remaining identity state and the telemetry spool,
// overwrites and removes the journal, and records a receipt the next pairing
// presents. It runs as root on a decommissioned Guard whose service is stopped.
func runAgentHandoff(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("techstack agent handoff", flag.ContinueOnError)
	server := flags.String("server", "", "control-plane base URL that confirms the handoff before anything is removed")
	confirmationFile := flags.String("confirmation-file", "", "file holding the prior owner's handoff confirmation (deleted after use)")
	exportFile := flags.String("export-file", "", "export: new file outside the Guard state directory that receives the prior tenant's payloads")
	quarantineDir := flags.String("quarantine-dir", "", "quarantine: root-only directory outside the Guard state directory (default: <state-dir>-quarantine)")
	stateDir := flags.String("state-dir", "", "Guard state directory (env "+guardStateDirEnv+")")
	enrollmentFile := flags.String("enrollment-file", defaultEnrollmentFile, "enrollment file of the prior binding, destroyed by the handoff")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*confirmationFile) == "" || strings.TrimSpace(*server) == "" {
		return errors.New("--server and --confirmation-file are required")
	}
	token, err := readSecretFile(*confirmationFile)
	if err != nil {
		return fmt.Errorf("read the handoff confirmation: %w", err)
	}
	now := time.Now().UTC()
	confirmation, err := guardbinding.ParseHandoffConfirmation(string(token), now)
	if err != nil {
		return err
	}
	dir := resolveGuardStateDir(*stateDir)
	manager, err := hostidentity.Open(dir)
	if err != nil {
		return err
	}
	// A bound Guard still acts for a tenant. An unfinished pairing (for example
	// one the control plane refused because this handoff was missing) holds no
	// confirmed binding; the handoff ends it with the rest of the prior state.
	if manager.Record().State == hostidentity.StateBound {
		return errors.New("this Guard is bound; the handoff runs only after the server was detached or retired")
	}
	write, target, err := handoffArchiveTarget(confirmation, dir, *exportFile, *quarantineDir)
	if err != nil {
		return err
	}
	// Nothing is removed under a confirmation the control plane does not hold
	// for this server: the check proves this server's host key, which the
	// prior owner's confirmation is bound to.
	if err := verifyHandoffConfirmation(manager, *server, strings.TrimSpace(string(token))); err != nil {
		return err
	}
	root, err := manager.OpenCustodyRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	store, err := journal.OpenRoot(root, journal.Options{})
	if errors.Is(err, journal.ErrLocked) {
		return errors.New("the Guard service holds its journal; stop the service and repeat the handoff")
	}
	if err != nil {
		return fmt.Errorf("open the Guard journal: %w", err)
	}
	defer store.Close()
	if denied := store.Denied(); denied != nil {
		return fmt.Errorf("the Guard journal cannot be read completely (%v); keep the state directory and contact support", denied)
	}
	// The journal is held exclusively now. The prior binding's credential and
	// pairing capability go first: they are its state, and the custodian
	// confirmed its end. The identity key stays: the confirmation is bound to
	// it, and the next pairing must prove it for the receipt to be accepted.
	if err := manager.DecommissionKeepingKey(*enrollmentFile); err != nil {
		return fmt.Errorf("destroy the prior identity state: %w", err)
	}
	entries := store.Entries(func(e journal.Entry) bool { return e.Intent.TenantID == confirmation.TenantID })
	archive, err := json.Marshal(handoffArchive{
		ContractVersion: handoffArchiveVersion, PriorTenantID: confirmation.TenantID, HandoffID: confirmation.HandoffID,
		Disposition: confirmation.Disposition, CreatedAt: now, Entries: entries,
	})
	if err != nil {
		return err
	}
	if err := write(archive); err != nil {
		return fmt.Errorf("write the handoff archive: %w", err)
	}
	removed, err := store.HandOff(confirmation.TenantID)
	if err != nil {
		return fmt.Errorf("the archive %s is complete, but removing the payloads from the Guard state failed: %w", target, err)
	}
	if err := root.RemoveAll("telemetry-spool"); err != nil {
		return fmt.Errorf("remove the telemetry spool: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.Intent.CommandID)
	}
	archiveSum := sha256.Sum256(archive)
	receipt := guardbinding.TenantHandoffReceipt{
		ContractVersion: guardbinding.TenantHandoffContractVersion,
		PriorTenantID:   confirmation.TenantID, HandoffID: confirmation.HandoffID, Confirmation: confirmation.Code,
		Disposition: confirmation.Disposition, Entries: removed, CommandsDigest: guardbinding.CommandsDigest(ids),
		ArchiveDigest: hex.EncodeToString(archiveSum[:]), RemovedAt: time.Now().UTC(),
	}
	if err := storeHandoffReceipt(root, receipt); err != nil {
		return fmt.Errorf("record the handoff receipt: %w", err)
	}
	_ = os.Remove(*confirmationFile)
	_, _ = fmt.Fprintf(stdout, "GUARD_TENANT_HANDOFF_OK handoff_id=%s disposition=%s entries=%d archive=%s archive_sha256=%s\n",
		receipt.HandoffID, receipt.Disposition, receipt.Entries, target, receipt.ArchiveDigest)
	return nil
}

// handoffArchiveTarget returns the writer of the archive for the confirmed
// disposition. The archive never lands in the Guard state directory: the next
// binding's Guard must not be able to reach it.
func handoffArchiveTarget(c guardbinding.HandoffConfirmation, stateDir, exportFile, quarantineDir string) (func([]byte) error, string, error) {
	state, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, "", err
	}
	outside := func(path string) (string, error) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		if absolute == state || strings.HasPrefix(absolute, state+string(filepath.Separator)) {
			return "", errors.New("the handoff archive must be outside the Guard state directory")
		}
		return absolute, nil
	}
	switch c.Disposition {
	case guardbinding.HandoffExport:
		if strings.TrimSpace(exportFile) == "" {
			return nil, "", errors.New("the prior owner confirmed an export: --export-file is required")
		}
		path, err := outside(exportFile)
		if err != nil {
			return nil, "", err
		}
		return func(data []byte) error {
			// #nosec G304 -- the operator names a new file; O_EXCL never follows or replaces an existing one.
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			return writeSynced(file, data)
		}, path, nil
	default:
		if strings.TrimSpace(quarantineDir) == "" {
			quarantineDir = state + "-quarantine"
		}
		path, err := outside(quarantineDir)
		if err != nil {
			return nil, "", err
		}
		name := c.HandoffID + ".json"
		return func(data []byte) error {
			// privatefs admits only a root-owned, owner-only, non-symlink directory.
			quarantine, err := privatefs.Open(path)
			if err != nil {
				return err
			}
			defer quarantine.Close()
			file, err := privatefs.OpenFile(quarantine, name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			return writeSynced(file, data)
		}, filepath.Join(path, name), nil
	}
}

func writeSynced(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func loadHandoffReceipts(root *os.Root) ([]guardbinding.TenantHandoffReceipt, error) {
	file, err := privatefs.OpenFile(root, handoffReceiptsFile, os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var receipts []guardbinding.TenantHandoffReceipt
	if err := json.NewDecoder(io.LimitReader(file, 64<<10)).Decode(&receipts); err != nil {
		return nil, fmt.Errorf("handoff receipts are malformed: %w", err)
	}
	return receipts, nil
}

func storeHandoffReceipt(root *os.Root, receipt guardbinding.TenantHandoffReceipt) error {
	receipts, err := loadHandoffReceipts(root)
	if err != nil {
		return err
	}
	// One receipt per prior tenant: a later handoff of the same tenant replaces
	// an earlier one, so a refused receipt never blocks the next pairing.
	receipts = slices.DeleteFunc(receipts, func(r guardbinding.TenantHandoffReceipt) bool {
		return r.HandoffID == receipt.HandoffID || r.PriorTenantID == receipt.PriorTenantID
	})
	receipts = append(receipts, receipt)
	if len(receipts) > guardbinding.MaxHandoffReceipts {
		receipts = receipts[len(receipts)-guardbinding.MaxHandoffReceipts:]
	}
	raw, err := json.Marshal(receipts)
	if err != nil {
		return err
	}
	return privatefs.Replace(root, handoffReceiptsFile, raw, 0o600)
}

// guardPriorCustody is the declaration a pairing redemption carries: which
// tenants' journal entries this server still holds and which handoffs it
// completed since its last binding. A journal the enrollment cannot read
// fails the enrollment closed, so a held payload is never hidden from the
// control plane's cross-tenant check.
func guardPriorCustody(manager *hostidentity.Manager) (*guardbinding.PriorCustody, error) {
	root, err := manager.OpenCustodyRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	declaration := &guardbinding.PriorCustody{Contract: guardbinding.TenantHandoffContractVersion}
	if declaration.Receipts, err = loadHandoffReceipts(root); err != nil {
		return nil, err
	}
	if _, statErr := root.Stat("journal.log"); errors.Is(statErr, os.ErrNotExist) {
		return declaration, nil
	}
	store, err := journal.OpenRoot(root, journal.Options{})
	if errors.Is(err, journal.ErrLocked) {
		return nil, errors.New("the Guard service holds its journal; stop the service and repeat the enrollment")
	}
	if err != nil {
		return nil, fmt.Errorf("read the Guard journal before pairing: %w", err)
	}
	defer store.Close()
	held := store.Entries(func(journal.Entry) bool { return true })
	declaration.HeldEntries = len(held)
	for _, e := range held {
		if !slices.Contains(declaration.HeldTenants, e.Intent.TenantID) {
			declaration.HeldTenants = append(declaration.HeldTenants, e.Intent.TenantID)
		}
	}
	return declaration, nil
}

// declarePriorCustody adds the prior-custody declaration to a redemption.
func declarePriorCustody(manager *hostidentity.Manager, bootstrap *hostidentity.Bootstrap) error {
	declaration, err := guardPriorCustody(manager)
	if err != nil {
		return err
	}
	if bootstrap.Extra == nil {
		bootstrap.Extra = map[string]any{}
	}
	bootstrap.Extra["prior_custody"] = declaration
	return nil
}

// clearHandoffReceipts removes the consumed receipts after a binding was
// issued; the control plane holds their digests.
func clearHandoffReceipts(manager *hostidentity.Manager) {
	root, err := manager.OpenCustodyRoot()
	if err != nil {
		return
	}
	defer root.Close()
	_ = root.Remove(handoffReceiptsFile)
}

// verifyHandoffConfirmation asks the control plane whether the confirmation is
// valid for this server's host key. Any answer other than an explicit
// verification refuses the handoff.
func verifyHandoffConfirmation(manager *hostidentity.Manager, server, token string) error {
	if manager.Key() == nil {
		if err := manager.EnsureKey(); err != nil {
			return fmt.Errorf("load the Guard host key: %w", err)
		}
	}
	key := manager.Key()
	if key == nil {
		return errors.New("this Guard holds no host key, so the control plane cannot confirm the handoff for this server")
	}
	endpoint := strings.TrimRight(strings.TrimSpace(server), "/") + "/api/v1/workers/tenant-handoffs/verify"
	body, err := json.Marshal(map[string]string{"confirmation": token})
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	proof, err := guardidentity.SignProof(key, guardidentity.ProofInput{Method: http.MethodPost, URL: endpoint, AccessToken: token})
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(guardidentity.ProofHeader, proof)
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("the control plane could not confirm the handoff: %w", err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
	var answer struct {
		Data struct {
			Verified bool `json:"verified"`
		} `json:"data"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &answer)
	if response.StatusCode != http.StatusOK || !answer.Data.Verified {
		return fmt.Errorf("the control plane refused the handoff confirmation (HTTP %d): %s; nothing was removed", response.StatusCode, strings.ToValidUTF8(answer.Error.Message, ""))
	}
	return nil
}
