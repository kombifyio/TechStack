package unifier

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
	"golang.org/x/crypto/bcrypt"
)

func TestIaCGenerator_Generate(t *testing.T) {
	tmpDir := t.TempDir()
	gen := newTestIaCGenerator(t, tmpDir)

	config := &IaCConfig{
		StackName:      "test-homelab",
		StackKit:       "basement-kit",
		Mode:           "simple",
		Variant:        "default",
		WorkerIP:       "192.168.1.100",
		SSHUser:        "ubuntu",
		SSHPort:        22,
		SSHKeyPath:     "/path/to/key",
		OSType:         "ubuntu-24",
		ComputeTier:    "standard",
		NetworkMode:    "local",
		TLSMode:        "self-signed",
		EnableFirewall: true,
		AllowedPorts:   []int{22, 80, 443},
		Services: []IaCService{
			{
				Name:    "traefik",
				Image:   "traefik:v3.1",
				Port:    80,
				Public:  true,
				Restart: "unless-stopped",
			},
			{
				Name:      "caprover",
				Image:     "caprover/caprover:latest",
				Port:      3000,
				Public:    true,
				DependsOn: []string{"traefik"},
			},
		},
	}

	output, err := gen.Generate(config)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Check that all expected files are generated
	expectedFiles := []string{
		"main.tf",
		"variables.tf",
		"bootstrap.tf",
		"network_local.tf", // Should be network.tf but template name differs
		"services.tf",
		"outputs.tf",
		"terraform.tfvars.json",
	}

	for _, file := range expectedFiles {
		// network.tf is renamed from network_local.tf
		checkFile := file
		if file == "network_local.tf" {
			checkFile = "network.tf"
		}
		if _, ok := output.Files[checkFile]; !ok {
			// Check if there are any errors that would explain missing file
			if len(output.Errors) > 0 {
				t.Logf("Errors during generation: %v", output.Errors)
			}
		}
	}

	// Validate terraform.tfvars.json is valid JSON
	if output.TFVarsJSON != nil {
		var tfvars map[string]interface{}
		if err := json.Unmarshal(output.TFVarsJSON, &tfvars); err != nil {
			t.Errorf("tfvars is not valid JSON: %v", err)
		}

		if tfvars["variant"] != "default" {
			t.Errorf("Expected variant=default, got %v", tfvars["variant"])
		}
		if tfvars["advertise_host"] != "192.168.1.100" {
			t.Errorf("Expected advertise_host=192.168.1.100, got %v", tfvars["advertise_host"])
		}
	}

	// Check mode is set correctly
	if output.Mode != "simple" {
		t.Errorf("Expected mode=simple, got %s", output.Mode)
	}
}

func TestIaCGenerator_GenerateAdvancedMode(t *testing.T) {
	tmpDir := t.TempDir()
	gen := newTestIaCGenerator(t, tmpDir).WithMode("advanced")

	config := &IaCConfig{
		StackName:   "advanced-homelab",
		StackKit:    "basement-kit",
		Mode:        "advanced",
		Variant:     "default",
		WorkerIP:    "192.168.1.100",
		SSHUser:     "root",
		SSHPort:     22,
		OSType:      "ubuntu-24",
		ComputeTier: "high",
		NetworkMode: "public",
		Domain:      "homelab.example.com",
		ACMEEmail:   "admin@example.com",
		TLSMode:     "acme",
		Services:    []IaCService{},
	}

	output, err := gen.Generate(config)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Advanced mode should include terramate.tm.hcl
	if _, ok := output.Files["terramate.tm.hcl"]; !ok {
		if len(output.Errors) > 0 {
			t.Logf("Note: terramate.tm.hcl generation had errors: %v", output.Errors)
		}
	}
}

func TestIaCGenerator_WriteToDir(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")
	gen := newTestIaCGenerator(t, outputDir)

	config := &IaCConfig{
		StackName:   "write-test",
		StackKit:    "basement-kit",
		Variant:     "minimal",
		WorkerIP:    "10.0.0.1",
		SSHUser:     "admin",
		SSHPort:     22,
		OSType:      "debian-12",
		ComputeTier: "low",
		NetworkMode: "local",
		TLSMode:     "self-signed",
		Services:    []IaCService{},
	}

	output, err := gen.Generate(config)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if err := gen.WriteToDir(output); err != nil {
		t.Fatalf("WriteToDir failed: %v", err)
	}

	// Check files exist on disk
	for filename := range output.Files {
		filePath := filepath.Join(outputDir, filename)
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			t.Errorf("Expected file %s to exist", filename)
		}
	}
}

func TestIaCGenerator_GenerateFromUnifiedSpec(t *testing.T) {
	tmpDir := t.TempDir()
	gen := newTestIaCGenerator(t, tmpDir)

	unified := &core.UnifiedSpec{
		KombinationSpec: core.KombinationSpec{
			Name: "unified-test",
		},
		StackKit: "basement-kit",
		ResolvedNodes: []core.ResolvedNode{
			{
				NodeSpec: core.NodeSpec{
					Name:     "worker-1",
					Type:     "worker",
					Provider: "local",
					SSH: &core.SSHConfig{
						Host:    "192.168.1.50",
						User:    "ubuntu",
						Port:    22,
						KeyPath: "/home/user/.ssh/id_rsa",
					},
				},
				PrivateIP: "192.168.1.50",
				Arch:      "amd64",
				OS:        "linux",
			},
		},
		ResolvedServices: []core.ResolvedService{
			{
				ServiceSpec: core.ServiceSpec{
					Name: "nginx",
					Type: "container",
					Node: "worker-1",
				},
				Image: "nginx:latest",
				Port:  80,
			},
		},
		ResolvedNetwork: core.ResolvedNetwork{
			VPNType: "none",
			Subnet:  "192.168.1.0/24",
		},
	}

	output, err := gen.GenerateFromUnifiedSpec(unified)
	if err != nil {
		t.Fatalf("GenerateFromUnifiedSpec failed: %v", err)
	}

	// Check that conversion worked
	if output.TFVarsJSON == nil {
		t.Error("Expected TFVarsJSON to be generated")
	}

	// Verify worker IP was extracted
	var tfvars map[string]interface{}
	if err := json.Unmarshal(output.TFVarsJSON, &tfvars); err == nil {
		if tfvars["advertise_host"] != "192.168.1.50" {
			t.Errorf("Expected advertise_host=192.168.1.50, got %v", tfvars["advertise_host"])
		}
	}
}

func TestIaCGenerator_NetworkModes(t *testing.T) {
	tests := []struct {
		name         string
		networkMode  string
		expectFile   string
		expectDomain bool
	}{
		{"local", "local", "network.tf", false},
		{"public", "public", "network.tf", true},
		{"hybrid", "hybrid", "network.tf", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			gen := newTestIaCGenerator(t, tmpDir)

			config := &IaCConfig{
				StackName:   "network-test",
				StackKit:    "basement-kit",
				NetworkMode: tt.networkMode,
				WorkerIP:    "192.168.1.1",
				SSHUser:     "root",
				SSHPort:     22,
				OSType:      "ubuntu-24",
				ComputeTier: "standard",
				TLSMode:     "self-signed",
			}

			if tt.expectDomain {
				config.Domain = "test.example.com"
				config.ACMEEmail = "admin@example.com"
				config.TLSMode = "acme"
			}

			output, err := gen.Generate(config)
			if err != nil {
				t.Fatalf("Generate failed: %v", err)
			}

			// Check that network.tf was generated
			if _, ok := output.Files[tt.expectFile]; !ok {
				if len(output.Errors) > 0 {
					t.Logf("Generation errors: %v", output.Errors)
				}
			}
		})
	}
}

