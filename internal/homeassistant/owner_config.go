package homeassistant

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/kombifyio/techstack/internal/gocommon/runtimeexecutor"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/backupstore"
)

type LocalOwnerConfig struct {
	RecoveryBackupOperationID string                         `json:"recovery_backup_operation_id"`
	OwnerID                   string                         `json:"owner_id"`
	BackupStackID             string                         `json:"backup_stack_id"`
	DependencyAssessmentRef   string                         `json:"dependency_assessment_ref"`
	LifecycleGrants           Grants                         `json:"lifecycle_grants"`
	RecoveryBindingRef        string                         `json:"recovery_binding_ref"`
	TenantID                  string                         `json:"tenant_id"`
	StackID                   string                         `json:"stack_id"`
	RuntimeAgentID            string                         `json:"runtime_agent_id"`
	BindingRef                string                         `json:"binding_ref"`
	Origin                    string                         `json:"origin"`
	ManagementScope           string                         `json:"management_scope"`
	Target                    runtimeexecutor.RuntimeTarget  `json:"target"`
	Health                    []runtimeexecutor.HealthTarget `json:"health"`
	Core                      LocalAPIConfig                 `json:"core"`
	Supervisor                LocalAPIConfig                 `json:"supervisor"`
	Settings                  BaselineSettings               `json:"settings"`
	ConfigureGranted          bool                           `json:"configure_granted"`
}

func LoadLocalOwnerBindings(ctx context.Context, db *sql.DB, path string) ([]OwnerBinding, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1024*1024 {
		return nil, errors.New("private regular Home Assistant owner file required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var configs []LocalOwnerConfig
	if err = decoder.Decode(&configs); err != nil {
		return nil, err
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return nil, errors.New("trailing owner configuration")
	}
	bindings := []OwnerBinding{}
	for _, cfg := range configs {
		if cfg.TenantID == "" || cfg.StackID == "" || cfg.RuntimeAgentID == "" || cfg.BindingRef == "" || cfg.Target.InstanceRef == "" || cfg.Target.ExecutionChannelRef == "" {
			return nil, errors.New("exact enrolled Home Assistant owner required")
		}
		if (cfg.Origin != "new" && cfg.Origin != "existing" && cfg.Origin != "imported") || (cfg.ManagementScope != "observed" && cfg.ManagementScope != "managed") || (cfg.Origin == "existing" && cfg.ManagementScope != "observed") {
			return nil, ErrUnauthorized
		}
		core, err := localCore(ctx, cfg.Core)
		if err != nil {
			return nil, err
		}
		instance := &InstanceBinding{RecoveryBackupOperationID: cfg.RecoveryBackupOperationID, BindingRef: cfg.BindingRef, Origin: cfg.Origin, ManagementScope: cfg.ManagementScope, Core: core, Settings: cfg.Settings, ConfigureGranted: cfg.ConfigureGranted}
		if cfg.Origin == "new" || cfg.ManagementScope == "managed" {
			if db == nil || auth.GetEncryptor() == nil {
				return nil, errors.New("durable baseline custody unavailable")
			}
			instance.Custody = &Journal{DB: db, Encryptor: auth.GetEncryptor(), TenantID: cfg.TenantID}
			instance.Supervisor, err = localSupervisor(ctx, cfg.Supervisor, cfg.LifecycleGrants)
			if err != nil {
				return nil, err
			}
			if cfg.LifecycleGrants.Backup || cfg.LifecycleGrants.Update || cfg.LifecycleGrants.Restore {
				if cfg.OwnerID == "" || cfg.BackupStackID != cfg.StackID || cfg.DependencyAssessmentRef == "" {
					return nil, ErrUnauthorized
				}
				store, err := backupstore.NewPostgresCustodyStore(db, auth.GetEncryptor())
				if err != nil {
					return nil, err
				}
				credentials, err := store.Get(ctx, cfg.TenantID, cfg.BackupStackID)
				if err != nil {
					return nil, err
				}
				instance.Archives, err = backupstore.NewHomeAssistantArchive(ctx, credentials)
				if err != nil {
					return nil, err
				}
				instance.DependencyAssessmentRef = cfg.DependencyAssessmentRef
			}
		}
		bindings = append(bindings, OwnerBinding{OwnerID: cfg.OwnerID, RecoveryBindingRef: cfg.RecoveryBindingRef, TenantID: cfg.TenantID, StackID: cfg.StackID, RuntimeAgentID: cfg.RuntimeAgentID, Target: cfg.Target, Health: cfg.Health, Instance: instance})
	}
	return bindings, nil
}
