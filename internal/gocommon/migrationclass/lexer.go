package migrationclass

import (
	"errors"
	"fmt"
	"strings"
)

type tokenKind int

const (
	tokWord   tokenKind = iota // unquoted keyword or identifier, lower-cased
	tokQuoted                  // "quoted identifier", content without quotes
	tokString                  // 'literal' or E'literal', raw text with quotes
	tokDollar                  // $tag$ body $tag$, raw text
	tokNumber
	tokParam // $1
	tokPunct // one character, or "::"
)

type token struct {
	kind tokenKind
	text string
	line int
}

func (t token) is(word string) bool { return t.kind == tokWord && t.text == word }

func (t token) isPunct(p string) bool { return t.kind == tokPunct && t.text == p }

// statement is one top-level SQL statement: its tokens, the line it starts on
// and its source text with collapsed whitespace for reports.
type statement struct {
	tokens []token
	line   int
	source string
}

// lexer splits PostgreSQL migration SQL into statements on top-level
// semicolons. It reads line comments (ended by LF or CR, as PostgreSQL does),
// nested block comments, standard and escape string literals, quoted
// identifiers and dollar quoting. Anything it cannot read to its end, and any
// form PostgreSQL could read differently (adjacent string literals, which
// PostgreSQL may join across a newline, or a backslash before a quote, which
// depends on standard_conforming_strings), is an error. The classifier treats
// an error as a contract migration.
type lexer struct {
	body       string
	at, line   int
	current    []token
	start      int
	statements []statement
	// lenient reads history files: adjacent literals continue the previous
	// one (an escape string keeps its escapes, as PostgreSQL does) and a
	// backslash before a quote is literal. Classification never uses it.
	lenient bool
}

func lexStatements(body string, firstLine int) ([]statement, error) {
	return lex(&lexer{body: body, line: firstLine, start: -1})
}

// lexHistory reads an applied migration for its routine names.
func lexHistory(body string) ([]statement, error) {
	return lex(&lexer{body: body, line: 1, start: -1, lenient: true})
}

func lex(l *lexer) ([]statement, error) {
	for l.at < len(l.body) {
		if err := l.step(); err != nil {
			return nil, fmt.Errorf("line %d: %w", l.line, err)
		}
	}
	l.flush(len(l.body))
	return l.statements, nil
}

func (l *lexer) flush(end int) {
	if len(l.current) > 0 {
		l.statements = append(l.statements, statement{
			tokens: l.current,
			line:   l.current[0].line,
			source: strings.Join(strings.Fields(l.body[l.start:end]), " "),
		})
	}
	l.current, l.start = nil, -1
}

func (l *lexer) emit(kind tokenKind, end int) {
	if l.start < 0 {
		l.start = l.at
	}
	text := l.body[l.at:end]
	if kind == tokWord {
		text = strings.ToLower(text)
	}
	l.current = append(l.current, token{kind: kind, text: text, line: l.line})
	l.line += strings.Count(l.body[l.at:end], "\n")
	l.at = end
}

func (l *lexer) peek(offset int) byte {
	if l.at+offset < len(l.body) {
		return l.body[l.at+offset]
	}
	return 0
}

func (l *lexer) step() error {
	if l.trivia() {
		return nil
	}
	switch c := l.body[l.at]; {
	case c == '/' && l.peek(1) == '*':
		return l.blockComment()
	case c == ';':
		l.flush(l.at)
		l.at++
	case c == '\'':
		return l.stringLiteral(l.at, false)
	case (c == 'e' || c == 'E') && l.peek(1) == '\'' && (l.at == 0 || !isWordByte(l.body[l.at-1], true)):
		return l.stringLiteral(l.at+1, true)
	case c == '"':
		return l.quotedIdentifier()
	case c == '$':
		return l.dollar()
	default:
		return l.plain(c)
	}
	return nil
}

// trivia skips whitespace and a line comment, which ends at LF or CR.
func (l *lexer) trivia() bool {
	switch c := l.body[l.at]; {
	case c == '\n':
		l.line++
		l.at++
	case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
		l.at++
	case c == '-' && l.peek(1) == '-':
		for l.at < len(l.body) && l.body[l.at] != '\n' && l.body[l.at] != '\r' {
			l.at++
		}
	default:
		return false
	}
	return true
}

func (l *lexer) plain(c byte) error {
	switch {
	case isDigit(c) || c == '.' && isDigit(l.peek(1)):
		l.number()
	case isWordByte(c, false):
		return l.word()
	case c == ':' && l.peek(1) == ':':
		l.emit(tokPunct, l.at+2)
	case c < 0x20 || c >= 0x7f:
		return fmt.Errorf("unexpected byte 0x%02x outside a literal", c)
	default:
		l.emit(tokPunct, l.at+1)
	}
	return nil
}

