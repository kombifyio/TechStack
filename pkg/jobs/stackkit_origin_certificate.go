package jobs

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/originca"
)

// OriginCertificateResultField is the job-result key of the managed origin
// certificate evidence (ids, hostnames and expiry only).
const OriginCertificateResultField = "origin_certificate"

const originCertificateStep = "origin_certificate"

var originRecordLabelSanitizer = regexp.MustCompile(`[^a-zA-Z0-9]`)

// managedOriginRecordHost is the DNS-only origin record the kombify.me Worker
// resolves for a runtime (kombify-Me worker/src/dns.ts): o-<ip>-<port>.
func managedOriginRecordHost(publicIP string) (string, error) {
	address, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(publicIP), "[]"))
	if err != nil || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return "", fmt.Errorf("managed origin record requires a public runtime IP")
	}
	return "o-" + originRecordLabelSanitizer.ReplaceAllString(address.String(), "-") + "-443.origin" + originca.DomainSuffix, nil
}

// managedOriginCertificateHosts are the names one install's certificate
// covers: its route hostnames plus the origin record (ADR-0047 section b).
func managedOriginCertificateHosts(routeHosts []string, publicIP string) ([]string, error) {
	origin, err := managedOriginRecordHost(publicIP)
	if err != nil {
		return nil, err
	}
	return originca.NormalizeHosts(append(append([]string(nil), routeHosts...), origin))
}

// releaseRendersOriginCertificateRouter reports whether a node running this
// release serves managed kombify.me routes from the delivered Origin CA
// certificate. The renderer and the origin-certificate operations ship
// together (StackKits v0.51.7), so a catalog that does not list the request
// operation belongs to a release whose router still orders ACME itself. A
// release without a catalog is treated as current: the rollout fails on that
// missing catalog before the host is touched.
func releaseRendersOriginCertificateRouter(release stackkitrelease.Release) bool {
	catalog, err := release.AdvancedOperations()
	if err != nil {
		return true
	}
	_, listed := catalog.Lookup(advancedissuer.OperationOriginCertificateRequest)
	return listed
}

// DegradedComponentsResultField lists the independent parts of a rollout that
// did not complete while the stack core did. Each entry is retryable and names
// how it is repaired; the stack projection keeps it until a repair replaces it.
const DegradedComponentsResultField = "degraded_components"

const (
	degradedComponentPublicTLS = "public_tls"
	publicTLSRepairScheduled   = "scheduled"
	publicTLSRepairFailed      = "failed"
)

// publicTLSDegradedComponent describes managed public TLS without an installed
// origin certificate. It carries hostnames and the failing stage, never a
// token, key or certificate body.
func publicTLSDegradedComponent(err error, hosts []string, repairStatus string) map[string]interface{} {
	stage := "certificate"
	var originErr *OriginCertificateError
	var unavailable *AdvancedOperationUnavailableError
	switch {
	case errors.As(err, &originErr):
		stage = originErr.Stage
	case errors.As(err, &unavailable):
		stage = "capability"
	}
	component := map[string]interface{}{
		"component": degradedComponentPublicTLS, resultStatusField: "degraded",
		"reason_code": "origin_certificate_" + stage + "_failed", "stage": stage,
		"retryable": true, "hosts": append([]string(nil), hosts...),
		"repair": map[string]interface{}{"mode": "automatic", resultStatusField: repairStatus},
	}
	if err != nil {
		component["detail"] = err.Error()
	}
	return component
}

// DegradedPublicTLSHosts returns the hostnames of a retryable degraded public
// TLS component recorded in a rollout result or stack projection.
func DegradedPublicTLSHosts(result map[string]interface{}) []string {
	components, _ := result[DegradedComponentsResultField].([]interface{})
	for _, raw := range components {
		component, _ := raw.(map[string]interface{})
		if stringFromInterface(component["component"]) != degradedComponentPublicTLS {
			continue
		}
		if normalized, err := originca.NormalizeHosts(stringListFromInterface(component["hosts"])); err == nil {
			return normalized
		}
	}
	return nil
}

// OriginCertificateError is a managed origin certificate failure. It never
// carries a token, key or certificate body.
type OriginCertificateError struct {
	Stage string
	Err   error
}

func (e *OriginCertificateError) Error() string {
	return fmt.Sprintf("managed origin certificate %s failed: %v", e.Stage, e.Err)
}

func (e *OriginCertificateError) Unwrap() error { return e.Err }

// managedOriginCertificate is the rollout hook that runs request -> issue ->
// install before apply, which verifies TLS on the node.
type managedOriginCertificate struct {
	service *originca.Service
	issuer  AdvancedIssuer
	hosts   []string
}

