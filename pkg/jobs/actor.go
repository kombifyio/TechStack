package jobs

import (
	"strings"

	"github.com/kombifyio/go-common/identity"
)

const jobPayloadActorKey = "actor"

type JobActor struct {
	UserID   string   `json:"user_id,omitempty"`
	TenantID string   `json:"tenant_id,omitempty"`
	Email    string   `json:"email,omitempty"`
	Roles    []string `json:"roles,omitempty"`
}

func ActorPayloadFromIdentity(id *identity.Identity) map[string]interface{} {
	if id == nil {
		return nil
	}
	return map[string]interface{}{
		"user_id":   strings.TrimSpace(id.UserID),
		"tenant_id": strings.TrimSpace(id.OrgID),
		"email":     strings.TrimSpace(id.Email),
		"roles":     append([]string(nil), id.Roles...),
	}
}

func jobActorFromPayload(payload map[string]interface{}) JobActor {
	if len(payload) == 0 {
		return JobActor{}
	}
	if actor := actorFromValue(payload[jobPayloadActorKey]); len(actor.Roles) > 0 || actor.UserID != "" || actor.TenantID != "" {
		return actor
	}
	return JobActor{
		UserID:   strings.TrimSpace(stringFromMap(payload, "actor_user_id")),
		TenantID: strings.TrimSpace(stringFromMap(payload, "actor_tenant_id")),
		Email:    strings.TrimSpace(stringFromMap(payload, "actor_email")),
		Roles:    compactStrings(firstStringSlice(payload["actor_roles"], payload["user_roles"], payload["roles"])),
	}
}

func actorFromValue(value interface{}) JobActor {
	switch actor := value.(type) {
	case JobActor:
		actor.UserID = strings.TrimSpace(actor.UserID)
		actor.TenantID = strings.TrimSpace(actor.TenantID)
		actor.Email = strings.TrimSpace(actor.Email)
		actor.Roles = compactStrings(actor.Roles)
		return actor
	case map[string]interface{}:
		return actorFromMap(actor)
	default:
		return JobActor{}
	}
}

func actorFromMap(actor map[string]interface{}) JobActor {
	if actor == nil {
		return JobActor{}
	}
	return JobActor{
		UserID:   firstNonEmpty(stringFromMap(actor, "user_id"), stringFromMap(actor, "userId"), stringFromMap(actor, "sub")),
		TenantID: firstNonEmpty(stringFromMap(actor, "tenant_id"), stringFromMap(actor, "tenantId"), stringFromMap(actor, "org_id"), stringFromMap(actor, "orgId")),
		Email:    firstNonEmpty(stringFromMap(actor, "email"), stringFromMap(actor, "mail")),
		Roles:    compactStrings(firstStringSlice(actor["roles"], actor["user_roles"], actor["actor_roles"])),
	}
}
