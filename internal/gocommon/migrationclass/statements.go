package migrationclass

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// cursor walks the tokens of one statement.
type cursor struct {
	tokens []token
	at     int
}

func newCursor(tokens []token) *cursor { return &cursor{tokens: tokens} }

func (c *cursor) done() bool { return c.at >= len(c.tokens) }

func (c *cursor) peek() token {
	if c.done() {
		return token{kind: tokPunct}
	}
	return c.tokens[c.at]
}

func (c *cursor) next() token {
	t := c.peek()
	if !c.done() {
		c.at++
	}
	return t
}

func (c *cursor) peekWord(word string) bool { return c.peek().is(word) }

func (c *cursor) peekPunct(p string) bool { return !c.done() && c.peek().isPunct(p) }

// words consumes the exact word sequence, or nothing.
func (c *cursor) words(sequence ...string) bool {
	if c.at+len(sequence) > len(c.tokens) {
		return false
	}
	for i, word := range sequence {
		if !c.tokens[c.at+i].is(word) {
			return false
		}
	}
	c.at += len(sequence)
	return true
}

func (c *cursor) punct(p string) bool {
	if c.peekPunct(p) {
		c.at++
		return true
	}
	return false
}

// objectName is a possibly qualified name as canonical parts: an unquoted
// part lower-cased, a quoted part kept quoted unless it reads the same
// unquoted, so two different PostgreSQL names never compare equal.
type objectName struct{ parts []string }

// relation is the name exactly as written: "x" and "public.x" stay apart,
// because the search path can resolve "x" to another schema.
func (n objectName) relation() string { return strings.Join(n.parts, ".") }

// catalogName reports a name that can resolve to a pg_catalog relation, which
// the search path finds before any created table, or that lives in a pg_
// schema such as pg_temp, which the migration session searches first.
func (n objectName) catalogName() bool {
	for _, part := range n.parts {
		if strings.HasPrefix(strings.Trim(part, `"`), "pg_") {
			return true
		}
	}
	return false
}

func (n objectName) last() string { return n.parts[len(n.parts)-1] }

var plainIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_$]*$`)

// canonicalPart renders one identifier token as PostgreSQL resolves it.
func canonicalPart(t token) (string, bool) {
	switch {
	case t.kind == tokWord, t.kind == tokQuoted && plainIdentifier.MatchString(t.text):
		return t.text, true
	case t.kind == tokQuoted:
		return `"` + strings.ReplaceAll(t.text, `"`, `""`) + `"`, true
	}
	return "", false
}

func (c *cursor) object() (objectName, bool) {
	var name objectName
	for {
		part, ok := canonicalPart(c.peek())
		if c.done() || !ok {
			return objectName{}, false
		}
		c.at++
		name.parts = append(name.parts, part)
		if !c.punct(".") {
			return name, true
		}
	}
}

// group consumes a parenthesized group and returns its inner tokens.
func (c *cursor) group() ([]token, bool) {
	if !c.punct("(") {
		return nil, false
	}
	start, depth := c.at, 1
	for !c.done() {
		switch t := c.next(); {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
			if depth == 0 {
				return c.tokens[start : c.at-1], true
			}
		}
	}
	return nil, false
}

func (c *cursor) rest() []token { return c.tokens[c.at:] }

func opens(t token) bool  { return t.isPunct("(") || t.isPunct("[") }
func closes(t token) bool { return t.isPunct(")") || t.isPunct("]") }

// splitTop splits tokens on top-level commas.
func splitTop(tokens []token) [][]token {
	var parts [][]token
	depth, start := 0, 0
	for i, t := range tokens {
		switch {
		case opens(t):
			depth++
		case closes(t):
			depth--
		case depth == 0 && t.isPunct(","):
			parts = append(parts, tokens[start:i])
			start = i + 1
		}
	}
	return append(parts, tokens[start:])
}

// splitTopWord splits tokens on a top-level word.
func splitTopWord(tokens []token, word string) [][]token {
	var parts [][]token
	for {
		at := topWord(tokens, word)
		if at < 0 {
			return append(parts, tokens)
		}
		parts = append(parts, tokens[:at])
		tokens = tokens[at+1:]
	}
}

// topWord returns the index of the first top-level unqualified word among
// words, or -1.
func topWord(tokens []token, words ...string) int {
	depth := 0
	for i, t := range tokens {
		switch {
		case opens(t):
			depth++
		case closes(t):
			depth--
		case depth == 0 && t.kind == tokWord && (i == 0 || !tokens[i-1].isPunct(".")):
			for _, word := range words {
				if t.text == word {
					return i
				}
			}
		}
	}
	return -1
}

// unwrap removes parentheses that enclose the whole expression.
func unwrap(tokens []token) []token {
	for len(tokens) >= 2 && tokens[0].isPunct("(") {
		c := newCursor(tokens)
		inner, ok := c.group()
		if !ok || !c.done() {
			break
		}
		tokens = inner
	}
	return tokens
}

func setOf(words ...string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, word := range words {
		set[word] = true
	}
	return set
}

type policy struct {
	statement   *statement
	restrictive bool
	expressions [][]token
}

type newTable struct {
	statement  *statement
	name       objectName
	tenant     []string
	rlsEnabled bool
	rlsForced  bool
	policies   []policy
}

// fileState tracks the objects one migration creates and the columns it adds,
// in statement order: an object is usable only after the statement creating it.
type fileState struct {
	tenantColumns  map[string]bool
	runtimeRoles   map[string]bool
	knownFunctions map[string]bool
	tables         map[string]*newTable
	order          []string
	added          map[string]map[string]bool
	indexes        map[string]bool
	functions      map[string]bool
	// qualifiedFunctions holds the argument types of created functions by
	// their written name; only a schema-qualified one can be granted, since an
	// unqualified grant can resolve to a pg_catalog function first.
	qualifiedFunctions map[string][]string
	functionPrefix     string
	// existing holds the existing tables the migration locks, by written name.
	existing  map[string]bool
	backfills int
}

func newFileState(rules Rules, knownFunctions map[string]bool) *fileState {
	state := &fileState{
		tenantColumns:  map[string]bool{},
		runtimeRoles:   map[string]bool{},
		knownFunctions: knownFunctions,
		tables:         map[string]*newTable{},
		added:          map[string]map[string]bool{},
		indexes:        map[string]bool{},
		functions:      map[string]bool{},

		qualifiedFunctions: map[string][]string{},
		functionPrefix:     rules.FunctionPrefix,
		existing:           map[string]bool{},
	}
	for _, column := range append(append([]string{}, DefaultTenantColumns...), rules.TenantColumns...) {
		state.tenantColumns[strings.ToLower(column)] = true
	}
	for _, role := range rules.RuntimeRoles {
		state.runtimeRoles[role] = true
	}
	return state
}