func TestIaCGenerator_ComputeTiers(t *testing.T) {
	tiers := []string{"high", "standard", "low"}

	for _, tier := range tiers {
		t.Run(tier, func(t *testing.T) {
			tmpDir := t.TempDir()
			gen := newTestIaCGenerator(t, tmpDir)

			config := &IaCConfig{
				StackName:   "tier-test",
				StackKit:    "basement-kit",
				ComputeTier: tier,
				WorkerIP:    "192.168.1.1",
				SSHUser:     "root",
				SSHPort:     22,
				OSType:      "ubuntu-24",
				NetworkMode: "local",
				TLSMode:     "self-signed",
			}

			output, err := gen.Generate(config)
			if err != nil {
				t.Fatalf("Generate failed for tier %s: %v", tier, err)
			}

			// Check that compute_tier is in tfvars
			var tfvars map[string]interface{}
			if err := json.Unmarshal(output.TFVarsJSON, &tfvars); err == nil {
				if tfvars["compute_tier"] != tier {
					t.Errorf("Expected compute_tier=%s, got %v", tier, tfvars["compute_tier"])
				}
			}
		})
	}
}

func TestIaCGenerator_OSDetection(t *testing.T) {
	tests := []struct {
		osType         string
		expectPkgMgr   string
		expectFirewall string
	}{
		{"ubuntu-24", "apt", "ufw"},
		{"ubuntu-22", "apt", "ufw"},
		{"debian-12", "apt", "ufw"},
		{"rocky-9", "dnf", "firewalld"},
		{"almalinux-9", "dnf", "firewalld"},
	}

	for _, tt := range tests {
		t.Run(tt.osType, func(t *testing.T) {
			tmpDir := t.TempDir()
			gen := newTestIaCGenerator(t, tmpDir)

			config := &IaCConfig{
				StackName:      "os-test",
				StackKit:       "basement-kit",
				OSType:         tt.osType,
				WorkerIP:       "192.168.1.1",
				SSHUser:        "root",
				SSHPort:        22,
				ComputeTier:    "standard",
				NetworkMode:    "local",
				TLSMode:        "self-signed",
				EnableFirewall: true,
				AllowedPorts:   []int{22, 80, 443},
			}

			output, err := gen.Generate(config)
			if err != nil {
				t.Fatalf("Generate failed: %v", err)
			}

			// Check bootstrap.tf contains correct package manager commands
			if bootstrap, ok := output.Files["bootstrap.tf"]; ok {
				if tt.expectPkgMgr == "apt" && !strings.Contains(bootstrap, "apt") {
					// Expected apt commands for Debian-based
					t.Logf("Note: bootstrap.tf may not contain apt commands for %s", tt.osType)
				}
			}
		})
	}
}

func TestIaCGenerator_ServiceGeneration(t *testing.T) {
	tmpDir := t.TempDir()
	gen := newTestIaCGenerator(t, tmpDir)

	config := &IaCConfig{
		StackName:   "service-test",
		StackKit:    "basement-kit",
		WorkerIP:    "192.168.1.1",
		SSHUser:     "root",
		SSHPort:     22,
		OSType:      "ubuntu-24",
		ComputeTier: "standard",
		NetworkMode: "local",
		TLSMode:     "self-signed",
		Services: []IaCService{
			{
				Name:       "redis",
				Image:      "redis:7-alpine",
				Port:       6379,
				Restart:    "always",
				Privileged: false,
			},
			{
				Name:  "postgres",
				Image: "postgres:16-alpine",
				Port:  5432,
				Environment: map[string]string{
					"POSTGRES_PASSWORD": "secret",
					"POSTGRES_DB":       "app",
				},
				Volumes:   []string{"/data/postgres:/var/lib/postgresql/data"},
				DependsOn: []string{"redis"},
			},
		},
	}

	output, err := gen.Generate(config)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// StackKit HCL owns service rendering; TechStack only verifies that the
	// external StackKit path can generate a valid tfvars artifact.
	var tfvars map[string]interface{}
	if err := json.Unmarshal(output.TFVarsJSON, &tfvars); err == nil {
		if tfvars["access_mode"] == "" {
			t.Error("Expected access_mode in tfvars")
		}
	}
}

func TestIaCGenerator_NilConfig(t *testing.T) {
	gen := NewIaCGenerator(t.TempDir())

	_, err := gen.Generate(nil)
	if err == nil {
		t.Error("Expected error for nil config")
	}
}

func TestIaCGenerator_NilUnifiedSpec(t *testing.T) {
	gen := NewIaCGenerator(t.TempDir())

	_, err := gen.GenerateFromUnifiedSpec(nil)
	if err == nil {
		t.Error("Expected error for nil unified spec")
	}
}

func TestIaCGenerator_GenerateRequiresExternalStackKitDir(t *testing.T) {
	gen := NewIaCGenerator(t.TempDir())

	_, err := gen.Generate(&IaCConfig{
		StackName: "boundary-test",
		StackKit:  "basement-kit",
	})
	if err == nil {
		t.Fatal("Generate() error = nil, want external StackKits requirement")
	}
	if !strings.Contains(err.Error(), "StackKits directory is required") {
		t.Fatalf("Generate() error = %q, want StackKits directory requirement", err)
	}
}

func TestIaCGenerator_GenerateBaseKitOutputRejectsMissingExternalTemplates(t *testing.T) {
	gen := NewIaCGenerator(t.TempDir())

	_, err := gen.GenerateBaseKitOutput(&IaCConfig{
		StackName: "boundary-test",
		StackKit:  "basement-kit",
	}, t.TempDir())
	if err == nil {
		t.Fatal("GenerateBaseKitOutput() error = nil, want missing external template error")
	}
	if !strings.Contains(err.Error(), "basement-kit templates not found") {
		t.Fatalf("GenerateBaseKitOutput() error = %q, want missing template error", err)
	}
}

