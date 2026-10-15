package db

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	EnvManagedTestAssignment       = "TECHSTACK_MANAGED_TEST_ASSIGNMENT"
	EnvMigrationDatabaseURL        = "TECHSTACK_MIGRATION_DATABASE_URL"
	EnvManagedTestAdminDatabaseURL = "TECHSTACK_MANAGED_TEST_ADMIN_DATABASE_URL"
	ManagedTestServiceID           = "44d4056d-5f7b-82d0-b6bf-9010682190cb"
	ManagedTestHost                = "kombify-test-pg-a51883be.pg8ka0avazfe0ptzs0438468cq.c0.eu-central-1.aws.pg.clickhouse.cloud"
	ManagedTestCASHA256            = "b673e6cd3839ce9e078ca9fbc75ca09f7a74279f7af53202c50163420109bbf3"
	ManagedTestDatabase            = "techstack_test"
	ManagedTestOwner               = "techstack_test_owner"
	ManagedTestApp                 = "techstack_test_app"
	ManagedTestProvider            = "techstack_provider_control_runtime"
	ManagedTestIssuer              = "techstack_advanced_issuer_runtime"
	ManagedTestTarget              = "database-test-migrations"
	managedTestCoordinate          = "clickhouse-postgres:b6140b1d-3833-455a-b515-729055d3140d:" + ManagedTestServiceID
)

// ManagedTestAssignment is non-secret, exact runtime scope supplied by the
// reviewed native Delivery checkpoint. It is not an administrative credential.
type ManagedTestAssignment struct {
	Schema              string `json:"schema"`
	ServiceID           string `json:"service_id"`
	Database            string `json:"database"`
	SystemIdentifier    string `json:"system_identifier"`
	TargetCatalogSHA256 string `json:"target_catalog_sha256"`
	FixturePrefix       string `json:"fixture_prefix"`
	SubjectID           string `json:"subject_id"`
	TenantID            string `json:"tenant_id"`
	Email               string `json:"email"`
	AuthAudience        string `json:"auth_audience"`
}

// LoadManagedTestAssignmentFile loads another exact non-secret test assignment.
func LoadManagedTestAssignmentFile(path string) (*ManagedTestAssignment, error) {
	return loadManagedTestAssignmentPath(path, true)
}

func LoadManagedTestAssignment() (*ManagedTestAssignment, error) {
	return loadManagedTestAssignment(true)
}

func loadManagedTestAssignment(requirePhysical bool) (*ManagedTestAssignment, error) {
	return loadManagedTestAssignmentPath(os.Getenv(EnvManagedTestAssignment), requirePhysical)
}

