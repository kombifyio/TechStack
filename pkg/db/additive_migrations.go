package db

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// AdditiveMigrationSuffix marks a migration whose SQL the allowlist below
// proves additive. The schema floor admits any newer ledger row; the marker
// keeps its stricter CI proof. Applied names never change.
const AdditiveMigrationSuffix = ".additive.sql"

var migrationNumberPattern = regexp.MustCompile(`^([0-9]+)_[a-z0-9_]+(?:\.additive)?\.sql$`)

func migrationNumber(version string) (int, bool) {
	match := migrationNumberPattern.FindStringSubmatch(version)
	if match == nil {
		return 0, false
	}
	number, err := strconv.Atoi(match[1])
	return number, err == nil
}

var (
	errAdditiveStatement = errors.New("statement is not allowed in an additive migration")

	identifierPart = `(?:"[^"]+"|[a-z_][a-z0-9_$]*)`
	qualifiedName  = identifierPart + `(?:\.` + identifierPart + `)?`

	createTablePattern    = regexp.MustCompile(`^create table (` + qualifiedName + `) ?\(`)
	createIndexPattern    = regexp.MustCompile(`^create (?:unique )?index (?:if not exists )?(?:` + identifierPart + ` )?on (?:only )?(` + qualifiedName + `)[ (]`)
	createSequencePattern = regexp.MustCompile(`^create sequence (?:if not exists )?` + qualifiedName + `(?: |$)`)
	createSchemaPattern   = regexp.MustCompile(`^create schema (?:if not exists )?` + identifierPart + `$`)
	createEnumPattern     = regexp.MustCompile(`^create type ` + qualifiedName + ` as enum ?\(`)
	createPolicyPattern   = regexp.MustCompile(`^create policy ` + identifierPart + ` on (` + qualifiedName + `)(?: |$)`)
	alterTablePattern     = regexp.MustCompile(`^alter table (?:if exists )?(?:only )?(` + qualifiedName + `) (.+)$`)
	addColumnPattern      = regexp.MustCompile(`^add column (?:if not exists )?` + identifierPart + ` (.+)$`)
	grantPattern          = regexp.MustCompile(`^grant [a-z, ()_]+ on `)
	setLocalPattern       = regexp.MustCompile(`^set local (?:lock_timeout|statement_timeout|search_path) `)
	searchPathPattern     = regexp.MustCompile(`^select pg_catalog\.set_config\('search_path', .+, true\)$`)
	constantDefault       = regexp.MustCompile(`^(?:null|true|false|-?[0-9]+(?:\.[0-9]+)?|'(?:[^']|'')*')(?:::` + qualifiedName + `(?:\[\])?)?$`)
	refusedColumnTerms    = regexp.MustCompile(`\b(?:unique|primary key|check|generated|collate)\b`)
)

// ValidateAdditiveMigrationSQL proves that a marked migration only adds
// objects an older binary ignores: it never rewrites, locks for a scan,
// narrows or removes anything the running code reads or writes. Indexes,
// row security, policies and ownership changes are limited to tables the same
// migration creates. Everything not on the allowlist is refused.
func ValidateAdditiveMigrationSQL(body string) error {
	statements, err := splitMigrationStatements(body)
	if err != nil {
		return err
	}
	if len(statements) == 0 {
		return errors.New("additive migration has no statements")
	}
	created := map[string]bool{}
	for _, statement := range statements {
		if err := validateAdditiveStatement(statement, created); err != nil {
			return fmt.Errorf("%w: %q", err, statement)
		}
	}
	return nil
}