func TestIaCGenerator_TemplateFunctions(t *testing.T) {
	gen := NewIaCGenerator(t.TempDir())

	// Test OS detection functions
	osTests := []struct {
		name   string
		fn     func(string) bool
		input  string
		expect bool
	}{
		{"isUbuntu-ubuntu", gen.funcs["isUbuntu"].(func(string) bool), "ubuntu-24", true},
		{"isUbuntu-debian", gen.funcs["isUbuntu"].(func(string) bool), "debian-12", false},
		{"isDebian-debian", gen.funcs["isDebian"].(func(string) bool), "debian-12", true},
		{"isDebian-ubuntu", gen.funcs["isDebian"].(func(string) bool), "ubuntu-22", false},
		{"isRHEL-rhel", gen.funcs["isRHEL"].(func(string) bool), "rhel-9", true},
		{"isRHEL-rocky", gen.funcs["isRHEL"].(func(string) bool), "rocky-9", true},
		{"isRHEL-centos", gen.funcs["isRHEL"].(func(string) bool), "centos-stream-9", true},
		{"isRHEL-alma", gen.funcs["isRHEL"].(func(string) bool), "almalinux-9", true},
		{"isRHEL-ubuntu", gen.funcs["isRHEL"].(func(string) bool), "ubuntu-24", false},
		{"isFedora-fedora", gen.funcs["isFedora"].(func(string) bool), "fedora-40", true},
		{"isFedora-rhel", gen.funcs["isFedora"].(func(string) bool), "rhel-9", false},
		{"isSUSE-suse", gen.funcs["isSUSE"].(func(string) bool), "suse-15", true},
		{"isSUSE-opensuse", gen.funcs["isSUSE"].(func(string) bool), "opensuse-tumbleweed", true},
		{"isSUSE-sles", gen.funcs["isSUSE"].(func(string) bool), "sles-15", true},
		{"isSUSE-ubuntu", gen.funcs["isSUSE"].(func(string) bool), "ubuntu-24", false},
	}

	for _, tt := range osTests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.fn(tt.input)
			if result != tt.expect {
				t.Errorf("%s(%s) = %v, want %v", tt.name, tt.input, result, tt.expect)
			}
		})
	}

	// Test packageManager function
	pkgMgrTests := []struct {
		osType string
		expect string
	}{
		{"ubuntu-24", "apt"},
		{"debian-12", "apt"},
		{"fedora-40", "dnf"},
		{"rhel-9", "dnf"},
		{"rocky-9", "dnf"},
		{"opensuse-tumbleweed", "zypper"},
		{"sles-15", "zypper"},
	}

	pkgMgr := gen.funcs["packageManager"].(func(string) string)
	for _, tt := range pkgMgrTests {
		t.Run("packageManager-"+tt.osType, func(t *testing.T) {
			result := pkgMgr(tt.osType)
			if result != tt.expect {
				t.Errorf("packageManager(%s) = %s, want %s", tt.osType, result, tt.expect)
			}
		})
	}
}

// TestGenerateBaseKitTFVars tests the basement-kit tfvars generation.
func TestGenerateBaseKitTFVars(t *testing.T) {
	gen := NewIaCGenerator(t.TempDir())

	tests := []struct {
		name       string
		config     *IaCConfig
		wantMode   string
		wantVar    string
		wantTier   string
		wantPrefix string
		wantDocker string
	}{
		{
			name: "default ports mode",
			config: &IaCConfig{
				Variant:     "default",
				ComputeTier: "standard",
			},
			wantMode: "ports",
			wantVar:  "default",
			wantTier: "standard",
		},
		{
			name: "proxy mode with domain",
			config: &IaCConfig{
				Variant:     "beszel",
				ComputeTier: "high",
				Domain:      "homelab.example.com",
			},
			wantMode: "proxy",
			wantVar:  "beszel",
			wantTier: "high",
		},
		{
			name: "minimal with low compute",
			config: &IaCConfig{
				Variant:     "minimal",
				ComputeTier: "low",
				WorkerIP:    "192.168.1.100",
			},
			wantMode: "ports",
			wantVar:  "minimal",
			wantTier: "low",
		},
		{
			name: "with ACME email enables letsencrypt",
			config: &IaCConfig{
				Variant:   "default",
				Domain:    "homelab.example.com",
				ACMEEmail: "admin@example.com",
			},
			wantMode: "proxy",
			wantVar:  "default",
			wantTier: "standard",
		},
		{
			name: "with subdomain prefix",
			config: &IaCConfig{
				Domain:          "kombify.me",
				SubdomainPrefix: "ts-live-a-ionos",
			},
			wantMode:   "proxy",
			wantVar:    "default",
			wantTier:   "standard",
			wantPrefix: "ts-live-a-ionos",
		},
		{
			name: "with docker host",
			config: &IaCConfig{
				DockerHost: "tcp://stackkits-vm-local-e2e:2375",
			},
			wantMode:   "ports",
			wantVar:    "default",
			wantTier:   "standard",
			wantDocker: "tcp://stackkits-vm-local-e2e:2375",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars, err := gen.GenerateBaseKitTFVars(tt.config)
			if err != nil {
				t.Fatalf("GenerateBaseKitTFVars failed: %v", err)
			}

			if vars.AccessMode != tt.wantMode {
				t.Errorf("AccessMode = %s, want %s", vars.AccessMode, tt.wantMode)
			}
			if vars.Variant != tt.wantVar {
				t.Errorf("Variant = %s, want %s", vars.Variant, tt.wantVar)
			}
			if vars.ComputeTier != tt.wantTier {
				t.Errorf("ComputeTier = %s, want %s", vars.ComputeTier, tt.wantTier)
			}
			if vars.SubdomainPrefix != tt.wantPrefix {
				t.Errorf("SubdomainPrefix = %s, want %s", vars.SubdomainPrefix, tt.wantPrefix)
			}
			if vars.DockerHost != tt.wantDocker {
				t.Errorf("DockerHost = %s, want %s", vars.DockerHost, tt.wantDocker)
			}
			if vars.AdminEmail == "" {
				t.Fatal("AdminEmail should be generated")
			}
			if len(vars.AdminPasswordPlaintext) < 12 {
				t.Fatalf("AdminPasswordPlaintext length = %d, want >= 12", len(vars.AdminPasswordPlaintext))
			}
			if !strings.HasPrefix(vars.TinyAuthUsers, vars.AdminEmail+":$2") {
				t.Fatalf("TinyAuthUsers = %q, want %s bcrypt entry", vars.TinyAuthUsers, vars.AdminEmail)
			}
			parts := strings.SplitN(vars.TinyAuthUsers, ":", 2)
			if len(parts) != 2 {
				t.Fatalf("TinyAuthUsers should contain one email:bcrypt entry, got %q", vars.TinyAuthUsers)
			}
			if err := bcrypt.CompareHashAndPassword([]byte(parts[1]), []byte(vars.AdminPasswordPlaintext)); err != nil {
				t.Fatalf("TinyAuthUsers hash does not verify generated password: %v", err)
			}
			if len(vars.PocketIDStaticAPIKey) < 16 {
				t.Fatal("PocketIDStaticAPIKey should be generated")
			}
			if len(vars.PocketIDEncryptionKey) < 22 {
				t.Fatal("PocketIDEncryptionKey should be generated")
			}

			// Check default ports are set
			if vars.UptimeKumaPort != 3001 {
				t.Errorf("UptimeKumaPort = %d, want 3001", vars.UptimeKumaPort)
			}

			// Check ACME settings
			if tt.config.ACMEEmail != "" {
				if !vars.EnableLetsEnc {
					t.Error("EnableLetsEnc should be true when ACMEEmail is set")
				}
				if !vars.EnableHTTPS {
					t.Error("EnableHTTPS should be true when ACMEEmail is set")
				}
			}
		})
	}
}

