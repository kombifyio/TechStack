// Package migrationclass is the one shared schema migration classifier of
// workspace CI-CD-PLATFORM-STANDARD.md §4.6 "Schema publication on merge"
// (D-2026-10-24-05). It decides whether a PostgreSQL migration is expand
// (running code survives it, so it may reach production on merge) or contract
// (it ships with the release).
//
// The allowlist is closed: a migration is expand only when every statement is
// one of the forms below. Anything unlisted or unreadable is contract, and so
// are non-SQL migrations and files carrying the marker
// "-- kombify:migration contract". Product rules (Rules.ExpandSuffix) can only
// narrow the list.
//
//   - CREATE TABLE of a new table (never IF NOT EXISTS, AS, PARTITION OF,
//     INHERITS or LIKE, never a pg_ name) whose name no earlier migration gives
//     a table, view, sequence, type or routine in any schema: a new table in a
//     schema earlier on the search path would shadow the existing one for
//     running code. A table with a tenant or user column
//     also enables and forces row level security in the same migration, and
//     each of its permissive policies holds the conjunct
//     "<tenant column> = <context function call>". Statements that only change
//     objects the same migration creates are part of creating them: indexes,
//     constraints, comments, row security, policies and triggers. Ownership,
//     renames, drops, partition attachment, inheritance and rows written
//     during the migration are not. Their expressions call only built-in pure
//     functions (pureFunctions) and cast only to built-in types, because
//     PostgreSQL plans some of them, and names compare exactly as written:
//     "x" and "public.x" can be different tables.
//   - ADD COLUMN on an existing table of a built-in type, nullable or with a
//     constant default, with no constraint, generated value, identity, serial
//     type, reference or collation, and never a tenant column: the table would
//     become a tenant table without row security. A second spelling of a table
//     the same migration creates ("public.x" for "x") is refused, because both
//     can name it.
//   - A named, non-unique CREATE INDEX CONCURRENTLY of plain columns, with a
//     predicate of column comparisons, alone in its file outside a
//     transaction (goose "-- +goose NO TRANSACTION", AI Platform
//     "-- kombify:migration no-transaction"); the verdict names the index for
//     the invalid-index readback.
//   - CREATE FUNCTION (never OR REPLACE, never SECURITY DEFINER, only LANGUAGE
//     sql or plpgsql) of a name that starts with the product's function prefix
//     (Rules.FunctionPrefix; without a valid one every new function is
//     contract) and that no earlier migration uses. PostgreSQL resolves a call
//     by argument types across every schema on the search path, so a new
//     function that shares a name with a built-in or extension function can
//     take over existing calls; the prefix keeps new names apart from them.
//   - Policies, triggers, comments and grants only on objects the same
//     migration creates; grants only on tables and schema-qualified functions
//     (with the argument types they were created with, when named), only to
//     the declared runtime roles, never PUBLIC, never WITH GRANT OPTION.
//   - One backfill UPDATE of an existing table that sets only columns the same
//     migration adds, to non-NULL values without a subquery, of the shape
//     UPDATE ONLY t SET c = <expr> WHERE c IS NULL AND ctid IN (SELECT ctid FROM ONLY t [WHERE <column comparisons>] [ORDER BY <columns>] LIMIT n)
//     with every set column guarded, n at most MaxBackfillBatch and only
//     unqualified columns in the subquery, so it cannot be correlated with the
//     updated row, calling only built-in pure functions and casting only to
//     built-in types. A backfill still fires the table's existing triggers,
//     rules and publications; the classifier cannot see them.
//   - At most one existing table per migration is changed, backfilled or
//     referenced by a foreign key of a new table, because each lock is held
//     until the migration commits and further lock waits would add up.
//   - SET LOCAL lock_timeout (1 ms to MaxLockTimeoutMillis) and SET LOCAL
//     statement_timeout (1 ms to MaxStatementTimeoutMillis), which every
//     transactional migration sets before its first other statement.
//
// Expressions cast and type literals ('x'::t, CAST, t 'x') only to built-in
// types, because a type's input or cast function runs when PostgreSQL reads
// the statement. Identifiers longer than PostgreSQL's 63 bytes are refused,
// and history names compare as PostgreSQL truncates them.
//
// The rules assume the migration role's search path keeps pg_catalog first,
// as PostgreSQL does unless a role or database setting lists it later, so an
// unqualified built-in name resolves to the built-in. An applied migration the
// classifier cannot read, or a non-SQL one, makes the history unknown, and
// every later function call, new function or new table is contract.
package migrationclass

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Version identifies the rule set; receipts carry it so a changed rule is
// visible in the evidence.
const Version = "2"