func (f *fileState) statement(s *statement) *refusal {
	c := newCursor(s.tokens)
	switch {
	case c.words("create", "or", "replace"):
		return refuse("create_or_replace", s, "CREATE OR REPLACE changes an existing definition")
	case c.words("create", "table"), c.words("create", "unlogged", "table"):
		return f.createTable(s, c)
	case c.words("create", "index"), c.words("create", "unique", "index"):
		return f.createIndex(s, c)
	case c.words("create", "function"):
		return f.createFunction(s, c)
	case c.words("create", "policy"):
		return f.createPolicy(s, c)
	case c.words("create", "trigger"), c.words("create", "constraint", "trigger"):
		return f.createTrigger(s, c)
	case c.words("alter", "table"):
		return f.alterTable(s, c)
	case c.words("grant"):
		return f.grant(s, c)
	case c.words("update"):
		return f.update(s, c)
	case c.words("insert", "into"):
		return f.insert(s, c)
	case c.words("comment", "on"):
		return f.comment(s, c)
	}
	return refuse("not_on_allowlist", s, "the statement is not on the expand allowlist")
}

// lockExisting records an existing table the migration locks. Each lock is
// held until the migration commits, so a wait for a second one would stack on
// top of the first.
func (f *fileState) lockExisting(s *statement, table string) *refusal {
	f.existing[table] = true
	if len(f.existing) > MaxExistingTables {
		return refuse("lock_scope", s, "a migration changes, backfills or references at most %d existing table; each lock is held until it commits", MaxExistingTables)
	}
	return nil
}

// referencesExisting locks every existing table a foreign key in tokens
// references: the key takes a lock on it and adds triggers there.
func (f *fileState) referencesExisting(s *statement, tokens []token) *refusal {
	for i, t := range tokens {
		if !t.is("references") {
			continue
		}
		c := &cursor{tokens: tokens, at: i + 1}
		name, ok := c.object()
		if !ok {
			return refuse("unparseable", s, "unreadable REFERENCES")
		}
		if f.tables[name.relation()] != nil {
			continue
		}
		if refused := f.lockExisting(s, name.relation()); refused != nil {
			return refused
		}
	}
	return nil
}

// bareName renders one name part as PostgreSQL stores it, folded to lower
// case so that two spellings that may be one object compare equal.
func bareName(part string) string {
	return clipIdentifier(strings.ToLower(strings.Trim(part, `"`)))
}

// tooLong reports a name part PostgreSQL would truncate.
func tooLong(name objectName) bool {
	for _, part := range name.parts {
		if len(strings.Trim(part, `"`)) > MaxIdentifierBytes {
			return true
		}
	}
	return false
}

// respelled reports a name that is not written like a table this migration
// creates but can name it ("public.notes" for "notes").
func (f *fileState) respelled(name objectName) bool {
	if f.tables[name.relation()] != nil {
		return false
	}
	last := bareName(name.last())
	for _, created := range f.order {
		if bareName(f.tables[created].name.last()) == last {
			return true
		}
	}
	return false
}

func startsConstraint(c *cursor) bool {
	for _, word := range []string{"constraint", "primary", "unique", "check", "foreign", "exclude"} {
		if c.peekWord(word) {
			return true
		}
	}
	return false
}

func (f *fileState) createTable(s *statement, c *cursor) *refusal {
	if c.words("if", "not", "exists") {
		return refuse("create_table_shape", s, "CREATE TABLE IF NOT EXISTS could skip an existing table and unlock changes to it")
	}
	name, ok := c.object()
	if !ok {
		return refuse("unparseable", s, "unreadable table name")
	}
	if refused := f.newTableName(s, name); refused != nil {
		return refused
	}
	elements, ok := c.group()
	if !ok {
		return refuse("create_table_shape", s, "only a column list creates a new table (no AS, PARTITION OF or OF type)")
	}
	if call, found := f.callOutsideAllowlist(elements); found {
		return refuse("ddl_function", s, "PostgreSQL can evaluate %s while it creates the table; a new table calls only built-in pure functions", call)
	}
	if !c.done() {
		if !c.words("with") {
			return refuse("create_table_shape", s, "a new table takes no INHERITS, PARTITION BY, USING, TABLESPACE or ON COMMIT clause")
		}
		if _, ok := c.group(); !ok || !c.done() {
			return refuse("create_table_shape", s, "unreadable table storage parameters")
		}
	}
	table := &newTable{statement: s, name: name}
	f.tables[name.relation()] = table
	f.order = append(f.order, name.relation())
	if refused := f.referencesExisting(s, elements); refused != nil {
		return refused
	}
	return f.tableElements(s, table, elements)
}

// tableElements records the columns of a new table and refuses LIKE.
func (f *fileState) tableElements(s *statement, table *newTable, elements []token) *refusal {
	for _, element := range splitTop(elements) {
		e := newCursor(element)
		switch {
		case e.done():
			return refuse("unparseable", s, "empty table element")
		case e.peekWord("like"):
			return refuse("create_table_shape", s, "CREATE TABLE ... LIKE copies an existing table")
		case startsConstraint(e):
			continue
		}
		if !f.addTableColumn(table, e.next()) {
			return refuse("unparseable", s, "unreadable column definition")
		}
	}
	return nil
}

// newTableName admits the name of a new table: no catalog or pg_ schema name,
// no built-in type name, no name PostgreSQL would truncate, and none the
// history or this migration already uses.
func (f *fileState) newTableName(s *statement, name objectName) *refusal {
	switch bare := bareName(name.last()); {
	case name.catalogName():
		return refuse("create_table_shape", s, "a pg_ name can resolve to a catalog relation")
	case tooLong(name):
		return refuse("identifier_too_long", s, "PostgreSQL truncates names longer than %d bytes, so the name can match another object", MaxIdentifierBytes)
	case typeWords[bare]:
		return refuse("create_table_shape", s, "a new table's row type %s would share its name with a built-in type", bare)
	case f.knownFunctions == nil:
		return refuse("table_history_unknown", s, "a new table needs the migration history to prove that no table, view or type of another schema has its name")
	case f.knownFunctions[HistoryRelationPrefix+bare]:
		return refuse("table_exists", s, "an earlier migration already names %s; a new table of that name in another schema can shadow it for running code", bare)
	case f.respelled(name):
		return refuse("ambiguous_relation", s, "%s can name a table this migration already creates", name.relation())
	}
	return nil
}

func (f *fileState) addTableColumn(table *newTable, column token) bool {
	name, ok := canonicalPart(column)
	if ok && f.tenantColumns[strings.ToLower(column.text)] {
		table.tenant = append(table.tenant, strings.ToLower(column.text))
	}
	return ok && name != ""
}

