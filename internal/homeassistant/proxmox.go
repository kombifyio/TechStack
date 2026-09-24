package homeassistant

import (
	"context"
	"errors"
	"strings"

	"github.com/kombifyio/techstack/internal/substrate"
)

// ProxmoxMigrationRuntime controls only the exact locally granted source and
// target. Imported source authority is power-only; this adapter has no delete.
type AdmittedHAOSGuest struct {
	LeaseID, OperationID string
	Spec                 substrate.GuestSpec
	Ready                bool
}
type AdmittedHAOSProvisioner interface {
	PrepareIsolatedGuest(context.Context) (AdmittedHAOSGuest, error)
}

type ProxmoxMigrationRuntime struct {
	SourceLifecycle SourceLifecycle
	Provisioner     AdmittedHAOSProvisioner
	Source, Target  *substrate.Client
	SourceGrant     substrate.ObservedGuestGrant
	TargetSpec      substrate.GuestSpec
	Management      substrate.ManagementIsolation
	Production      substrate.ProductionNetworkPolicy
	USB             *substrate.USBTransferGrant
}

func (p *ProxmoxMigrationRuntime) PrepareIsolated(ctx context.Context) (bool, error) {
	if err := p.verifyUSBSource(ctx); err != nil {
		return false, err
	}
	if (p.Source == nil && p.SourceLifecycle == nil) || p.Target == nil || p.Provisioner == nil {
		return false, errors.New("admitted isolated HAOS provisioner required")
	}
	admitted, err := p.Provisioner.PrepareIsolatedGuest(ctx)
	if err != nil {
		return false, err
	}
	if admitted.LeaseID == "" || admitted.OperationID == "" || admitted.Spec.ProfileID != "haos" || !admitted.Spec.Isolated || admitted.Spec.ID == p.SourceGrant.ID {
		return false, errors.New("exact admitted isolated HAOS guest required")
	}
	if (p.TargetSpec.ID != 0 && p.TargetSpec.ID != admitted.Spec.ID) || (p.TargetSpec.Name != "" && p.TargetSpec.Name != admitted.Spec.Name) {
		return false, ErrUnauthorized
	}
	p.TargetSpec = admitted.Spec
	if !admitted.Ready {
		return false, nil
	}
	guest, err := p.Target.Guest(ctx, p.TargetSpec.ID)
	if err != nil {
		return false, err
	}
	if guest.Status == "stopped" {
		if err := p.Target.ConfigureManagementIsolation(ctx, p.TargetSpec.GuestIdentity, p.Management); err != nil {
			return false, err
		}
	}
	if ok, err := p.VerifyIsolation(ctx); err != nil || !ok {
		return false, err
	}
	out, err := p.Target.SubmitLifecycle(ctx, p.TargetSpec.GuestIdentity, "start")
	if err != nil {
		return false, err
	}
	return p.targetState(ctx, "running", out)
}
func (p *ProxmoxMigrationRuntime) VerifyIsolation(ctx context.Context) (bool, error) {
	if err := p.verifyUSBSource(ctx); err != nil {
		return false, err
	}
	if _, err := p.Target.VerifyManagementIsolation(ctx, p.TargetSpec.GuestIdentity, p.Management); err != nil {
		return false, err
	}
	if err := p.noRadioAttachments(ctx); err != nil {
		return false, err
	}
	target, err := p.Target.Guest(ctx, p.TargetSpec.ID)
	if err != nil {
		return false, err
	}
	for key := range target.Config {
		if strings.HasPrefix(key, "usb") || strings.HasPrefix(key, "hostpci") {
			return false, errors.New("restore target has a physical device attachment")
		}
	}
	return true, nil
}
func (p *ProxmoxMigrationRuntime) StopSource(ctx context.Context) (bool, error) {
	if err := p.verifyUSBSource(ctx); err != nil {
		return false, err
	}
	if p.SourceLifecycle != nil {
		return p.SourceLifecycle.Stop(ctx)
	}
	grant, err := p.sourceGrant(ctx)
	if err != nil {
		return false, err
	}
	out, err := p.Source.SubmitObservedLifecycle(ctx, grant, "stop")
	if err != nil {
		return false, err
	}
	if out.TaskRef != "" {
		task, err := p.Source.ObserveTask(ctx, out.TaskRef)
		if err != nil {
			return false, err
		}
		if task.Complete() && !task.Succeeded() {
			return false, errors.New("source stop failed")
		}
	}
	return p.SourceStopped(ctx)
}
func (p *ProxmoxMigrationRuntime) StartSource(ctx context.Context) (bool, error) {
	if err := p.verifyUSBSource(ctx); err != nil {
		return false, err
	}
	if ok, err := p.TargetStopped(ctx); err != nil || !ok {
		return false, errors.New("target must be stopped before source start")
	}
	if p.SourceLifecycle != nil {
		return p.SourceLifecycle.Start(ctx)
	}
	grant, err := p.sourceGrant(ctx)
	if err != nil {
		return false, err
	}
	_, err = p.Source.SubmitObservedLifecycle(ctx, grant, "start")
	if err != nil {
		return false, err
	}
	g, err := p.Source.Guest(ctx, p.SourceGrant.ID)
	return err == nil && g.Status == "running", err
}
func (p *ProxmoxMigrationRuntime) StopTarget(ctx context.Context) (bool, error) {
	if err := p.verifyUSBSource(ctx); err != nil {
		return false, err
	}
	out, err := p.Target.SubmitLifecycle(ctx, p.TargetSpec.GuestIdentity, "stop")
	if err != nil {
		return false, err
	}
	return p.targetState(ctx, "stopped", out)
}
func (p *ProxmoxMigrationRuntime) StartTarget(ctx context.Context) (bool, error) {
	if err := p.verifyUSBSource(ctx); err != nil {
		return false, err
	}
	if ok, err := p.SourceStopped(ctx); err != nil || !ok {
		return false, errors.New("source must be stopped before target start")
	}
	out, err := p.Target.SubmitLifecycle(ctx, p.TargetSpec.GuestIdentity, "start")
	if err != nil {
		return false, err
	}
	return p.targetState(ctx, "running", out)
}
func (p *ProxmoxMigrationRuntime) SourceStopped(ctx context.Context) (bool, error) {
	if err := p.verifyUSBSource(ctx); err != nil {
		return false, err
	}
	if p.SourceLifecycle != nil {
		return p.SourceLifecycle.Stopped(ctx)
	}
	g, err := p.Source.Guest(ctx, p.SourceGrant.ID)
	if err != nil {
		return false, err
	}
	grant, grantErr := p.sourceGrant(ctx)
	if grantErr != nil {
		return false, grantErr
	}
	if g.Name != grant.Name || substrate.GuestConfigDigest(g) != grant.ConfigDigest {
		return false, errors.New("source identity changed")
	}
	return g.Status == "stopped", nil
}
func (p *ProxmoxMigrationRuntime) TargetStopped(ctx context.Context) (bool, error) {
	if err := p.verifyUSBSource(ctx); err != nil {
		return false, err
	}
	g, err := p.Target.Guest(ctx, p.TargetSpec.ID)
	if err != nil {
		return false, err
	}
	if g.Name != p.TargetSpec.Name || !strings.Contains(";"+g.Tags+";", ";"+p.TargetSpec.OperationTag+";") {
		return false, errors.New("target ownership changed")
	}
	return g.Status == "stopped", nil
}
func (p *ProxmoxMigrationRuntime) TransferToTarget(ctx context.Context) (bool, error) {
	if err := p.verifyUSBSource(ctx); err != nil {
		return false, err
	}
	if ok, err := p.SourceStopped(ctx); err != nil || !ok {
		return false, errors.New("source must be stopped before network transfer")
	}
	if err := p.noRadioAttachments(ctx); err != nil {
		return false, err
	}
	if ok, err := p.StopTarget(ctx); err != nil || !ok {
		return false, err
	}
	if p.USB != nil {
		if err := p.Target.TransferUSB(ctx, *p.USB, p.TargetSpec.GuestIdentity, true); err != nil {
			return false, err
		}
	}
	if err := p.Target.ActivateProductionNetwork(ctx, p.TargetSpec.GuestIdentity, p.Production); err != nil {
		return false, err
	}
	return true, nil
}
func (p *ProxmoxMigrationRuntime) TransferToSource(ctx context.Context) (bool, error) {
	if err := p.verifyUSBSource(ctx); err != nil {
		return false, err
	}
	if ok, err := p.TargetStopped(ctx); err != nil || !ok {
		return false, errors.New("target must be stopped before network return")
	}
	if err := p.noRadioAttachments(ctx); err != nil {
		return false, err
	}
	if p.USB != nil {
		if err := p.Target.TransferUSB(ctx, *p.USB, p.TargetSpec.GuestIdentity, false); err != nil {
			return false, err
		}
	}
	if err := p.Target.SetNetworkIsolation(ctx, p.TargetSpec.GuestIdentity, true); err != nil {
		return false, err
	}
	return true, nil
}
func (p *ProxmoxMigrationRuntime) noRadioAttachments(ctx context.Context) error {
	if p.SourceLifecycle != nil {
		if p.USB != nil {
			return errors.New("container USB transfer requires a supported explicit adapter")
		}
		if err := p.SourceLifecycle.VerifyNoRadios(ctx); err != nil {
			return err
		}
	}
	for _, entry := range []struct {
		client *substrate.Client
		id     int
	}{{p.Source, p.SourceGrant.ID}, {p.Target, p.TargetSpec.ID}} {
		if entry.client == nil {
			continue
		}
		g, err := entry.client.Guest(ctx, entry.id)
		if err != nil {
			return err
		}
		for key := range g.Config {
			if p.USB != nil && key == p.USB.Slot {
				continue
			}
			if strings.HasPrefix(key, "usb") || strings.HasPrefix(key, "hostpci") {
				return errors.New("radio/PCI migration requires an explicitly selected serial-verified exclusive transfer adapter")
			}
		}
	}
	return nil
}
func (p *ProxmoxMigrationRuntime) targetState(ctx context.Context, state string, out substrate.Submission) (bool, error) {
	if out.TaskRef != "" {
		task, err := p.Target.ObserveTask(ctx, out.TaskRef)
		if err != nil {
			return false, err
		}
		if task.Complete() && !task.Succeeded() {
			return false, errors.New("target lifecycle failed")
		}
	}
	g, err := p.Target.Guest(ctx, p.TargetSpec.ID)
	return err == nil && g.Status == state, err
}

func (p *ProxmoxMigrationRuntime) verifyUSBSource(ctx context.Context) error {
	if p.USB == nil {
		return nil
	}
	_, err := p.sourceGrant(ctx)
	return err
}

func (p *ProxmoxMigrationRuntime) sourceGrant(ctx context.Context) (substrate.ObservedGuestGrant, error) {
	grant := p.SourceGrant
	if p.USB == nil {
		return grant, nil
	}
	if p.SourceLifecycle != nil {
		return grant, ErrUnauthorized
	}
	return p.Source.VerifyUSBPowerSource(ctx, p.Target, *p.USB, grant)
}