const (
	// MaxBackfillBatch bounds the rows one backfill UPDATE may touch.
	MaxBackfillBatch = 10000
	// MaxBackfills bounds the backfill UPDATEs of one migration; a larger
	// backfill is a series of migrations, each committing its batch.
	MaxBackfills = 1
	// MaxExistingTables bounds the existing tables one migration locks.
	MaxExistingTables = 1
	// MaxIdentifierBytes is PostgreSQL's identifier length (NAMEDATALEN - 1).
	MaxIdentifierBytes = 63
	// MaxLockTimeoutMillis bounds how long a migration may wait for a lock.
	MaxLockTimeoutMillis = 60_000
	// MaxStatementTimeoutMillis bounds how long one migration statement may run.
	MaxStatementTimeoutMillis = 900_000
)

// Class is the verdict of a migration.
type Class string

const (
	Expand   Class = "expand"
	Contract Class = "contract"
)

// Format names how a product's migration runner reads its files.
type Format string

const (
	// FormatGolangMigrate: NNN_name.up.sql files; down files never run forward.
	FormatGolangMigrate Format = "golang-migrate"
	// FormatGoose: NNN_name.sql with "-- +goose Up" and "-- +goose Down" sections
	// (pressly/goose v3 annotation rules), each file one transaction unless
	// "-- +goose NO TRANSACTION".
	FormatGoose Format = "goose"
	// FormatPlain: NNN_name.sql, the whole file runs as one transaction.
	FormatPlain Format = "plain"
)

// ParseFormat reads a format name.
func ParseFormat(name string) (Format, error) {
	switch format := Format(strings.ToLower(strings.TrimSpace(name))); format {
	case FormatGolangMigrate, FormatGoose, FormatPlain:
		return format, nil
	}
	return "", fmt.Errorf("unknown migration format %q (golang-migrate, goose, plain)", name)
}

// DefaultTenantColumns name the columns that make a new table a tenant or user
// table (D-2026-10-22-02). Rules.TenantColumns adds product names to them.
// Names compare without case, so a quoted variant still counts.
var DefaultTenantColumns = []string{
	"tenant_id", "org_id", "organization_id", "workspace_id", "account_id",
	"team_id", "user_id", "user_sub", "owner_id", "owner_sub", "member_id",
	"customer_id", "subject_id", "principal_id",
}

// Rules are one product's classifier settings. They can only narrow the
// shared allowlist.
type Rules struct {
	Format Format
	// RuntimeRoles are the only roles a grant may name, as PostgreSQL stores
	// them. Without any, every grant is contract.
	RuntimeRoles []string
	// TenantColumns are added to DefaultTenantColumns.
	TenantColumns []string
	// ExpandSuffix, when set, makes every file without this name suffix
	// contract (Techstack: ".additive.sql").
	ExpandSuffix string
	// FunctionPrefix is the product's own name prefix for new functions, for
	// example "paperwork_" (lower-case letters, digits and underscores, ending
	// in "_", at least 4 bytes, never "pg_"). No built-in or extension
	// function may start with it. Without a valid prefix every new function is
	// contract.
	FunctionPrefix string
}