func (f *fileState) createIndex(s *statement, c *cursor) *refusal {
	concurrently := c.words("concurrently")
	c.words("if", "not", "exists")
	var name objectName
	if !c.peekWord("on") {
		var ok bool
		if name, ok = c.object(); !ok {
			return refuse("unparseable", s, "unreadable index name")
		}
	}
	if !c.words("on") {
		return refuse("unparseable", s, "unreadable CREATE INDEX")
	}
	c.words("only")
	table, ok := c.object()
	if !ok {
		return refuse("unparseable", s, "unreadable index table")
	}
	if f.tables[table.relation()] == nil {
		if concurrently {
			return refuse("concurrent_index_in_transaction", s, "CREATE INDEX CONCURRENTLY on an existing table must be alone in its file, outside a transaction")
		}
		return refuse("index_on_existing_table", s, "an index on an existing table must be built CONCURRENTLY, alone in its file")
	}
	if call, found := f.callOutsideAllowlist(c.rest()); found {
		return refuse("ddl_function", s, "PostgreSQL evaluates %s while it plans the index; an index calls only built-in pure functions", call)
	}
	if len(name.parts) > 0 {
		if name.catalogName() {
			return refuse("create_table_shape", s, "a pg_ name can resolve to a catalog relation")
		}
		if tooLong(name) {
			return refuse("identifier_too_long", s, "PostgreSQL truncates names longer than %d bytes, so the name can match another object", MaxIdentifierBytes)
		}
		f.indexes[name.relation()] = true
	}
	return nil
}

func (f *fileState) createFunction(s *statement, c *cursor) *refusal {
	name, ok := c.object()
	if !ok || !c.peekPunct("(") {
		return refuse("unparseable", s, "unreadable function signature")
	}
	arguments, ok := c.group()
	if !ok {
		return refuse("unparseable", s, "unreadable function signature")
	}
	bare := bareName(name.last())
	switch {
	case tooLong(name):
		return refuse("identifier_too_long", s, "PostgreSQL truncates names longer than %d bytes, so the name can match another function", MaxIdentifierBytes)
	case f.knownFunctions == nil:
		return refuse("function_history_unknown", s, "a new function needs the migration history to prove its name is new")
	case f.knownFunctions[bare]:
		return refuse("function_exists", s, "an earlier migration already uses the routine name %s; an overload can change which function existing calls resolve to", bare)
	case pureFunctions[bare], typeWords[bare], callKeywords[bare]:
		return refuse("function_shadows_builtin", s, "a function named %s can shadow a built-in function or keyword", bare)
	case hasWordPair(c.rest(), "security", "definer"):
		return refuse("function_security_definer", s, "a SECURITY DEFINER function is executable by PUBLIC with the owner's rights")
	case !routineLanguage(c.rest()):
		return refuse("function_language", s, "a new function is LANGUAGE sql or plpgsql; a C or internal function binds native code")
	case !ValidFunctionPrefix(f.functionPrefix):
		return refuse("function_prefix", s, "the product declares no valid function prefix, so no new function is expand")
	case !strings.HasPrefix(bare, f.functionPrefix):
		return refuse("function_prefix", s, "a new function is named %s..., so it cannot share a name with a built-in or extension function, which PostgreSQL could resolve to it", f.functionPrefix)
	}
	if refused := routineCasts(s, arguments, c.rest()); refused != nil {
		return refused
	}
	f.functions[bare] = true
	f.qualifiedFunctions[name.relation()] = append(f.qualifiedFunctions[name.relation()], argumentTypes(arguments))
	return nil
}

const languageSQL = "sql"

// routineCasts refuses a cast or typed literal of a type that is not built in
// in a new function's argument defaults, its clauses and, for LANGUAGE sql,
// its body: PostgreSQL reads all of them while it creates the function.
func routineCasts(s *statement, arguments, clauses []token) *refusal {
	tokens := append([]token{}, arguments...)
	sql := false
	for i := 0; i < len(clauses); i++ {
		if clauses[i].is("language") && i+1 < len(clauses) {
			name := strings.ToLower(strings.Trim(clauses[i+1].text, "'"))
			sql = sql || name == languageSQL
			i++
			continue
		}
		tokens = append(tokens, clauses[i])
	}
	if cast, found := castOutsideAllowlist(tokens); found {
		return refuse("ddl_function", s, "PostgreSQL evaluates %s while it creates the function; a new function casts only to built-in types", cast)
	}
	if !sql {
		return nil
	}
	for i := 0; i+1 < len(tokens); i++ {
		if !tokens[i].is("as") {
			continue
		}
		body, ok := routineBody(tokens[i+1])
		if !ok {
			continue
		}
		statements, err := lexStatements(body, s.line)
		if err != nil {
			return refuse("unparseable", s, "unreadable SQL function body: %v", err)
		}
		for _, inner := range statements {
			if cast, found := castOutsideAllowlist(inner.tokens); found {
				return refuse("ddl_function", s, "PostgreSQL analyzes the SQL function body and evaluates %s while it creates the function", cast)
			}
		}
	}
	return nil
}

// routineBody returns the text of a quoted function body.
func routineBody(t token) (string, bool) {
	switch t.kind {
	case tokDollar:
		end := strings.Index(t.text[1:], "$")
		if end < 0 || len(t.text) < 2*(end+2) {
			return "", false
		}
		tag := t.text[:end+2]
		return t.text[len(tag) : len(t.text)-len(tag)], true
	case tokString:
		if !strings.HasPrefix(t.text, "'") {
			return "", false
		}
		return strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(t.text, "'"), "'"), "''", "'"), true
	}
	return "", false
}

// routineLanguage requires a top-level LANGUAGE clause naming sql or plpgsql;
// a RETURNS TABLE column may be named language.
func routineLanguage(tokens []token) bool {
	found, depth := false, 0
	for i := 0; i+1 < len(tokens); i++ {
		switch {
		case opens(tokens[i]):
			depth++
		case closes(tokens[i]):
			depth--
		}
		if depth > 0 || !tokens[i].is("language") {
			continue
		}
		found = true
		language := tokens[i+1]
		name := language.text
		switch language.kind {
		case tokWord, tokQuoted:
		case tokString:
			name = strings.ToLower(strings.Trim(name, "'"))
		default:
			return false
		}
		if name != languageSQL && name != "plpgsql" {
			return false
		}
	}
	return found
}

// argumentTypes renders a routine's input argument types, so a grant can be
// matched to the overload it names: modes, argument names and defaults are
// dropped and OUT arguments skipped, as PostgreSQL identifies a routine.
func argumentTypes(arguments []token) string {
	var types []string
	for _, element := range splitTop(arguments) {
		if len(element) == 0 || element[0].is("out") {
			continue
		}
		parts := []string{}
		for _, t := range argumentType(element) {
			parts = append(parts, t.text)
		}
		types = append(types, strings.Join(parts, " "))
	}
	return strings.Join(types, ", ")
}

// argumentType reduces one input argument to its type tokens.
func argumentType(element []token) []token {
	if element[0].is("in") || element[0].is("inout") || element[0].is("variadic") {
		element = element[1:]
	}
	if at := topWord(element, "default"); at >= 0 {
		element = element[:at]
	}
	if at := slices.IndexFunc(element, func(t token) bool { return t.isPunct("=") }); at >= 0 {
		element = element[:at]
	}
	if len(element) > 1 && !builtinType(element) && !element[1].isPunct(".") && !opens(element[1]) {
		element = element[1:]
	}
	return element
}

func hasWordPair(tokens []token, first, second string) bool {
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i].is(first) && tokens[i+1].is(second) {
			return true
		}
	}
	return false
}