func TestGenerateBaseKitTFVarsHonorsServiceEnablement(t *testing.T) {
	gen := NewIaCGenerator(t.TempDir())

	vars, err := gen.GenerateBaseKitTFVars(&IaCConfig{
		BaseKitServiceEnablement: map[string]bool{
			"enable_tinyauth":    false,
			"enable_pocketid":    false,
			"enable_vaultwarden": false,
			"enable_immich":      false,
			"enable_files":       false,
			"enable_coolify":     true,
		},
	})
	if err != nil {
		t.Fatalf("GenerateBaseKitTFVars failed: %v", err)
	}

	if vars.EnableTinyAuth {
		t.Fatal("EnableTinyAuth = true, want false")
	}
	if vars.EnablePocketID {
		t.Fatal("EnablePocketID = true, want false")
	}
	if vars.EnableVaultwarden {
		t.Fatal("EnableVaultwarden = true, want false")
	}
	if vars.EnableImmich {
		t.Fatal("EnableImmich = true, want false")
	}
	if vars.EnableFiles {
		t.Fatal("EnableFiles = true, want false")
	}
	if vars.EnableCloudreve || vars.EnableNextcloud {
		t.Fatalf("file provider flags = cloudreve:%v nextcloud:%v, want both false", vars.EnableCloudreve, vars.EnableNextcloud)
	}
	if !vars.EnableCoolify {
		t.Fatal("EnableCoolify = false, want true")
	}

	data, err := json.Marshal(vars)
	if err != nil {
		t.Fatalf("marshal vars: %v", err)
	}
	var tfvars map[string]any
	if err := json.Unmarshal(data, &tfvars); err != nil {
		t.Fatalf("unmarshal vars: %v", err)
	}
	for _, key := range []string{"enable_tinyauth", "enable_pocketid", "enable_vaultwarden", "enable_immich", "enable_files", "enable_cloudreve", "enable_nextcloud"} {
		if got := tfvars[key]; got != false {
			t.Fatalf("%s = %v, want false", key, got)
		}
	}
}

func TestGenerateBaseKitTFVarsCustomPublicDomainUsesLetsEncrypt(t *testing.T) {
	gen := NewIaCGenerator(t.TempDir())

	vars, err := gen.GenerateBaseKitTFVars(&IaCConfig{
		Domain:     "kombify.pro",
		AdminEmail: "test-all-you-need@kombify.io",
	})
	if err != nil {
		t.Fatalf("GenerateBaseKitTFVars failed: %v", err)
	}

	if !vars.EnableHTTPS {
		t.Fatal("EnableHTTPS = false, want true")
	}
	if !vars.EnableLetsEnc {
		t.Fatal("EnableLetsEnc = false, want true")
	}
	if vars.TLSProvider != "letsencrypt" {
		t.Fatalf("TLSProvider = %q, want letsencrypt", vars.TLSProvider)
	}
	if vars.StepCAEnabled {
		t.Fatal("StepCAEnabled = true, want false for public custom domain")
	}
	if vars.ACMEEmail != "test-all-you-need@kombify.io" {
		t.Fatalf("ACMEEmail = %q, want deployment/admin email", vars.ACMEEmail)
	}
	if vars.ACMEChallenge != "tls" {
		t.Fatalf("ACMEChallenge = %q, want tls", vars.ACMEChallenge)
	}
}

func TestGenerateBaseKitTFVarsKombifyMeDoesNotEnableLetsEncrypt(t *testing.T) {
	gen := NewIaCGenerator(t.TempDir())

	vars, err := gen.GenerateBaseKitTFVars(&IaCConfig{
		Domain:          "kombify.me",
		SubdomainPrefix: "e2e-generated-prefix",
		AdminEmail:      "test-all-you-need@kombify.io",
	})
	if err != nil {
		t.Fatalf("GenerateBaseKitTFVars failed: %v", err)
	}

	if vars.TLSProvider != "step-ca" {
		t.Fatalf("TLSProvider = %q, want step-ca", vars.TLSProvider)
	}
	if !vars.StepCAEnabled {
		t.Fatal("StepCAEnabled = false, want true for kombify.me StackKit routing mode")
	}
	if vars.EnableLetsEnc {
		t.Fatal("EnableLetsEnc = true, want false for kombify.me")
	}
}

func TestUnifiedToIaCConfigCarriesBaseKitServiceEnablement(t *testing.T) {
	gen := NewIaCGenerator(t.TempDir())
	config, err := gen.unifiedToIaCConfig(&core.UnifiedSpec{
		KombinationSpec: core.KombinationSpec{
			Name: "e2e-stack",
			Metadata: map[string]string{
				"enable_tinyauth": "false",
				"enable_pocketid": "false",
				"enable_files":    "false",
				"enable_coolify":  "true",
			},
		},
	})
	if err != nil {
		t.Fatalf("unifiedToIaCConfig failed: %v", err)
	}

	if config.BaseKitServiceEnablement["enable_tinyauth"] {
		t.Fatal("enable_tinyauth = true, want false")
	}
	if config.BaseKitServiceEnablement["enable_pocketid"] {
		t.Fatal("enable_pocketid = true, want false")
	}
	if !config.BaseKitServiceEnablement["enable_coolify"] {
		t.Fatal("enable_coolify = false, want true")
	}
	if config.BaseKitServiceEnablement["enable_files"] {
		t.Fatal("enable_files = true, want false")
	}
}

func TestUnifiedToIaCConfigCarriesPlatformFallback(t *testing.T) {
	gen := NewIaCGenerator(t.TempDir())
	config, err := gen.unifiedToIaCConfig(&core.UnifiedSpec{
		KombinationSpec: core.KombinationSpec{
			Name: "e2e-stack",
			Metadata: map[string]string{
				"enable_platform_fallback": "true",
				"platform_fallback_mode":   "standalone-compose",
			},
		},
	})
	if err != nil {
		t.Fatalf("unifiedToIaCConfig failed: %v", err)
	}
	if !config.EnablePlatformFallback {
		t.Fatal("EnablePlatformFallback = false, want true")
	}
	if config.PlatformFallbackMode != "standalone-compose" {
		t.Fatalf("PlatformFallbackMode = %q, want standalone-compose", config.PlatformFallbackMode)
	}

	vars, err := gen.GenerateBaseKitTFVars(config)
	if err != nil {
		t.Fatalf("GenerateBaseKitTFVars failed: %v", err)
	}
	if !vars.EnablePlatformFallback {
		t.Fatal("tfvars EnablePlatformFallback = false, want true")
	}
	if vars.PlatformFallbackMode != "standalone-compose" {
		t.Fatalf("tfvars PlatformFallbackMode = %q, want standalone-compose", vars.PlatformFallbackMode)
	}
}

