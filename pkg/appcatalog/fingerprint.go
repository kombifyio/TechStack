package appcatalog

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"math/bits"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Layer 3 of recognition: HTTP fingerprints generated from ProjectDiscovery
// nuclei-templates (MIT), `http/technologies` and `http/exposed-panels`. The
// Guard fetches a unit's own published LAN port and hands the responses here;
// matching is pure and never performs I/O.

// HTTPObservation is one response the Guard received from a unit's port.
type HTTPObservation struct {
	// Path is the request path the response answers, after redirects.
	Path   string
	Status int
	Header http.Header
	// Body is the bounded response body.
	Body []byte
}

type fingerprintMatcher struct {
	Type            string   `json:"type"`
	Part            string   `json:"part"`
	Values          []string `json:"values"`
	Status          []int    `json:"status"`
	Condition       string   `json:"condition"`
	Negative        bool     `json:"negative"`
	CaseInsensitive bool     `json:"case_insensitive"`

	compiled []*regexp.Regexp
}

type httpFingerprint struct {
	App       string               `json:"app"`
	Template  string               `json:"template"`
	Paths     []string             `json:"paths"`
	Condition string               `json:"condition"`
	Matchers  []fingerprintMatcher `json:"matchers"`
}

type faviconFingerprint struct {
	App  string `json:"app"`
	Hash int32  `json:"hash"`
}

type fingerprintFile struct {
	Schema  string               `json:"schema"`
	Sources []generatedSource    `json:"sources"`
	HTTP    []httpFingerprint    `json:"http"`
	Favicon []faviconFingerprint `json:"favicon"`
}

type fingerprintSet struct {
	http    []httpFingerprint
	favicon map[int32]string
	paths   []string
	sources []generatedSource
}

const (
	conditionAnd       = "and"
	matcherStatus      = "status"
	matcherWord        = "word"
	matcherRegex       = "regex"
	partBody           = "body"
	partHeader         = "header"
	partAll            = "all"
	maxFingerprintPath = 3
)

var loadFingerprints = sync.OnceValue(func() *fingerprintSet {
	set := &fingerprintSet{favicon: map[int32]string{}}
	raw, err := generatedFiles.ReadFile("generated/fingerprints.json")
	var file fingerprintFile
	if err == nil {
		err = json.Unmarshal(raw, &file)
	}
	if err != nil {
		slog.Error("generated HTTP fingerprints are invalid", "error", err)
		return set
	}
	set.sources = file.Sources
	pathCount := map[string]int{}
	for _, fingerprint := range file.HTTP {
		if lookupKnown(fingerprint.App, true) == nil || !compileMatchers(fingerprint.Matchers) {
			continue
		}
		for index, path := range fingerprint.Paths {
			fingerprint.Paths[index] = normalizeFingerprintPath(path)
			if fingerprint.Paths[index] != "/" {
				pathCount[fingerprint.Paths[index]]++
			}
		}
		set.http = append(set.http, fingerprint)
	}
	for _, favicon := range file.Favicon {
		if _, taken := set.favicon[favicon.Hash]; !taken && lookupKnown(favicon.App, true) != nil {
			set.favicon[favicon.Hash] = favicon.App
		}
	}
	for path := range pathCount {
		set.paths = append(set.paths, path)
	}
	sort.Slice(set.paths, func(i, j int) bool {
		if pathCount[set.paths[i]] != pathCount[set.paths[j]] {
			return pathCount[set.paths[i]] > pathCount[set.paths[j]]
		}
		return set.paths[i] < set.paths[j]
	})
	if len(set.paths) > maxFingerprintPath {
		set.paths = set.paths[:maxFingerprintPath]
	}
	return set
})

// compileMatchers prepares a fingerprint's regexes. A fingerprint without a
// positive word or regex matcher, or with a pattern Go cannot compile, is
// dropped rather than matched weakly.
func compileMatchers(matchers []fingerprintMatcher) bool {
	if positiveMatchers(matchers) == 0 {
		return false
	}
	for index := range matchers {
		matcher := &matchers[index]
		if matcher.Type != matcherRegex {
			continue
		}
		for _, pattern := range matcher.Values {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return false
			}
			matcher.compiled = append(matcher.compiled, compiled)
		}
	}
	return true
}

func normalizeFingerprintPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || !strings.HasPrefix(path, "/") {
		return "/"
	}
	return path
}

// FingerprintPaths are the extra request paths worth fetching after the root
// page: the most common login and panel paths of the fingerprint set. The
// list is short so a probe stays a handful of requests per port.
func FingerprintPaths() []string {
	return append([]string(nil), loadFingerprints().paths...)
}

// MatchHTTP names the application the observed responses come from, or "".
// A fingerprint whose own request path was observed wins over one matched on
// a different page; the more positive matchers, the better the match.
func MatchHTTP(observations []HTTPObservation) string {
	set := loadFingerprints()
	prepared := make([]preparedResponse, 0, len(observations))
	for _, observation := range observations {
		prepared = append(prepared, prepareResponse(observation))
	}
	best, bestScore := "", 0
	for index := range set.http {
		fingerprint := &set.http[index]
		for responseIndex := range prepared {
			response := &prepared[responseIndex]
			if !fingerprint.matches(response) {
				continue
			}
			score := 1 + positiveMatchers(fingerprint.Matchers)
			if slices.Contains(fingerprint.Paths, response.path) {
				score += 100
			}
			if score > bestScore || (score == bestScore && fingerprint.App < best) {
				best, bestScore = fingerprint.App, score
			}
		}
	}
	return best
}

