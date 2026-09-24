package controlplane

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresStoreRILStateUsesTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 22, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ril_servers")).
		WithArgs("server-1", "tenant-1", "instance-1", "stack-1", "node-1", "media host", "healthy", sqlmock.AnyArg(), sqlmock.AnyArg(), now).
		WillReturnRows(rilServerRows().AddRow(
			"server-1", "tenant-1", "instance-1", "stack-1", "node-1", "media host", "healthy", `{"ok":true}`, `{"cpu":"4"}`, now, now, now,
		))
	mock.ExpectCommit()

	server, err := store.UpsertRILServer(context.Background(), RILServer{
		ID:         "server-1",
		TenantID:   "tenant-1",
		InstanceID: "instance-1",
		StackID:    "stack-1",
		NodeID:     "node-1",
		Name:       "media host",
		Status:     "healthy",
		Health:     map[string]any{"ok": true},
		Inventory:  map[string]any{"cpu": "4"},
		LastSeenAt: &now,
	})
	if err != nil {
		t.Fatalf("UpsertRILServer: %v", err)
	}
	if server.ID != "server-1" || server.Health["ok"] != true {
		t.Fatalf("unexpected RIL server: %#v", server)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ril_commands")).
		WithArgs("cmd-1", "tenant-1", "server-1", "auth0|user-1", "shell", "queued", sqlmock.AnyArg(), sqlmock.AnyArg(), "", nil).
		WillReturnRows(rilCommandRows().AddRow(
			"cmd-1", "tenant-1", "server-1", "auth0|user-1", "shell", "queued", `{"argv":["uptime"]}`, `{}`, nil, now, now, nil,
		))
	mock.ExpectCommit()

	command, err := store.EnqueueRILCommand(context.Background(), RILCommand{
		ID:             "cmd-1",
		TenantID:       "tenant-1",
		ServerID:       "server-1",
		ActorSubjectID: "auth0|user-1",
		CommandClass:   "shell",
		Status:         "queued",
		Request:        map[string]any{"argv": []any{"uptime"}},
	})
	if err != nil {
		t.Fatalf("EnqueueRILCommand: %v", err)
	}
	if command.ID != "cmd-1" || command.Request["argv"] == nil {
		t.Fatalf("unexpected RIL command: %#v", command)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ril_action_cards")).
		WithArgs("card-1", "tenant-1", "server-1", "stack-1", "Restart service", "open", "warning", sqlmock.AnyArg(), sqlmock.AnyArg(), nil).
		WillReturnRows(rilActionCardRows().AddRow(
			"card-1", "tenant-1", "server-1", "stack-1", "Restart service", "open", "warning", `{"kind":"restart"}`, `{}`, now, now, nil,
		))
	mock.ExpectCommit()

	card, err := store.UpsertActionCard(context.Background(), RILActionCard{
		ID:       "card-1",
		TenantID: "tenant-1",
		ServerID: "server-1",
		StackID:  "stack-1",
		Title:    "Restart service",
		Status:   "open",
		Severity: "warning",
		Action:   map[string]any{"kind": "restart"},
	})
	if err != nil {
		t.Fatalf("UpsertActionCard: %v", err)
	}
	if card.ID != "card-1" || card.Action["kind"] != "restart" {
		t.Fatalf("unexpected action card: %#v", card)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ril_heal_events")).
		WithArgs("heal-1", "tenant-1", "server-1", "card-1", "completed", "service unhealthy", sqlmock.AnyArg()).
		WillReturnRows(rilHealEventRows().AddRow(
			"heal-1", "tenant-1", "server-1", "card-1", "completed", "service unhealthy", `{"service":"jellyfin"}`, now, now,
		))
	mock.ExpectCommit()

	event, err := store.RecordHealEvent(context.Background(), RILHealEvent{
		ID:           "heal-1",
		TenantID:     "tenant-1",
		ServerID:     "server-1",
		ActionCardID: "card-1",
		Status:       "completed",
		Cause:        "service unhealthy",
		Details:      map[string]any{"service": "jellyfin"},
	})
	if err != nil {
		t.Fatalf("RecordHealEvent: %v", err)
	}
	if event.ID != "heal-1" || event.Details["service"] != "jellyfin" {
		t.Fatalf("unexpected heal event: %#v", event)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func rilServerRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "node_id", "name", "status", "health_json", "inventory_json", "last_seen_at", "created_at", "updated_at",
	})
}

func rilCommandRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "server_id", "actor_subject_id", "command_class", "status", "request_json", "result_json", "error", "created_at", "updated_at", "completed_at",
	})
}

func rilActionCardRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "server_id", "stack_id", "title", "status", "severity", "action_json", "decision_json", "created_at", "updated_at", "resolved_at",
	})
}

func rilHealEventRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "server_id", "action_card_id", "status", "cause", "details_json", "created_at", "updated_at",
	})
}