var functionPrefixShape = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*_$`)

// builtinFamilies are first name segments of PostgreSQL built-in functions
// (json_each, string_agg, regexp_replace, ...); a prefix starting with one of
// them would not keep new names apart.
var builtinFamilies = map[string]bool{}

func init() {
	for _, family := range strings.Fields(`pg json jsonb array string regexp date to ts lo has gen generate get set current is make
		range multirange row num txid uuid inet cidr interval justify quote query schema table var spg gin gist brin btree
		hash enum cash mxid cursor websearch phraseto plainto satisfies similar regr covar octet first last col obj clock
		trim text time timestamp timestamptz timetz binary convert format percentile percent dense cume xml network split
		starts tsvector tsquery statement transaction bit bool char int2 int4 int8 float4 float8 numeric macaddr macaddr8
		box line lseg path point polygon circle tid xid xid8 oid reg regclass regtype regproc any anyarray anyelement lower
		upper isempty bound area width height acl host bytea name money record trigger event internal cstring unknown void
		fdw tsm index heap sha224 sha256 sha384 sha512 md5 encode decode overlay ascii chr substring substr position ltrim
		rtrim btrim lpad rpad repeat replace reverse translate initcap concat left right length unistr parse unnest age now`) {
		builtinFamilies[family] = true
	}
}

// ValidFunctionPrefix reports whether prefix can keep new function names
// apart from built-in ones.
func ValidFunctionPrefix(prefix string) bool {
	return len(prefix) >= 4 && len(prefix) < MaxIdentifierBytes && functionPrefixShape.MatchString(prefix) &&
		!builtinFamilies[strings.SplitN(prefix, "_", 2)[0]]
}

// HistoryRelationPrefix marks a relation name in the history map Classify
// takes; a key without it is a routine name.
const HistoryRelationPrefix = "relation:"

// Migration is one file of a product's migration directory.
type Migration struct {
	// ID is the file name without its runner extension (.up.sql, .sql).
	ID string
	// Name is the file name.
	Name string
	// Version is the numeric prefix of the name.
	Version int64
	Body    []byte
	// SQL is false for a numbered migration the classifier cannot read as SQL
	// (a Go migration, a recipe step).
	SQL bool
}

// Verdict is the classification of one migration. A contract verdict names
// the first refused statement, its line and the rule it breaks.
type Verdict struct {
	ID            string   `json:"id"`
	File          string   `json:"file"`
	SHA256        string   `json:"sha256"`
	Class         Class    `json:"class"`
	Code          string   `json:"code,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	Line          int      `json:"line,omitempty"`
	Statement     string   `json:"statement,omitempty"`
	IndexReadback []string `json:"index_readback,omitempty"`
}

// ClassifyAll classifies migrations in ledger order. Each migration sees the
// routine and relation names of all migrations before it, so CREATE FUNCTION
// and CREATE TABLE are expand only for a name that is new to the history.
func (r Rules) ClassifyAll(migrations []Migration) []Verdict {
	verdicts := make([]Verdict, 0, len(migrations))
	known := map[string]bool{}
	for _, migration := range migrations {
		verdicts = append(verdicts, r.Classify(migration, known))
		if known == nil {
			continue
		}
		if !migration.SQL {
			// A Go migration or a recipe step may create any routine or table.
			known = nil
			continue
		}
		routines, relations, complete := historyNames(string(migration.Body))
		if !complete {
			// An unreadable applied migration may define any routine, so every
			// later migration has an unknown history.
			known = nil
			continue
		}
		for _, name := range routines {
			known[name] = true
		}
		for _, name := range relations {
			known[HistoryRelationPrefix+name] = true
		}
	}
	return verdicts
}

