package failureknowledge

import (
	_ "embed"
	"regexp"
	"strings"
	"unicode"
)

// rule replaces one class of variable token. Rules run in order: wider
// tokens (URLs, quoted values) go before the tokens they may contain.
type rule struct {
	pattern     *regexp.Regexp
	replacement string
	keep        func(match string) bool
}

var hostSuffixes = map[string]bool{
	"com": true, "net": true, "org": true, "io": true, "dev": true, "app": true, "cloud": true,
	"de": true, "eu": true, "at": true, "ch": true, "nl": true, "fr": true, "uk": true, "us": true,
	"info": true, "biz": true, "me": true, "co": true, "tech": true, "online": true, "site": true,
	"local": true, "lan": true, "home": true, "arpa": true, "internal": true, "localdomain": true,
	"test": true, "example": true, "invalid": true, "localhost": true, "cc": true,
}

var templateRules = []rule{
	{pattern: regexp.MustCompile(`\S*\[REDACTED\]\S*`), replacement: "<secret>"},
	{pattern: regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`), replacement: "<url>"},
	{pattern: regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}\b`), replacement: "<email>"},
	{pattern: regexp.MustCompile("`[^`\n]{0,200}`"), replacement: "<q>"},
	{pattern: regexp.MustCompile(`"[^"\n]{0,200}"`), replacement: "<q>"},
	{pattern: regexp.MustCompile(`(^|[\s=:(\[,])'[^'\n]{0,200}'`), replacement: "${1}<q>"},
	{pattern: regexp.MustCompile(`\b(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\b`), replacement: "<ts>"},
	{pattern: regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:[.,]\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?\b`), replacement: "<ts>"},
	{pattern: regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?\b`), replacement: "<ts>"},
	{pattern: regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`), replacement: "<uuid>"},
	{pattern: regexp.MustCompile(`(?i)\[?(?:\b[0-9a-f]{1,4}(?::[0-9a-f]{1,4}){7}\b|\b(?:[0-9a-f]{1,4}:){1,7}:(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4}){0,6}\b)?|::(?:[0-9a-f]{1,4}(?::[0-9a-f]{1,4}){0,6}\b)?)(?:%[0-9a-z]+)?\]?(?::\d{1,5}\b)?(?:/\d{1,3}\b)?`), replacement: "<ip>"},
	{pattern: regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?(?:/\d{1,2})?\b`), replacement: "<ip>"},
	{pattern: regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,24}\b(?::\d{1,5}\b)?`), replacement: "<host>", keep: notHostname},
	{pattern: regexp.MustCompile(`(?i)\blocalhost(?::\d{1,5})?\b`), replacement: "<host>"},
	{pattern: regexp.MustCompile(`(?i)\b[a-z_][a-z0-9_.-]*@(<host>|<ip>)`), replacement: "<user>@${1}"},
	{pattern: regexp.MustCompile(`(?i)\b[a-z_][a-z0-9_.-]*@[a-z0-9][a-z0-9.-]*\b`), replacement: "<user>@<host>"},
	{pattern: regexp.MustCompile(`(?i)(\b(?:user(?:name)?|login|owner|uid|account)(?:=|: ?)|--?user(?:name)?[ =])[a-z_][a-z0-9_.-]*`), replacement: "${1}<user>"},
	{pattern: regexp.MustCompile(`(^|[\s=:("'\[,<>])(?:~|\.{1,2})?/[A-Za-z0-9._@%+~-][^\s:"'(),\[\]<>]*`), replacement: "${1}<path>"},
	{pattern: regexp.MustCompile(`(?i)\b[a-z]:\\[^\s:"'<>|]*`), replacement: "<path>"},
	{pattern: regexp.MustCompile(`(?i)\b[0-9a-f]{8,}\b`), replacement: "<hex>", keep: lacksDigit},
	{pattern: regexp.MustCompile(`\b[A-Za-z0-9][A-Za-z0-9_-]{11,}\b`), replacement: "<id>", keep: lowEntropy},
	{pattern: regexp.MustCompile(`(?i)\b[a-z]{2,12}[_-][a-z0-9]{6,}\b`), replacement: "<id>", keep: lacksDigit},
	{pattern: regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?\s?(?:b|kb|mb|gb|tb|kib|mib|gib|tib)\b`), replacement: "<size>"},
	{pattern: regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h)(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))*\b`), replacement: "<dur>"},
	{pattern: regexp.MustCompile(`(?i)\bv?\d+(?:\.\d+){1,3}(?:[-+][0-9a-z.]+)?\b`), replacement: "<ver>"},
}

var (
	standaloneNumber = regexp.MustCompile(`\b\d+\b`)
	codePrefix       = regexp.MustCompile(`(?i)\b(?:http(?:/<ver>)?|status|errno|error|code)[ :=]*$`)
	exitPrefix       = regexp.MustCompile(`(?i)\bexit(?:ed)?(?: with)? (?:code|status)[ :=]*$`)
	whitespace       = regexp.MustCompile(`\s+`)
	// templateToken is one placeholder or one word, where a word may join
	// letters and digits with an apostrophe, dot, hyphen or underscore.
	templateToken = regexp.MustCompile(`<(?:secret|url|email|q|ts|uuid|ip|host|user|path|hex|id|size|dur|ver|n|word|name)>|[\p{L}\p{M}\p{N}_]+(?:['.-][\p{L}\p{M}\p{N}_]+)*`)
	placeholder   = regexp.MustCompile(`^<[a-z]+>$`)
)

//go:embed vocabulary_v2.txt
var vocabularyFile string

// vocabulary is the closed set of words a template keeps (section 5.8: a
// field that is not on the allowlist is not emitted).
var vocabulary = func() map[string]bool {
	words := map[string]bool{}
	for _, line := range strings.Split(vocabularyFile, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			words[line] = true
		}
	}
	return words
}()

