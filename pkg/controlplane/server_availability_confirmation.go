package controlplane

import (
	"context"
	"strings"
	"time"
)

// ServerAvailabilityConfirmationKey is the servers.metadata_json member that
// holds the owner's confirmation of how available the machine is. It is the
// owner's word and sits next to, never inside, what the Guard observed.
const ServerAvailabilityConfirmationKey = "availability_confirmation"

// ServerAvailabilityConfirmation is the owner's statement about one server:
// "this machine is on all the time" or "this machine is switched off at
// times". Class is "always-on" or "part-time".
type ServerAvailabilityConfirmation struct {
	Class       string
	ConfirmedAt time.Time
}

// ServerAvailabilityConfirmer writes or clears the owner's confirmation. The
// write is bound to tenant, server and owner in the store; a server outside
// that triple is ErrNotFound so a caller cannot probe for existence. An empty
// class clears the confirmation.
//
// It merges one key into servers.metadata_json inside the database instead of
// going through the aggregate command boundary: the aggregate's own writes
// read the row under FOR UPDATE and merge metadata, so the two cannot lose
// each other's update, and a confirmation is not a state transition.
type ServerAvailabilityConfirmer interface {
	SetServerAvailabilityConfirmation(ctx context.Context, tenantID, ownerSubjectID, serverID, class string, confirmedAt time.Time) (*ServerRuntime, error)
}

// ServerAvailabilityConfirmationOf reads the owner's confirmation from a
// server's metadata. It reports false for an absent or malformed value, so a
// hand-edited row never reads as a confirmation.
func ServerAvailabilityConfirmationOf(server ServerRuntime) (ServerAvailabilityConfirmation, bool) {
	raw, ok := server.Metadata[ServerAvailabilityConfirmationKey].(map[string]any)
	if !ok {
		return ServerAvailabilityConfirmation{}, false
	}
	class, _ := raw["class"].(string)
	class = strings.TrimSpace(class)
	if class != "always-on" && class != "part-time" {
		return ServerAvailabilityConfirmation{}, false
	}
	confirmation := ServerAvailabilityConfirmation{Class: class}
	if at, _ := raw["confirmed_at"].(string); at != "" {
		if parsed, err := time.Parse(time.RFC3339, at); err == nil {
			confirmation.ConfirmedAt = parsed.UTC()
		}
	}
	return confirmation, true
}

// SetServerAvailabilityConfirmation implements ServerAvailabilityConfirmer.
func (s *PostgresStore) SetServerAvailabilityConfirmation(ctx context.Context, tenantID, ownerSubjectID, serverID, class string, confirmedAt time.Time) (*ServerRuntime, error) {
	ownerSubjectID, serverID, class = strings.TrimSpace(ownerSubjectID), strings.TrimSpace(serverID), strings.TrimSpace(class)
	if ownerSubjectID == "" || serverID == "" {
		return nil, ErrNotFound
	}
	return queryOneTenantRow(ctx, s, tenantID, `
		UPDATE servers SET
			metadata_json = CASE
				WHEN $4::text = '' THEN COALESCE(metadata_json, '{}'::jsonb) - '`+ServerAvailabilityConfirmationKey+`'
				ELSE COALESCE(metadata_json, '{}'::jsonb) || jsonb_build_object('`+ServerAvailabilityConfirmationKey+`',
					jsonb_build_object('class', $4::text, 'confirmed_at', $5::text))
			END,
			updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND owner_subject_id = $3
		RETURNING `+serverRuntimeColumns, scanServerRuntime, serverID, ownerSubjectID, class, confirmedAt.UTC().Format(time.RFC3339))
}

// SetServerAvailabilityConfirmation implements ServerAvailabilityConfirmer for
// the memory store.
func (s *MemoryStore) SetServerAvailabilityConfirmation(_ context.Context, tenantID, ownerSubjectID, serverID, class string, confirmedAt time.Time) (*ServerRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	server, ok := s.servers[strings.TrimSpace(serverID)]
	if !ok || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(ownerSubjectID) == "" ||
		server.TenantID != strings.TrimSpace(tenantID) || server.OwnerSubjectID != strings.TrimSpace(ownerSubjectID) {
		return nil, ErrNotFound
	}
	metadata := cloneMap(server.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	if class = strings.TrimSpace(class); class == "" {
		delete(metadata, ServerAvailabilityConfirmationKey)
	} else {
		metadata[ServerAvailabilityConfirmationKey] = map[string]any{
			"class": class, "confirmed_at": confirmedAt.UTC().Format(time.RFC3339),
		}
	}
	server.Metadata = metadata
	server.UpdatedAt = s.now()
	s.servers[server.ID] = server
	return cloneServerRuntime(server), nil
}

var (
	_ ServerAvailabilityConfirmer = (*PostgresStore)(nil)
	_ ServerAvailabilityConfirmer = (*MemoryStore)(nil)
)