// Classify classifies one migration. knownFunctions holds the routine names
// and, with HistoryRelationPrefix, the relation names of every earlier
// migration (lower-cased, truncated as PostgreSQL truncates identifiers); nil
// means the history is unknown, which makes any CREATE FUNCTION, CREATE TABLE
// and function call contract.
func (r Rules) Classify(migration Migration, knownFunctions map[string]bool) Verdict {
	sum := sha256.Sum256(migration.Body)
	verdict := Verdict{ID: migration.ID, File: migration.Name, SHA256: hex.EncodeToString(sum[:]), Class: Expand}
	readback, refused := r.classify(migration, knownFunctions)
	if refused != nil {
		verdict.Class = Contract
		verdict.Code = refused.code
		verdict.Reason = refused.reason
		verdict.Line = refused.line
		verdict.Statement = refused.statement
		return verdict
	}
	verdict.IndexReadback = readback
	return verdict
}

// Prefix splits verdicts in ledger order into the admitted prefix, the first
// refused migration (nil when all are expand) and the migrations held behind it.
func Prefix(verdicts []Verdict) (admitted []Verdict, refused *Verdict, held []Verdict) {
	for i := range verdicts {
		if verdicts[i].Class != Expand {
			return verdicts[:i], &verdicts[i], verdicts[i+1:]
		}
	}
	return verdicts, nil, nil
}

type refusal struct {
	code, reason, statement string
	line                    int
}

func refuse(code string, s *statement, format string, args ...any) *refusal {
	r := &refusal{code: code, reason: fmt.Sprintf(format, args...)}
	if s != nil {
		r.line = s.line
		r.statement = abbreviate(s.source)
	}
	return r
}

var (
	contractMarker = regexp.MustCompile(`(?im)^[ \t]*--[ \t]*kombify:migration[ \t]+contract\b`)
	// noTransactionMarker is the AI Platform runner's line-1 marker for a file
	// it applies statement by statement outside a transaction. Found on any
	// line it only restricts: such a file may hold one concurrent index.
	noTransactionMarker = regexp.MustCompile(`(?im)^[ \t]*--[ \t]*kombify:migration[ \t]+no-transaction\b`)
)

func (r Rules) classify(migration Migration, knownFunctions map[string]bool) ([]string, *refusal) {
	statements, noTransaction, refused := r.readStatements(migration)
	if refused != nil {
		return nil, refused
	}
	explicit := false
	if isTransactionStart(statements[0]) {
		if len(statements) < 2 || !isTransactionEnd(statements[len(statements)-1]) {
			return nil, refuse("transaction_control", &statements[0], "BEGIN without a final COMMIT")
		}
		statements, explicit = statements[1:len(statements)-1], true
	}
	for i := range statements {
		if isTransactionControl(statements[i]) {
			return nil, refuse("transaction_control", &statements[i], "transaction control inside a migration")
		}
	}
	if len(statements) == 1 && !explicit && isConcurrentIndex(statements[0]) {
		if r.Format == FormatGoose && !noTransaction {
			return nil, refuse("concurrent_index_in_transaction", &statements[0], "CREATE INDEX CONCURRENTLY needs -- +goose NO TRANSACTION")
		}
		name, refused := concurrentIndex(&statements[0])
		if refused != nil {
			return nil, refused
		}
		return []string{name}, nil
	}
	if noTransaction {
		return nil, refuse("no_transaction", &statements[0], "a migration outside a transaction may hold only one CREATE INDEX CONCURRENTLY")
	}
	if len(statements) == 0 {
		return nil, refuse("empty", nil, "the migration has no statement inside its transaction")
	}
	return nil, r.transaction(statements, knownFunctions)
}

// readStatements applies the file-level rules and returns the statements the
// runner applies forward and whether they run outside a transaction.
func (r Rules) readStatements(migration Migration) ([]statement, bool, *refusal) {
	if !migration.SQL {
		return nil, false, refuse("not_sql", nil, "a non-SQL migration cannot be proved expand")
	}
	if r.ExpandSuffix != "" && !strings.HasSuffix(migration.Name, r.ExpandSuffix) {
		return nil, false, refuse("product_rule", nil, "the product admits only %s files as expand", r.ExpandSuffix)
	}
	body := string(migration.Body)
	if match := contractMarker.FindStringIndex(body); match != nil {
		return nil, false, refuse("marked_contract", nil, "the file is marked -- kombify:migration contract (line %d)", strings.Count(body[:match[0]], "\n")+1)
	}
	section, firstLine, noTransaction, refused := r.forwardSection(body)
	if refused != nil {
		return nil, false, refused
	}
	noTransaction = noTransaction || noTransactionMarker.MatchString(body)
	statements, err := lexStatements(section, firstLine)
	if err != nil {
		return nil, false, refuse("unparseable", nil, "%v", err)
	}
	if len(statements) == 0 {
		return nil, false, refuse("empty", nil, "the migration has no statement")
	}
	return statements, noTransaction, nil
}

