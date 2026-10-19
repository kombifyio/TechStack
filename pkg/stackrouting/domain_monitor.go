package stackrouting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/releasechannel"
)

type DomainEvidenceStore interface {
	ListOwner(context.Context, Principal, time.Time) ([]DomainRow, error)
	Save(context.Context, DomainTarget, DomainRow, time.Time) (*DomainRow, error)
}

// DomainMonitor uses canonical inventory to select targets and the existing
// routing authority for expectations. API reads do not initiate network probes.
type DomainMonitor struct {
	Directory                               controlplane.DriftScheduleTenantDirectory
	Stacks                                  controlplane.StackStore
	Servers                                 controlplane.ServerRuntimeStore
	Services                                controlplane.ServiceRuntimeStore
	Routing                                 Store
	Evidence                                DomainEvidenceStore
	Collector                               *DomainCollector
	OnError                                 func(error)
	mu                                      sync.Mutex
	afterTenant, currentTenant, afterTarget string
}

type domainCandidate struct {
	target   DomainTarget
	row      DomainRow
	expected Expectations
}

func domainTargetID(target DomainTarget, port int) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{target.TenantID, target.StackID, target.ServerID, target.Domain, strconv.Itoa(port)}, "\x00")))
	return hex.EncodeToString(digest[:16])
}

func (m *DomainMonitor) candidates(ctx context.Context, tenantID, ownerID string) ([]domainCandidate, error) {
	if m == nil || m.Stacks == nil || m.Servers == nil || m.Services == nil || m.Routing == nil {
		return nil, ErrUnavailable
	}
	stacks, err := m.Stacks.ListStacksByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	servers, err := m.Servers.ListServerRuntimesByTenant(ctx, tenantID, "")
	if err != nil {
		return nil, err
	}
	services, err := m.Services.ListServiceRuntimes(ctx, tenantID, "", "")
	if err != nil {
		return nil, err
	}
	byID := map[string]*domainCandidate{}
	for _, stack := range stacks {
		if stack.DeletedAt != nil || (ownerID != "" && stack.OwnerSubjectID != ownerID) || stack.OwnerSubjectID == "" {
			continue
		}
		byServer := map[string]controlplane.ServerRuntime{}
		for _, server := range servers {
			if server.StackID != stack.ID || server.OwnerSubjectID != stack.OwnerSubjectID || server.DesiredState == "absent" || server.LifecycleState == "decommissioning" || server.LifecycleState == "decommissioned" {
				continue
			}
			byServer[server.ID] = server
		}
		add := func(serverID, raw string, expected Expectations, revision int64, service *controlplane.ServiceRuntime) {
			if _, ok := byServer[serverID]; !ok {
				return
			}
			host, port, ok := domainEndpoint(raw)
			if !ok {
				return
			}
			target := DomainTarget{TenantID: tenantID, OwnerSubjectID: stack.OwnerSubjectID, StackID: stack.ID, ServerID: serverID, Domain: host, RoutingRevision: revision}
			id := domainTargetID(target, port)
			candidate := byID[id]
			if candidate == nil {
				candidate = &domainCandidate{target: target, row: DomainRow{ID: id, Domain: host, StackID: stack.ID, ServerID: serverID, TLSPort: port, TLSDisabled: strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "http://"), Services: []DomainService{}, Conditions: []DomainCondition{}}, expected: expected}
				byID[id] = candidate
			}
			if revision > 0 {
				candidate.target.RoutingRevision, candidate.row.RoutingRevision, candidate.expected = revision, revision, expected
			}
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "http://") {
				candidate.row.TLSDisabled = false
			}
			if service != nil {
				for _, associated := range candidate.row.Services {
					if associated.ID == service.ID {
						return
					}
				}
				candidate.row.Services = append(candidate.row.Services, DomainService{ID: service.ID, Name: service.Name})
			}
		}
		desired, routingErr := m.Routing.Get(ctx, tenantID, stack.ID)
		if routingErr != nil && !errors.Is(routingErr, ErrNotFound) {
			return nil, routingErr
		}
		if desired != nil && desired.OwnerSubjectID == stack.OwnerSubjectID {
			add(desired.ServerID, desired.Domain, desired.Expectations, desired.Revision, nil)
		}
		for _, server := range byServer {
			host, _ := server.Metadata["host"].(map[string]any)
			for _, metadata := range []map[string]any{server.Metadata, host} {
				if domain, ok := metadata["domain"].(string); ok {
					add(server.ID, domain, Expectations{}, 0, nil)
				}
			}
			for _, endpoint := range domainEndpointMaps(server.Metadata["endpoints"]) {
				if value, ok := endpoint["url"].(string); ok {
					add(server.ID, value, Expectations{}, 0, nil)
				}
			}
		}
		for _, service := range services {
			if service.StackID != stack.ID || service.DesiredState == "absent" || service.DesiredState == "archived" {
				continue
			}
			if raw, ok := service.Access["url"].(string); ok {
				add(service.ServerID, raw, Expectations{}, 0, &service)
			}
		}
		if desired != nil && desired.OwnerSubjectID == stack.OwnerSubjectID {
			for _, candidate := range byID {
				if candidate.target.StackID == stack.ID && candidate.target.ServerID == desired.ServerID && candidate.row.Domain == desired.Domain {
					candidate.target.RoutingRevision, candidate.row.RoutingRevision = desired.Revision, desired.Revision
					candidate.expected = CloneExpectations(desired.Expectations)
					if candidate.row.TLSPort != 443 {
						candidate.expected.TLS = TLSExpectation{Hostname: candidate.row.Domain}
					}
				}
			}
		}
	}
	if len(byID) > 4096 {
		return nil, fmt.Errorf("%w: domain monitoring target limit exceeded", ErrUnavailable)
	}
	result := make([]domainCandidate, 0, len(byID))
	for _, candidate := range byID {
		sort.Slice(candidate.row.Services, func(i, j int) bool { return candidate.row.Services[i].ID < candidate.row.Services[j].ID })
		result = append(result, *candidate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].row.ID < result[j].row.ID })
	return result, nil
}

