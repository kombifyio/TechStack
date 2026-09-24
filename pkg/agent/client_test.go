package agent

import (
	"context"
	"path/filepath"
	"testing"
)

func TestNewClientRequiresTLSIdentity(t *testing.T) {
	_, err := NewClient(Config{
		CoreAddress: "core:5263",
		AgentID:     "test-agent",
	})
	if err == nil {
		t.Fatal("client admitted without TLS identity")
	}
}

func TestLoadTLSConfigRejectsMissingCredentialFiles(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	_, err := loadTLSConfig(Config{
		CertFile: missing + ".crt",
		KeyFile:  missing + ".key",
		CAFile:   missing + ".ca",
	})
	if err == nil {
		t.Fatal("TLS configuration admitted missing credential files")
	}
}

func TestRegisterRejectsDisconnectedClient(t *testing.T) {
	if err := (&Client{}).Register(context.Background()); err == nil {
		t.Fatal("disconnected client registration succeeded")
	}
}
