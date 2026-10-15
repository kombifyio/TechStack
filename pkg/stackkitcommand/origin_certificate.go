package stackkitcommand

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/originca"
)

const (
	// OriginCertificateVersion is the data subject of the origin-certificate
	// operations (stackkit.origin-certificate/v1).
	OriginCertificateVersion = "stackkit.origin-certificate/v1"
	// MaxOriginCertificateBytes bounds the public certificate transport.
	MaxOriginCertificateBytes = 64 << 10

	originCertificateRequestOperation = "public-tls.origin-certificate.request"
	originCertificateInstallOperation = "public-tls.origin-certificate.install"
)

// IsOriginCertificateOperation reports the two Advanced operations that move
// the managed kombify.me origin certificate: the node generates and keeps the
// key, and only a CSR and the public certificate cross the control plane.
func IsOriginCertificateOperation(operation agentpb.StackKitOperation) bool {
	return operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_REQUEST ||
		operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_INSTALL
}

// validateOriginCertificateFields keeps the origin inputs on exactly the
// operation that consumes them.
func validateOriginCertificateFields(command *agentpb.StackKitCommand) error {
	request := command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_REQUEST
	install := command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_INSTALL
	if !request && len(command.OriginHosts) != 0 {
		return fmt.Errorf("StackKit origin_hosts is only valid for origin certificate request")
	}
	if !install && len(command.OriginCertificatePem) != 0 {
		return fmt.Errorf("StackKit origin_certificate_pem is only valid for origin certificate install")
	}
	switch {
	case request:
		hosts, err := originca.NormalizeHosts(command.OriginHosts)
		if err != nil {
			return fmt.Errorf("StackKit origin certificate request: %w", err)
		}
		if len(hosts) != len(command.OriginHosts) {
			return fmt.Errorf("StackKit origin certificate request hostnames must be unique")
		}
	case install:
		if len(command.OriginCertificatePem) == 0 || len(command.OriginCertificatePem) > MaxOriginCertificateBytes {
			return fmt.Errorf("StackKit origin certificate install requires one certificate within %d bytes", MaxOriginCertificateBytes)
		}
		if originca.ContainsPrivateKey(command.OriginCertificatePem) {
			return fmt.Errorf("StackKit origin certificate install carries a public certificate only")
		}
		if _, err := parseCertificatePEM(command.OriginCertificatePem); err != nil {
			return fmt.Errorf("StackKit origin certificate install: %w", err)
		}
	}
	return nil
}

func parseCertificatePEM(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("certificate must be one PEM CERTIFICATE")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return certificate, nil
}

// OriginCertificateRequestEvidence is the admitted CSR of a request.
type OriginCertificateRequestEvidence struct {
	Hosts     []string
	CSRPEM    string
	CSRSHA256 string
}

// OriginCertificateInstallEvidence is the node's confirmation of an install.
type OriginCertificateInstallEvidence struct {
	Hosts             []string
	SerialHex         string
	NotAfter          time.Time
	CertificateSHA256 string
}

type originCertificateData struct {
	SchemaVersion     string   `json:"schemaVersion"`
	Phase             string   `json:"phase"`
	Hosts             []string `json:"hosts"`
	KeyAlgorithm      string   `json:"keyAlgorithm"`
	CSRPEM            string   `json:"csrPem"`
	CSRSHA256         string   `json:"csrSha256"`
	SerialHex         string   `json:"serialHex"`
	NotAfter          string   `json:"notAfter"`
	CertificateSHA256 string   `json:"certificateSha256"`
}

func decodeOriginCertificateData(command *agentpb.StackKitCommand, result *agentpb.StackKitResult, phase string) (originCertificateData, error) {
	if command == nil || result == nil || !result.Success || !IsOriginCertificateOperation(command.Operation) {
		return originCertificateData{}, fmt.Errorf("successful StackKit origin certificate evidence is required")
	}
	var envelope struct {
		SchemaVersion string          `json:"schemaVersion"`
		Command       string          `json:"command"`
		Status        string          `json:"status"`
		Data          json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return originCertificateData{}, fmt.Errorf("decode StackKit origin certificate result: %w", err)
	}
	if envelope.SchemaVersion != CommandResultVersion || envelope.Command != ResultCommandName(command.Operation) || envelope.Status != commandResultStatusSuccess {
		return originCertificateData{}, fmt.Errorf("StackKit origin certificate command-result identity is invalid")
	}
	var data originCertificateData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return originCertificateData{}, fmt.Errorf("decode StackKit origin certificate data: %w", err)
	}
	if data.SchemaVersion != OriginCertificateVersion || data.Phase != phase {
		return originCertificateData{}, fmt.Errorf("StackKit origin certificate result is not a %s %s", phase, OriginCertificateVersion)
	}
	return data, nil
}

func sameHostSet(want, got []string) ([]string, bool) {
	if !originca.SameHosts(want, got) {
		return nil, false
	}
	normalized, _ := originca.NormalizeHosts(got)
	return normalized, true
}