func domainEndpoint(raw string) (string, int, bool) {
	raw = strings.TrimSpace(raw)
	port := 443
	if strings.Contains(raw, "://") {
		endpoint, err := url.Parse(raw)
		if err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil {
			return "", 0, false
		}
		raw = endpoint.Hostname()
		if endpoint.Port() != "" && endpoint.Scheme == "https" {
			port, err = strconv.Atoi(endpoint.Port())
			if err != nil || port < 1 || port > 65535 {
				return "", 0, false
			}
		}
	}
	host := strings.ToLower(strings.TrimSuffix(raw, "."))
	if net.ParseIP(host) != nil || validateDomain(host) != nil {
		return "", 0, false
	}
	return host, port, true
}

func domainEndpointMaps(value any) []map[string]any {
	if maps, ok := value.([]map[string]any); ok {
		return maps
	}
	values, _ := value.([]any)
	result := []map[string]any{}
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			result = append(result, item)
		}
	}
	return result
}

// Report projects current targets, retaining historical readings without calling
// a target healthy after the desired routing revision changes.
func (m *DomainMonitor) Report(ctx context.Context, p Principal, serverID, serviceID string, now time.Time) (*DomainReport, error) {
	if m == nil || m.Evidence == nil || p.TenantID == "" || p.OwnerSubjectID == "" {
		return nil, ErrUnavailable
	}
	candidates, err := m.candidates(ctx, p.TenantID, p.OwnerSubjectID)
	if err != nil {
		return nil, err
	}
	recorded, err := m.Evidence.ListOwner(ctx, p, now)
	if err != nil {
		return nil, err
	}
	byID := map[string]DomainRow{}
	for _, row := range recorded {
		byID[row.ID] = row
	}
	report := &DomainReport{GeneratedAt: now.UTC(), Domains: []DomainRow{}}
	for _, candidate := range candidates {
		row := candidate.row
		if serverID != "" && row.ServerID != serverID {
			continue
		}
		if serviceID != "" {
			found := false
			for _, service := range row.Services {
				if service.ID == serviceID {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		if snapshot, ok := byID[row.ID]; ok {
			if snapshot.RoutingRevision != row.RoutingRevision {
				// Preserve previous evidence visibly as historical; reads never
				// resolve or open durable condition episodes.
				snapshot.DNS.State, snapshot.TLSDrift.State = "unknown", "unknown"
				snapshot.DNS.ReasonCode, snapshot.TLSDrift.ReasonCode = "routing_expectation_changed", "routing_expectation_changed"
				snapshot.DNS.FreshUntil, snapshot.TLSDrift.FreshUntil = &now, &now
				snapshot.DNS.Differences = pendingDNSDifferences(candidate.expected.DNS, snapshot.DNS.Differences)
				snapshot.TLSDrift.Differences = pendingTLSDifferences(row.Domain, candidate.expected.TLS, snapshot.TLSDrift.Differences)
			}
			snapshot.Services, snapshot.RoutingRevision = row.Services, row.RoutingRevision
			snapshot.TLSDisabled = row.TLSDisabled
			row = snapshot
		}
		row = RefreshDomain(row, now)
		if row.DNS.Differences == nil {
			row.DNS.Differences = pendingDNSDifferences(candidate.expected.DNS, nil)
		}
		if row.TLSDrift.Differences == nil {
			row.TLSDrift.Differences = pendingTLSDifferences(row.Domain, candidate.expected.TLS, nil)
		}
		if row.RoutingRevision > 0 {
			row.DNS.ExpectedSource, row.TLSDrift.ExpectedSource = "stack_routing_desired", "stack_routing_desired"
		} else {
			row.TLSDrift.ExpectedSource = "inventory_endpoint"
		}
		if row.TLSDisabled {
			row.TLS = TLSEvidence{ExpiryEvidence: ExpiryEvidence{State: "unknown", Freshness: "unknown", ReasonCode: "tls_not_configured"}}
			row.TLSDrift = DriftEvidence{State: "unknown", Freshness: "unknown", ReasonCode: "tls_not_configured", Differences: []EvidenceDifference{}}
		}
		report.Domains = append(report.Domains, row)
	}
	sort.Slice(report.Domains, func(i, j int) bool { return report.Domains[i].Domain < report.Domains[j].Domain })
	return report, nil
}

func pendingDNSDifferences(records []DNSRecord, previous []EvidenceDifference) []EvidenceDifference {
	result := []EvidenceDifference{}
	for _, record := range records {
		difference := EvidenceDifference{Field: record.Name + " " + record.Type, Expected: record.Values, Observed: []string{}, State: "unknown"}
		for _, old := range previous {
			if old.Field == difference.Field {
				difference.Observed = old.Observed
			}
		}
		result = append(result, difference)
	}
	return result
}
func pendingTLSDifferences(domain string, expected TLSExpectation, previous []EvidenceDifference) []EvidenceDifference {
	if expected.Hostname == "" {
		expected.Hostname = domain
	}
	values := []EvidenceDifference{{Field: "hostname", Expected: []string{expected.Hostname}}, {Field: "trusted_chain", Expected: []string{"true"}}}
	if expected.Issuer != "" {
		values = append(values, EvidenceDifference{Field: "issuer", Expected: []string{expected.Issuer}})
	}
	if expected.FingerprintSHA256 != "" {
		values = append(values, EvidenceDifference{Field: "fingerprint_sha256", Expected: []string{expected.FingerprintSHA256}})
	}
	for i := range values {
		values[i].State, values[i].Observed = "unknown", []string{}
		for _, old := range previous {
			if old.Field == values[i].Field {
				values[i].Observed = old.Observed
			}
		}
	}
	return values
}

func (m *DomainMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		if err := m.SweepOnce(ctx); err != nil && ctx.Err() == nil && m.OnError != nil {
			m.OnError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SweepOnce retains cursors so a bounded pass cannot permanently starve a later
// tenant or endpoint. Network observations run outside database transactions.
func (m *DomainMonitor) SweepOnce(ctx context.Context) error {
	if m == nil || m.Directory == nil || m.Evidence == nil || m.Collector == nil {
		return ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	var sweepErr error
	for visited, observed := 0, 0; visited < 100 && observed < 128; visited++ {
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < 35*time.Second {
			return sweepErr
		}
		if m.currentTenant == "" {
			tenants, err := m.Directory.ListDriftScheduleTenants(ctx, m.afterTenant, 1)
			if err != nil {
				return err
			}
			if len(tenants) == 0 {
				m.afterTenant = ""
				return sweepErr
			}
			m.currentTenant = tenants[0]
		}
		// The other release channel runs this tenant's work (pkg/releasechannel).
		if !releasechannel.OwnsTenant(m.currentTenant) {
			m.afterTenant, m.currentTenant, m.afterTarget = m.currentTenant, "", ""
			continue
		}
		candidates, err := m.candidates(ctx, m.currentTenant, "")
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if sweepErr == nil {
				sweepErr = err
			}
			m.afterTenant, m.currentTenant, m.afterTarget = m.currentTenant, "", ""
			continue
		}
		batch := []domainCandidate{}
		for _, candidate := range candidates {
			if candidate.row.ID <= m.afterTarget {
				continue
			}
			if len(batch) >= 4 || observed+len(batch) >= 128 {
				break
			}
			batch = append(batch, candidate)
		}
		var wg sync.WaitGroup
		limit := make(chan struct{}, 4)
		var firstErr error
		var errorMu sync.Mutex
		for _, candidate := range batch {
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				wg.Wait()
				return ctx.Err()
			}
			wg.Add(1)
			go func(candidate domainCandidate) {
				defer wg.Done()
				defer func() { <-limit }()
				row := m.Collector.Collect(ctx, candidate.row, candidate.expected)
				_, err := m.Evidence.Save(ctx, candidate.target, row, time.Now().UTC())
				if err != nil {
					errorMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errorMu.Unlock()
				}
			}(candidate)
		}
		wg.Wait()
		if firstErr != nil && sweepErr == nil {
			sweepErr = firstErr
		}
		observed += len(batch)
		if len(batch) > 0 {
			m.afterTarget = batch[len(batch)-1].row.ID
		}
		if len(candidates) == 0 || m.afterTarget >= candidates[len(candidates)-1].row.ID {
			m.afterTenant, m.currentTenant, m.afterTarget = m.currentTenant, "", ""
		}
	}
	return sweepErr
}
