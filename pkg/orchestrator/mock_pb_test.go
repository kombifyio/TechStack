package orchestrator

import (
	"errors"
	"sync"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// MockPocketBaseApp provides a test double for PocketBaseApp.
// All operations are configurable through callbacks or default behaviors.
type MockPocketBaseApp struct {
	mu sync.RWMutex

	// Storage for mock records and collections
	records     map[string]map[string]*core.Record // collectionID -> recordID -> record
	collections map[string]*core.Collection        // collectionNameOrID -> collection

	// Callbacks for custom behavior (called before default behavior if set)
	OnFindRecordById       func(collectionModelOrIdentifier any, recordId string, optFilters ...func(q *dbx.SelectQuery) error) (*core.Record, error)
	OnFindRecordsByFilter  func(collectionModelOrIdentifier any, filter, sort string, limit, offset int, params ...dbx.Params) ([]*core.Record, error)
	OnFindCollectionByName func(nameOrId string) (*core.Collection, error)
	OnSave                 func(model core.Model) error

	// Error injection
	FindRecordByIdError      error
	FindRecordsByFilterError error
	FindCollectionError      error
	SaveError                error
}

// NewMockPocketBaseApp creates a new mock with empty storage.
func NewMockPocketBaseApp() *MockPocketBaseApp {
	return &MockPocketBaseApp{
		records:     make(map[string]map[string]*core.Record),
		collections: make(map[string]*core.Collection),
	}
}

// AddCollection adds a collection to the mock storage.
func (m *MockPocketBaseApp) AddCollection(name, id string) *core.Collection {
	m.mu.Lock()
	defer m.mu.Unlock()

	col := core.NewBaseCollection(name)
	col.Id = id
	m.collections[name] = col
	m.collections[id] = col
	m.records[name] = make(map[string]*core.Record)
	m.records[id] = m.records[name]
	return col
}

// AddRecord adds a record to a collection in mock storage.
func (m *MockPocketBaseApp) AddRecord(collectionNameOrId string, record *core.Record) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.records[collectionNameOrId] == nil {
		m.records[collectionNameOrId] = make(map[string]*core.Record)
	}
	m.records[collectionNameOrId][record.Id] = record
}

// GetSavedRecords returns all saved records for a collection.
func (m *MockPocketBaseApp) GetSavedRecords(collectionNameOrId string) []*core.Record {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*core.Record
	for _, r := range m.records[collectionNameOrId] {
		result = append(result, r)
	}
	return result
}

// getCollectionName extracts the collection name from various identifier types.
func getCollectionName(collectionModelOrIdentifier any) string {
	switch c := collectionModelOrIdentifier.(type) {
	case *core.Collection:
		return c.Name
	case core.Collection:
		return c.Name
	case string:
		return c
	default:
		return ""
	}
}

// FindRecordById implements PocketBaseApp.
func (m *MockPocketBaseApp) FindRecordById(collectionModelOrIdentifier any, recordId string, optFilters ...func(q *dbx.SelectQuery) error) (*core.Record, error) {
	// Custom callback takes precedence
	if m.OnFindRecordById != nil {
		return m.OnFindRecordById(collectionModelOrIdentifier, recordId, optFilters...)
	}

	// Error injection
	if m.FindRecordByIdError != nil {
		return nil, m.FindRecordByIdError
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	colName := getCollectionName(collectionModelOrIdentifier)
	if recs, ok := m.records[colName]; ok {
		if r, ok := recs[recordId]; ok {
			return r, nil
		}
	}
	return nil, errors.New("record not found")
}

// FindRecordsByFilter implements PocketBaseApp.
func (m *MockPocketBaseApp) FindRecordsByFilter(collectionModelOrIdentifier any, filter string, sort string, limit int, offset int, params ...dbx.Params) ([]*core.Record, error) {
	// Custom callback takes precedence
	if m.OnFindRecordsByFilter != nil {
		return m.OnFindRecordsByFilter(collectionModelOrIdentifier, filter, sort, limit, offset, params...)
	}

	// Error injection
	if m.FindRecordsByFilterError != nil {
		return nil, m.FindRecordsByFilterError
	}

	// Default: return all records in collection (mock doesn't parse filter)
	m.mu.RLock()
	defer m.mu.RUnlock()

	colName := getCollectionName(collectionModelOrIdentifier)
	var result []*core.Record
	if recs, ok := m.records[colName]; ok {
		for _, r := range recs {
			result = append(result, r)
			if limit > 0 && len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

// FindCollectionByNameOrId implements PocketBaseApp.
func (m *MockPocketBaseApp) FindCollectionByNameOrId(nameOrId string) (*core.Collection, error) {
	// Custom callback takes precedence
	if m.OnFindCollectionByName != nil {
		return m.OnFindCollectionByName(nameOrId)
	}

	// Error injection
	if m.FindCollectionError != nil {
		return nil, m.FindCollectionError
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if col, ok := m.collections[nameOrId]; ok {
		return col, nil
	}
	return nil, errors.New("collection not found")
}

// Save implements PocketBaseApp.
func (m *MockPocketBaseApp) Save(model core.Model) error {
	// Custom callback takes precedence
	if m.OnSave != nil {
		return m.OnSave(model)
	}

	// Error injection
	if m.SaveError != nil {
		return m.SaveError
	}

	// Default: generate ID if not set and store
	m.mu.Lock()
	defer m.mu.Unlock()

	record, ok := model.(*core.Record)
	if !ok {
		return nil // Not a record, skip storage
	}

	if record.Id == "" {
		record.Id = generateMockID()
	}

	collectionName := record.Collection().Name
	if m.records[collectionName] == nil {
		m.records[collectionName] = make(map[string]*core.Record)
	}
	m.records[collectionName][record.Id] = record
	return nil
}

var mockIDCounter int
var mockIDMu sync.Mutex

func generateMockID() string {
	mockIDMu.Lock()
	defer mockIDMu.Unlock()
	mockIDCounter++
	return "mock_" + string(rune('a'+mockIDCounter%26)) + string(rune('0'+mockIDCounter/26))
}