// ParseOriginCertificateRequest admits the CSR of a successful request. The
// CSR must be a valid, self-signed ECDSA request for exactly the dispatched
// hostnames, and its digest must match the node's. A result that carries
// private key material is refused outright.
func ParseOriginCertificateRequest(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (OriginCertificateRequestEvidence, error) {
	if command == nil || command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_REQUEST {
		return OriginCertificateRequestEvidence{}, fmt.Errorf("StackKit origin certificate request evidence needs the request command")
	}
	if result != nil && originca.ContainsPrivateKey(result.CommandResultJson) {
		return OriginCertificateRequestEvidence{}, fmt.Errorf("StackKit origin certificate result carries private key material")
	}
	data, err := decodeOriginCertificateData(command, result, "request")
	if err != nil {
		return OriginCertificateRequestEvidence{}, err
	}
	want, err := originca.NormalizeHosts(command.OriginHosts)
	if err != nil {
		return OriginCertificateRequestEvidence{}, err
	}
	hosts, ok := sameHostSet(want, data.Hosts)
	if !ok || data.KeyAlgorithm != "ecdsa-p256" {
		return OriginCertificateRequestEvidence{}, fmt.Errorf("StackKit origin certificate request does not cover the dispatched hostnames")
	}
	block, _ := pem.Decode([]byte(data.CSRPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return OriginCertificateRequestEvidence{}, fmt.Errorf("StackKit origin certificate request returned no PEM CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return OriginCertificateRequestEvidence{}, fmt.Errorf("StackKit origin certificate CSR is invalid")
	}
	if csr.PublicKeyAlgorithm != x509.ECDSA {
		return OriginCertificateRequestEvidence{}, fmt.Errorf("StackKit origin certificate CSR is not ECDSA")
	}
	for _, name := range csr.DNSNames {
		if !originca.ValidHost(name) {
			return OriginCertificateRequestEvidence{}, fmt.Errorf("StackKit origin certificate CSR names %q outside kombify.me", name)
		}
	}
	if len(csr.DNSNames) > 0 {
		if _, ok := sameHostSet(want, csr.DNSNames); !ok {
			return OriginCertificateRequestEvidence{}, fmt.Errorf("StackKit origin certificate CSR differs from the dispatched hostnames")
		}
	}
	digest := sha256.Sum256(block.Bytes)
	if data.CSRSHA256 != "sha256:"+hex.EncodeToString(digest[:]) {
		return OriginCertificateRequestEvidence{}, fmt.Errorf("StackKit origin certificate CSR digest does not match")
	}
	return OriginCertificateRequestEvidence{Hosts: hosts, CSRPEM: data.CSRPEM, CSRSHA256: data.CSRSHA256}, nil
}

// ParseOriginCertificateInstall admits the node's install confirmation: the
// hostnames and the digest must be those of the certificate Techstack sent.
func ParseOriginCertificateInstall(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (OriginCertificateInstallEvidence, error) {
	if command == nil || command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_INSTALL {
		return OriginCertificateInstallEvidence{}, fmt.Errorf("StackKit origin certificate install evidence needs the install command")
	}
	data, err := decodeOriginCertificateData(command, result, "install")
	if err != nil {
		return OriginCertificateInstallEvidence{}, err
	}
	leaf, err := parseCertificatePEM(command.OriginCertificatePem)
	if err != nil {
		return OriginCertificateInstallEvidence{}, err
	}
	digest := sha256.Sum256(leaf.Raw)
	notAfter, parseErr := time.Parse(time.RFC3339, data.NotAfter)
	if data.CertificateSHA256 != "sha256:"+hex.EncodeToString(digest[:]) || parseErr != nil || !notAfter.Equal(leaf.NotAfter) {
		return OriginCertificateInstallEvidence{}, fmt.Errorf("StackKit origin certificate install does not pin the delivered certificate")
	}
	hosts, err := originca.NormalizeHosts(data.Hosts)
	if err != nil {
		return OriginCertificateInstallEvidence{}, err
	}
	return OriginCertificateInstallEvidence{Hosts: hosts, SerialHex: data.SerialHex, NotAfter: notAfter.UTC(), CertificateSHA256: data.CertificateSHA256}, nil
}

// admitOriginCertificateResult is the ParseAdvancedOperationResult branch of
// the two operations; the typed evidence is read with the parsers above.
func admitOriginCertificateResult(operation string, command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (AdvancedOperationResult, error) {
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_REQUEST {
		evidence, err := ParseOriginCertificateRequest(command, result)
		if err != nil {
			return AdvancedOperationResult{}, err
		}
		report, _ := json.Marshal(map[string]interface{}{"phase": "request", "hosts": evidence.Hosts, "csrSha256": evidence.CSRSHA256})
		return AdvancedOperationResult{Operation: operation, Status: "requested", Report: report, OriginRequest: &evidence}, nil
	}
	evidence, err := ParseOriginCertificateInstall(command, result)
	if err != nil {
		return AdvancedOperationResult{}, err
	}
	report, _ := json.Marshal(map[string]interface{}{
		"phase": "install", "hosts": evidence.Hosts, "serialHex": evidence.SerialHex,
		"notAfter": evidence.NotAfter.Format(time.RFC3339), "certificateSha256": evidence.CertificateSHA256,
	})
	return AdvancedOperationResult{Operation: operation, Status: "installed", Report: report, OriginInstall: &evidence}, nil
}