func (m *managedOriginCertificate) run(ctx context.Context, sender StackKitCommandSender, jobID string, base StackKitLifecycleRequest, release stackkitrelease.Release) (map[string]interface{}, error) {
	request := base
	request.Operation = StackKitLifecycleGenerate // normalization needs an operator operation
	request.OwnerApproved = true
	request.InventoryJSON, request.CandidateSpecJSON = nil, nil
	if request.BoundSpecPath != "" {
		request.SpecPath = request.BoundSpecPath
	}
	request, err := NormalizeStackKitLifecycleRequest(request)
	if err != nil {
		return nil, &OriginCertificateError{Stage: "request", Err: err}
	}
	dispatcher, err := newAdvancedDispatcher(ctx, sender, m.issuer, release, request)
	if err != nil {
		return nil, &OriginCertificateError{Stage: "capability", Err: err}
	}
	return rotateManagedOriginCertificate(ctx, dispatcher, m.service, jobID, m.hosts, true)
}

// rotateManagedOriginCertificate requests a CSR from the node, has Cloudflare
// issue it, installs the result and only then revokes the superseded
// certificate. The key exists on the node alone; Techstack handles the CSR,
// the public certificate and Cloudflare ids. With force false a certificate
// that already covers hosts and is within two thirds of its lifetime is kept.
func rotateManagedOriginCertificate(ctx context.Context, d *advancedDispatcher, service *originca.Service, jobID string, hosts []string, force bool) (map[string]interface{}, error) {
	if service == nil {
		return nil, &OriginCertificateError{Stage: "configuration", Err: originca.ErrUnavailable}
	}
	hosts, err := originca.NormalizeHosts(hosts)
	if err != nil {
		return nil, &OriginCertificateError{Stage: "hostnames", Err: err}
	}
	tenantID, stackID := d.request.TenantID, d.request.StackID
	if !force {
		current, found, currentErr := service.Current(ctx, tenantID, stackID)
		if currentErr != nil {
			return nil, &OriginCertificateError{Stage: "custody", Err: currentErr}
		}
		if found && !service.NeedsRenewal(current, hosts) {
			return map[string]interface{}{resultStatusField: "current", "certificate_id": current.CertificateID, "not_after": current.NotAfter.UTC().Format(time.RFC3339)}, nil
		}
	}
	operations, err := d.require(stackKitStepOriginCertificateRequest, stackKitStepOriginCertificateInstall)
	if err != nil {
		return nil, err
	}
	capability, err := d.issue(ctx, operations)
	if err != nil {
		return nil, err
	}
	requested, _, err := d.dispatch(ctx, jobID+"-oca-request", stackKitStepOriginCertificateRequest, capability, func(command *agentpb.StackKitCommand) {
		command.OriginHosts = append([]string(nil), hosts...)
	})
	if err != nil {
		return nil, err
	}
	if requested.OriginRequest == nil {
		return nil, &OriginCertificateError{Stage: "request", Err: fmt.Errorf("the node returned no CSR")}
	}
	certificate, err := service.Issue(ctx, hosts, requested.OriginRequest.CSRPEM)
	if err != nil {
		return nil, &OriginCertificateError{Stage: "issue", Err: err}
	}
	// Persist the id before the install: a certificate must never exist at
	// Cloudflare without custody that can revoke it.
	if err := service.Track(ctx, tenantID, stackID, certificate); err != nil {
		return nil, &OriginCertificateError{Stage: "custody", Err: err}
	}
	installed, _, err := d.dispatch(ctx, jobID+"-oca-install", stackKitStepOriginCertificateInstall, capability, func(command *agentpb.StackKitCommand) {
		command.OriginCertificatePem = []byte(certificate.PEM)
	})
	if err != nil {
		return nil, err
	}
	evidence := installed.OriginInstall
	if evidence == nil || !originca.SameHosts(evidence.Hosts, hosts) || !evidence.NotAfter.Equal(certificate.ExpiresOn) {
		return nil, &OriginCertificateError{Stage: "install", Err: fmt.Errorf("the node did not confirm the issued certificate")}
	}
	summary := map[string]interface{}{
		resultStatusField: "installed", "certificate_id": certificate.ID, "hosts": hosts,
		"not_after": evidence.NotAfter.Format(time.RFC3339), "certificate_sha256": evidence.CertificateSHA256,
		"capability_id": capability.ID,
	}
	// The old certificate is revoked only now that the node confirmed the swap.
	// A revocation failure keeps its id in custody (retired) and is surfaced as a warning,
	// never swallowed; the install itself stands.
	if err := service.CompleteInstall(ctx, tenantID, stackID, certificate.ID, evidence.CertificateSHA256, time.Now().UTC()); err != nil {
		summary["post_install_warning"] = err.Error()
	}
	return summary, nil
}