func loadManagedTestAssignmentPath(raw string, requirePhysical bool) (*ManagedTestAssignment, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return nil, fmt.Errorf("assigned managed test runtime is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("managed test assignment is unreadable")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 32769))
	decoder.DisallowUnknownFields()
	var assignment ManagedTestAssignment
	if decoder.Decode(&assignment) != nil {
		return nil, fmt.Errorf("managed test assignment is invalid")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("managed test assignment has trailing content")
	}
	if err := assignment.validate(requirePhysical); err != nil {
		return nil, err
	}
	if err := assignment.ValidateTarget(); err != nil {
		return nil, err
	}
	return &assignment, nil
}

func (a ManagedTestAssignment) Validate() error {
	return a.validate(true)
}

func (a ManagedTestAssignment) validate(requirePhysical bool) error {
	if a.Schema != "techstack.managed-test-runtime/v1" || a.ServiceID != ManagedTestServiceID || a.Database != ManagedTestDatabase {
		return fmt.Errorf("managed test assignment is not the approved Techstack service/database")
	}
	if (requirePhysical && a.SystemIdentifier == "") || strings.Trim(a.SystemIdentifier, "0123456789") != "" {
		return fmt.Errorf("managed test assignment requires its physical system identifier")
	}
	if len(a.TargetCatalogSHA256) != 64 || strings.ToLower(a.TargetCatalogSHA256) != a.TargetCatalogSHA256 {
		return fmt.Errorf("managed test assignment requires its native catalog digest")
	}
	if _, err := hex.DecodeString(a.TargetCatalogSHA256); err != nil {
		return fmt.Errorf("managed test catalog digest is invalid")
	}
	if a.FixturePrefix != "TEST_RIL_PRO_USER" && a.FixturePrefix != "TEST_ALL_YOU_NEED_USER" {
		return fmt.Errorf("managed test assignment requires a canonical customer test identity")
	}
	if !strings.HasPrefix(a.SubjectID, "auth0|") || len(a.SubjectID) <= len("auth0|") ||
		!strings.HasPrefix(a.Email, "test-") || !strings.HasSuffix(a.Email, "@kombify.io") ||
		(strings.TrimSpace(a.Email) != a.Email) || strings.ContainsAny(a.Email, "\r\n\t ") {
		return fmt.Errorf("managed test assignment cannot use a fabricated or personal owner identity")
	}
	if (!strings.HasPrefix(a.TenantID, "org_") || len(a.TenantID) <= 4) && a.TenantID != "usr:"+a.SubjectID {
		return fmt.Errorf("managed test assignment requires the actual shared-test tenant")
	}
	if a.AuthAudience == "" || strings.TrimSpace(a.AuthAudience) != a.AuthAudience {
		return fmt.Errorf("managed test Auth0 audience is required")
	}
	return nil
}

func (a ManagedTestAssignment) ValidateTarget() error {
	if os.Getenv("KOMBIFY_DATABASE_TARGET") != ManagedTestTarget {
		return fmt.Errorf("managed test requires its governed migration target")
	}
	raw, err := os.ReadFile(os.Getenv("KOMBIFY_DATABASE_TARGET_CATALOG"))
	if err != nil {
		return fmt.Errorf("managed test native target catalog is unreadable")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != a.TargetCatalogSHA256 {
		return fmt.Errorf("managed test native catalog differs from its assignment")
	}
	var catalog struct {
		Version int `json:"schema_version"`
		Targets []struct {
			ID       string   `json:"id"`
			Adapter  string   `json:"adapter"`
			Resource string   `json:"provider_resource"`
			Channels []string `json:"channels"`
		} `json:"targets"`
	}
	if json.Unmarshal(raw, &catalog) != nil || catalog.Version != 1 {
		return fmt.Errorf("managed test native catalog is invalid")
	}
	seen := map[string]bool{}
	for _, target := range catalog.Targets {
		if target.ID != ManagedTestTarget && target.ID != "database-managed-test-postgresql" {
			if strings.TrimSuffix(target.Resource, ":migrations") == managedTestCoordinate {
				return fmt.Errorf("managed test service aliases another target")
			}
			continue
		}
		if seen[target.ID] || len(target.Channels) != 1 || target.Channels[0] != "internal" {
			return fmt.Errorf("managed test target is ambiguous or outside its internal channel")
		}
		seen[target.ID] = true
		adapter, resource := "managed-data-provider", managedTestCoordinate
		if target.ID == ManagedTestTarget {
			adapter, resource = "database-migration", resource+":migrations"
		}
		if target.Adapter != adapter || target.Resource != resource {
			return fmt.Errorf("managed test target differs from its approved service")
		}
	}
	if !seen[ManagedTestTarget] || !seen["database-managed-test-postgresql"] {
		return fmt.Errorf("managed test target pair is incomplete")
	}
	return nil
}

// Connection rejects every unassigned transport before credentials can be sent.
func (a ManagedTestAssignment) Connection(dsn, role string) (*pgx.ConnConfig, error) {
	return a.connection(dsn, role, true)
}

func (a ManagedTestAssignment) connection(dsn, role string, requirePhysical bool) (*pgx.ConnConfig, error) {
	if err := a.validate(requirePhysical); err != nil {
		return nil, err
	}
	if err := a.ValidateTarget(); err != nil {
		return nil, err
	}
	database := ManagedTestDatabase
	switch role {
	case "postgres":
		database = "postgres"
	case ManagedTestOwner, ManagedTestApp, ManagedTestProvider, ManagedTestIssuer:
	default:
		return nil, fmt.Errorf("managed test login is not assigned")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() != ManagedTestHost || parsed.Port() != "5432" ||
		parsed.User == nil || parsed.User.Username() != role || parsed.Path != "/"+database || parsed.Fragment != "" {
		return nil, fmt.Errorf("managed test connection differs from its assigned service/database/login")
	}
	if password, explicit := parsed.User.Password(); !explicit || password == "" {
		return nil, fmt.Errorf("managed test login requires its explicit assigned credential")
	}
	query := parsed.Query()
	if len(query["sslmode"]) != 1 || query.Get("sslmode") != "verify-full" || len(query["sslrootcert"]) != 1 || query.Get("sslrootcert") == "" {
		return nil, fmt.Errorf("managed test connection requires explicit verified trust")
	}
	for key := range query {
		if key != "sslmode" && key != "sslrootcert" && key != "application_name" {
			return nil, fmt.Errorf("managed test connection has unassigned session options")
		}
	}
	ca, err := os.ReadFile(query.Get("sslrootcert"))
	if err != nil {
		return nil, fmt.Errorf("managed test trust is unreadable")
	}
	caHash := sha256.Sum256(ca)
	if hex.EncodeToString(caHash[:]) != ManagedTestCASHA256 {
		return nil, fmt.Errorf("managed test trust differs from the approved service")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("managed test connection configuration is invalid")
	}
	if cfg.Host != ManagedTestHost || cfg.Port != 5432 || cfg.Database != database || cfg.User != role || cfg.Password == "" || cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.ServerName != ManagedTestHost {
		return nil, fmt.Errorf("managed test effective transport/login is unassigned")
	}
	if len(cfg.TLSConfig.Certificates) > 0 || os.Getenv("PGSSLCERT") != "" || os.Getenv("PGSSLKEY") != "" {
		return nil, fmt.Errorf("managed test connection cannot borrow ambient client credentials")
	}
	for key, value := range cfg.RuntimeParams {
		if key != "application_name" && value != "" {
			return nil, fmt.Errorf("managed test effective session has an unassigned parameter")
		}
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback.Host != ManagedTestHost || fallback.Port != 5432 || fallback.TLSConfig == nil || fallback.TLSConfig.InsecureSkipVerify || fallback.TLSConfig.ServerName != ManagedTestHost {
			return nil, fmt.Errorf("managed test transport permits an unassigned fallback")
		}
	}
	return cfg, nil
}

func (a ManagedTestAssignment) VerifyPin() error {
	pin, pinned, err := ExpectedDatabaseIdentityFromEnv()
	if err != nil || !pinned || pin.DatabaseName != a.Database || pin.SystemIdentifier != a.SystemIdentifier {
		return fmt.Errorf("managed test connection requires its exact physical/name identity pin")
	}
	return nil
}

func MigrationConfigFromEnv() (Config, error) {
	dsn := strings.TrimSpace(os.Getenv(EnvMigrationDatabaseURL))
	if os.Getenv(EnvManagedTestAssignment) == "" && dsn == "" {
		return ConfigFromEnv()
	}
	if dsn == "" {
		return Config{}, fmt.Errorf("separate migration database connection is required")
	}
	if os.Getenv(EnvManagedTestAssignment) != "" || isManagedTestDSN(dsn) {
		a, err := LoadManagedTestAssignment()
		if err != nil {
			return Config{}, err
		}
		return ManagedTestConfig(*a, dsn, ManagedTestOwner)
	}
	cfg := DefaultConfig()
	cfg.Backend = StoreBackendPostgres
	cfg.DSN = dsn
	cfg.DriverName = PostgresDriverName
	return cfg, nil
}

func ManagedTestConfig(a ManagedTestAssignment, dsn, role string) (Config, error) {
	if role != ManagedTestOwner && role != ManagedTestApp && role != ManagedTestProvider && role != ManagedTestIssuer {
		return Config{}, fmt.Errorf("managed test database role is not assigned")
	}
	if _, err := a.Connection(dsn, role); err != nil {
		return Config{}, err
	}
	if err := a.VerifyPin(); err != nil {
		return Config{}, err
	}
	cfg := DefaultConfig()
	cfg.Backend = StoreBackendPostgres
	cfg.DriverName = PostgresDriverName
	cfg.DSN = dsn
	cfg.managedTestRole = role
	return cfg, nil
}

func isManagedTestDSN(dsn string) bool {
	parsed, err := pgx.ParseConfig(dsn)
	return err == nil && parsed.Host == ManagedTestHost
}