// preparedResponse holds each matchable part of one response once, so a few
// thousand matchers never copy the body again.
type preparedResponse struct {
	path   string
	status int
	parts  map[string]string
	folded map[string]string
}

func prepareResponse(observation HTTPObservation) preparedResponse {
	keys := make([]string, 0, len(observation.Header))
	for key := range observation.Header {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var header strings.Builder
	for _, key := range keys {
		for _, value := range observation.Header[key] {
			header.WriteString(key + ": " + value + "\r\n")
		}
	}
	body := string(observation.Body)
	return preparedResponse{
		path: normalizeFingerprintPath(observation.Path), status: observation.Status,
		parts:  map[string]string{partBody: body, partHeader: header.String(), partAll: header.String() + "\r\n" + body},
		folded: map[string]string{},
	}
}

func (r *preparedResponse) part(part string, fold bool) string {
	if part != partHeader && part != partAll {
		part = partBody
	}
	if !fold {
		return r.parts[part]
	}
	if folded, ok := r.folded[part]; ok {
		return folded
	}
	folded := strings.ToLower(r.parts[part])
	r.folded[part] = folded
	return folded
}

func positiveMatchers(matchers []fingerprintMatcher) int {
	count := 0
	for _, matcher := range matchers {
		if !matcher.Negative && (matcher.Type == matcherWord || matcher.Type == matcherRegex) {
			count++
		}
	}
	return count
}

// matches applies the template's matchers-condition. Under "or" only a
// positive word or regex matcher can make the match: a status code or an
// absent word alone is true for almost every page.
func (f *httpFingerprint) matches(response *preparedResponse) bool {
	and := f.Condition == conditionAnd
	for index := range f.Matchers {
		matcher := &f.Matchers[index]
		matched := matcher.matches(response)
		if and && !matched {
			return false
		}
		if !and && matched && !matcher.Negative && matcher.Type != matcherStatus {
			return true
		}
	}
	return and
}

func (m *fingerprintMatcher) matches(response *preparedResponse) bool {
	var result bool
	switch m.Type {
	case matcherStatus:
		result = slices.Contains(m.Status, response.status)
	case matcherWord:
		subject := response.part(m.Part, m.CaseInsensitive)
		result = evaluateAll(len(m.Values), m.Condition == conditionAnd, func(index int) bool {
			value := m.Values[index]
			if m.CaseInsensitive {
				value = strings.ToLower(value)
			}
			return strings.Contains(subject, value)
		})
	case matcherRegex:
		subject := response.part(m.Part, false)
		result = evaluateAll(len(m.compiled), m.Condition == conditionAnd, func(index int) bool {
			return m.compiled[index].MatchString(subject)
		})
	default:
		return false
	}
	return result != m.Negative
}

func evaluateAll(count int, all bool, test func(int) bool) bool {
	if count == 0 {
		return false
	}
	for index := 0; index < count; index++ {
		matched := test(index)
		if all && !matched {
			return false
		}
		if !all && matched {
			return true
		}
	}
	return all
}

// MatchFavicon names the application a favicon belongs to, by the Shodan-style
// hash nuclei uses: MurmurHash3 (x86, 32-bit, seed 0) of the body's
// MIME-wrapped base64 encoding.
func MatchFavicon(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return loadFingerprints().favicon[FaviconHash(body)]
}

// FaviconHash is mmh3(base64_py(body)) as nuclei and Shodan compute it.
func FaviconHash(body []byte) int32 {
	encoded := base64.StdEncoding.EncodeToString(body)
	var wrapped bytes.Buffer
	for len(encoded) > 76 {
		wrapped.WriteString(encoded[:76])
		wrapped.WriteByte('\n')
		encoded = encoded[76:]
	}
	wrapped.WriteString(encoded)
	wrapped.WriteByte('\n')
	return int32(murmur3(wrapped.Bytes()))
}

func murmur3(data []byte) uint32 {
	const c1, c2 = 0xcc9e2d51, 0x1b873593
	mix := func(k uint32) uint32 {
		k *= c1
		k = bits.RotateLeft32(k, 15)
		return k * c2
	}
	var hash uint32
	blocks := len(data) / 4
	for index := 0; index < blocks; index++ {
		hash ^= mix(binary.LittleEndian.Uint32(data[index*4:]))
		hash = bits.RotateLeft32(hash, 13)
		hash = hash*5 + 0xe6546b64
	}
	var tail uint32
	switch rest := data[blocks*4:]; len(rest) {
	case 3:
		tail ^= uint32(rest[2]) << 16
		fallthrough
	case 2:
		tail ^= uint32(rest[1]) << 8
		fallthrough
	case 1:
		tail ^= uint32(rest[0])
		hash ^= mix(tail)
	}
	hash ^= uint32(len(data))
	hash ^= hash >> 16
	hash *= 0x85ebca6b
	hash ^= hash >> 13
	hash *= 0xc2b2ae35
	hash ^= hash >> 16
	return hash
}