func (f *fileState) createPolicy(s *statement, c *cursor) *refusal {
	if _, ok := c.object(); !ok || !c.words("on") {
		return refuse("unparseable", s, "unreadable CREATE POLICY")
	}
	name, ok := c.object()
	if !ok {
		return refuse("unparseable", s, "unreadable policy table")
	}
	table := f.tables[name.relation()]
	if table == nil {
		return refuse("object_not_created", s, "a policy on an existing table changes who reads or writes its rows")
	}
	p := policy{statement: s}
	for !c.done() {
		switch {
		case c.words("as", "restrictive"):
			p.restrictive = true
		case c.words("using"), c.words("with", "check"):
			expression, ok := c.group()
			if !ok {
				return refuse("unparseable", s, "unreadable policy expression")
			}
			if cast, found := castOutsideAllowlist(expression); found {
				return refuse("ddl_function", s, "PostgreSQL evaluates %s while it reads the policy; a policy casts only to built-in types", cast)
			}
			p.expressions = append(p.expressions, unwrap(expression))
		default:
			c.next()
		}
	}
	table.policies = append(table.policies, p)
	return nil
}

func (f *fileState) createTrigger(s *statement, c *cursor) *refusal {
	if _, ok := c.object(); !ok {
		return refuse("unparseable", s, "unreadable trigger name")
	}
	on := topWord(c.rest(), "on")
	if on < 0 {
		return refuse("unparseable", s, "unreadable CREATE TRIGGER")
	}
	c.at += on + 1
	table, ok := c.object()
	if !ok {
		return refuse("unparseable", s, "unreadable trigger table")
	}
	if f.tables[table.relation()] == nil {
		return refuse("object_not_created", s, "a trigger on an existing table changes what its writes do")
	}
	if cast, found := castOutsideAllowlist(c.rest()); found {
		return refuse("ddl_function", s, "PostgreSQL evaluates %s while it reads the trigger; a trigger casts only to built-in types", cast)
	}
	return nil
}

func (f *fileState) alterTable(s *statement, c *cursor) *refusal {
	c.words("if", "exists")
	c.words("only")
	name, ok := c.object()
	if !ok || c.done() {
		return refuse("unparseable", s, "unreadable ALTER TABLE")
	}
	actions := splitTop(c.rest())
	if table := f.tables[name.relation()]; table != nil {
		return f.alterNewTable(s, table, actions)
	}
	if f.respelled(name) {
		return refuse("ambiguous_relation", s, "%s can name a table this migration creates; write it the same way", name.relation())
	}
	if refused := f.lockExisting(s, name.relation()); refused != nil {
		return refused
	}
	for _, action := range actions {
		if refused := f.addColumn(s, name.relation(), action); refused != nil {
			return refused
		}
	}
	return nil
}

// alterNewTable admits the actions that complete a table this migration
// creates: row security, columns, constraints and column changes.
func (f *fileState) alterNewTable(s *statement, table *newTable, actions [][]token) *refusal {
	for _, action := range actions {
		if call, found := f.callOutsideAllowlist(action); found {
			return refuse("ddl_function", s, "PostgreSQL can evaluate %s while it changes the table; a new table calls only built-in pure functions", call)
		}
		if refused := f.referencesExisting(s, action); refused != nil {
			return refused
		}
		a := newCursor(action)
		switch {
		case a.words("enable", "row", "level", "security"):
			table.rlsEnabled = true
		case a.words("force", "row", "level", "security"):
			table.rlsForced = true
		case a.words("alter"):
		case a.words("add"):
			if startsConstraint(a) {
				continue
			}
			a.words("column")
			a.words("if", "not", "exists")
			if !f.addTableColumn(table, a.next()) {
				return refuse("unparseable", s, "unreadable ADD COLUMN")
			}
		default:
			return refuse("new_table_change", s, "a new table is completed only with row security, columns, constraints and column changes")
		}
	}
	return nil
}

var (
	columnStopWords = []string{"default", "not", "null", "constraint", "check", "unique", "primary", "references", "generated", "collate"}
	serialTypes     = setOf("serial", "bigserial", "smallserial", "serial2", "serial4", "serial8")
	builtinTypes    = setOf("text", "varchar", "char", "character", "bpchar", "bit", "boolean", "bool", "smallint",
		"integer", "int", "int2", "int4", "int8", "bigint", "numeric", "decimal", "real", "float", "float4",
		"float8", "date", "interval", "uuid", "jsonb", "json", "bytea", "inet", "cidr", "macaddr", "tsvector",
		"timestamptz", "timetz")
)

// addColumn admits one ADD COLUMN action on an existing table. A column added
// with IF NOT EXISTS may already exist, so it unlocks no backfill or comment.
func (f *fileState) addColumn(s *statement, table string, action []token) *refusal {
	a := newCursor(action)
	if !a.words("add") {
		return refuse("existing_table_change", s, "on an existing table only ADD COLUMN is expand")
	}
	if startsConstraint(a) {
		return refuse("existing_table_change", s, "a constraint on an existing table can fail on existing rows and narrows writes")
	}
	a.words("column")
	ifNotExists := a.words("if", "not", "exists")
	columnToken := a.next()
	column, ok := canonicalPart(columnToken)
	if !ok || a.done() {
		return refuse("unparseable", s, "unreadable ADD COLUMN")
	}
	if f.tenantColumns[strings.ToLower(columnToken.text)] {
		return refuse("tenant_column_on_existing_table", s, "tenant column %s makes an existing table a tenant table, whose row security this migration cannot prove", columnToken.text)
	}
	if refused := columnDefinition(s, column, a.rest()); refused != nil {
		return refused
	}
	if !ifNotExists {
		if f.added[table] == nil {
			f.added[table] = map[string]bool{}
		}
		f.added[table][column] = true
	}
	return nil
}

func columnDefinition(s *statement, column string, definition []token) *refusal {
	typeEnd := topWord(definition, columnStopWords...)
	if typeEnd == 0 {
		return refuse("unparseable", s, "ADD COLUMN %s has no type", column)
	}
	if typeEnd < 0 {
		typeEnd = len(definition)
	}
	if serialTypes[definition[0].text] {
		return refuse("column_definition", s, "a serial column on an existing table fills every row from a sequence")
	}
	if !builtinType(definition[:typeEnd]) {
		return refuse("column_type", s, "a new column on an existing table needs a built-in type; a domain or extension type can carry defaults and checks")
	}
	hasDefault, notNull := false, false
	for c := newCursor(definition[typeEnd:]); !c.done(); {
		switch {
		case c.words("not", "null"):
			notNull = true
		case c.words("null"):
		case c.words("default"):
			value := c.rest()
			end := len(value)
			if stop := topWord(value[min(1, len(value)):], columnStopWords...); stop >= 0 {
				end = stop + 1
			}
			isNull, ok := defaultValue(value[:end])
			if !ok {
				return refuse("column_default", s, "the default of %s on an existing table must be a constant", column)
			}
			hasDefault = !isNull
			c.at += end
		default:
			return refuse("column_definition", s, "a new column on an existing table carries no constraint, generated value, identity, reference or collation")
		}
	}
	if notNull && !hasDefault {
		return refuse("not_null_without_default", s, "a NOT NULL column on an existing table needs a non-null constant default, or older inserts fail")
	}
	return nil
}