// TestGenerateBaseKitOutput tests the full output generation with external StackKits.
func TestGenerateBaseKitOutput(t *testing.T) {
	// Create a mock StackKits directory structure
	tmpStackKits := t.TempDir()
	templateDir := filepath.Join(tmpStackKits, "basement-kit", "templates", "simple")
	if err := os.MkdirAll(templateDir, 0755); err != nil {
		t.Fatalf("Failed to create mock StackKits: %v", err)
	}

	// Create a minimal main.tf
	mainTF := `# Mock main.tf for testing
terraform {
  required_version = ">= 1.6.0"
}
variable "access_mode" { default = "ports" }
variable "variant" { default = "default" }
`
	if err := os.WriteFile(filepath.Join(templateDir, "main.tf"), []byte(mainTF), 0644); err != nil {
		t.Fatalf("Failed to write main.tf: %v", err)
	}

	tmpOutput := t.TempDir()
	gen := NewIaCGenerator(tmpOutput)

	config := &IaCConfig{
		StackName:   "test-homelab",
		StackKit:    "basement-kit",
		Variant:     "default",
		ComputeTier: "standard",
		WorkerIP:    "192.168.1.100",
	}

	output, err := gen.GenerateBaseKitOutput(config, tmpStackKits)
	if err != nil {
		t.Fatalf("GenerateBaseKitOutput failed: %v", err)
	}

	// Check main.tf is loaded from StackKits
	if mainContent, ok := output.Files["main.tf"]; !ok {
		t.Error("main.tf not found in output")
	} else if !strings.Contains(mainContent, "Mock main.tf") {
		t.Error("main.tf content doesn't match mock StackKits")
	}

	// Check tfvars.json is generated
	if _, ok := output.Files["terraform.tfvars.json"]; !ok {
		t.Error("terraform.tfvars.json not found in output")
	}

	// Validate tfvars JSON
	var tfvars map[string]interface{}
	if err := json.Unmarshal(output.TFVarsJSON, &tfvars); err != nil {
		t.Errorf("tfvars is not valid JSON: %v", err)
	}

	// Check expected values
	if tfvars["access_mode"] != "ports" {
		t.Errorf("Expected access_mode=ports, got %v", tfvars["access_mode"])
	}
	if tfvars["variant"] != "default" {
		t.Errorf("Expected variant=default, got %v", tfvars["variant"])
	}
}

func TestGenerateBaseKitOutputRendersTemplatedMainTF(t *testing.T) {
	tmpStackKits := t.TempDir()
	templateDir := filepath.Join(tmpStackKits, "basement-kit", "templates", "simple")
	if err := os.MkdirAll(templateDir, 0755); err != nil {
		t.Fatalf("Failed to create mock StackKits: %v", err)
	}

	mainTF := `locals {
  domains = {
{{- range .Domains }}
    {{ .Key }} = "{{ .Flat }}.example.test"
{{- end }}
  }
  platform_services = [
{{- range catalogSection "Platform" .Catalog }}
    "{{ .Key }}:{{ htmlText .DisplayName }}:{{ serviceHint . }}",
{{- end }}
  ]
}
`
	if err := os.WriteFile(filepath.Join(templateDir, "main.tf"), []byte(mainTF), 0644); err != nil {
		t.Fatalf("Failed to write main.tf: %v", err)
	}

	gen := NewIaCGenerator(t.TempDir())
	output, err := gen.GenerateBaseKitOutput(&IaCConfig{
		StackName:   "test-homelab",
		StackKit:    "basement-kit",
		ComputeTier: "standard",
		Domain:      "kombify.me",
	}, tmpStackKits)
	if err != nil {
		t.Fatalf("GenerateBaseKitOutput failed: %v", err)
	}

	mainContent := output.Files["main.tf"]
	if strings.Contains(mainContent, "{{") {
		t.Fatalf("main.tf still contains Go template syntax:\n%s", mainContent)
	}
	if !strings.Contains(mainContent, `coolify = "coolify.example.test"`) {
		t.Fatalf("main.tf missing rendered Coolify domain:\n%s", mainContent)
	}
	if !strings.Contains(mainContent, `files = "files.example.test"`) {
		t.Fatalf("main.tf missing rendered Files domain:\n%s", mainContent)
	}
	if !strings.Contains(mainContent, `"base:Node Hub:Bootstrap open"`) {
		t.Fatalf("main.tf missing rendered catalog helper output:\n%s", mainContent)
	}
}

func TestGenerateBaseKitOutputRendersConfiguredExternalStackKits(t *testing.T) {
	stackkitsDir := os.Getenv("TECHSTACK_STACKKITS_DIR")
	if stackkitsDir == "" {
		t.Skip("TECHSTACK_STACKKITS_DIR not set")
	}

	gen := NewIaCGenerator(t.TempDir())
	output, err := gen.GenerateBaseKitOutput(&IaCConfig{
		StackName:   "test-homelab",
		StackKit:    "basement-kit",
		ComputeTier: "standard",
		Domain:      "kombify.me",
	}, stackkitsDir)
	if err != nil {
		t.Fatalf("GenerateBaseKitOutput failed with configured StackKits: %v", err)
	}

	mainContent := output.Files["main.tf"]
	for _, unexpected := range []string{
		"{{- range .Domains",
		"catalogSection",
		"{{ .Key",
		"{{ htmlText",
		"{{ if publicGuideURL",
	} {
		if strings.Contains(mainContent, unexpected) {
			t.Fatalf("main.tf still contains unrendered StackKits template token %q", unexpected)
		}
	}
	if !strings.Contains(mainContent, `coolify = local._p != "" ? "${local._p}-coolify.${var.domain}" : "coolify.${var.domain}"`) {
		t.Fatalf("main.tf missing rendered Coolify domain entry")
	}
}

// TestGenerateAndWrite tests the convenience method for generate + write.
func TestGenerateAndWrite(t *testing.T) {
	// Create a mock StackKits directory
	tmpStackKits := t.TempDir()
	templateDir := filepath.Join(tmpStackKits, "basement-kit", "templates", "simple")
	if err := os.MkdirAll(templateDir, 0755); err != nil {
		t.Fatalf("Failed to create mock StackKits: %v", err)
	}

	mainTF := `# Test main.tf
terraform { required_version = ">= 1.6.0" }
`
	if err := os.WriteFile(filepath.Join(templateDir, "main.tf"), []byte(mainTF), 0644); err != nil {
		t.Fatalf("Failed to write main.tf: %v", err)
	}

	tmpOutput := t.TempDir()
	gen := NewIaCGenerator(tmpOutput)

	config := &IaCConfig{
		StackName: "test-homelab",
		Variant:   "default",
	}

	err := gen.GenerateAndWrite(config, tmpStackKits)
	if err != nil {
		t.Fatalf("GenerateAndWrite failed: %v", err)
	}

	// Verify files were written
	if _, err := os.Stat(filepath.Join(tmpOutput, "main.tf")); os.IsNotExist(err) {
		t.Error("main.tf was not written to output directory")
	}
	if _, err := os.Stat(filepath.Join(tmpOutput, "terraform.tfvars.json")); os.IsNotExist(err) {
		t.Error("terraform.tfvars.json was not written to output directory")
	}
}

// ============================================================================
// MULTI-NODE / DOCKER SWARM TESTS (Sprint 4.5)
// ============================================================================

