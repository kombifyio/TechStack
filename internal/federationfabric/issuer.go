// Package federationfabric is Techstack's side of the external inter-site
// fabric authority for a managed Modern deployment. It authors the opaque,
// unsigned ExternalFederationLinkBinding for the requirement the Home node
// compiled; StackKits validates, Owner-signs and adopts it. It creates no
// interface, key or tunnel.
package federationfabric

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kombifyio/stackkits/pkg/federationbinding"
)

// BindingValidity leaves a margin under the 24 hour contract maximum so the
// binding is still inside its window when StackKits adopts it.
const BindingValidity = 20 * time.Hour

// Issuer authors bindings. Now and Random exist for tests.
type Issuer struct {
	Now    func() time.Time
	Random func([]byte) error
}

// NewIssuer returns an issuer on the wall clock and the system random source.
func NewIssuer() *Issuer {
	return &Issuer{Now: time.Now, Random: func(buffer []byte) error { _, err := rand.Read(buffer); return err }}
}

// Request is the exact material one binding is authored from.
type Request struct {
	TenantID         string
	StackID          string
	Requirement      []byte
	StackKitsVersion string
	CandidateDigest  string
}

// Issue returns the unsigned binding for the requirement. The fabric and
// custody references are stable per tenant and stack, so a re-issued binding
// names the same fabric; the binding reference is fresh each time.
func (issuer *Issuer) Issue(request Request) ([]byte, error) {
	if request.TenantID == "" || request.StackID == "" {
		return nil, errors.New("a federation binding needs exact tenant and stack identity")
	}
	var requirement federationbinding.Document
	if err := json.Unmarshal(request.Requirement, &requirement); err != nil {
		return nil, fmt.Errorf("decode the node's link requirement: %w", err)
	}
	if requirement["stackId"] == nil || requirement["stackId"] == "" {
		return nil, errors.New("the link requirement names no stack")
	}
	nonce := make([]byte, 32)
	if err := issuer.Random(nonce); err != nil {
		return nil, fmt.Errorf("draw binding nonce: %w", err)
	}
	issuedAt := issuer.Now().UTC().Truncate(time.Second)
	binding, err := federationbinding.Build(requirement, federationbinding.Input{
		BindingEvidence:  nonce,
		FabricEvidence:   stable("fabric", request.TenantID, request.StackID),
		CustodyEvidence:  stable("custody", request.TenantID, request.StackID),
		StackKitsVersion: request.StackKitsVersion, CandidateDigest: request.CandidateDigest,
		IssuedAt: issuedAt, ValidUntil: issuedAt.Add(BindingValidity),
	})
	if err != nil {
		return nil, fmt.Errorf("build the federation link binding: %w", err)
	}
	return json.Marshal(binding)
}

func stable(label string, parts ...string) []byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte("techstack.federation-fabric/v1\x00" + label))
	for _, part := range parts {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(part))
	}
	return digest.Sum(nil)
}