// defaultValue admits a literal (string, number, TRUE, FALSE, NULL) with
// optional casts to built-in types, which PostgreSQL stores without rewriting
// rows; isNull reports a NULL default.
func defaultValue(tokens []token) (isNull, ok bool) {
	segments := [][]token{{}}
	for _, t := range tokens {
		if t.isPunct("::") {
			segments = append(segments, []token{})
			continue
		}
		segments[len(segments)-1] = append(segments[len(segments)-1], t)
	}
	for _, segment := range segments[1:] {
		if !builtinType(segment) {
			return false, false
		}
	}
	literal := segments[0]
	switch {
	case len(literal) == 1 && literal[0].is("null"):
		return true, true
	case len(literal) == 1:
		t := literal[0]
		return false, t.kind == tokString || t.kind == tokNumber || t.is("true") || t.is("false")
	case len(literal) == 2:
		return false, (literal[0].isPunct("-") || literal[0].isPunct("+")) && literal[1].kind == tokNumber
	}
	return false, false
}

// builtinType admits a built-in scalar type with optional modifiers and array
// bounds.
func builtinType(tokens []token) bool {
	c := newCursor(tokens)
	return builtinTypePrefix(c) && c.done()
}

// builtinTypePrefix consumes one built-in type at the cursor.
func builtinTypePrefix(c *cursor) bool {
	if c.words("pg_catalog") && !c.punct(".") {
		return false
	}
	switch {
	case c.words("double", "precision"):
	case c.words("character", "varying"), c.words("char", "varying"), c.words("bit", "varying"):
		if !typeModifier(c) {
			return false
		}
	case c.words("timestamp"), c.words("time"):
		if !typeModifier(c) {
			return false
		}
		if !c.words("with", "time", "zone") {
			c.words("without", "time", "zone")
		}
	default:
		if t := c.next(); t.kind != tokWord || !builtinTypes[t.text] || !typeModifier(c) {
			return false
		}
	}
	for c.punct("[") {
		if c.peek().kind == tokNumber {
			c.next()
		}
		if !c.punct("]") {
			return false
		}
	}
	return true
}

func typeModifier(c *cursor) bool {
	if !c.peekPunct("(") {
		return true
	}
	inner, ok := c.group()
	if !ok || len(inner)%2 == 0 {
		return false
	}
	for i, t := range inner {
		if i%2 == 0 && t.kind != tokNumber || i%2 == 1 && !t.isPunct(",") {
			return false
		}
	}
	return true
}

// update admits a backfill of the shape
// UPDATE ONLY t SET c = <expr> WHERE c IS NULL AND ctid IN (SELECT ctid FROM ONLY t ... LIMIT n).
// ONLY keeps a ctid batch inside one table: partitions repeat ctids.
func (f *fileState) update(s *statement, c *cursor) *refusal {
	only := c.words("only")
	target, ok := c.object()
	if !ok {
		return refuse("unparseable", s, "unreadable UPDATE")
	}
	if refused := f.backfillTarget(s, target); refused != nil {
		return refused
	}
	alias := ""
	if !c.peekWord("set") {
		c.words("as")
		if alias, ok = canonicalPart(c.next()); !ok {
			return refuse("unparseable", s, "unreadable UPDATE")
		}
	}
	if !c.words("set") {
		return refuse("unparseable", s, "unreadable UPDATE")
	}
	rest := c.rest()
	if topWord(rest, "from", "returning") >= 0 {
		return refuse("backfill_shape", s, "a backfill takes no FROM or RETURNING clause")
	}
	if subqueries(rest) > 1 {
		return refuse("backfill_shape", s, "a backfill holds no subquery besides its ctid batch; another one could read, lock or copy other rows and tables")
	}
	where := topWord(rest, "where")
	if where < 0 {
		return refuse("backfill_unbounded", s, "a backfill needs WHERE <column> IS NULL AND <key> IN (SELECT <key> FROM <table> ... LIMIT n), n at most %d", MaxBackfillBatch)
	}
	assigned, refused := f.backfillColumns(s, target.relation(), rest[:where])
	if refused != nil {
		return refused
	}
	if refused := backfillWhere(s, target, alias, assigned, rest[where+1:]); refused != nil {
		return refused
	}
	if call, found := f.callOutsideAllowlist(rest); found {
		return refuse("backfill_function", s, "a backfill runs only built-in pure functions; %s could run any code", call)
	}
	if !only {
		return refuse("backfill_unbounded", s, "a backfill uses UPDATE ONLY, so a ctid batch never reaches partitions or child tables")
	}
	return nil
}

// backfillTarget admits the table of a backfill: an existing table, written
// as it is everywhere in the migration, in the migration's one backfill.
func (f *fileState) backfillTarget(s *statement, target objectName) *refusal {
	switch {
	case f.tables[target.relation()] != nil:
		return refuse("write_new_table", s, "writing rows during the migration runs defaults, checks and triggers; seed rows ship with the release")
	case f.respelled(target):
		return refuse("ambiguous_relation", s, "%s can name a table this migration creates", target.relation())
	}
	if f.backfills++; f.backfills > MaxBackfills {
		return refuse("backfill_unbounded", s, "a migration runs at most %d backfill batch; a larger backfill is a series of migrations, each committing its batch", MaxBackfills)
	}
	return f.lockExisting(s, target.relation())
}

func (f *fileState) backfillColumns(s *statement, table string, assignments []token) ([]string, *refusal) {
	var assigned []string
	for _, assignment := range splitTop(assignments) {
		if len(assignment) < 2 || !assignment[1].isPunct("=") {
			return nil, refuse("backfill_column", s, "a backfill sets single columns")
		}
		column, ok := canonicalPart(assignment[0])
		if !ok || !f.added[table][column] {
			return nil, refuse("backfill_column", s, "a backfill on an existing table sets only columns this migration adds; %s is not one", assignment[0].text)
		}
		if len(assignment) < 3 || assignment[2].is("null") {
			return nil, refuse("backfill_not_idempotent", s, "a backfill sets %s to a value, never NULL, so a second run skips the row", column)
		}
		assigned = append(assigned, column)
	}
	return assigned, nil
}

// backfillWhere requires every set column to be guarded by IS NULL, so a
// second run skips every row the first one set, and a ctid batch, since ctid
// is the one key that is unique without knowing the table.
func backfillWhere(s *statement, target objectName, alias string, assigned []string, clause []token) *refusal {
	if topWord(clause, "or", "between") >= 0 {
		return refuse("backfill_shape", s, "a backfill WHERE clause is a conjunction without OR or BETWEEN at its top level")
	}
	guarded, batch := map[string]bool{}, false
	for _, conjunct := range splitTopWord(clause, "and") {
		if column, ok := nullGuard(conjunct, target, alias); ok {
			guarded[column] = true
		}
		batch = batch || batchSubquery(conjunct, target, alias)
	}
	for _, column := range assigned {
		if !guarded[column] {
			return refuse("backfill_not_idempotent", s, "a backfill must skip rows it already set: WHERE %s IS NULL", column)
		}
	}
	if !batch {
		return refuse("backfill_unbounded", s, "a backfill bounds its batch with ctid IN (SELECT ctid FROM <table> ... LIMIT n), n at most %d", MaxBackfillBatch)
	}
	return nil
}

