package actions

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/pocketbase/pocketbase/core"

	"github.com/kombifyio/techstack/pkg/logger"
)

const collectionName = "ril_action_cards"

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

// CardType identifies the category of an action card.
type CardType string

const (
	TypeUpdate   CardType = "update"
	TypeErrorFix CardType = "error_fix"
	TypeResource CardType = "resource"
	TypeDrift    CardType = "drift"
	TypeSecurity CardType = "security"
)

// Severity indicates how urgent an action card is.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// CardStatus tracks where an action card is in the lifecycle.
type CardStatus string

const (
	StatusPending   CardStatus = "pending"
	StatusApproved  CardStatus = "approved"
	StatusExecuting CardStatus = "executing"
	StatusCompleted CardStatus = "completed"
	StatusDismissed CardStatus = "dismissed"
	StatusFailed    CardStatus = "failed"
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// ActionCard is the core domain object for a proactive intelligence card.
type ActionCard struct {
	ID            string       `json:"id"`
	CardID        string       `json:"card_id"`
	ServerID      string       `json:"server_id"`
	OwnerID       string       `json:"owner_id"`
	Type          CardType     `json:"type"`
	Severity      Severity     `json:"severity"`
	Title         string       `json:"title"`
	Description   string       `json:"description,omitempty"`
	Engine        string       `json:"engine,omitempty"`
	Evidence      any          `json:"evidence,omitempty"`
	SuggestedPlan *ActionPlan  `json:"suggested_plan,omitempty"`
	Status        CardStatus   `json:"status"`
	DetectedAt    *time.Time   `json:"detected_at,omitempty"`
	ApprovedAt    *time.Time   `json:"approved_at,omitempty"`
	CompletedAt   *time.Time   `json:"completed_at,omitempty"`
	AuditTrail    []AuditEntry `json:"audit_trail,omitempty"`
	Created       time.Time    `json:"created"`
	Updated       time.Time    `json:"updated"`
}

// ActionPlan describes the steps the agent will execute.
type ActionPlan struct {
	Steps        []ActionStep `json:"steps"`
	DryRunResult string       `json:"dry_run_result,omitempty"`
	Rollback     string       `json:"rollback,omitempty"`
	EstDuration  string       `json:"est_duration,omitempty"`
}

// ActionStep is a single executable step inside an ActionPlan.
type ActionStep struct {
	Description   string `json:"description"`
	Command       string `json:"command,omitempty"`
	TargetService string `json:"target_service,omitempty"`
}

// AuditEntry records a lifecycle event.
type AuditEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"`
	Actor     string    `json:"actor"`
	Detail    string    `json:"detail,omitempty"`
}

