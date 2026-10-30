package main

import (
	_ "embed"
	"encoding/json"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/db"
)

// backup_owner.json declares which process owns scheduled backups in hosted
// Stable (platform-77da5). "in-process" keeps the due-stack scanner and backup
// jobs in the HTTP process, the behaviour before the private worker split,
// until a governed Stable worker target exists; "private-worker" hands both to
// the backup-worker process. Preview and Early Access always leave backups to
// the private worker. The release runner reads the same file from the release
// source, and /api/v1/health reports the owner this process applies.
//
//go:embed backup_owner.json
var backupOwnerDeclaration []byte

const (
	backupOwnerInProcess     = "in-process"
	backupOwnerPrivateWorker = "private-worker"
	backupOwnerNative        = "native-combined"
)

// declaredStableBackupOwner fails closed to the private worker when the
// declaration is missing, malformed or names an unknown owner. Keys match
// exactly, as in the release runner that reads the same file.
func declaredStableBackupOwner() string {
	var declaration map[string]any
	if json.Unmarshal(backupOwnerDeclaration, &declaration) != nil ||
		declaration["schema"] != "techstack.backup-owner/v1" || declaration["stable"] != backupOwnerInProcess {
		return backupOwnerPrivateWorker
	}
	return backupOwnerInProcess
}

// scheduledBackupOwner names the process that scans and runs scheduled
// backups for this runtime: native installations keep their combined
// lifecycle; hosted secondary channels and any hosted process pinned to the
// Stage database never own them.
func scheduledBackupOwner(cfg *config.Config) string {
	if !hostedRuntime(cfg) {
		return backupOwnerNative
	}
	if config.SecondaryChannel() || db.DeclaresStagingDatabaseBinding() {
		return backupOwnerPrivateWorker
	}
	return declaredStableBackupOwner()
}