// columnRef reads a column optionally qualified by the target table or its
// alias.
func columnRef(c *cursor, target objectName, alias string) (string, bool) {
	first, ok := canonicalPart(c.next())
	if !ok || !c.punct(".") {
		return first, ok
	}
	column, ok := canonicalPart(c.next())
	return column, ok && (first == target.last() || first == alias)
}

func nullGuard(tokens []token, target objectName, alias string) (string, bool) {
	c := newCursor(unwrap(tokens))
	column, ok := columnRef(c, target, alias)
	return column, ok && c.words("is", "null") && c.done()
}

func batchSubquery(tokens []token, target objectName, alias string) bool {
	c := newCursor(unwrap(tokens))
	key, ok := columnRef(c, target, alias)
	if !ok || key != "ctid" || !c.words("in") {
		return false
	}
	inner, ok := c.group()
	if !ok || !c.done() {
		return false
	}
	sub := newCursor(inner)
	if !sub.words("select") {
		return false
	}
	selected, ok := canonicalPart(sub.next())
	if sub.punct(".") {
		selected, ok = canonicalPart(sub.next())
	}
	if !ok || selected != key || !sub.words("from", "only") {
		return false
	}
	from, ok := sub.object()
	return ok && from.relation() == target.relation() && batchTail(sub.rest())
}

// subqueries counts the subqueries at any depth: SELECT, and the forms
// without it, TABLE t and VALUES (...).
func subqueries(tokens []token) int {
	count := 0
	for _, t := range tokens {
		if t.is("select") || t.is("table") || t.is("values") {
			count++
		}
	}
	return count
}

// batchTail admits what follows FROM ONLY t in a batch subquery:
// [WHERE <column comparison> AND ...] [ORDER BY <column> [ASC|DESC] ...] LIMIT n [FOR UPDATE [SKIP LOCKED]].
// Its columns are unqualified and it holds no parenthesis, so it reads only
// the inner table and cannot be correlated with the updated row, which would
// apply its LIMIT once per row.
func batchTail(tokens []token) bool {
	if !limitTail(tokens) {
		return false
	}
	head := tokens[:topWord(tokens, "limit")]
	for _, t := range head {
		if t.isPunct(".") || opens(t) {
			return false
		}
	}
	c := newCursor(head)
	if c.words("where") {
		conditions := c.rest()
		if order := topWord(conditions, "order"); order >= 0 {
			conditions = conditions[:order]
		}
		if topWord(conditions, "or", "between") >= 0 {
			return false
		}
		for _, conjunct := range splitTopWord(conditions, "and") {
			if !columnComparison(conjunct) {
				return false
			}
		}
		c.at += len(conditions)
	}
	if c.words("order", "by") {
		return indexColumns(c.rest())
	}
	return c.done()
}

// limitTail requires the subquery to end with its own LIMIT n, optionally
// followed by FOR UPDATE [SKIP LOCKED].
func limitTail(tokens []token) bool {
	n := len(tokens)
	if n >= 2 && tokens[n-2].is("skip") && tokens[n-1].is("locked") {
		n -= 2
	}
	if n >= 2 && tokens[n-2].is("for") && tokens[n-1].is("update") {
		n -= 2
	}
	if n < 2 || topWord(tokens[:n], "limit") != n-2 || tokens[n-1].kind != tokNumber {
		return false
	}
	limit, err := strconv.Atoi(tokens[n-1].text)
	return err == nil && limit > 0 && limit <= MaxBackfillBatch
}

// pureFunctions are the built-in functions a backfill or a concurrently built
// index may evaluate on existing rows. Any other call could run arbitrary code
// while the migration holds its locks.
var pureFunctions = setOf(
	"coalesce", "nullif", "greatest", "least", "lower", "upper", "btrim", "ltrim", "rtrim", "trim",
	"substr", "substring", "left", "right", "length", "char_length", "octet_length", "replace",
	"split_part", "concat", "concat_ws", "format", "md5", "encode", "decode", "to_char", "to_date",
	"to_number", "to_timestamp", "to_json", "to_jsonb", "jsonb_build_object", "jsonb_build_array",
	"json_build_object", "json_build_array", "jsonb_set", "jsonb_strip_nulls", "jsonb_typeof",
	"jsonb_array_length", "array_length", "array_position", "cardinality", "string_to_array",
	"array_to_string", "regexp_replace", "starts_with", "lpad", "rpad", "position", "strpos", "abs",
	"round", "floor", "ceil", "ceiling", "trunc", "mod", "date_trunc", "date_part", "extract",
	"make_interval", "now", "statement_timestamp", "transaction_timestamp", "gen_random_uuid",
	"count", "min", "max", "sum", "avg", "bool_and", "bool_or", "string_agg", "array_agg",
	"jsonb_agg", "jsonb_object_agg", "row_number", "to_tsvector", "setweight", "current_setting",
)

// typeWords name built-in types; before "(" they carry a type modifier or a
// built-in cast.
var typeWords = func() map[string]bool {
	words := setOf("varying", "timestamp", "time")
	for word := range builtinTypes {
		words[word] = true
	}
	return words
}()

// callKeywords are PostgreSQL reserved and column-name keywords that precede
// a parenthesis and can never name a function. Unreserved keywords such as
// filter or over can, so they count as calls except right after ")".
var callKeywords = setOf(
	"in", "exists", "any", "all", "some", "values", "array", "row", "as", "using", "on", "when",
	"then", "else", "where", "from", "lateral", "select", "and", "or", "not", "between", "case",
	"distinct", "having", "limit", "offset", "returning", "into", "with", "union", "intersect", "except",
	"only", "group", "order", "table", "default", "check", "unique", "primary", "references", "for", "to",
)

// clauseAfterParen are unreserved keywords that open a clause right after a
// closing parenthesis: agg(...) FILTER (...), f(...) OVER (...), INCLUDE (...).
var clauseAfterParen = setOf("filter", "over", "include")

// isCallAt reports whether tokens[i] names a function that tokens[i+1] calls.
func isCallAt(tokens []token, i int) bool {
	t := tokens[i]
	if t.kind != tokWord && t.kind != tokQuoted || i+1 >= len(tokens) || !tokens[i+1].isPunct("(") {
		return false
	}
	previous := token{kind: tokPunct}
	if i > 0 {
		previous = tokens[i-1]
	}
	switch {
	case referencedTable(tokens, i):
		return false
	case previous.isPunct(".") || t.kind == tokQuoted:
		return true
	}
	return !syntaxParenthesis(tokens, i)
}