// transaction classifies the statements of one transactional migration.
func (r Rules) transaction(statements []statement, knownFunctions map[string]bool) *refusal {
	state := newFileState(r, knownFunctions)
	firstWork, lockAt, statementAt := -1, -1, -1
	for i := range statements {
		s := &statements[i]
		if !s.tokens[0].is("set") {
			if firstWork < 0 {
				firstWork = i
			}
			if refused := state.statement(s); refused != nil {
				return refused
			}
			continue
		}
		setting, refused := setLocal(s)
		if refused != nil {
			return refused
		}
		lockAt, statementAt = firstTimeout(setting, "lock_timeout", lockAt, i), firstTimeout(setting, "statement_timeout", statementAt, i)
	}
	if firstWork < 0 {
		return refuse("empty", nil, "the migration only sets timeouts")
	}
	if lockAt < 0 || lockAt > firstWork || statementAt < 0 || statementAt > firstWork {
		return refuse("timeouts_missing", &statements[firstWork], "the transaction must SET LOCAL lock_timeout and statement_timeout before its first statement")
	}
	return state.tenantTablesIsolated()
}

func firstTimeout(setting, name string, seen, at int) int {
	if setting == name && seen < 0 {
		return at
	}
	return seen
}

// forwardSection returns the part of the file the runner applies forward, the
// line it starts on and whether it runs outside a transaction.
func (r Rules) forwardSection(body string) (string, int, bool, *refusal) {
	switch r.Format {
	case FormatGoose:
		return gooseUpSection(body)
	case FormatGolangMigrate, FormatPlain:
		return body, 1, false, nil
	}
	return "", 0, false, refuse("format", nil, "unknown migration format %q", r.Format)
}

var gooseAnnotations = []string{"Up", "Down", "StatementBegin", "StatementEnd", "NO TRANSACTION", "ENVSUB ON", "ENVSUB OFF"}

// gooseUpSection reads goose annotations the way pressly/goose v3.28 does: a
// comment line containing the exact "+goose" is an annotation, whose command
// compares without case. A comment that only looks like an annotation in
// another spelling is refused, because goose would run it as SQL.
func gooseUpSection(body string) (string, int, bool, *refusal) {
	lines := strings.SplitAfter(body, "\n")
	start, end, noTransaction := -1, len(lines), false
	for i, line := range lines {
		text := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		trimmed := strings.TrimSpace(text)
		if !strings.HasPrefix(trimmed, "--") || !strings.Contains(strings.ToLower(text), "+goose") {
			continue
		}
		command, ok := gooseAnnotation(text)
		if !ok {
			return "", 0, false, refuse("goose_annotation", nil, "line %d is not a goose annotation goose reads the same way: %q", i+1, trimmed)
		}
		switch command {
		case "Up":
			if start >= 0 {
				return "", 0, false, refuse("goose_annotation", nil, "line %d repeats -- +goose Up", i+1)
			}
			start = i + 1
		case "Down":
			if start < 0 {
				return "", 0, false, refuse("goose_up_missing", nil, "-- +goose Down comes before -- +goose Up")
			}
			if end == len(lines) {
				end = i
			}
		case "NO TRANSACTION":
			noTransaction = true
		case "ENVSUB ON":
			return "", 0, false, refuse("goose_envsub", nil, "environment substitution makes the applied SQL unknowable")
		}
	}
	if start < 0 {
		return "", 0, false, refuse("goose_up_missing", nil, "the file has no -- +goose Up section")
	}
	return strings.Join(lines[start:end], ""), start + 1, noTransaction, nil
}

