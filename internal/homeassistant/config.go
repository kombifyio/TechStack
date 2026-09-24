package homeassistant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/kombifyio/techstack/internal/substrate"
	"github.com/kombifyio/techstack/internal/substrateprovision"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
	"io"
	"os"
	"strings"
)

type LocalAPIConfig struct {
	ViaCore   bool   `json:"via_core"`
	Endpoint  string `json:"endpoint"`
	TokenFile string `json:"token_file"`
}
type LocalMigrationConfig struct {
	SourceDocker            *DockerSourceGrant                `json:"source_docker,omitempty"`
	StackID                 string                            `json:"stack_id"`
	NodeID                  string                            `json:"node_id"`
	Placement               substrateprovision.Placement      `json:"placement"`
	TenantID                string                            `json:"tenant_id"`
	OwnerID                 string                            `json:"owner_id"`
	SourceServiceID         string                            `json:"source_service_id"`
	TargetBindingRef        string                            `json:"target_binding_ref"`
	AuthorizationRef        string                            `json:"authorization_ref"`
	DependencyAssessmentRef string                            `json:"dependency_assessment_ref"`
	BackupStackID           string                            `json:"backup_stack_id"`
	SourceMethod            string                            `json:"source_method"`
	SourceBackupAgent       string                            `json:"source_backup_agent"`
	SourceCore              LocalAPIConfig                    `json:"source_core"`
	TargetCore              LocalAPIConfig                    `json:"target_core"`
	SourceSupervisor        LocalAPIConfig                    `json:"source_supervisor"`
	TargetSupervisor        LocalAPIConfig                    `json:"target_supervisor"`
	SourceProxmox           substrate.Config                  `json:"source_proxmox"`
	TargetProxmox           substrate.Config                  `json:"target_proxmox"`
	SourcePowerGrant        substrate.ObservedGuestGrant      `json:"source_power_grant"`
	TargetSpec              substrate.GuestSpec               `json:"target_spec"`
	Management              substrate.ManagementIsolation     `json:"management"`
	Production              substrate.ProductionNetworkPolicy `json:"production_network"`
	USB                     *substrate.USBTransferGrant       `json:"usb_transfer,omitempty"`
}