// ListFilters controls optional narrowing of list queries.
type ListFilters struct {
	Status   string
	Severity string
	ServerID string
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// Store provides CRUD access to the ril_action_cards PocketBase collection.
type Store struct {
	app core.App
	log *logger.Logger
}

// NewStore creates a new action-card store.
func NewStore(app core.App) *Store {
	return &Store{
		app: app,
		log: logger.Get().WithComponent("ril.actions"),
	}
}

// Create persists a new action card. It generates a card_id, sets status to
// pending, and initialises the audit trail.
func (s *Store) Create(card *ActionCard) error {
	coll, err := s.app.FindCollectionByNameOrId(collectionName)
	if err != nil {
		return fmt.Errorf("ril_action_cards collection not found: %w", err)
	}

	card.CardID = uuid.New().String()
	card.Status = StatusPending
	now := time.Now().UTC()
	card.DetectedAt = &now
	card.AuditTrail = []AuditEntry{{
		Timestamp: now,
		Action:    "created",
		Actor:     "system",
		Detail:    fmt.Sprintf("card created by engine %s", card.Engine),
	}}

	record := core.NewRecord(coll)
	record.Set("card_id", card.CardID)
	record.Set("server_id", card.ServerID)
	record.Set("owner_id", card.OwnerID)
	record.Set("type", string(card.Type))
	record.Set("severity", string(card.Severity))
	record.Set("title", card.Title)
	record.Set("description", card.Description)
	record.Set("engine", card.Engine)
	record.Set("evidence", card.Evidence)
	record.Set("suggested_plan", card.SuggestedPlan)
	record.Set("status", string(card.Status))
	record.Set("detected_at", now)
	record.Set("audit_trail", card.AuditTrail)

	if err := s.app.Save(record); err != nil {
		return fmt.Errorf("failed to create action card: %w", err)
	}

	card.ID = record.Id
	card.Created = record.GetDateTime("created").Time()
	card.Updated = record.GetDateTime("updated").Time()

	s.log.Info("ril_action_card_created",
		"card_id", card.CardID,
		"server_id", card.ServerID,
		"type", string(card.Type),
		"severity", string(card.Severity),
	)
	return nil
}

// GetByID finds a single action card by its card_id.
func (s *Store) GetByID(cardID string) (*ActionCard, error) {
	record, err := s.findRecord(cardID)
	if err != nil {
		return nil, err
	}
	return s.recordToCard(record), nil
}

// ListByOwner returns action cards owned by ownerID, optionally filtered.
func (s *Store) ListByOwner(ownerID string, filters ListFilters) ([]*ActionCard, error) {
	expr := "owner_id = {:owner}"
	params := map[string]any{"owner": ownerID}

	if filters.Status != "" {
		expr += " && status = {:status}"
		params["status"] = filters.Status
	}
	if filters.Severity != "" {
		expr += " && severity = {:severity}"
		params["severity"] = filters.Severity
	}
	if filters.ServerID != "" {
		expr += " && server_id = {:sid}"
		params["sid"] = filters.ServerID
	}

	records, err := s.app.FindRecordsByFilter(
		collectionName,
		expr,
		"-detected_at",
		0, 0,
		params,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list action cards: %w", err)
	}

	cards := make([]*ActionCard, 0, len(records))
	for _, r := range records {
		cards = append(cards, s.recordToCard(r))
	}
	return cards, nil
}

// ListByServer returns action cards for a specific server, scoped to ownerID.
func (s *Store) ListByServer(serverID, ownerID string) ([]*ActionCard, error) {
	return s.ListByOwner(ownerID, ListFilters{ServerID: serverID})
}

// Approve transitions a pending card to approved.
func (s *Store) Approve(cardID, actor string) error {
	record, err := s.findRecord(cardID)
	if err != nil {
		return err
	}

	current := CardStatus(record.GetString("status"))
	if err := ValidateTransition(current, StatusApproved); err != nil {
		return err
	}

	now := time.Now().UTC()
	record.Set("status", string(StatusApproved))
	record.Set("approved_at", now)
	appendAudit(record, AuditEntry{
		Timestamp: now,
		Action:    "approved",
		Actor:     actor,
	})

	if err := s.app.Save(record); err != nil {
		return fmt.Errorf("failed to approve action card: %w", err)
	}

	s.log.Info("ril_action_card_approved", "card_id", cardID, "actor", actor)
	return nil
}

// Dismiss transitions a pending card to dismissed.
func (s *Store) Dismiss(cardID, actor string) error {
	record, err := s.findRecord(cardID)
	if err != nil {
		return err
	}

	current := CardStatus(record.GetString("status"))
	if err := ValidateTransition(current, StatusDismissed); err != nil {
		return err
	}

	now := time.Now().UTC()
	record.Set("status", string(StatusDismissed))
	appendAudit(record, AuditEntry{
		Timestamp: now,
		Action:    "dismissed",
		Actor:     actor,
	})

	if err := s.app.Save(record); err != nil {
		return fmt.Errorf("failed to dismiss action card: %w", err)
	}

	s.log.Info("ril_action_card_dismissed", "card_id", cardID, "actor", actor)
	return nil
}

// UpdateStatus performs a general status transition with an audit detail.
func (s *Store) UpdateStatus(cardID string, status CardStatus, detail string) error {
	record, err := s.findRecord(cardID)
	if err != nil {
		return err
	}

	current := CardStatus(record.GetString("status"))
	if err := ValidateTransition(current, status); err != nil {
		return err
	}

	now := time.Now().UTC()
	record.Set("status", string(status))
	appendAudit(record, AuditEntry{
		Timestamp: now,
		Action:    fmt.Sprintf("status_change:%s", status),
		Actor:     "system",
		Detail:    detail,
	})

	if err := s.app.Save(record); err != nil {
		return fmt.Errorf("failed to update action card status: %w", err)
	}

	s.log.Info("ril_action_card_status_changed",
		"card_id", cardID,
		"from", string(current),
		"to", string(status),
	)
	return nil
}

// Complete marks a card as completed and sets the completed_at timestamp.
func (s *Store) Complete(cardID string, detail string) error {
	record, err := s.findRecord(cardID)
	if err != nil {
		return err
	}

	current := CardStatus(record.GetString("status"))
	if err := ValidateTransition(current, StatusCompleted); err != nil {
		return err
	}

	now := time.Now().UTC()
	record.Set("status", string(StatusCompleted))
	record.Set("completed_at", now)
	appendAudit(record, AuditEntry{
		Timestamp: now,
		Action:    "completed",
		Actor:     "system",
		Detail:    detail,
	})

	if err := s.app.Save(record); err != nil {
		return fmt.Errorf("failed to complete action card: %w", err)
	}

	s.log.Info("ril_action_card_completed", "card_id", cardID)
	return nil
}

// Fail marks a card as failed with a reason.
func (s *Store) Fail(cardID string, reason string) error {
	record, err := s.findRecord(cardID)
	if err != nil {
		return err
	}

	current := CardStatus(record.GetString("status"))
	if err := ValidateTransition(current, StatusFailed); err != nil {
		return err
	}

	now := time.Now().UTC()
	record.Set("status", string(StatusFailed))
	appendAudit(record, AuditEntry{
		Timestamp: now,
		Action:    "failed",
		Actor:     "system",
		Detail:    reason,
	})

	if err := s.app.Save(record); err != nil {
		return fmt.Errorf("failed to mark action card as failed: %w", err)
	}

	s.log.Info("ril_action_card_failed", "card_id", cardID, "reason", reason)
	return nil
}

// ---------------------------------------------------------------------------
// internal helpers
// ---------------------------------------------------------------------------

func (s *Store) findRecord(cardID string) (*core.Record, error) {
	records, err := s.app.FindRecordsByFilter(
		collectionName,
		"card_id = {:cid}",
		"",
		1, 0,
		map[string]any{"cid": cardID},
	)
	if err != nil || len(records) == 0 {
		return nil, fmt.Errorf("action card not found: %s", cardID)
	}
	return records[0], nil
}

func (s *Store) recordToCard(r *core.Record) *ActionCard {
	card := &ActionCard{
		ID:          r.Id,
		CardID:      r.GetString("card_id"),
		ServerID:    r.GetString("server_id"),
		OwnerID:     r.GetString("owner_id"),
		Type:        CardType(r.GetString("type")),
		Severity:    Severity(r.GetString("severity")),
		Title:       r.GetString("title"),
		Description: r.GetString("description"),
		Engine:      r.GetString("engine"),
		Status:      CardStatus(r.GetString("status")),
		Created:     r.GetDateTime("created").Time(),
		Updated:     r.GetDateTime("updated").Time(),
	}

	// Evidence (raw JSON)
	if raw := r.Get("evidence"); raw != nil {
		card.Evidence = raw
	}

	// SuggestedPlan (typed)
	if raw := r.Get("suggested_plan"); raw != nil {
		if b, err := json.Marshal(raw); err == nil {
			var plan ActionPlan
			if json.Unmarshal(b, &plan) == nil {
				card.SuggestedPlan = &plan
			}
		}
	}

	// Timestamps
	if t := r.GetDateTime("detected_at"); !t.IsZero() {
		ts := t.Time()
		card.DetectedAt = &ts
	}
	if t := r.GetDateTime("approved_at"); !t.IsZero() {
		ts := t.Time()
		card.ApprovedAt = &ts
	}
	if t := r.GetDateTime("completed_at"); !t.IsZero() {
		ts := t.Time()
		card.CompletedAt = &ts
	}

	// AuditTrail
	if raw := r.Get("audit_trail"); raw != nil {
		if b, err := json.Marshal(raw); err == nil {
			var trail []AuditEntry
			if json.Unmarshal(b, &trail) == nil {
				card.AuditTrail = trail
			}
		}
	}

	return card
}

// appendAudit reads the existing audit_trail JSON, appends entry, and writes back.
func appendAudit(record *core.Record, entry AuditEntry) {
	var trail []AuditEntry
	if raw := record.Get("audit_trail"); raw != nil {
		if b, err := json.Marshal(raw); err == nil {
			_ = json.Unmarshal(b, &trail)
		}
	}
	trail = append(trail, entry)
	record.Set("audit_trail", trail)
}
