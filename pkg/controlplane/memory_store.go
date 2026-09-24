package controlplane

import (
	"sync"
	"time"
)

type MemoryStore struct {
	mu                sync.RWMutex
	now               func() time.Time
	homelabs          map[string]Homelab
	wizardRuns        map[string]WizardRun
	onboarding        map[string]OnboardingState
	stacks            map[string]Stack
	jobs              map[string]Job
	jobLeases         map[string]memoryJobExecutionLease
	worker            map[string]Worker
	tokens            map[string]PairingToken
	nodes             map[string]Node
	svcs              map[string]Service
	serviceRuntime    map[string]ServiceRuntime
	rilSrv            map[string]RILServer
	rilCmd            map[string]RILCommand
	rilEvt            map[string]RILHealEvent
	rilCrd            map[string]RILActionCard
	servers           map[string]ServerRuntime
	serverTransitions map[string][]ServerStateTransition
	serverInventory   map[string][]ServerInventorySnapshot
	serverOutbox      []ServerRegistryOutboxItem
	serverGuardEpochs map[string]struct{}
	nextServerEventID int64
	wallet            map[string]WalletItem
	driftResults      map[string]DriftResult
	ownerSpecTokens   map[string]OwnerSpecToken
	events            map[string]ActivityEvent
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		now:               func() time.Time { return time.Now().UTC() },
		homelabs:          make(map[string]Homelab),
		wizardRuns:        make(map[string]WizardRun),
		onboarding:        make(map[string]OnboardingState),
		stacks:            make(map[string]Stack),
		jobs:              make(map[string]Job),
		jobLeases:         make(map[string]memoryJobExecutionLease),
		worker:            make(map[string]Worker),
		tokens:            make(map[string]PairingToken),
		nodes:             make(map[string]Node),
		svcs:              make(map[string]Service),
		serviceRuntime:    make(map[string]ServiceRuntime),
		rilSrv:            make(map[string]RILServer),
		rilCmd:            make(map[string]RILCommand),
		rilEvt:            make(map[string]RILHealEvent),
		rilCrd:            make(map[string]RILActionCard),
		servers:           make(map[string]ServerRuntime),
		serverTransitions: make(map[string][]ServerStateTransition),
		serverInventory:   make(map[string][]ServerInventorySnapshot),
		serverGuardEpochs: make(map[string]struct{}),
		wallet:            make(map[string]WalletItem),
		driftResults:      make(map[string]DriftResult),
		ownerSpecTokens:   make(map[string]OwnerSpecToken),
		events:            make(map[string]ActivityEvent),
	}
}

func (s *MemoryStore) SetNow(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
		return
	}
	s.now = now
}
