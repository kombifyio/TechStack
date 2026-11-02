package migrationclass

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var numberedName = regexp.MustCompile(`^([0-9]+)_[^/\\]+$`)

// LoadDir reads the forward migrations of one directory in ledger order:
// numeric prefix, then name. golang-migrate down files are skipped; other
// numbered files that are not SQL load as non-SQL migrations, which are
// contract.
func LoadDir(fsys fs.FS, format Format) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var migrations []Migration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		match := numberedName.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		id, sql, ok := migrationID(name, format)
		if !ok {
			continue
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, Migration{ID: id, Name: name, Version: version, Body: body, SQL: sql})
	}
	sort.SliceStable(migrations, func(i, j int) bool {
		if migrations[i].Version != migrations[j].Version {
			return migrations[i].Version < migrations[j].Version
		}
		return migrations[i].Name < migrations[j].Name
	})
	return migrations, nil
}

// golangMigrateName is golang-migrate's own file name parser: the last
// ".up." or ".down." before the extension decides the direction.
var golangMigrateName = regexp.MustCompile(`^([0-9]+)_(.*)\.(down|up)\.(.*)$`)

// migrationID returns the ledger ID of a numbered file and whether it is SQL;
// ok is false for files the runner never applies forward.
func migrationID(name string, format Format) (string, bool, bool) {
	if format == FormatGolangMigrate {
		match := golangMigrateName.FindStringSubmatch(name)
		if match == nil || match[3] == "down" {
			return "", false, false
		}
		return match[1] + "_" + match[2], match[4] == "sql", true
	}
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".sql") {
		return name[:len(name)-len(".sql")], true, true
	}
	if i := strings.IndexByte(name, '.'); i > 0 {
		return name[:i], false, true
	}
	return name, false, true
}

// Pending selects the migrations a production ledger does not hold yet, in
// ledger order. after (when not nil) is the highest applied version, as
// golang-migrate records it; applied lists ledger entries as file names, IDs
// or version numbers. A migration is pending when neither marks it applied.
func Pending(migrations []Migration, after *int64, applied []string) []Migration {
	done := map[string]bool{}
	for _, entry := range applied {
		if entry = strings.TrimSpace(entry); entry != "" && !strings.HasPrefix(entry, "#") {
			done[entry] = true
		}
	}
	var pending []Migration
	for _, migration := range migrations {
		if after != nil && migration.Version <= *after {
			continue
		}
		if done[migration.Name] || done[migration.ID] || done[strconv.FormatInt(migration.Version, 10)] && !duplicateVersion(migrations, migration.Version) {
			continue
		}
		pending = append(pending, migration)
	}
	return pending
}

func duplicateVersion(migrations []Migration, version int64) bool {
	count := 0
	for _, migration := range migrations {
		if migration.Version == version {
			count++
		}
	}
	return count > 1
}

// Receipt is the classifier evidence of a schema publication run or a pull
// request gate (the compatibility evidence of the database-migration profile).
type Receipt struct {
	Schema         string    `json:"schema"`
	Classifier     string    `json:"classifier"`
	Product        string    `json:"product,omitempty"`
	SHA            string    `json:"sha,omitempty"`
	Format         Format    `json:"format"`
	Directory      string    `json:"directory"`
	RuntimeRoles   []string  `json:"runtime_roles"`
	ExpandSuffix   string    `json:"expand_suffix,omitempty"`
	FunctionPrefix string    `json:"function_prefix,omitempty"`
	Migrations     []Verdict `json:"migrations"`
	Admitted       []string  `json:"admitted"`
	FirstRefused   *Verdict  `json:"first_refused,omitempty"`
	Held           []string  `json:"held"`
}

// NewReceipt builds the receipt of verdicts in ledger order.
func NewReceipt(rules Rules, directory, classifier string, verdicts []Verdict) Receipt {
	admitted, refused, held := Prefix(verdicts)
	receipt := Receipt{
		Schema:         "kombify.schema-classification.v1",
		Classifier:     classifier,
		Format:         rules.Format,
		Directory:      directory,
		RuntimeRoles:   append([]string{}, rules.RuntimeRoles...),
		ExpandSuffix:   rules.ExpandSuffix,
		FunctionPrefix: rules.FunctionPrefix,
		Migrations:     verdicts,
		Admitted:       []string{},
		FirstRefused:   refused,
		Held:           []string{},
	}
	for _, verdict := range admitted {
		receipt.Admitted = append(receipt.Admitted, verdict.ID)
	}
	for _, verdict := range held {
		receipt.Held = append(receipt.Held, verdict.ID)
	}
	return receipt
}
