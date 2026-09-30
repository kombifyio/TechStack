package controlplane

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxOwnerChosenNameLength bounds owner-chosen server and homelab names in
// runes; migration 123 enforces the same bound for servers.display_name.
const MaxOwnerChosenNameLength = 100

// ErrInvalidOwnerChosenName rejects an empty, overlong, or control-character
// name before any store write.
var ErrInvalidOwnerChosenName = errors.New("controlplane: invalid owner-chosen name")

// NormalizeOwnerChosenName trims a name and enforces 1..100 runes without
// control characters.
func NormalizeOwnerChosenName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxOwnerChosenNameLength {
		return "", ErrInvalidOwnerChosenName
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrInvalidOwnerChosenName
		}
	}
	return name, nil
}

// MaxStackIdentityNameLength is the StackIdentityV1 name bound. The homelab
// name is the Stack Identity name, so a longer one could never reach kombify
// Cloud.
const MaxStackIdentityNameLength = 30

// NormalizeStackIdentityName applies the owner-chosen name rules with the
// Stack Identity bound.
func NormalizeStackIdentityName(raw string) (string, error) {
	name, err := NormalizeOwnerChosenName(raw)
	if err != nil || utf8.RuneCountInString(name) > MaxStackIdentityNameLength {
		return "", ErrInvalidOwnerChosenName
	}
	return name, nil
}

// InventoryServerRenamer writes the owner-chosen display name of one server.
// The write is bound to tenant, server and owner in SQL; a server outside that
// triple is ErrNotFound so callers cannot probe for existence. An empty
// displayName clears the rename.
type InventoryServerRenamer interface {
	RenameInventoryServer(ctx context.Context, tenantID, ownerSubjectID, serverID, displayName string) (*ServerRuntime, error)
}

// RenameInventoryServer implements InventoryServerRenamer. It is the only SQL
// path that writes servers.display_name.
func (s *PostgresStore) RenameInventoryServer(ctx context.Context, tenantID, ownerSubjectID, serverID, displayName string) (*ServerRuntime, error) {
	ownerSubjectID, serverID = strings.TrimSpace(ownerSubjectID), strings.TrimSpace(serverID)
	if ownerSubjectID == "" || serverID == "" {
		return nil, ErrNotFound
	}
	return queryOneTenantRow(ctx, s, tenantID, `
		UPDATE servers SET display_name = NULLIF($4, ''), updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND owner_subject_id = $3
		RETURNING `+serverRuntimeColumns, scanServerRuntime, serverID, ownerSubjectID, strings.TrimSpace(displayName))
}

// RenameInventoryServer implements InventoryServerRenamer for the memory store.
func (s *MemoryStore) RenameInventoryServer(_ context.Context, tenantID, ownerSubjectID, serverID, displayName string) (*ServerRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	server, ok := s.servers[strings.TrimSpace(serverID)]
	if !ok || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(ownerSubjectID) == "" ||
		server.TenantID != strings.TrimSpace(tenantID) || server.OwnerSubjectID != strings.TrimSpace(ownerSubjectID) {
		return nil, ErrNotFound
	}
	server.DisplayName = strings.TrimSpace(displayName)
	server.UpdatedAt = s.now()
	s.servers[server.ID] = server
	return cloneServerRuntime(server), nil
}

// RenameOwnedHomelab renames the owner's own homelab. Resolving by owner is the
// authorization: a caller can only ever rename their own homelab, never one
// addressed by id. Both the REST route and the MCP tool use this path.
func RenameOwnedHomelab(ctx context.Context, store HomelabStore, tenantID, ownerSubjectID, rawName string) (*Homelab, error) {
	name, err := NormalizeStackIdentityName(rawName)
	if err != nil {
		return nil, err
	}
	homelab, err := store.GetHomelabByOwner(ctx, tenantID, ownerSubjectID)
	if err != nil {
		return nil, err
	}
	return store.UpdateHomelabName(ctx, tenantID, homelab.ID, name)
}
