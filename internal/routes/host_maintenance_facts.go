package routes

import (
	"regexp"
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/servermaintenance"
)

var (
	hostBootIDPattern        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	hostMachineDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// hostMaintenanceFacts are the node facts a Guard heartbeat and inventory
// report for server maintenance. Malformed values are dropped, never stored.
type hostMaintenanceFacts struct {
	HostBootID          string `json:"host_boot_id,omitempty"`
	HostMachineIDSHA256 string `json:"host_machine_id_sha256,omitempty"`
	HostMaintenance     bool   `json:"host_maintenance"`
}

// metadata renders the facts as server metadata. Every key is always
// present, so a newer observation replaces an older one.
func (facts hostMaintenanceFacts) metadata() map[string]any {
	bootID := strings.TrimSpace(facts.HostBootID)
	if !hostBootIDPattern.MatchString(bootID) {
		bootID = ""
	}
	machine := strings.TrimSpace(facts.HostMachineIDSHA256)
	if !hostMachineDigestPattern.MatchString(machine) {
		machine = ""
	}
	return map[string]any{
		servermaintenance.MetadataBootID:          bootID,
		servermaintenance.MetadataMachineIDDigest: machine,
		servermaintenance.MetadataHostMaintenance: facts.HostMaintenance,
	}
}

// hostMaintenanceMetadataFromWorker copies the facts the last heartbeat
// stored on the worker record.
func hostMaintenanceMetadataFromWorker(worker controlplane.Worker) map[string]any {
	if worker.Capabilities == nil {
		return nil
	}
	if _, reported := worker.Capabilities[servermaintenance.MetadataHostMaintenance]; !reported {
		return nil
	}
	bootID, _ := worker.Capabilities[servermaintenance.MetadataBootID].(string)
	machine, _ := worker.Capabilities[servermaintenance.MetadataMachineIDDigest].(string)
	capable, _ := worker.Capabilities[servermaintenance.MetadataHostMaintenance].(bool)
	return hostMaintenanceFacts{HostBootID: bootID, HostMachineIDSHA256: machine, HostMaintenance: capable}.metadata()
}