func gooseAnnotation(line string) (string, bool) {
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || !strings.Contains(line, "+goose") {
		return "", false
	}
	command := strings.Replace(strings.ReplaceAll(line, "--", ""), "+goose", "", 1)
	if strings.Contains(command, "+goose") {
		return "", false
	}
	command = strings.TrimSpace(command)
	for _, annotation := range gooseAnnotations {
		if strings.EqualFold(annotation, command) {
			return annotation, true
		}
	}
	return "", false
}

func isTransactionStart(s statement) bool {
	t := s.tokens
	return t[0].is("begin") || len(t) > 1 && t[0].is("start") && t[1].is("transaction")
}

func isTransactionEnd(s statement) bool {
	t := s.tokens
	return (t[0].is("commit") || t[0].is("end")) && (len(t) == 1 || len(t) == 2 && (t[1].is("transaction") || t[1].is("work")))
}

func isTransactionControl(s statement) bool {
	switch first := s.tokens[0]; {
	case first.is("begin"), first.is("commit"), first.is("end"), first.is("rollback"), first.is("abort"),
		first.is("savepoint"), first.is("release"), first.is("prepare"):
		return true
	case first.is("start") && len(s.tokens) > 1 && s.tokens[1].is("transaction"):
		return true
	}
	return false
}

func isConcurrentIndex(s statement) bool {
	c := newCursor(s.tokens)
	if !c.words("create") {
		return false
	}
	c.words("unique")
	return c.words("index", "concurrently")
}

// concurrentIndex admits a lone named, non-unique CREATE INDEX CONCURRENTLY of
// plain columns with a simple predicate. An expression could fail on a row
// the running code writes later, so the index would reject that write.
func concurrentIndex(s *statement) (string, *refusal) {
	c := newCursor(s.tokens)
	c.words("create")
	if c.words("unique") {
		return "", refuse("unique_index_on_existing_table", s, "a unique index on an existing table can fail on existing rows and narrows writes")
	}
	c.words("index", "concurrently")
	c.words("if", "not", "exists")
	if c.peekWord("on") {
		return "", refuse("index_name_missing", s, "CREATE INDEX CONCURRENTLY needs an explicit name for the invalid-index readback")
	}
	name, ok := c.object()
	if !ok || !c.words("on") {
		return "", refuse("unparseable", s, "unreadable CREATE INDEX")
	}
	c.words("only")
	if _, ok := c.object(); !ok {
		return "", refuse("unparseable", s, "unreadable index table")
	}
	if !plainIndex(c) {
		return "", refuse("index_expression", s, "a concurrent index on an existing table indexes plain columns with a predicate of column comparisons; an expression can reject later writes")
	}
	return name.relation(), nil
}

var indexMethods = setOf("btree", "hash", "gist", "spgist", "gin", "brin")

// plainIndex reads [USING method] (column [opclass] [ASC|DESC] [NULLS
// FIRST|LAST], ...) [INCLUDE (column, ...)] [WHERE comparison AND ...].
func plainIndex(c *cursor) bool {
	if c.words("using") && !indexMethods[c.next().text] {
		return false
	}
	columns, ok := c.group()
	if !ok || !indexColumns(columns) {
		return false
	}
	if c.words("include") {
		included, ok := c.group()
		if !ok || !indexColumns(included) {
			return false
		}
	}
	if c.done() {
		return true
	}
	if !c.words("where") {
		return false
	}
	for _, conjunct := range splitTopWord(c.rest(), "and") {
		if !columnComparison(conjunct) {
			return false
		}
	}
	return true
}

