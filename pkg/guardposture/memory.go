package guardposture

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryStore is the in-memory Store for editions without a control-plane
// database and for tests. It enforces the same tenant scoping and ledger rules.
type MemoryStore struct {
	mu       sync.Mutex
	postures map[string]*Posture
	findings []FindingRecord
	repairs  []Repair
	policies map[string]AutoRepairPolicy
	sink     AlertSink
}

// NewMemoryStore returns an empty store. sink may be nil.
func NewMemoryStore(sink AlertSink) *MemoryStore {
	return &MemoryStore{postures: map[string]*Posture{}, policies: map[string]AutoRepairPolicy{}, sink: sink}
}

func key(tenantID, workerID string) string { return tenantID + "\x00" + workerID }

func (s *MemoryStore) Get(_ context.Context, tenantID, workerID string) (*Posture, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	posture, ok := s.postures[key(strings.TrimSpace(tenantID), strings.TrimSpace(workerID))]
	if !ok {
		return nil, ErrNotFound
	}
	copied := *posture
	return &copied, nil
}

func (s *MemoryStore) ListByTenant(_ context.Context, tenantID string) ([]Posture, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Posture
	for _, posture := range s.postures {
		if posture.TenantID == strings.TrimSpace(tenantID) {
			out = append(out, *posture)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out, nil
}

func (s *MemoryStore) ensure(identity Identity, name string, now time.Time) *Posture {
	posture, ok := s.postures[key(identity.TenantID, identity.WorkerID)]
	if !ok {
		posture = &Posture{TenantID: identity.TenantID, WorkerID: identity.WorkerID, CreatedAt: now}
		s.postures[key(identity.TenantID, identity.WorkerID)] = posture
	}
	posture.ServerID, posture.OwnerSubjectID, posture.UpdatedAt = identity.ServerID, identity.OwnerSubjectID, now
	if name != "" {
		posture.DisplayName = name
	}
	return posture
}

func (s *MemoryStore) ApplyReport(ctx context.Context, write ReportWrite) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	posture := s.ensure(write.Identity, write.DisplayName, write.Now)
	if posture.SourceEpoch == write.Report.SourceEpoch && write.Report.Sequence <= posture.SourceSequence && posture.SourceSequence != 0 {
		return false, nil
	}
	posture.SourceEpoch, posture.SourceSequence, posture.ReportedAt = write.Report.SourceEpoch, write.Report.Sequence, write.Now
	posture.GuardVersion, posture.GuardSigned = write.Report.GuardVersion, write.Report.GuardSigned
	if write.Security != nil {
		posture.Security = MergeSecurity(posture.Security, *write.Security)
		AnswerVerifyRequest(posture, write.Report.VerifyRequestID)
	}
	if write.Backup != nil {
		posture.Backup = MergeBackup(posture.Backup, *write.Backup)
	}
	for _, reconciliation := range write.Reconciliations {
		if err := s.reconcile(ctx, reconciliation, write.Identity, posture.DisplayName, write.Now); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *MemoryStore) ApplyGuard(ctx context.Context, write GuardWrite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	posture := s.ensure(write.Identity, write.DisplayName, write.Now)
	obs := write.Observation
	posture.GuardMode, posture.GuardUpdateState, posture.GuardUpdateVersion = obs.Mode, obs.UpdateState, obs.UpdateVersion
	posture.GuardDenyMutations, posture.GuardObservedAt = obs.DenyMutations, write.Now
	return s.reconcile(ctx, write.Reconciliation, write.Identity, posture.DisplayName, write.Now)
}

func (s *MemoryStore) reconcile(ctx context.Context, rec Reconciliation, identity Identity, name string, now time.Time) error {
	if rec.Empty() {
		return nil
	}
	var open []FindingRecord
	for _, finding := range s.findings {
		if finding.TenantID == identity.TenantID && finding.WorkerID == identity.WorkerID && finding.Scope == rec.Scope && finding.ResolvedAt == nil {
			open = append(open, finding)
		}
	}
	change := planReconciliation(rec, open, identity, name, now, uuid.NewString)
	for _, record := range change.open {
		s.findings = append(s.findings, record)
	}
	replace := func(record FindingRecord) {
		for i := range s.findings {
			if s.findings[i].ID == record.ID {
				s.findings[i] = record
			}
		}
	}
	for _, record := range change.update {
		replace(record)
	}
	for _, record := range change.close {
		replace(record)
	}
	if s.sink != nil {
		for _, event := range change.alerts {
			if err := s.sink.EnqueueAlert(ctx, nil, event); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *MemoryStore) RequestVerify(_ context.Context, identity Identity, name, requestID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	posture := s.ensure(identity, name, at)
	posture.VerifyRequestedAt, posture.VerifyRequestID, posture.VerifyUnechoed = &at, requestID, 0
	return nil
}

func (s *MemoryStore) OpenFindings(_ context.Context, tenantID, workerID string) ([]FindingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []FindingRecord
	for _, finding := range s.findings {
		if finding.TenantID == tenantID && finding.WorkerID == workerID && finding.ResolvedAt == nil {
			out = append(out, finding)
		}
	}
	return out, nil
}

func (s *MemoryStore) Findings(_ context.Context, tenantID, workerID string, since time.Time, limit int) ([]FindingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []FindingRecord
	for _, finding := range s.findings {
		if finding.TenantID == tenantID && finding.WorkerID == workerID && !finding.OpenedAt.Before(since) {
			out = append(out, finding)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OpenedAt.After(out[j].OpenedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) history(tenantID, workerID string) []Repair {
	var out []Repair
	for _, repair := range s.repairs {
		if repair.TenantID == tenantID && repair.WorkerID == workerID {
			out = append(out, repair)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.After(out[j].RequestedAt) })
	return out
}

func (s *MemoryStore) AdmitRepair(_ context.Context, tenantID, workerID string, now time.Time, admit func([]Repair, *Posture) (*Repair, error)) (*Repair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var posture *Posture
	if stored, ok := s.postures[key(tenantID, workerID)]; ok {
		copied := *stored
		posture = &copied
	}
	repair, err := admit(s.history(tenantID, workerID), posture)
	if err != nil {
		return nil, err
	}
	repair.TenantID, repair.WorkerID = tenantID, workerID
	s.repairs = append(s.repairs, *repair)
	copied := *repair
	return &copied, nil
}

func (s *MemoryStore) UpdateRepair(_ context.Context, tenantID, repairID string, fn func(*Repair)) (*Repair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.repairs {
		if s.repairs[i].TenantID == tenantID && s.repairs[i].ID == repairID {
			fn(&s.repairs[i])
			copied := s.repairs[i]
			return &copied, nil
		}
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) Repairs(_ context.Context, tenantID, workerID string, limit int) ([]Repair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.history(tenantID, workerID)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) GetAutoRepair(_ context.Context, tenantID, workerID string) (*AutoRepairPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	policy, ok := s.policies[key(tenantID, workerID)]
	if !ok {
		return nil, nil
	}
	return &policy, nil
}

func (s *MemoryStore) SetAutoRepair(_ context.Context, policy AutoRepairPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policies[key(policy.TenantID, policy.WorkerID)] = policy
	return nil
}