// syntaxParenthesis reports an unqualified word whose parenthesis is SQL
// syntax rather than a call.
func syntaxParenthesis(tokens []token, i int) bool {
	t, previous := tokens[i], token{kind: tokPunct}
	if i > 0 {
		previous = tokens[i-1]
	}
	switch {
	case callKeywords[t.text]:
		return true
	case clauseAfterParen[t.text]:
		return previous.isPunct(")")
	case t.is("key"):
		return previous.is("primary") || previous.is("foreign")
	}
	// USING <index method> opens the column list of an index or an exclusion
	// constraint, never an ALTER COLUMN ... USING expression.
	return previous.is("using") && indexMethods[t.text] && (i == 1 || tokens[i-2].is("exclude"))
}

// referencedTable reports a table name after REFERENCES, whose parenthesis
// lists columns.
func referencedTable(tokens []token, i int) bool {
	j := i - 1
	if j >= 1 && tokens[j].isPunct(".") {
		j -= 2
	}
	return j >= 0 && tokens[j].is("references")
}

// callOutsideAllowlist finds a call that is not provably a built-in pure
// function: any other name, a pure name qualified by a schema other than
// pg_catalog, a pure name some migration of the product also defines, CAST
// (...), or a cast or typed literal of a type that is not built in.
func (f *fileState) callOutsideAllowlist(tokens []token) (string, bool) {
	if cast, found := castOutsideAllowlist(tokens); found {
		return cast, true
	}
	for i := range tokens {
		if !isCallAt(tokens, i) || f.builtinCall(tokens, i) {
			continue
		}
		return tokens[i].text, true
	}
	return "", false
}

// castOutsideAllowlist finds a :: cast or a typed literal (t 'x') of a type
// that is not built in: PostgreSQL runs that type's input or cast function
// while it reads the statement, and a domain's checks with it.
func castOutsideAllowlist(tokens []token) (string, bool) {
	for i := range tokens {
		if tokens[i].isPunct("::") {
			c := &cursor{tokens: tokens, at: i + 1}
			if !builtinTypePrefix(c) || c.peekPunct(".") {
				return "a :: cast to a type that is not built in", true
			}
			continue
		}
		if i+1 < len(tokens) && (tokens[i+1].kind == tokString || tokens[i+1].kind == tokDollar) && typedLiteral(tokens, i) {
			return "a literal typed " + tokens[i].text, true
		}
		if tokens[i].is("cast") && i+1 < len(tokens) && tokens[i+1].isPunct("(") {
			inner, ok := (&cursor{tokens: tokens, at: i + 1}).group()
			if as := topWord(inner, "as"); !ok || as < 0 || !builtinType(inner[as+1:]) {
				return "a CAST to a type that is not built in", true
			}
		}
	}
	return "", false
}

// stringKeywords are reserved words that precede a string literal as SQL
// syntax; being reserved, they never name its type. RETURN opens an SQL
// function body, and b, x and n are the bit, hex and national string prefixes.
var stringKeywords = setOf("default", "then", "else", "when", "case", "and", "or", "not", "to", "from", "for", "as",
	"between", "symmetric", "asymmetric", "in", "select", "both", "leading", "trailing", "placing", "any", "all",
	"some", "using", "with", "on", "where", "having", "values", "return", "b", "x", "n")

// operatorKeywords can also name a type, so before a string they are
// operators only right after an operand.
var operatorKeywords = setOf("is", "like", "ilike", "similar", "escape")

// typedLiteral reports tokens[i] typing the string literal after it: a
// quoted or qualified type name, or a word that is neither a built-in type nor
// syntax before a string.
func typedLiteral(tokens []token, i int) bool {
	t, previous := tokens[i], token{kind: tokPunct}
	if i > 0 {
		previous = tokens[i-1]
	}
	switch {
	case t.kind == tokQuoted:
		return true
	case t.kind != tokWord:
		return false
	case previous.isPunct("."):
		return true
	case typeWords[t.text], stringKeywords[t.text]:
		return false
	case operatorKeywords[t.text]:
		return !endsOperand(previous)
	case t.is("zone"):
		return !previous.is("time")
	case t.is("precision"):
		return !previous.is("double")
	}
	return true
}

func endsOperand(t token) bool {
	switch t.kind {
	case tokWord, tokQuoted, tokString, tokDollar, tokNumber, tokParam:
		return true
	}
	return closes(t)
}

// builtinCall admits a pure function or a type modifier that no migration of
// the product shadows, unqualified (pg_catalog comes first in the default
// search path) or qualified by pg_catalog.
func (f *fileState) builtinCall(tokens []token, i int) bool {
	name := tokens[i].text
	if tokens[i].kind != tokWord || !pureFunctions[name] && !typeWords[name] || f.knownFunctions == nil || f.knownFunctions[name] || f.functions[name] {
		return false
	}
	return i < 2 || !tokens[i-1].isPunct(".") || tokens[i-2].is("pg_catalog")
}

func (f *fileState) insert(s *statement, c *cursor) *refusal {
	table, ok := c.object()
	if !ok {
		return refuse("unparseable", s, "unreadable INSERT")
	}
	if f.tables[table.relation()] == nil {
		return refuse("write_existing_table", s, "writing rows into an existing table is not expand")
	}
	return refuse("write_new_table", s, "writing rows during the migration runs defaults, checks and triggers; seed rows ship with the release")
}

func (f *fileState) grant(s *statement, c *cursor) *refusal {
	rest := c.rest()
	on := topWord(rest, "on")
	if on < 0 {
		return refuse("grant_shape", s, "granting a role is not expand")
	}
	columnLevel := hasGroup(rest[:on])
	c.at += on + 1
	function := false
	switch {
	case c.words("table"):
	case c.words("function"), c.words("routine"), c.words("procedure"):
		function = true
	case startsGrantScope(c):
		return refuse("grant_shape", s, "only grants on tables and functions this migration creates are expand")
	}
	if columnLevel && function {
		return refuse("grant_shape", s, "unreadable column grant")
	}
	rest = c.rest()
	to := topWord(rest, "to")
	if to < 0 {
		return refuse("unparseable", s, "unreadable GRANT")
	}
	if refused := f.grantObjects(s, function, rest[:to]); refused != nil {
		return refused
	}
	return f.grantRoles(s, rest[to+1:])
}

func hasGroup(tokens []token) bool {
	for _, t := range tokens {
		if t.isPunct("(") {
			return true
		}
	}
	return false
}

func startsGrantScope(c *cursor) bool {
	for _, word := range []string{"all", "sequence", "schema", "database", "domain", "type", "language", "large", "foreign", "tablespace", "parameter"} {
		if c.peekWord(word) {
			return true
		}
	}
	return false
}