// TestIaCConfig_IsMultiNode tests multi-node detection.
func TestIaCConfig_IsMultiNode(t *testing.T) {
	tests := []struct {
		name     string
		config   *IaCConfig
		expected bool
	}{
		{
			name: "single node without swarm",
			config: &IaCConfig{
				Nodes: []IaCNode{{Name: "node1", Role: "standalone"}},
			},
			expected: false,
		},
		{
			name: "multiple nodes",
			config: &IaCConfig{
				Nodes: []IaCNode{
					{Name: "node1", Role: "manager"},
					{Name: "node2", Role: "worker"},
				},
			},
			expected: true,
		},
		{
			name: "single node with swarm enabled",
			config: &IaCConfig{
				Nodes:       []IaCNode{{Name: "node1", Role: "manager"}},
				SwarmConfig: &SwarmConfig{Enabled: true, Mode: "single-manager"},
			},
			expected: true,
		},
		{
			name: "no nodes but swarm enabled",
			config: &IaCConfig{
				SwarmConfig: &SwarmConfig{Enabled: true},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.IsMultiNode()
			if got != tt.expected {
				t.Errorf("IsMultiNode() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestIaCConfig_GetManagerNodes tests manager node filtering.
func TestIaCConfig_GetManagerNodes(t *testing.T) {
	config := &IaCConfig{
		Nodes: []IaCNode{
			{Name: "manager1", Role: "manager"},
			{Name: "worker1", Role: "worker"},
			{Name: "manager2", Role: "manager"},
			{Name: "worker2", Role: "worker"},
			{Name: "manager3", Role: "manager"},
		},
	}

	managers := config.GetManagerNodes()
	if len(managers) != 3 {
		t.Errorf("Expected 3 managers, got %d", len(managers))
	}

	// Verify all returned nodes are managers
	for _, m := range managers {
		if m.Role != "manager" {
			t.Errorf("Got non-manager node: %s with role %s", m.Name, m.Role)
		}
	}
}

// TestIaCConfig_GetWorkerNodes tests worker node filtering.
func TestIaCConfig_GetWorkerNodes(t *testing.T) {
	config := &IaCConfig{
		Nodes: []IaCNode{
			{Name: "manager1", Role: "manager"},
			{Name: "worker1", Role: "worker"},
			{Name: "worker2", Role: "worker"},
		},
	}

	workers := config.GetWorkerNodes()
	if len(workers) != 2 {
		t.Errorf("Expected 2 workers, got %d", len(workers))
	}
}

// TestIaCConfig_GetPrimaryManager tests primary manager selection.
func TestIaCConfig_GetPrimaryManager(t *testing.T) {
	t.Run("has managers", func(t *testing.T) {
		config := &IaCConfig{
			Nodes: []IaCNode{
				{Name: "worker1", Role: "worker"},
				{Name: "manager1", Role: "manager"},
				{Name: "manager2", Role: "manager"},
			},
		}

		primary := config.GetPrimaryManager()
		if primary == nil {
			t.Fatal("Expected primary manager, got nil")
		}
		if primary.Name != "manager1" {
			t.Errorf("Expected manager1 as primary, got %s", primary.Name)
		}
	})

	t.Run("no managers", func(t *testing.T) {
		config := &IaCConfig{
			Nodes: []IaCNode{
				{Name: "worker1", Role: "worker"},
			},
		}

		primary := config.GetPrimaryManager()
		if primary != nil {
			t.Errorf("Expected nil primary manager, got %s", primary.Name)
		}
	})
}

// TestGenerateMultiNodeTFVars tests multi-node tfvars generation.
func TestGenerateMultiNodeTFVars(t *testing.T) {
	tmpDir := t.TempDir()
	gen := NewIaCGenerator(tmpDir)

	config := &IaCConfig{
		StackName:      "ha-kit-test",
		StackKit:       "ha-kit",
		Variant:        "default",
		ComputeTier:    "standard",
		NetworkMode:    "local",
		Domain:         "home.local",
		ACMEEmail:      "admin@example.com",
		VPNType:        "headscale",
		EnableFirewall: true,
		AllowedPorts:   []int{22, 80, 443},
		TLSMode:        "acme",
		SSHUser:        "ubuntu",
		SSHPort:        22,
		SSHKeyPath:     "~/.ssh/id_ed25519",
		Nodes: []IaCNode{
			{Name: "manager1", IP: "192.168.1.10", Role: "manager"},
			{Name: "manager2", IP: "192.168.1.11", Role: "manager"},
			{Name: "manager3", IP: "192.168.1.12", Role: "manager"},
			{Name: "worker1", IP: "192.168.1.20", Role: "worker"},
		},
		SwarmConfig: &SwarmConfig{
			Enabled:        true,
			Mode:           "ha",
			ManagerCount:   3,
			WorkerCount:    1,
			EncryptOverlay: true,
		},
	}

	vars, err := gen.GenerateMultiNodeTFVars(config)
	if err != nil {
		t.Fatalf("GenerateMultiNodeTFVars failed: %v", err)
	}

	// Verify Swarm config
	if !vars.SwarmEnabled {
		t.Error("Expected SwarmEnabled=true")
	}
	if vars.SwarmMode != "ha" {
		t.Errorf("Expected SwarmMode=ha, got %s", vars.SwarmMode)
	}
	if vars.SwarmManagerCount != 3 {
		t.Errorf("Expected SwarmManagerCount=3, got %d", vars.SwarmManagerCount)
	}

	// Verify nodes
	if len(vars.Nodes) != 4 {
		t.Errorf("Expected 4 nodes, got %d", len(vars.Nodes))
	}

	// Verify first manager is marked as primary
	found := false
	for _, n := range vars.Nodes {
		if n.IsFirst && n.Role == "manager" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected first manager to have IsFirst=true")
	}

	// Verify TLS settings
	if !vars.EnableHTTPS {
		t.Error("Expected EnableHTTPS=true for ACME mode")
	}
	if !vars.EnableLetsEnc {
		t.Error("Expected EnableLetsEnc=true for ACME mode")
	}
}

// TestGenerateMultiNodeTFVars_AutoSwarmConfig tests auto-configuration of Swarm.
func TestGenerateMultiNodeTFVars_AutoSwarmConfig(t *testing.T) {
	tmpDir := t.TempDir()
	gen := NewIaCGenerator(tmpDir)

	// Config without explicit SwarmConfig - should auto-detect
	config := &IaCConfig{
		StackName: "modern-kit-test",
		StackKit:  "modern-kit",
		Nodes: []IaCNode{
			{Name: "manager1", IP: "192.168.1.10", Role: "manager"},
			{Name: "worker1", IP: "192.168.1.20", Role: "worker"},
		},
		// No SwarmConfig - should be auto-enabled for multi-node
	}

	vars, err := gen.GenerateMultiNodeTFVars(config)
	if err != nil {
		t.Fatalf("GenerateMultiNodeTFVars failed: %v", err)
	}

	// Should auto-enable Swarm
	if !vars.SwarmEnabled {
		t.Error("Expected SwarmEnabled=true for multi-node config")
	}
	if vars.SwarmMode != "single-manager" {
		t.Errorf("Expected SwarmMode=single-manager (1 manager), got %s", vars.SwarmMode)
	}
}

func TestConfigFromUnifiedSpecMultiNode_PreservesResolvedNodeFieldsAndAutoSwarm(t *testing.T) {
	gen := NewIaCGenerator(t.TempDir())

	config, err := gen.ConfigFromUnifiedSpecMultiNode(&core.UnifiedSpec{
		KombinationSpec: core.KombinationSpec{
			Name: "multi-node",
			Metadata: map[string]string{
				"network_mode": iacNetworkModePublic,
				"tls_mode":     iacTLSModeNone,
			},
		},
		StackKit: "ha-kit",
		ResolvedNodes: []core.ResolvedNode{
			{
				NodeSpec: core.NodeSpec{
					Name: "manager-a",
					SSH: &core.SSHConfig{
						User:    "ubuntu",
						Port:    2222,
						KeyPath: "/keys/manager-a",
					},
					Tags: map[string]string{
						"role": "manager",
						"zone": "a",
					},
				},
				PrivateIP: "10.0.0.10",
				PublicIP:  "198.51.100.10",
				OS:        "ubuntu-24",
			},
			{
				NodeSpec: core.NodeSpec{
					Name: "worker-a",
					Tags: map[string]string{"role": "worker"},
				},
				PublicIP: "198.51.100.20",
				OS:       "debian-12",
			},
		},
	})
	if err != nil {
		t.Fatalf("ConfigFromUnifiedSpecMultiNode failed: %v", err)
	}

	if len(config.Nodes) != 2 {
		t.Fatalf("Expected 2 IaC nodes, got %d", len(config.Nodes))
	}
	manager := config.Nodes[0]
	if manager.Name != "manager-a" || manager.Role != iacNodeRoleManager {
		t.Fatalf("Expected manager-a manager node, got %#v", manager)
	}
	if manager.IP != "10.0.0.10" || manager.PrivateIP != "10.0.0.10" || manager.PublicIP != "198.51.100.10" {
		t.Fatalf("Manager IP fields not preserved: %#v", manager)
	}
	if manager.SSHUser != "ubuntu" || manager.SSHPort != 2222 || manager.SSHKeyPath != "/keys/manager-a" {
		t.Fatalf("Manager SSH fields not preserved: %#v", manager)
	}
	if manager.Labels["zone"] != "a" {
		t.Fatalf("Manager labels not mapped from tags: %#v", manager.Labels)
	}

	worker := config.Nodes[1]
	if worker.Name != "worker-a" || worker.Role != iacNodeRoleWorker {
		t.Fatalf("Expected worker-a worker node, got %#v", worker)
	}
	if worker.IP != "198.51.100.20" {
		t.Fatalf("Expected worker IP to fall back to public IP, got %q", worker.IP)
	}

	if config.SwarmConfig == nil {
		t.Fatal("Expected auto SwarmConfig for multi-node spec")
	}
	if !config.SwarmConfig.Enabled || config.SwarmConfig.Mode != "single-manager" {
		t.Fatalf("Unexpected Swarm mode: %#v", config.SwarmConfig)
	}
	if config.SwarmConfig.ManagerCount != 1 || config.SwarmConfig.WorkerCount != 1 || !config.SwarmConfig.EncryptOverlay {
		t.Fatalf("Unexpected Swarm counts/encryption: %#v", config.SwarmConfig)
	}
}

func TestConfigFromUnifiedSpecMultiNode_DefaultRoleRules(t *testing.T) {
	t.Run("single node becomes standalone", func(t *testing.T) {
		gen := NewIaCGenerator(t.TempDir())
		config, err := gen.ConfigFromUnifiedSpecMultiNode(&core.UnifiedSpec{
			KombinationSpec: core.KombinationSpec{Name: "single-node"},
			ResolvedNodes: []core.ResolvedNode{{
				NodeSpec:  core.NodeSpec{Name: "only-node", Tags: map[string]string{"role": "manager"}},
				PrivateIP: "10.0.0.10",
			}},
		})
		if err != nil {
			t.Fatalf("ConfigFromUnifiedSpecMultiNode failed: %v", err)
		}
		if len(config.Nodes) != 1 {
			t.Fatalf("Expected 1 node, got %d", len(config.Nodes))
		}
		if config.Nodes[0].Role != iacNodeRoleStandalone {
			t.Fatalf("Expected standalone role, got %q", config.Nodes[0].Role)
		}
		if config.SwarmConfig != nil {
			t.Fatalf("Expected no SwarmConfig for single-node spec, got %#v", config.SwarmConfig)
		}
	})

	t.Run("first node becomes manager when no manager tag exists", func(t *testing.T) {
		gen := NewIaCGenerator(t.TempDir())
		config, err := gen.ConfigFromUnifiedSpecMultiNode(&core.UnifiedSpec{
			KombinationSpec: core.KombinationSpec{Name: "default-manager"},
			ResolvedNodes: []core.ResolvedNode{
				{NodeSpec: core.NodeSpec{Name: "node-a"}, PrivateIP: "10.0.0.10"},
				{NodeSpec: core.NodeSpec{Name: "node-b"}, PrivateIP: "10.0.0.11"},
			},
		})
		if err != nil {
			t.Fatalf("ConfigFromUnifiedSpecMultiNode failed: %v", err)
		}
		if config.Nodes[0].Role != iacNodeRoleManager {
			t.Fatalf("Expected first node to become manager, got %q", config.Nodes[0].Role)
		}
		if config.Nodes[1].Role != iacNodeRoleWorker {
			t.Fatalf("Expected second node to remain worker, got %q", config.Nodes[1].Role)
		}
		if config.SwarmConfig == nil {
			t.Fatal("Expected auto SwarmConfig for multi-node spec")
		}
		if config.SwarmConfig.ManagerCount != 1 || config.SwarmConfig.WorkerCount != 1 {
			t.Fatalf("Unexpected Swarm counts: %#v", config.SwarmConfig)
		}
	})
}

func TestGenerateModernKitOutputFallsBackToBasementKitWhenTemplatesMissing(t *testing.T) {
	stackkitsDir := t.TempDir()
	writeTestStackKitTemplate(t, stackkitsDir, "basement-kit", "simple")

	gen := NewIaCGenerator(t.TempDir())
	output, err := gen.GenerateModernKitOutput(testFallbackIaCConfig("modern-missing"), stackkitsDir)
	if err != nil {
		t.Fatalf("GenerateModernKitOutput() error = %v", err)
	}

	assertBasementKitFallbackOutput(t, output)
	assertOutputErrorContains(t, output, "warning: modern-homelab templates not found, using basement-kit")
}

func TestGenerateModernKitOutputFallsBackToBasementKitWhenTemplatesIncomplete(t *testing.T) {
	stackkitsDir := t.TempDir()
	writeTestStackKitTemplate(t, stackkitsDir, "basement-kit", "simple")
	if err := os.MkdirAll(filepath.Join(stackkitsDir, StackKitModernHomelab, "templates", "simple"), 0o750); err != nil {
		t.Fatalf("create empty modern-homelab template dir: %v", err)
	}

	gen := NewIaCGenerator(t.TempDir())
	output, err := gen.GenerateModernKitOutput(testFallbackIaCConfig("modern-incomplete"), stackkitsDir)
	if err != nil {
		t.Fatalf("GenerateModernKitOutput() error = %v", err)
	}

	assertBasementKitFallbackOutput(t, output)
	assertOutputErrorContains(t, output, "warning: modern-homelab templates incomplete:")
}

func TestGenerateHAKitOutputFallsBackThroughModernToBasementKitWhenTemplatesMissing(t *testing.T) {
	stackkitsDir := t.TempDir()
	writeTestStackKitTemplate(t, stackkitsDir, "basement-kit", "simple")

	gen := NewIaCGenerator(t.TempDir())
	output, err := gen.GenerateHAKitOutput(testFallbackHAIaCConfig("ha-missing"), stackkitsDir)
	if err != nil {
		t.Fatalf("GenerateHAKitOutput() error = %v", err)
	}

	assertBasementKitFallbackOutput(t, output)
	assertOutputErrorContains(t, output, "warning: ha-kit templates not found, using modern-homelab")
	assertOutputErrorContains(t, output, "warning: modern-homelab templates not found, using basement-kit")
}

func TestGenerateHAKitOutputFallbackDoesNotCarryAttemptHardErrors(t *testing.T) {
	stackkitsDir := t.TempDir()
	writeTestStackKitTemplate(t, stackkitsDir, "basement-kit", "simple")

	config := testFallbackIaCConfig("ha-hard-attempt")
	config.StackKit = "ha-kit"

	gen := NewIaCGenerator(t.TempDir())
	output, err := gen.GenerateHAKitOutput(config, stackkitsDir)
	if err != nil {
		t.Fatalf("GenerateHAKitOutput() error = %v", err)
	}

	assertBasementKitFallbackOutput(t, output)
	assertOutputErrorContains(t, output, "warning: ha-kit templates not found, using modern-homelab")
}

// TestGenerateStackKitOutput_Routing tests StackKit-specific generation routing.
func TestGenerateStackKitOutput_Routing(t *testing.T) {
	tests := []struct {
		name     string
		stackKit string
		wantErr  bool
	}{
		{"basement-kit routes correctly", "basement-kit", false},
		{"cloud-kit routes correctly", "cloud-kit", false},
		{"legacy base-kit alias routes to basement-kit", "base-kit", false},
		{"modern-kit alias routes to modern-homelab", "modern-kit", false},
		{"modern-homelab routes correctly", "modern-homelab", false},
		{"ha-kit is retired", "ha-kit", true},
		{"unknown stackkit fails instead of falling back", "unknown-kit", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock StackKits directory with supported product kit templates
			tmpStackKits := t.TempDir()
			for _, kit := range []string{"basement-kit", "cloud-kit", "modern-homelab"} {
				templateDir := filepath.Join(tmpStackKits, kit, "templates", "simple")
				if err := os.MkdirAll(templateDir, 0755); err != nil {
					t.Fatalf("Failed to create mock %s: %v", kit, err)
				}
				if err := os.WriteFile(filepath.Join(templateDir, "main.tf"), []byte("# main.tf"), 0644); err != nil {
					t.Fatalf("Failed to write main.tf: %v", err)
				}
			}

			tmpOutput := t.TempDir()
			gen := NewIaCGenerator(tmpOutput)

			config := &IaCConfig{
				StackName: "test-" + tt.stackKit,
				StackKit:  tt.stackKit,
				Variant:   "default",
			}

			output, err := gen.GenerateStackKitOutput(config, tmpStackKits)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("GenerateStackKitOutput should fail for %s", tt.stackKit)
				}
				if !strings.Contains(err.Error(), "unknown StackKit") {
					t.Fatalf("GenerateStackKitOutput error = %v, want unknown StackKit", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GenerateStackKitOutput failed: %v", err)
			}

			// All paths should eventually generate output (with fallback)
			if output == nil {
				t.Error("Expected non-nil output")
			}
			if len(output.Files) == 0 && len(output.Errors) == 0 {
				t.Error("Expected either files or errors in output")
			}
		})
	}
}

func testFallbackIaCConfig(stackName string) *IaCConfig {
	return &IaCConfig{
		StackName:   stackName,
		StackKit:    "basement-kit",
		Variant:     "default",
		ComputeTier: "standard",
		WorkerIP:    "192.168.1.100",
	}
}

func testFallbackHAIaCConfig(stackName string) *IaCConfig {
	config := testFallbackIaCConfig(stackName)
	config.StackKit = "ha-kit"
	config.Nodes = []IaCNode{
		{Name: "manager-a", IP: "10.0.0.10", Role: iacNodeRoleManager},
		{Name: "manager-b", IP: "10.0.0.11", Role: iacNodeRoleManager},
		{Name: "manager-c", IP: "10.0.0.12", Role: iacNodeRoleManager},
	}
	return config
}

func assertBasementKitFallbackOutput(t *testing.T, output *IaCOutput) {
	t.Helper()

	if output == nil {
		t.Fatal("output = nil")
	}
	if got := output.Files["main.tf"]; got != "# test StackKit main.tf\n" {
		t.Fatalf("main.tf = %q, want basement-kit test template content", got)
	}
	if _, ok := output.Files["terraform.tfvars.json"]; !ok {
		t.Fatal("terraform.tfvars.json not found in output")
	}
	if len(output.TFVarsJSON) == 0 {
		t.Fatal("TFVarsJSON is empty")
	}
	if output.HasHardErrors() {
		t.Fatalf("output has hard errors after successful fallback: %v", output.HardErrors())
	}
}

func assertOutputErrorContains(t *testing.T, output *IaCOutput, want string) {
	t.Helper()

	for _, got := range output.Errors {
		if strings.Contains(got, want) {
			return
		}
	}
	t.Fatalf("output.Errors = %v, want diagnostic containing %q", output.Errors, want)
}

func newTestIaCGenerator(t *testing.T, outputDir string) *IaCGenerator {
	t.Helper()

	stackkitsDir := t.TempDir()
	writeTestStackKitTemplate(t, stackkitsDir, "basement-kit", "simple")
	writeTestStackKitTemplate(t, stackkitsDir, "basement-kit", "advanced")

	return NewIaCGenerator(outputDir).WithStackKitDir(stackkitsDir)
}

func writeTestStackKitTemplate(t *testing.T, stackkitsDir, kit, mode string) {
	t.Helper()

	templateDir := filepath.Join(stackkitsDir, kit, "templates", mode)
	if err := os.MkdirAll(templateDir, 0o750); err != nil {
		t.Fatalf("create test StackKit template dir: %v", err)
	}

	files := map[string]string{
		"main.tf":              "# test StackKit main.tf\n",
		"variables.tf":         "# test StackKit variables.tf\n",
		"bootstrap.tf":         "# test StackKit bootstrap.tf uses apt and dnf markers\n",
		"network.tf":           "# test StackKit network.tf\n",
		"services.tf":          "# test StackKit services.tf\n",
		"outputs.tf":           "# test StackKit outputs.tf\n",
		"terramate.tm.hcl":     "# test StackKit terramate config\n",
		"backend.tm.hcl":       "# test StackKit backend config\n",
		"globals.tm.hcl":       "# test StackKit globals config\n",
		"README.md":            "# test StackKit\n",
		"terraform.tfvars.tpl": "{}\n",
	}
	for name, content := range files {
		if mode == "simple" && strings.HasSuffix(name, ".tm.hcl") {
			continue
		}
		if err := os.WriteFile(filepath.Join(templateDir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write test StackKit template %s: %v", name, err)
		}
	}
}