func (l *lexer) word() error {
	end := l.at
	for end < len(l.body) && isWordByte(l.body[end], true) {
		end++
	}
	if end-l.at == 1 && (l.body[l.at] == 'u' || l.body[l.at] == 'U') && end < len(l.body) && l.body[end] == '&' {
		return errors.New("unicode escape names and strings (U&) are not read")
	}
	l.emit(tokWord, end)
	return nil
}

func (l *lexer) blockComment() error {
	depth := 0
	for {
		if l.at+1 >= len(l.body) {
			return errors.New("unterminated block comment")
		}
		switch {
		case l.body[l.at] == '/' && l.body[l.at+1] == '*':
			depth++
			l.at += 2
		case l.body[l.at] == '*' && l.body[l.at+1] == '/':
			depth--
			l.at += 2
		default:
			if l.body[l.at] == '\n' {
				l.line++
			}
			l.at++
		}
		if depth == 0 {
			return nil
		}
	}
}

// stringLiteral reads the literal whose opening quote is at open; escapes
// marks an E” string, whose backslash escapes the next byte.
func (l *lexer) stringLiteral(open int, escapes bool) error {
	if n := len(l.current); n > 0 && l.current[n-1].kind == tokString {
		if !l.lenient {
			return errors.New("adjacent string literals can be joined across a newline; write one literal")
		}
		previous := l.current[n-1].text
		escapes = escapes || strings.HasPrefix(previous, "E") || strings.HasPrefix(previous, "e")
	}
	for i := open + 1; i < len(l.body); i++ {
		switch l.body[i] {
		case '\\':
			if escapes {
				i++
			} else if !l.lenient && i+1 < len(l.body) && l.body[i+1] == '\'' {
				return errors.New("a backslash before a quote reads differently without standard_conforming_strings")
			}
		case '\'':
			if i+1 < len(l.body) && l.body[i+1] == '\'' {
				i++
				continue
			}
			l.emit(tokString, i+1)
			return nil
		}
	}
	return errors.New("unterminated string literal")
}

func (l *lexer) quotedIdentifier() error {
	var name strings.Builder
	for end := l.at + 1; end < len(l.body); end++ {
		if l.body[end] != '"' {
			name.WriteByte(l.body[end])
			continue
		}
		if end+1 < len(l.body) && l.body[end+1] == '"' {
			name.WriteByte('"')
			end++
			continue
		}
		if name.Len() == 0 {
			return errors.New("empty quoted identifier")
		}
		l.emit(tokQuoted, end+1)
		l.current[len(l.current)-1].text = name.String()
		return nil
	}
	return errors.New("unterminated quoted identifier")
}

func (l *lexer) dollar() error {
	if isDigit(l.peek(1)) {
		end := l.at + 1
		for end < len(l.body) && isDigit(l.body[end]) {
			end++
		}
		l.emit(tokParam, end)
		return nil
	}
	tagEnd := l.at + 1
	for tagEnd < len(l.body) && l.body[tagEnd] != '$' && isWordByte(l.body[tagEnd], tagEnd > l.at+1) {
		tagEnd++
	}
	if tagEnd >= len(l.body) || l.body[tagEnd] != '$' {
		return errors.New("unreadable dollar sign")
	}
	tag := l.body[l.at : tagEnd+1]
	closeAt := strings.Index(l.body[tagEnd+1:], tag)
	if closeAt < 0 {
		return fmt.Errorf("unterminated dollar-quoted string %s", tag)
	}
	l.emit(tokDollar, tagEnd+1+closeAt+len(tag))
	return nil
}

func (l *lexer) number() {
	end := l.at
	for end < len(l.body) && (isDigit(l.body[end]) || l.body[end] == '.' || l.body[end] == '_') {
		end++
	}
	if end < len(l.body) && (l.body[end] == 'e' || l.body[end] == 'E') {
		k := end + 1
		if k < len(l.body) && (l.body[k] == '+' || l.body[k] == '-') {
			k++
		}
		if k < len(l.body) && isDigit(l.body[k]) {
			for k < len(l.body) && isDigit(l.body[k]) {
				k++
			}
			end = k
		}
	}
	l.emit(tokNumber, end)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isWordByte(c byte, inside bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		return true
	case inside:
		return isDigit(c) || c == '$'
	}
	return false
}