// reissueOriginCertificateForRoutes keeps the certificate in step with the
// registered route set of a change set. Only a reconciliation that registered
// hosted routes (status registered) has a managed certificate to maintain; the
// rotation is a no-op while the installed certificate already covers exactly
// these hostnames and is within two thirds of its lifetime.
func reissueOriginCertificateForRoutes(ctx context.Context, service *originca.Service, d *advancedDispatcher, jobID string, reconciliation map[string]interface{}) (map[string]interface{}, error) {
	if status, _ := reconciliation[resultStatusField].(string); status != "registered" {
		return nil, nil
	}
	routeHosts, _ := reconciliation["hosts"].([]string)
	origin, _ := reconciliation["target_origin"].(string)
	publicIP := strings.TrimSuffix(strings.TrimPrefix(origin, "https://"), ":443")
	hosts, err := managedOriginCertificateHosts(routeHosts, publicIP)
	if err != nil {
		return nil, &OriginCertificateError{Stage: "hostnames", Err: err}
	}
	return rotateManagedOriginCertificate(ctx, d, service, jobID, hosts, false)
}

// repairPublicTLS installs the origin certificate a degraded rollout could not
// install. The router serves it as soon as it lands (Traefik file provider,
// watch=true). Its result replaces the stack's degraded components: empty on
// success, the still-degraded component with the failing stage otherwise.
func repairPublicTLS(ctx context.Context, cfg StackKitLifecycleConfig, d *advancedDispatcher, jobID string, request StackKitLifecycleRequest) (map[string]interface{}, error) {
	summary := map[string]interface{}{
		"schema_version": "techstack.stackkit-lifecycle-result/v1", "techstack_id": request.StackID,
		advancedOperationField: request.Operation, "agent_id": request.AgentID, StackKitModeField: StackKitModeAdvanced,
	}
	certificate, err := rotateManagedOriginCertificate(ctx, d, cfg.OriginCertificates, jobID, request.OriginHosts, true)
	if err != nil {
		repairStatus := publicTLSRepairScheduled
		var unavailable *AdvancedOperationUnavailableError
		if errors.As(err, &unavailable) {
			repairStatus = publicTLSRepairFailed
		}
		summary["success"] = false
		summary[DegradedComponentsResultField] = []interface{}{publicTLSDegradedComponent(err, request.OriginHosts, repairStatus)}
		return summary, err
	}
	summary["success"] = true
	summary[OriginCertificateResultField] = certificate
	summary[DegradedComponentsResultField] = []interface{}{}
	return summary, nil
}

// stringListFromInterface reads a string list from a job map that may have
// been decoded from JSON.
func stringListFromInterface(value interface{}) []string {
	var out []string
	switch values := value.(type) {
	case []string:
		out = append(out, values...)
	case []interface{}:
		for _, item := range values {
			if text := strings.TrimSpace(stringFromInterface(item)); text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

// stackKitPublicTLSModuleRef is the StackKits module that owns public TLS.
const stackKitPublicTLSModuleRef = "stackkits-public-tls-contract"

// appendApplyDegradedComponents adds the units a completed_degraded StackKits
// Apply names (data.outcomes.units) to the rollout's degraded components. The
// public TLS unit waiting for its origin certificate becomes public_tls with
// the certificate hostnames, so the automatic repair can install it; other
// degraded units are listed by their module or workload with their class.
func appendApplyDegradedComponents(components []interface{}, applyResult map[string]interface{}, hosts []string) []interface{} {
	commandResult, _ := applyResult["command_result"].(map[string]interface{})
	data, _ := commandResult["data"].(map[string]interface{})
	outcomes, _ := data["outcomes"].(map[string]interface{})
	units, _ := outcomes["units"].([]interface{})
	hasPublicTLS := false
	for _, raw := range components {
		component, _ := raw.(map[string]interface{})
		hasPublicTLS = hasPublicTLS || stringFromInterface(component["component"]) == degradedComponentPublicTLS
	}
	for _, raw := range units {
		unit, _ := raw.(map[string]interface{})
		if stringFromInterface(unit["outcome"]) != "degraded" && stringFromInterface(unit["outcome"]) != "failed" {
			continue
		}
		subject, _ := unit["subject"].(map[string]interface{})
		failure, _ := unit["failure"].(map[string]interface{})
		class := stringFromInterface(failure["class"])
		retryable, _ := failure["retryable"].(bool)
		if stringFromInterface(subject["moduleRef"]) == stackKitPublicTLSModuleRef && class == "origin_certificate_missing" {
			if !hasPublicTLS && len(hosts) > 0 {
				component := publicTLSDegradedComponent(nil, hosts, publicTLSRepairScheduled)
				component["reason_code"], component["stage"] = class, "install"
				components = append(components, component)
				hasPublicTLS = true
			}
			continue
		}
		name := firstNonEmpty(stringFromInterface(subject["workloadRef"]), stringFromInterface(subject["moduleRef"]), stringFromInterface(subject["requirementId"]))
		if name == "" {
			continue
		}
		components = append(components, map[string]interface{}{
			"component": name, resultStatusField: "degraded", "reason_code": firstNonEmpty(class, "unit_incomplete"),
			"retryable": retryable,
		})
	}
	return components
}