func validateAdditiveStatement(statement string, created map[string]bool) error {
	switch {
	case setLocalPattern.MatchString(statement), searchPathPattern.MatchString(statement),
		createSequencePattern.MatchString(statement), createSchemaPattern.MatchString(statement),
		createEnumPattern.MatchString(statement), grantPattern.MatchString(statement),
		strings.HasPrefix(statement, "comment on "):
		return nil
	}
	// No IF NOT EXISTS: a same-named existing table must fail the migration
	// instead of unlocking indexes and policies on it below.
	if match := createTablePattern.FindStringSubmatch(statement); match != nil {
		created[relationName(match[1])] = true
		return nil
	}
	if match := createIndexPattern.FindStringSubmatch(statement); match != nil {
		return requireCreated(created, match[1])
	}
	if match := createPolicyPattern.FindStringSubmatch(statement); match != nil {
		return requireCreated(created, match[1])
	}
	if match := alterTablePattern.FindStringSubmatch(statement); match != nil {
		if created[relationName(match[1])] {
			return nil
		}
		return validateAddColumns(match[2])
	}
	return errAdditiveStatement
}

func requireCreated(created map[string]bool, relation string) error {
	if !created[relationName(relation)] {
		return errors.New("indexes and policies are allowed only on tables this migration creates")
	}
	return nil
}

// validateAddColumns admits ADD COLUMN on an existing table when the column is
// nullable or has a constant default: PostgreSQL then neither rewrites nor
// scans the table and older INSERTs that omit the column keep working.
func validateAddColumns(actions string) error {
	for _, action := range splitTopLevel(actions, ',') {
		match := addColumnPattern.FindStringSubmatch(strings.TrimSpace(action))
		if match == nil {
			return errAdditiveStatement
		}
		definition := match[1]
		if refusedColumnTerms.MatchString(definition) {
			return errors.New("new columns on existing tables cannot carry constraints or generated values")
		}
		hasDefault := false
		if index := strings.Index(definition, " default "); index >= 0 {
			value := definition[index+len(" default "):]
			value = strings.TrimSpace(strings.Replace(value, " not null", "", 1))
			if !constantDefault.MatchString(value) {
				return errors.New("a column default on an existing table must be a constant")
			}
			hasDefault = true
		}
		if strings.Contains(definition, "not null") && !hasDefault {
			return errors.New("a NOT NULL column on an existing table needs a constant default")
		}
	}
	return nil
}

func relationName(name string) string {
	name = strings.ReplaceAll(name, `"`, "")
	return strings.TrimPrefix(name, "public.")
}

// splitMigrationStatements removes comments, refuses dollar quoting (function
// bodies and DO blocks are never additive) and returns lower-cased statements
// with collapsed whitespace. String literals keep their case and semicolons.
func splitMigrationStatements(body string) ([]string, error) {
	var statements []string
	var current strings.Builder
	flush := func() {
		statement := strings.Join(strings.Fields(current.String()), " ")
		if statement != "" {
			statements = append(statements, statement)
		}
		current.Reset()
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '-' && i+1 < len(body) && body[i+1] == '-':
			for i < len(body) && body[i] != '\n' {
				i++
			}
			current.WriteByte(' ')
		case c == '/' && i+1 < len(body) && body[i+1] == '*':
			end := strings.Index(body[i+2:], "*/")
			if end < 0 {
				return nil, errors.New("unterminated block comment")
			}
			i += end + 3
			current.WriteByte(' ')
		case c == '$':
			return nil, errors.New("dollar quoting is not allowed in an additive migration")
		case c == '\'':
			end := i + 1
			for ; end < len(body); end++ {
				if body[end] == '\'' {
					if end+1 < len(body) && body[end+1] == '\'' {
						end++
						continue
					}
					break
				}
			}
			if end >= len(body) {
				return nil, errors.New("unterminated string literal")
			}
			current.WriteString(body[i : end+1])
			i = end
		case c == ';':
			flush()
		default:
			current.WriteString(strings.ToLower(string(c)))
		}
	}
	flush()
	return statements, nil
}

func splitTopLevel(value string, separator byte) []string {
	var parts []string
	depth, start, quoted := 0, 0, false
	for i := 0; i < len(value); i++ {
		switch c := value[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == separator && depth == 0:
			parts = append(parts, value[start:i])
			start = i + 1
		}
	}
	return append(parts, value[start:])
}