func indexColumns(tokens []token) bool {
	for _, element := range splitTop(tokens) {
		e := newCursor(element)
		if _, ok := canonicalPart(e.next()); !ok {
			return false
		}
		if t := e.peek(); t.kind == tokWord && !t.is("asc") && !t.is("desc") && !t.is("nulls") {
			e.next()
		}
		if !e.words("asc") {
			e.words("desc")
		}
		if !e.words("nulls", "first") {
			e.words("nulls", "last")
		}
		if !e.done() {
			return false
		}
	}
	return true
}

// columnComparison admits "column IS [NOT] NULL" and "column <op> literal".
func columnComparison(tokens []token) bool {
	c := newCursor(tokens)
	if _, ok := canonicalPart(c.next()); !ok {
		return false
	}
	if c.words("is") {
		c.words("not")
		return c.words("null") && c.done()
	}
	operator := ""
	for !c.done() && c.peek().kind == tokPunct && strings.Contains("=<>!", c.peek().text) && len(operator) < 2 {
		operator += c.next().text
	}
	switch operator {
	case "=", "<>", "!=", "<", ">", "<=", ">=":
	default:
		return false
	}
	isNull, ok := defaultValue(c.rest())
	return ok && !isNull
}

// setLocal admits SET LOCAL lock_timeout and statement_timeout with a
// bounded value and names the setting.
func setLocal(s *statement) (string, *refusal) {
	c := newCursor(s.tokens)
	c.words("set")
	if !c.words("local") {
		return "", refuse("not_on_allowlist", s, "a session SET outlives the migration; only SET LOCAL timeouts are allowed")
	}
	setting := c.next()
	limit := 0.0
	switch {
	case setting.is("lock_timeout"):
		limit = MaxLockTimeoutMillis
	case setting.is("statement_timeout"):
		limit = MaxStatementTimeoutMillis
	default:
		return "", refuse("not_on_allowlist", s, "only SET LOCAL lock_timeout and statement_timeout are allowed")
	}
	if !c.punct("=") && !c.words("to") {
		return "", refuse("unparseable", s, "unreadable SET LOCAL")
	}
	millis, ok := durationMillis(c.next())
	if !c.done() || !ok || millis < 1 || millis > limit {
		return "", refuse("timeout_value", s, "%s needs one value from 1ms to %.0fms", setting.text, limit)
	}
	return setting.text, nil
}

var (
	timeoutValue = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*(us|ms|s|min|h|d)?$`)
	timeoutUnits = map[string]float64{"us": 0.001, "": 1, "ms": 1, "s": 1000, "min": 60_000, "h": 3_600_000, "d": 86_400_000}
)

// durationMillis reads a timeout value as PostgreSQL does: a bare number is
// milliseconds.
func durationMillis(value token) (float64, bool) {
	text := value.text
	switch value.kind {
	case tokString:
		if !strings.HasPrefix(text, "'") {
			return 0, false
		}
		text = strings.TrimSpace(strings.Trim(text, "'"))
	case tokNumber:
	default:
		return 0, false
	}
	match := timeoutValue.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	number, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, false
	}
	return number * timeoutUnits[match[2]], true
}

const historyName = `((?:"[^"]+"|[a-z_][a-z0-9_$]*)(?:\s*\.\s*(?:"[^"]+"|[a-z_][a-z0-9_$]*)){0,2})`

var (
	routineMention  = regexp.MustCompile(`\b(?:function|procedure|routine|aggregate)\s+(?:if\s+exists\s+)?` + historyName + `\s*\(`)
	relationMention = regexp.MustCompile(`\b(?:create\s+(?:or\s+replace\s+)?(?:(?:global|local)\s+)?(?:temp\s+|temporary\s+|unlogged\s+|recursive\s+)?` +
		`(?:table|view|materialized\s+view|sequence|type|domain|foreign\s+table)|into)\s+(?:if\s+not\s+exists\s+)?` + historyName)
	renameMention = regexp.MustCompile(`\brename\s+to\s+` + historyName)
)

// relationKinds are the objects whose names a new table could shadow or
// collide with.
var relationKinds = setOf("table", "view", "sequence", "type", "domain")

// historyNames returns the lower-cased bare names, truncated as PostgreSQL
// truncates identifiers, of every function, procedure, routine or aggregate
// (routines) and every table, view, sequence or type (relations) a migration
// body mentions; a name something is renamed to counts as both. It reads them
// by tokens (comments cannot split a mention) and by raw text (mentions inside
// strings and function bodies count too), so an overload or a shadow of an
// existing name is never mistaken for a new object. complete is false when
// the body cannot be read, so the names may be missing some. A name built at
// run time (EXECUTE format(...)) stays invisible.
func historyNames(body string) (routines, relations []string, complete bool) {
	lower := strings.ToLower(body)
	for _, mention := range []struct {
		pattern *regexp.Regexp
		lists   []*[]string
	}{
		{routineMention, []*[]string{&routines}},
		{relationMention, []*[]string{&relations}},
		{renameMention, []*[]string{&routines, &relations}},
	} {
		for _, match := range mention.pattern.FindAllStringSubmatch(lower, -1) {
			parts := strings.Split(match[1], ".")
			for _, list := range mention.lists {
				*list = append(*list, clipIdentifier(strings.Trim(strings.TrimSpace(parts[len(parts)-1]), `"`)))
			}
		}
	}
	statements, err := lexHistory(body)
	if err != nil {
		return routines, relations, false
	}
	for _, s := range statements {
		for i := range s.tokens {
			routine, relation := historyMention(s.tokens, i)
			if routine != "" {
				routines = append(routines, routine)
			}
			if relation != "" {
				relations = append(relations, relation)
			}
		}
	}
	return routines, relations, true
}

