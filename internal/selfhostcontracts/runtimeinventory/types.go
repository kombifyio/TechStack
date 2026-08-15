// Package runtimeinventory defines the secret-free self-host runtime read
// model that is shipped with the Open-Core tree.
package runtimeinventory

import "time"

// Freshness qualifies an observation using the owning authority's freshness
// policy.
type Freshness struct {
	State             string `json:"state"`
	AgeSeconds        *int64 `json:"age_seconds,omitempty"`
	StaleAfterSeconds int64  `json:"stale_after_seconds"`
	ReasonCode        string `json:"reason_code,omitempty"`
}

// Addresses contains sanitized network addresses and DNS names.
type Addresses struct {
	PublicIPs  []string `json:"public_ips"`
	PrivateIPs []string `json:"private_ips"`
	LocalIPs   []string `json:"local_ips"`
	Domains    []string `json:"domains"`
}

// ObservedState is a freshness-qualified runtime observation.
type ObservedState struct {
	State      string     `json:"state"`
	ReasonCode string     `json:"reason_code,omitempty"`
	ChangedAt  *time.Time `json:"changed_at,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

// LifecycleProjection is the control-plane lifecycle read projection.
type LifecycleProjection struct {
	State      string     `json:"state"`
	ReasonCode string     `json:"reason_code,omitempty"`
	ChangedAt  *time.Time `json:"changed_at,omitempty"`
}

// DesiredProjection is the user-owned desired-state read projection.
type DesiredProjection struct {
	State      string     `json:"state"`
	ReasonCode string     `json:"reason_code,omitempty"`
	ChangedAt  *time.Time `json:"changed_at,omitempty"`
}

// LeaseProjection contains only an opaque runtime-lease identity and state.
type LeaseProjection struct {
	ID    string `json:"id,omitempty"`
	State string `json:"state"`
}

// CleanupProjection reports evidence-qualified provider cleanup state.
type CleanupProjection struct {
	State                   string     `json:"state"`
	ProviderAbsenceVerified bool       `json:"provider_absence_verified"`
	VerifiedAt              *time.Time `json:"verified_at,omitempty"`
}

// StackKit identifies the observed StackKit without configuration or secrets.
type StackKit struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Variant string `json:"variant,omitempty"`
}

// Platform contains sanitized operating-system and architecture labels.
type Platform struct {
	OS        string `json:"os,omitempty"`
	OSVersion string `json:"os_version,omitempty"`
	Arch      string `json:"arch,omitempty"`
}

// Server is the canonical secret-free runtime-server read model.
type Server struct {
	ID                string              `json:"id"`
	StackID           string              `json:"stack_id,omitempty"`
	Name              string              `json:"name"`
	ObservedAt        *time.Time          `json:"observed_at"`
	Freshness         Freshness           `json:"freshness"`
	InventoryRevision int64               `json:"inventory_revision"`
	Addresses         Addresses           `json:"addresses"`
	Platform          Platform            `json:"platform"`
	StackKit          StackKit            `json:"stackkit"`
	Provider          string              `json:"provider,omitempty"`
	Lifecycle         LifecycleProjection `json:"lifecycle"`
	Desired           DesiredProjection   `json:"desired"`
	Lease             LeaseProjection     `json:"lease"`
	Cleanup           CleanupProjection   `json:"cleanup"`
	Connection        ObservedState       `json:"connection"`
	Health            ObservedState       `json:"health"`
}

// ServerList is one bounded server-collection page.
type ServerList struct {
	ObservedAt        time.Time `json:"observed_at"`
	Freshness         Freshness `json:"freshness"`
	InventoryRevision int64     `json:"inventory_revision"`
	CollectionCursor  string    `json:"collection_cursor"`
	NextCursor        string    `json:"next_cursor,omitempty"`
	PageSize          int       `json:"page_size"`
	Servers           []Server  `json:"servers"`
}

// ServerHealth is the compact health projection for one server.
type ServerHealth struct {
	ServerID          string        `json:"server_id"`
	ObservedAt        *time.Time    `json:"observed_at"`
	Freshness         Freshness     `json:"freshness"`
	InventoryRevision int64         `json:"inventory_revision"`
	Connection        ObservedState `json:"connection"`
	Health            ObservedState `json:"health"`
}

// ServiceLink is a sanitized HTTP(S) access link without query or fragment
// secrets.
type ServiceLink struct {
	URL  string `json:"url"`
	Mode string `json:"mode"`
}

// Service is the canonical secret-free runtime-service read model.
type Service struct {
	ID                string        `json:"id"`
	StackID           string        `json:"stack_id"`
	ServerID          string        `json:"server_id"`
	Key               string        `json:"key"`
	Name              string        `json:"name"`
	DesiredState      string        `json:"desired_state"`
	ObservedState     string        `json:"observed_state"`
	ObservedAt        *time.Time    `json:"observed_at"`
	Freshness         Freshness     `json:"freshness"`
	InventoryRevision int64         `json:"inventory_revision"`
	Health            ObservedState `json:"health"`
	StackKit          StackKit      `json:"stackkit"`
	Links             []ServiceLink `json:"links"`
	AllowedActions    []string      `json:"allowed_actions"`
	EvidenceRef       string        `json:"evidence_ref,omitempty"`
	Source            string        `json:"source"`
}

// ServiceList is one bounded service-collection page.
type ServiceList struct {
	ObservedAt        time.Time `json:"observed_at"`
	Freshness         Freshness `json:"freshness"`
	InventoryRevision int64     `json:"inventory_revision"`
	CollectionCursor  string    `json:"collection_cursor"`
	NextCursor        string    `json:"next_cursor,omitempty"`
	PageSize          int       `json:"page_size"`
	Services          []Service `json:"services"`
}

// Channel is sanitized connection-channel state without an endpoint reference.
type Channel struct {
	Type       string     `json:"type"`
	Role       string     `json:"role"`
	State      string     `json:"state"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

// AccessServiceLink is a reachable service link in a server access context.
type AccessServiceLink struct {
	ServiceID   string `json:"service_id"`
	ServiceName string `json:"service_name"`
	URL         string `json:"url"`
	Mode        string `json:"mode"`
	Health      string `json:"health"`
}

// Availability reports whether a sanitized access context is currently usable.
type Availability struct {
	State      string `json:"state"`
	ReasonCode string `json:"reason_code,omitempty"`
}

// ServerAccessContext contains sanitized connection and service-link data for
// an authorized operator.
type ServerAccessContext struct {
	ServerID          string              `json:"server_id"`
	ObservedAt        *time.Time          `json:"observed_at"`
	Freshness         Freshness           `json:"freshness"`
	InventoryRevision int64               `json:"inventory_revision"`
	Availability      Availability        `json:"availability"`
	Connection        ObservedState       `json:"connection"`
	Addresses         Addresses           `json:"addresses"`
	Channels          []Channel           `json:"channels"`
	ServiceLinks      []AccessServiceLink `json:"service_links"`
}