func (f *fileState) grantObjects(s *statement, function bool, objects []token) *refusal {
	for _, object := range splitTop(objects) {
		o := newCursor(object)
		name, ok := o.object()
		var arguments []token
		named := false
		if ok && function && !o.done() {
			arguments, ok = o.group()
			named = true
		}
		if !ok || !o.done() {
			return refuse("unparseable", s, "unreadable grant object")
		}
		created := f.tables[name.relation()] != nil
		if function {
			// An unqualified or pg_catalog name can resolve to a catalog
			// function, and other argument types to another overload.
			created = len(name.parts) > 1 && name.parts[0] != "pg_catalog" && len(f.qualifiedFunctions[name.relation()]) > 0
			if created && named {
				created = slices.Contains(f.qualifiedFunctions[name.relation()], argumentTypes(arguments))
			}
		}
		if !created {
			return refuse("object_not_created", s, "a grant on %s changes access to an object this migration does not create", name.relation())
		}
	}
	return nil
}

func (f *fileState) grantRoles(s *statement, roles []token) *refusal {
	if topWord(roles, "with", "granted") >= 0 {
		return refuse("grant_shape", s, "WITH GRANT OPTION and GRANTED BY are not expand")
	}
	if len(f.runtimeRoles) == 0 {
		return refuse("grant_role", s, "the product declares no runtime roles, so no grant is expand")
	}
	for _, role := range splitTop(roles) {
		if len(role) != 1 || !f.runtimeRole(role[0]) {
			return refuse("grant_role", s, "grants go only to declared runtime roles, never PUBLIC")
		}
	}
	return nil
}

// runtimeRole compares a grantee as PostgreSQL resolves it: an unquoted name
// lower-cased, a quoted name exactly.
func (f *fileState) runtimeRole(t token) bool {
	switch t.kind {
	case tokWord:
		return t.text != "public" && f.runtimeRoles[t.text]
	case tokQuoted:
		return f.runtimeRoles[t.text]
	}
	return false
}

func (f *fileState) comment(s *statement, c *cursor) *refusal {
	if !f.commentTarget(c) || !c.words("is") {
		return refuse("object_not_created", s, "comments are expand only on objects this migration creates or columns it adds")
	}
	if value := c.next(); value.kind != tokString && !value.is("null") || !c.done() {
		return refuse("unparseable", s, "unreadable COMMENT")
	}
	return nil
}

func (f *fileState) commentTarget(c *cursor) bool {
	switch {
	case c.words("table"):
		name, ok := c.object()
		return ok && f.tables[name.relation()] != nil
	case c.words("column"):
		return f.commentColumn(c)
	case c.words("index"):
		name, ok := c.object()
		return ok && f.indexes[name.relation()]
	case c.words("function"):
		return f.commentFunction(c)
	case c.words("policy"), c.words("trigger"), c.words("constraint"):
		if _, ok := c.object(); !ok || !c.words("on") {
			return false
		}
		name, ok := c.object()
		return ok && f.tables[name.relation()] != nil
	}
	return false
}

func (f *fileState) commentColumn(c *cursor) bool {
	name, ok := c.object()
	if !ok || len(name.parts) < 2 {
		return false
	}
	table := objectName{parts: name.parts[:len(name.parts)-1]}.relation()
	return f.tables[table] != nil || f.added[table][name.last()]
}

// commentFunction admits a comment on a function this migration creates,
// named like a grant: schema-qualified, with its own argument types if any.
func (f *fileState) commentFunction(c *cursor) bool {
	name, ok := c.object()
	if !ok || len(name.parts) < 2 || name.parts[0] == "pg_catalog" {
		return false
	}
	signatures := f.qualifiedFunctions[name.relation()]
	if !c.peekPunct("(") {
		return len(signatures) > 0
	}
	arguments, ok := c.group()
	return ok && slices.Contains(signatures, argumentTypes(arguments))
}

// tenantTablesIsolated enforces D-2026-10-22-02 on every new tenant table:
// row security enabled and forced, and every permissive policy filtering on a
// tenant column through a function of the verified context, never widened by
// a top-level OR. Restrictive policies only narrow.
func (f *fileState) tenantTablesIsolated() *refusal {
	for _, name := range f.order {
		table := f.tables[name]
		if len(table.tenant) == 0 {
			continue
		}
		if !table.rlsEnabled || !table.rlsForced {
			return refuse("tenant_table_without_rls", table.statement,
				"tenant table %s (column %s) needs ENABLE and FORCE ROW LEVEL SECURITY in the same migration", name, strings.Join(table.tenant, ", "))
		}
		permissive := 0
		for _, p := range table.policies {
			if p.restrictive {
				continue
			}
			permissive++
			if !isolating(p, table.tenant) {
				return refuse("tenant_policy", p.statement, "every permissive policy on tenant table %s filters on %s through a context function, without a top-level OR", name, strings.Join(table.tenant, ", "))
			}
		}
		if permissive == 0 {
			return refuse("tenant_table_without_rls", table.statement, "tenant table %s needs a policy on its tenant column in the same migration", name)
		}
	}
	return nil
}

// isolating requires each expression of a permissive policy to be a
// conjunction with one conjunct exactly "<tenant column> = <context call>":
// AND only narrows, so that conjunct bounds the whole expression.
func isolating(p policy, tenant []string) bool {
	if len(p.expressions) == 0 {
		return false
	}
	for _, expression := range p.expressions {
		// BETWEEN x AND y holds an AND that is not a conjunction.
		if topWord(expression, "or", "between") >= 0 {
			return false
		}
		filtered := false
		for _, conjunct := range splitTopWord(expression, "and") {
			filtered = filtered || tenantFilter(unwrap(conjunct), tenant)
		}
		if !filtered {
			return false
		}
	}
	return true
}

func tenantFilter(tokens []token, tenant []string) bool {
	equals := -1
	for i, t := range tokens {
		if t.isPunct("=") {
			if equals >= 0 {
				return false
			}
			equals = i
		}
	}
	if equals < 0 {
		return false
	}
	left, right := tokens[:equals], tokens[equals+1:]
	return tenantColumn(left, tenant) && contextCall(right, tenant) || tenantColumn(right, tenant) && contextCall(left, tenant)
}

func tenantColumn(tokens []token, tenant []string) bool {
	if len(tokens) == 3 && tokens[1].isPunct(".") {
		tokens = tokens[2:]
	}
	return len(tokens) == 1 && (tokens[0].kind == tokWord || tokens[0].kind == tokQuoted) && mentions(tokens, tenant)
}

// contextCall reads one function call, whose arguments do not name the tenant
// column, with optional casts: current_setting('app.tenant_id')::uuid.
func contextCall(tokens []token, tenant []string) bool {
	c := newCursor(tokens)
	name, ok := c.object()
	if !ok || len(name.parts) == 1 && callKeywords[name.parts[0]] {
		return false
	}
	arguments, ok := c.group()
	if !ok || mentions(arguments, tenant) {
		return false
	}
	for c.punct("::") {
		if _, ok := c.object(); !ok {
			return false
		}
	}
	return c.done()
}

func mentions(tokens []token, columns []string) bool {
	for _, t := range tokens {
		if t.kind != tokWord && t.kind != tokQuoted {
			continue
		}
		for _, column := range columns {
			if strings.EqualFold(t.text, column) {
				return true
			}
		}
	}
	return false
}