// Template reduces redacted error text to its invariant shape. The rules
// first replace token classes they recognize; then every remaining word that
// is not in the vocabulary becomes a class placeholder, so a user, host,
// container or stack name never reaches the template.
func Template(text string) string {
	for _, r := range templateRules {
		if r.keep == nil {
			text = r.pattern.ReplaceAllString(text, r.replacement)
			continue
		}
		text = r.pattern.ReplaceAllStringFunc(text, func(match string) string {
			if r.keep(match) {
				return match
			}
			return r.pattern.ReplaceAllString(match, r.replacement)
		})
	}
	text = replaceNumbers(text)
	text = closeVocabulary(text)
	text = strings.TrimSpace(whitespace.ReplaceAllString(text, " "))
	if runes := []rune(text); len(runes) > maxTemplateRunes {
		text = string(runes[:maxTemplateRunes])
	}
	return text
}

// replaceNumbers keeps a number that is part of an error code (an HTTP
// status, an errno) and replaces every other standalone number.
func replaceNumbers(text string) string {
	var out strings.Builder
	last := 0
	for _, span := range standaloneNumber.FindAllStringIndex(text, -1) {
		prefix := text[:span[0]]
		out.WriteString(text[last:span[0]])
		if codePrefix.MatchString(prefix) && !exitPrefix.MatchString(prefix) {
			out.WriteString(text[span[0]:span[1]])
		} else {
			out.WriteString("<n>")
		}
		last = span[1]
	}
	out.WriteString(text[last:])
	return out.String()
}

// closeVocabulary keeps placeholders, numbers that replaceNumbers kept as
// codes, vocabulary words and ASCII punctuation, and replaces everything else.
func closeVocabulary(text string) string {
	var out strings.Builder
	last := 0
	for _, span := range templateToken.FindAllStringIndex(text, -1) {
		out.WriteString(asciiPunctuation(text[last:span[0]]))
		out.WriteString(vocabularyToken(text[span[0]:span[1]]))
		last = span[1]
	}
	out.WriteString(asciiPunctuation(text[last:]))
	return out.String()
}

// vocabularyToken returns a known word in its canonical form and the class
// placeholder of any other token. A capitalized or upper-case word is kept
// only when its lower-case form is known; a mixed-case word only verbatim.
func vocabularyToken(token string) string {
	if placeholder.MatchString(token) || strings.Trim(token, "0123456789") == "" || vocabulary[token] {
		return token
	}
	lower := strings.ToLower(token)
	if vocabulary[lower] && (token == strings.ToUpper(token) || token == titleCase(lower)) {
		return lower
	}
	switch {
	case strings.ContainsAny(token, "0123456789"):
		return "<id>"
	case strings.Contains(token, ".") || token != lower:
		return "<name>"
	default:
		return "<word>"
	}
}

func titleCase(lower string) string {
	runes := []rune(lower)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// asciiPunctuation keeps the spacing and ASCII punctuation between tokens
// and drops any other rune.
func asciiPunctuation(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || (r < unicode.MaxASCII && (unicode.IsPunct(r) || unicode.IsSymbol(r))) {
			return r
		}
		return -1
	}, text)
}

func notHostname(match string) bool {
	name := strings.ToLower(match)
	if colon := strings.IndexByte(name, ':'); colon >= 0 {
		name = name[:colon]
	}
	return !hostSuffixes[name[strings.LastIndexByte(name, '.')+1:]]
}

func lacksDigit(match string) bool {
	return !strings.ContainsAny(match, "0123456789")
}

// lowEntropy keeps long tokens that read as words or codes (letters and
// separators only); a long token that mixes letters and digits is an ID.
func lowEntropy(match string) bool {
	return lacksDigit(match) || !strings.ContainsAny(strings.ToLower(match), "abcdefghijklmnopqrstuvwxyz")
}
