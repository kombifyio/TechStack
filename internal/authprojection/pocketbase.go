// Package authprojection contains bounded identity adapters for PocketBase-backed
// compatibility surfaces. Authentication authority remains in the Go control plane.
package authprojection

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/authlocal"
	"github.com/pocketbase/pocketbase/core"
)

const localSubjectPrefix = "breakglass:"

// PocketBaseUserID maps the canonical local-owner subject to its deterministic
// compatibility projection. All other identity subjects pass through unchanged.
func PocketBaseUserID(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == localSubjectPrefix+authlocal.BreakGlassRecordID {
		return authlocal.BreakGlassRecordID
	}
	return subject
}

// FindPocketBaseUser resolves the compatibility profile for a canonical auth
// subject. Local auth has a deterministic profile ID; Cloud profiles are
// linked by the verified provider subject because PocketBase IDs cannot carry
// arbitrary OIDC subjects.
func FindPocketBaseUser(app core.App, subject string) (*core.Record, error) {
	if app == nil || strings.TrimSpace(subject) == "" {
		return nil, nil
	}
	userID := PocketBaseUserID(subject)
	user, err := app.FindRecordById("users", userID)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	link, err := app.FindFirstRecordByFilter(
		"user_links",
		"external_id = {:subject} && provider = 'cloud'",
		map[string]any{"subject": strings.TrimSpace(subject)},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return app.FindRecordById("users", link.GetString("user"))
}
