package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/kombifyme"
)

const ManagedAddressReconciliationResultField = "kombify_me_address_reconciliation"

// Match the existing RFC1123 hostname boundary used by provider admission.
var managedAddressLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// ManagedAddressAuthority reloads the approved runtime and rollout at execution
// time. It returns only a public address, never provider or SSH credentials.
type ManagedAddressAuthority func(context.Context, StackKitLifecycleRequest) (ManagedAddressRuntime, error)

type ManagedAddressRuntime struct {
	PublicIP   string
	LeaseID    string
	ServerID   string
	Generation int64
	Rollout    StackKitRolloutBinding
}

func reconcileManagedChangeSetAddresses(ctx context.Context, authority ManagedAddressAuthority, release stackkitrelease.Release, req StackKitLifecycleRequest) (map[string]interface{}, error) {
	var candidate struct {
		Network struct {
			Domain struct {
				Base string `json:"base"`
			} `json:"domain"`
		} `json:"network"`
	}
	if json.Unmarshal(req.CandidateSpecJSON, &candidate) != nil {
		return nil, fmt.Errorf("candidate address intent is invalid")
	}
	domain := candidate.Network.Domain.Base
	zone := ""
	if strings.HasSuffix(domain, "."+addressModeKombifyMeDomain) {
		label := strings.TrimSuffix(domain, "."+addressModeKombifyMeDomain)
		if !managedAddressLabel.MatchString(label) {
			return nil, fmt.Errorf("managed installation zone must be one registered label")
		}
		zone = domain
	} else if domain != addressModeKombifyMeDomain {
		return map[string]interface{}{"status": "not_applicable"}, nil
	}
	if authority == nil {
		return nil, fmt.Errorf("managed change-set address authority is unavailable")
	}
	plan, err := pinnedCandidateAddressPlan(ctx, release, req.CandidateSpecJSON)
	if err != nil {
		return nil, err
	}
	if zone != "" {
		// The released CLI treats an installation zone like an own domain.
		// Preserve that plan, deriving only its existing registry lookup label.
		plan, err = normalizeRegisteredZonePlan(plan, zone)
		if err != nil {
			return nil, err
		}
	}
	if plan.APIVersion != stackKitAddressPlanV1 || plan.Provider != addressModeKombifyMe || plan.Domain != addressModeKombifyMeDomain || req.StackKitInstanceID == "" || plan.StackName != req.StackKitInstanceID || plan.SubdomainPrefix == "" || len(plan.Services) == 0 {
		return nil, fmt.Errorf("managed change set requires its existing managed address binding")
	}
	seen := map[string]bool{}
	hosts := make([]string, 0, len(plan.Services))
	for _, service := range plan.Services {
		want := plan.SubdomainPrefix + "-" + service.ServiceKey + "." + addressModeKombifyMeDomain
		if zone != "" {
			want = service.ServiceKey + "." + zone
		}
		if !managedAddressLabel.MatchString(service.ServiceKey) || seen[service.ServiceKey] || service.Host != want {
			return nil, fmt.Errorf("candidate service differs from its managed address binding")
		}
		seen[service.ServiceKey] = true
		hosts = append(hosts, want)
	}
	current, err := authority(ctx, req)
	if err != nil {
		return nil, err
	}
	if current.Rollout.SpecPath == "" || current.Rollout.SpecPath != req.SpecPath || current.Rollout.StackKit != req.StackKit || current.ServerID == "" || current.Generation <= 0 {
		return nil, fmt.Errorf("managed change set differs from the current rollout binding")
	}
	address, err := kombifyme.NewManagedAddress(req.TenantID, req.OwnerID, req.StackID)
	if err != nil {
		return nil, err
	}
	// An unleased/BYO runtime gains no provider-address mutation authority.
	// Preserve its existing change-set behavior only when every desired route
	// is already exposed under the exact registry binding.
	if current.LeaseID == "" {
		if err := verifyExistingManagedAddress(ctx, plan, address, ""); err != nil {
			return nil, err
		}
		latest, err := authority(ctx, req)
		if err != nil || latest != current {
			return nil, fmt.Errorf("managed address runtime changed during reconciliation")
		}
		return map[string]interface{}{"status": "not_applicable", "reason": "unleased_existing_routes"}, nil
	}
	target := &ManagedRuntimeTarget{PublicIP: current.PublicIP}
	origin, err := managedAddressOrigin(target)
	if err != nil {
		return nil, err
	}
	if err := verifyExistingManagedAddress(ctx, plan, address, origin); err != nil {
		return nil, err
	}
	bound, err := allocateManagedAddressPlan(ctx, plan, target, address)
	if err != nil {
		return nil, err
	}
	if bound.Prefix != plan.SubdomainPrefix || bound.Zone != zone {
		return nil, fmt.Errorf("managed change set changed its address layout")
	}
	for _, service := range plan.Services {
		if bound.Hosts[service.ServiceKey] != service.Host {
			return nil, fmt.Errorf("registered address differs from the candidate")
		}
	}
	latest, err := authority(ctx, req)
	if err != nil || latest != current {
		return nil, fmt.Errorf("managed address runtime changed during reconciliation")
	}
	sort.Strings(hosts)
	return map[string]interface{}{
		"status": "registered", "stack_ref": address.StackRef, "target_origin": origin,
		"hosts": hosts, "server_id": current.ServerID, "generation": current.Generation, "lease_id": current.LeaseID,
		"candidate_sha256": fmt.Sprintf("sha256:%x", sha256.Sum256(req.CandidateSpecJSON)),
		"verified_at":      time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// The CLI deliberately represents an existing installation zone as external.
// Both initial recovery and applied change sets must verify that exact zone
// against the owner's registry before registering any additional route.
func normalizeRegisteredZonePlan(plan stackKitAddressPlan, zone string) (stackKitAddressPlan, error) {
	label := strings.TrimSuffix(zone, "."+addressModeKombifyMeDomain)
	if !managedAddressLabel.MatchString(label) || plan.APIVersion != stackKitAddressPlanV1 || plan.Provider != "external" || plan.Domain != zone || plan.SubdomainPrefix != "" || len(plan.Services) == 0 {
		return plan, fmt.Errorf("candidate differs from its installation zone plan")
	}
	seen := map[string]bool{}
	for _, service := range plan.Services {
		if !managedAddressLabel.MatchString(service.ServiceKey) || seen[service.ServiceKey] || service.Host != service.ServiceKey+"."+zone {
			return plan, fmt.Errorf("candidate service escapes its installation zone")
		}
		seen[service.ServiceKey] = true
	}
	plan.Provider, plan.Domain = addressModeKombifyMe, addressModeKombifyMeDomain
	plan.SubdomainPrefix, plan.RegisteredZone = label, zone
	return plan, nil
}

func pinnedCandidateAddressPlan(ctx context.Context, release stackkitrelease.Release, candidate []byte) (stackKitAddressPlan, error) {
	var plan stackKitAddressPlan
	planner, err := controllerAddressPlannerRelease(release)
	if err != nil {
		return plan, err
	}
	unlock, err := acquireStackKitCLI(ctx, nil)
	if err != nil {
		return plan, err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, runtimeActionHTTPTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "stackkit-candidate-address-*")
	if err != nil {
		return plan, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "candidate.json"), candidate, 0o600); err != nil {
		return plan, err
	}
	command := exec.CommandContext(ctx, planner.BinaryPath(), "--no-log", "--chdir", dir, "--spec", "candidate.json", "address", "plan") // #nosec G204 -- exact admitted release; fixed command, private candidate file.
	command.Dir = dir
	// Address planning needs no platform/provider credentials. Do not inherit
	// Core's ambient secret-bearing environment into the StackKits process.
	command.Env = []string{"HOME=" + dir, "TMPDIR=" + dir, "USERPROFILE=" + dir, "LOCALAPPDATA=" + dir, "APPDATA=" + dir, "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "PATH=" + os.Getenv("PATH")}
	output, err := command.Output()
	if err != nil {
		return plan, fmt.Errorf("pinned StackKits candidate address planning failed")
	}
	if len(output) > 1<<20 || json.Unmarshal(output, &plan) != nil {
		return plan, fmt.Errorf("pinned StackKits candidate address plan is invalid")
	}
	return plan, nil
}

// Verify before registration: an existing installation cannot be rebound to a
// new base, another owner, another origin, or an unsupported zone layout.
func verifyExistingManagedAddress(ctx context.Context, plan stackKitAddressPlan, address kombifyme.ManagedAddress, origin string) error {
	client := kombifyme.NewClient(kombifyme.DefaultConfigFromEnv())
	response, err := client.Forward(ctx, http.MethodGet, "subdomains?binding_stack_ref="+url.QueryEscape(address.StackRef), nil, "")
	if err != nil {
		return fmt.Errorf("read existing managed address binding: %w", withUpstreamErrorCode(err))
	}
	routes, ok := response.Body.([]any)
	if !ok {
		return fmt.Errorf("managed address registry returned no route list")
	}
	baseID := ""
	layout := managedAddressLayoutFlat
	if plan.RegisteredZone != "" {
		layout = managedAddressLayoutZone
	}
	services := make([]map[string]any, 0)
	for _, raw := range routes {
		route, ok := raw.(map[string]any)
		if !ok || route["binding_owner_ref"] != address.OwnerRef || route["binding_stack_ref"] != address.StackRef {
			return fmt.Errorf("managed address registry returned a foreign binding")
		}
		switch route["subdomain_kind"] {
		case "base":
			if baseID != "" || route["name"] != plan.SubdomainPrefix || route["fqdn"] != plan.SubdomainPrefix+"."+addressModeKombifyMeDomain || (route["layout"] != nil && route["layout"] != "" && route["layout"] != layout) {
				return fmt.Errorf("candidate differs from its registered managed base")
			}
			baseID, _ = route["id"].(string)
			if baseID == "" {
				return fmt.Errorf("managed address base identity is missing")
			}
		case "service":
			services = append(services, route)
		default:
			return fmt.Errorf("managed address registry returned an unsupported route")
		}
	}
	if baseID == "" || len(services) == 0 {
		return fmt.Errorf("managed rollout has no existing address binding")
	}
	for _, service := range services {
		name, _ := service["name"].(string)
		key := strings.TrimPrefix(name, plan.SubdomainPrefix+"-")
		if plan.RegisteredZone != "" {
			key = strings.TrimSuffix(name, "."+plan.SubdomainPrefix)
		}
		if service["parent_id"] != baseID || service["target_type"] != "proxy" || (origin != "" && service["target_addr"] != origin) || service["fqdn"] != name+"."+addressModeKombifyMeDomain || key == name || !managedAddressLabel.MatchString(key) {
			return fmt.Errorf("existing managed service differs from the current runtime")
		}
	}
	if origin == "" {
		for _, planned := range plan.Services {
			found := false
			for _, service := range services {
				if service["fqdn"] == planned.Host && service["exposed"] == true {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("new hosted routes require a provider-owned runtime target")
			}
		}
	}
	return nil
}