// LoadLocalBindings accepts a private local configuration, never a remote JSON
// request. Presence is the explicit admin grant; every native API still verifies
// credentials and exact instance identity at execution time.
func LoadLocalBindings(ctx context.Context, db *sql.DB, path string) (map[string]*MigrationBinding, map[string]string, error) {
	bindings := map[string]*MigrationBinding{}
	tenants := map[string]string{}
	if path == "" {
		return bindings, tenants, nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1024*1024 {
		return nil, nil, errors.New("private regular Home Assistant binding file required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var configs []LocalMigrationConfig
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&configs); err != nil {
		return nil, nil, errors.New("invalid local Home Assistant binding file")
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return nil, nil, errors.New("trailing local Home Assistant binding configuration")
	}
	custody, err := backupstore.NewPostgresCustodyStore(db, auth.GetEncryptor())
	if err != nil {
		return nil, nil, err
	}
	for _, cfg := range configs {
		if cfg.TenantID == "" || cfg.OwnerID == "" || cfg.SourceServiceID == "" || cfg.TargetBindingRef == "" || cfg.AuthorizationRef == "" || cfg.DependencyAssessmentRef == "" || bindings[cfg.TargetBindingRef] != nil {
			return nil, nil, errors.New("incomplete or duplicate local migration identity")
		}
		sourceCore, err := localCore(ctx, cfg.SourceCore)
		if err != nil {
			return nil, nil, err
		}
		targetCore, err := localCore(ctx, cfg.TargetCore)
		if err != nil {
			return nil, nil, err
		}
		if targetCore.Endpoint() == sourceCore.Endpoint() {
			return nil, nil, errors.New("distinct source and target API endpoints required")
		}
		targetSupervisor, err := localSupervisor(ctx, cfg.TargetSupervisor, Grants{Backup: true, Restore: true})
		if err != nil {
			return nil, nil, err
		}
		var sourceBackup NativeBackup
		if cfg.SourceMethod == "container" {
			sourceBackup = &CoreBackup{Client: sourceCore, AgentID: cfg.SourceBackupAgent, BackupGranted: true}
		} else if cfg.SourceMethod == "haos" {
			sourceBackup, err = localSupervisor(ctx, cfg.SourceSupervisor, Grants{Backup: true})
			if err != nil {
				return nil, nil, err
			}
		} else {
			return nil, nil, errors.New("explicit source installation method required")
		}
		var sourcePVE *substrate.Client
		var sourceLifecycle SourceLifecycle
		if cfg.SourceDocker != nil {
			if cfg.SourceMethod != "container" || cfg.USB != nil || cfg.SourceProxmox.Endpoint != "" {
				return nil, nil, ErrUnauthorized
			}
			sourceLifecycle, err = NewDockerSource(*cfg.SourceDocker)
		} else {
			sourcePVE, err = substrate.NewClient(cfg.SourceProxmox)
		}
		if err != nil {
			return nil, nil, err
		}
		targetPVE, err := substrate.NewClient(cfg.TargetProxmox)
		if err != nil {
			return nil, nil, err
		}
		credentials, err := custody.Get(ctx, cfg.TenantID, cfg.BackupStackID)
		if err != nil {
			return nil, nil, err
		}
		archives, err := backupstore.NewHomeAssistantArchive(ctx, credentials)
		if err != nil {
			return nil, nil, err
		}
		bindings[cfg.TargetBindingRef] = &MigrationBinding{OwnerID: cfg.OwnerID, SourceServiceID: cfg.SourceServiceID, TargetBindingRef: cfg.TargetBindingRef, AuthorizationRef: cfg.AuthorizationRef, DependencyAssessmentRef: cfg.DependencyAssessmentRef, SourceCore: sourceCore, TargetCore: targetCore, SourceBackup: sourceBackup, TargetSupervisor: targetSupervisor, Custody: &Journal{DB: db, Encryptor: auth.GetEncryptor(), TenantID: cfg.TenantID}, Archives: archives, Runtime: &ProxmoxMigrationRuntime{Source: sourcePVE, Target: targetPVE, SourceGrant: cfg.SourcePowerGrant, TargetSpec: cfg.TargetSpec, Management: cfg.Management, Production: cfg.Production, USB: cfg.USB}}
		bindings[cfg.TargetBindingRef].ProvisionRequest = substrateprovision.Request{TenantID: cfg.TenantID, OwnerID: cfg.OwnerID, StackID: cfg.StackID, NodeID: cfg.NodeID, IdempotencyKey: "ha-migration/" + cfg.TargetBindingRef, Placement: cfg.Placement}
		bindings[cfg.TargetBindingRef].Runtime.(*ProxmoxMigrationRuntime).SourceLifecycle = sourceLifecycle
		tenants[cfg.TargetBindingRef] = cfg.TenantID
	}
	return bindings, tenants, nil
}
func localToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return "", errors.New("private regular Home Assistant token file required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}
func localCore(ctx context.Context, cfg LocalAPIConfig) (*Client, error) {
	token, err := localToken(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	return NewLocalClient(ctx, cfg.Endpoint, token)
}
func localSupervisor(ctx context.Context, cfg LocalAPIConfig, grants Grants) (*Supervisor, error) {
	token, err := localToken(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	if cfg.ViaCore {
		return NewSupervisorViaCore(ctx, cfg.Endpoint, token, grants)
	}
	return NewLocalSupervisor(ctx, cfg.Endpoint, token, grants)
}

func RegisterBindings(engine *workflow.Engine, bindings map[string]*MigrationBinding) error {
	if len(bindings) == 0 {
		return nil
	}
	all := map[string]workflow.ActivityFunc{}
	for _, binding := range bindings {
		for name := range binding.Activities() {
			activityName := name
			all[name] = func(ctx context.Context, input map[string]any) (map[string]any, error) {
				req, _ := input["request"].(map[string]any)
				bound := bindings[str(req, "target_binding_ref")]
				if bound == nil {
					return nil, errors.New("Home Assistant exact runtime binding missing")
				}
				return bound.Activities()[activityName](ctx, input)
			}
		}
		break
	}
	return workflows.RegisterHomeAssistantWorkflows(engine, all)
}
