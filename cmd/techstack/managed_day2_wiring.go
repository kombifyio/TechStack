package main

import (
	"context"
	"sync"

	"github.com/kombifyio/techstack/internal/executionchannel"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

// managedDay2 joins the two halves of managed Day-2 operations composed at
// different boot stages: provider control (power reconcile, IONOS guest
// shutdown consumer) and the route layer (execution channel over the managed
// runtime target resolver). Every accessor is nil-safe and fails closed.
var managedDay2 = &managedDay2Composition{}

type managedDay2Composition struct {
	mu      sync.RWMutex
	power   monthlyruntime.PowerController
	channel *executionchannel.Channel
}

func (m *managedDay2Composition) setPower(power monthlyruntime.PowerController) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.power = power
}

func (m *managedDay2Composition) setChannel(channel *executionchannel.Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channel = channel
}

func (m *managedDay2Composition) powerController() monthlyruntime.PowerController {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.power
}

// PowerOffGuest lets the IONOS adapter, composed before routes, reach the
// execution channel once the route layer has bound it.
func (m *managedDay2Composition) PowerOffGuest(ctx context.Context, req providercontrol.GuestPowerOffRequest) error {
	m.mu.RLock()
	channel := m.channel
	m.mu.RUnlock()
	if channel == nil {
		return monthlyruntime.ErrNodeChannelUnavailable
	}
	return channel.PowerOffGuest(ctx, req)
}

// bindManagedDay2 attaches the Day-2 executors to the monthly runtime
// service. Absent provider control leaves start/stop unconfigured, which the
// service answers with a structured fail-closed denial.
func bindManagedDay2(svc *monthlyruntime.Service, channel *executionchannel.Channel) {
	if svc == nil {
		return
	}
	managedDay2.setChannel(channel)
	if power := managedDay2.powerController(); power != nil {
		svc.Power = power
	}
	if channel != nil {
		svc.SSHAccess = channel
		svc.Reconnector = channel
	}
}

var _ providercontrol.GuestPowerOffExecutor = (*managedDay2Composition)(nil)
