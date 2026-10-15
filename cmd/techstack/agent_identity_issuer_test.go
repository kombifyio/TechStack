package main

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/config"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/grpcserver"
)

func TestConfiguredAgentIdentityIssuerRequiresCompleteMintingAuthority(t *testing.T) {
	server := &grpcserver.Server{}
	certManager := &auth.CertManager{}

	tests := []struct {
		name                 string
		server               *grpcserver.Server
		certManager          *auth.CertManager
		enrollmentStoreWired bool
		wantIssuer           bool
	}{
		{name: "https only", server: server, enrollmentStoreWired: true},
		{name: "missing enrollment store", server: server, certManager: certManager},
		{name: "missing grpc server", certManager: certManager, enrollmentStoreWired: true},
		{name: "complete authority", server: server, certManager: certManager, enrollmentStoreWired: true, wantIssuer: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issuer := configuredAgentIdentityIssuer(tt.server, tt.certManager, tt.enrollmentStoreWired)
			if got := issuer != nil; got != tt.wantIssuer {
				t.Fatalf("issuer configured = %v, want %v", got, tt.wantIssuer)
			}
		})
	}
}

func TestWorkerRoutesDoNotTreatTransportOnlyGRPCServerAsIdentityIssuer(t *testing.T) {
	server := &grpcserver.Server{}
	deps := routeDeps{grpc: &grpcBoot{server: server}}
	if issuer := workerAgentIdentityIssuer(deps); issuer != nil {
		t.Fatal("transport-only gRPC server was exposed as an agent identity issuer")
	}

	deps.grpc.identityIssuer = server
	if issuer := workerAgentIdentityIssuer(deps); issuer != server {
		t.Fatal("configured agent identity issuer was not exposed to worker routes")
	}
}

func TestHostedEditionsNeverIssueGRPCAgentIdentities(t *testing.T) {
	server := &grpcserver.Server{}
	certManager := &auth.CertManager{}
	for _, edition := range []config.Edition{config.EditionPreview, config.EditionSaaSStandalone, config.EditionSaaSEmbedded} {
		if issuer := editionAgentIdentityIssuer(edition, server, certManager, true); issuer != nil {
			t.Fatalf("edition %s issues gRPC agent identities with an agent CA configured", edition)
		}
	}
	if issuer := editionAgentIdentityIssuer(config.EditionSelfHostOSS, server, certManager, true); issuer == nil {
		t.Fatal("self-hosted edition lost gRPC agent identity issuance")
	}
}
