package routes

import (
	"strings"

	"github.com/kombifyio/techstack/pkg/appcatalog"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

const (
	placementClassLocal   = "local"
	placementClassCloud   = "cloud"
	placementClassManaged = "managed"
	placementClassUnknown = "unknown"
)

func serviceCatalogEntry(service controlplane.ServiceRuntime) *appcatalog.Entry {
	if entry := appcatalog.Match(serviceSubject(service)); entry != nil {
		return entry
	}
	return appcatalog.GenericEntry(service.Name)
}

// serviceSubject is what a persisted service row says about itself, in the
// shape the application catalog classifies. It reads only measured metadata.
func serviceSubject(service controlplane.ServiceRuntime) appcatalog.Subject {
	identity := stringMapFromAny(service.Metadata["runtime_identity"])
	subject := appcatalog.Subject{
		Name:           service.Name,
		Image:          stringFromAnyMap(service.Metadata, "image"),
		Platform:       stringFromAnyMap(service.Metadata, "platform_type"),
		ComposeService: identity["service"],
	}
	switch identity["kind"] {
	case "docker_compose_service":
		subject.ComposeProject = identity["project"]
	case "systemd_unit":
		subject.Platform = "systemd"
		subject.Name = firstNonEmptyString(identity["unit"], service.Name)
	}
	return subject
}

// resolveServiceApplication decides which application a service row belongs
// to, so rows persisted before recognition existed are reclassified on read.
//
//  1. an explicit application_key other than `system` wins;
//  2. a compose project wins, unless it is a StackKit platform project;
//  3. a catalog match becomes that catalog application;
//  4. any remaining observed docker container is its own application;
//  5. remaining systemd units and infrastructure stay in `system`.
//
// An agent also stamps `system` into rows it could not place, so for an
// observed row that explicit key is unresolved, not a decision. A key or name
// that is a StackKit platform project (stackkit-cloud-core) was stamped by an
// agent that grouped by folder; it names the whole kit, so it is unresolved
// too and every row of that kit falls back to its own application.
func resolveServiceApplication(service controlplane.ServiceRuntime) appcatalog.Grouping {
	subject := serviceSubject(service)
	observed := serviceregistry.CanonicalManagementState(service.ManagementState) == serviceregistry.ManagementObserved
	explicit := strings.ToLower(strings.TrimSpace(stringFromAnyMap(service.Metadata, "application_key")))
	declared := stringFromAnyMap(service.Metadata, "application_display_name")
	if appcatalog.IsPlatformProject(explicit) {
		explicit = ""
	}
	if appcatalog.IsPlatformProject(declared) {
		declared = ""
	}

	if explicit != "" && !(explicit == appcatalog.SystemKey && observed) {
		if explicit == appcatalog.SystemKey {
			return appcatalog.Grouping{Key: explicit, DisplayName: appcatalog.SystemDisplayName, Entry: appcatalog.Match(subject)}
		}
		grouping := appcatalog.Grouping{Key: explicit, Entry: appcatalog.Match(subject)}
		if grouping.Entry != nil && grouping.Entry.ID == explicit {
			grouping.DisplayName = firstNonEmptyString(declared, grouping.Entry.DisplayName)
		}
		grouping.DisplayName = firstNonEmptyString(grouping.DisplayName, declared, service.Name, explicit)
		return grouping
	}
	if observed || appcatalog.IsPlatformProject(subject.ComposeProject) {
		return appcatalog.Group(subject)
	}
	if subject.ComposeProject != "" {
		return appcatalog.Grouping{Key: strings.ToLower(subject.ComposeProject), DisplayName: subject.ComposeProject, Entry: appcatalog.Match(subject)}
	}
	key := strings.ToLower(strings.TrimSpace(service.ServiceKey))
	return appcatalog.Grouping{Key: key, DisplayName: firstNonEmptyString(declared, service.Name, key), Entry: appcatalog.Match(subject)}
}

func applicationKeyForService(service controlplane.ServiceRuntime) string {
	return resolveServiceApplication(service).Key
}

func applicationDisplayName(service controlplane.ServiceRuntime, key string) string {
	// The System bucket never inherits a member's name: "hermes-webui" as the
	// title of a bucket holding cups and symphony units misled the operator.
	if key == appcatalog.SystemKey {
		return appcatalog.SystemDisplayName
	}
	return resolveServiceApplication(service).DisplayName
}

// servicePlacementClass is the evidence-backed Local/Cloud/Managed class of a
// service. It is taken from the server's recorded runtime target, never from
// names or heuristics: a paired self-owned device is `local`, every VPS or VM
// is `cloud` (a Kombify-provisioned managed VPS included: it is still a server
// the homelab runs on), only a provider-native single-container workload is
// `managed`, and anything without recorded evidence stays `unknown`.
func servicePlacementClass(placement serviceregistry.Placement, server *controlplane.ServerRuntime) string {
	switch placement.TargetKind {
	case serviceregistry.TargetKindManagedWorkload:
		return placementClassManaged
	case serviceregistry.TargetKindServer:
		if server == nil {
			return placementClassUnknown
		}
		target := servedRuntimeTarget(*server)
		switch target.EnvironmentClass {
		case serverregistry.EnvironmentLocal:
			return placementClassLocal
		case serverregistry.EnvironmentCloud:
			return placementClassCloud
		}
	}
	return placementClassUnknown
}
