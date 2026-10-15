package guardrelease

import (
	"fmt"
	"sort"
	"time"
)

// Floor is what a host has already accepted from signed release manifests: the
// newest issue time, the highest minimum admitted version and every revocation
// ever seen. A manifest is a snapshot signed at one moment, so on its own it
// cannot tell a host that a newer one exists; the Floor is the host's memory of
// that. It only ever grows, which is what makes a revocation permanent even when
// an older, still-fresh manifest without it is presented later.
//
// The zero Floor accepts every manifest. The caller persists it in root-owned
// state and loads it before every manifest decision.
type Floor struct {
	IssuedAt               time.Time `json:"issued_at"`
	MinimumAdmittedVersion string    `json:"minimum_admitted_version,omitempty"`
	RevokedVersions        []string  `json:"revoked_versions,omitempty"`
	RevokedDigests         []string  `json:"revoked_digests,omitempty"`
}

// Accept refuses a manifest issued before the newest one already accepted and
// otherwise returns the Floor that includes it. The input is not modified.
func (f Floor) Accept(m *Manifest) (Floor, *Denial) {
	if !f.IssuedAt.IsZero() && m.IssuedAt.Before(f.IssuedAt) {
		return f, &Denial{CodeManifestSuperseded, fmt.Sprintf("the release manifest was issued at %s, before the newest accepted manifest (%s)",
			m.IssuedAt.UTC().Format(time.RFC3339), f.IssuedAt.UTC().Format(time.RFC3339))}
	}
	next := Floor{
		IssuedAt:               f.IssuedAt,
		MinimumAdmittedVersion: f.MinimumAdmittedVersion,
		RevokedVersions:        union(f.RevokedVersions, m.RevokedVersions),
		RevokedDigests:         union(f.RevokedDigests, m.RevokedDigests),
	}
	if m.IssuedAt.After(next.IssuedAt) {
		next.IssuedAt = m.IssuedAt
	}
	if order, ok := CompareVersions(m.MinimumAdmittedVersion, next.MinimumAdmittedVersion); next.MinimumAdmittedVersion == "" || (ok && order > 0) {
		next.MinimumAdmittedVersion = m.MinimumAdmittedVersion
	}
	return next, nil
}

// Admit applies the accumulated version policy to one version and digest.
func (f Floor) Admit(version, digest string) *Denial {
	return admit(f.MinimumAdmittedVersion, f.RevokedVersions, f.RevokedDigests, version, digest)
}

func union(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, item := range list {
			if _, ok := seen[item]; !ok {
				seen[item] = struct{}{}
				out = append(out, item)
			}
		}
	}
	sort.Strings(out)
	return out
}
