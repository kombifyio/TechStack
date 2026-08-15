// Package orchestrator provides the central orchestration layer for kombifyTechstack.
package orchestrator

import (
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// PocketBaseApp defines the interface for PocketBase operations used by the Orchestrator.
// This abstraction allows mocking in unit tests while keeping the production implementation
// using the real *pocketbase.PocketBase.
//
// The method signatures match PocketBase v0.34.x exactly.
type PocketBaseApp interface {
	// FindRecordById finds a single record by ID in the given collection.
	// collectionModelOrIdentifier can be a Collection, *Collection, or string (name/id).
	FindRecordById(collectionModelOrIdentifier any, recordId string, optFilters ...func(q *dbx.SelectQuery) error) (*core.Record, error)

	// FindRecordsByFilter finds records matching a filter expression.
	// collectionModelOrIdentifier can be a Collection, *Collection, or string (name/id).
	FindRecordsByFilter(collectionModelOrIdentifier any, filter string, sort string, limit int, offset int, params ...dbx.Params) ([]*core.Record, error)

	// FindCollectionByNameOrId finds a collection by name or ID.
	FindCollectionByNameOrId(nameOrId string) (*core.Collection, error)

	// Save persists a model (create or update). Record implements Model.
	Save(model core.Model) error
}

// Verify at compile time that *pocketbase.PocketBase satisfies PocketBaseApp.
// This is done implicitly in New() where we pass app directly.
