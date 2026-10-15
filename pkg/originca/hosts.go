package originca

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	// DomainSuffix is the only registered domain an origin certificate may
	// cover: the managed kombify.me routes and their origin records.
	DomainSuffix = ".kombify.me"
	// MaxHosts is Cloudflare's SAN limit for one Origin CA certificate.
	MaxHosts = 200
)

var hostLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidHost reports a hostname an origin certificate may cover: a lowercase
// DNS name below kombify.me, optionally with one leading wildcard label.
func ValidHost(host string) bool {
	host = strings.TrimPrefix(host, "*.")
	if !strings.HasSuffix(host, DomainSuffix) || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, DomainSuffix), ".") {
		if !hostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// NormalizeHosts validates, lowercases, de-duplicates and sorts a host set.
// Any name outside kombify.me refuses the whole set.
func NormalizeHosts(hosts []string) ([]string, error) {
	if len(hosts) == 0 || len(hosts) > MaxHosts {
		return nil, fmt.Errorf("origin certificate requires between 1 and %d hostnames", MaxHosts)
	}
	seen := make(map[string]bool, len(hosts))
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if !ValidHost(host) {
			return nil, fmt.Errorf("origin certificate hostname %q is not a managed kombify.me name", host)
		}
		if !seen[host] {
			seen[host] = true
			out = append(out, host)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SameHosts reports whether two host sets are equal after normalization.
func SameHosts(a, b []string) bool {
	left, err := NormalizeHosts(a)
	if err != nil {
		return false
	}
	right, err := NormalizeHosts(b)
	if err != nil || len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// ContainsPrivateKey reports PEM private-key material of any kind. No value
// that reaches Cloudflare, custody or a result may carry it: the key exists
// only on the node.
func ContainsPrivateKey(data []byte) bool {
	return strings.Contains(string(data), "PRIVATE KEY")
}