// historyMention reads the routine or relation a statement names at token i:
// FUNCTION|PROCEDURE|ROUTINE|AGGREGATE name (, CREATE ... TABLE|VIEW|SEQUENCE|
// TYPE|DOMAIN name, INTO name, or RENAME TO name (both).
func historyMention(tokens []token, i int) (routine, relation string) {
	t, c := tokens[i], &cursor{tokens: tokens, at: i + 1}
	switch {
	case t.is("function"), t.is("procedure"), t.is("routine"), t.is("aggregate"):
		c.words("if", "exists")
		if name, ok := c.object(); ok && c.peekPunct("(") {
			return historyBare(name), ""
		}
	case t.is("create"):
		return "", createdRelation(c)
	case t.is("into"):
		skipWords(c, "temp", "temporary", "unlogged", "table")
		if name, ok := c.object(); ok {
			return "", historyBare(name)
		}
	case t.is("rename") && c.words("to"):
		if name, ok := c.object(); ok {
			return historyBare(name), historyBare(name)
		}
	}
	return "", ""
}

// createdRelation reads the relation a CREATE statement names after its
// modifiers, or "".
func createdRelation(c *cursor) string {
	c.words("or", "replace")
	skipWords(c, "global", "local", "temp", "temporary", "unlogged", "recursive", "materialized", "foreign")
	if t := c.peek(); t.kind != tokWord || !relationKinds[t.text] {
		return ""
	}
	c.next()
	c.words("if", "not", "exists")
	if name, ok := c.object(); ok {
		return historyBare(name)
	}
	return ""
}

// skipWords consumes any run of the given words.
func skipWords(c *cursor, words ...string) {
	for slices.ContainsFunc(words, func(word string) bool { return c.words(word) }) {
	}
}

func historyBare(name objectName) string {
	return clipIdentifier(strings.ToLower(strings.Trim(name.last(), `"`)))
}

// clipIdentifier truncates a name to MaxIdentifierBytes on a character
// boundary, as PostgreSQL does with a UTF-8 database.
func clipIdentifier(name string) string {
	if len(name) <= MaxIdentifierBytes {
		return name
	}
	end := MaxIdentifierBytes
	for end > 0 && !utf8.RuneStart(name[end]) {
		end--
	}
	return name[:end]
}

func abbreviate(source string) string {
	const limit = 200
	if len(source) <= limit {
		return source
	}
	return source[:limit] + "..."
}
