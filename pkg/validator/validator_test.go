package validator

import (
	"testing"
)

func TestValidateDNSName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid single char", "a", false},
		{"valid short name", "ab", false},
		{"valid with numbers", "node1", false},
		{"valid with hyphens", "my-node", false},
		{"valid complex", "my-homelab-node-01", false},
		{"empty", "", true},
		{"starts with number", "1node", true},
		{"starts with hyphen", "-node", true},
		{"ends with hyphen", "node-", true},
		{"uppercase", "MyNode", true},
		{"underscore", "my_node", true},
		{"too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true}, // 65 chars
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDNSName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateDNSName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateStackKitName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "basement-kit", false},
		{"valid single char", "a", false},
		{"valid with numbers", "kit1", false},
		{"single digit", "1", true},
		{"empty", "", true},
		{"path traversal", "../secret", true},
		{"absolute path", "/etc/passwd", true},
		{"windows path", "C:\\Windows", true},
		{"uppercase", "HomeLab", true},
		{"spaces", "home lab", true},
		{"dot dot", "kit..name", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateStackKitName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateStackKitName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestIsCloudProvider(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{"hetzner", true},
		{"Hetzner", true}, // Case insensitive
		{"HETZNER", true},
		{"aws", true},
		{"gcp", true},
		{"azure", true},
		{"local", false},
		{"homelab", false},
		{"docker", false},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			if got := IsCloudProvider(tt.provider); got != tt.want {
				t.Errorf("IsCloudProvider(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestIsLocalProvider(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{"local", true},
		{"Local", true}, // Case insensitive
		{"homelab", true},
		{"docker", true},
		{"proxmox", true},
		{"hetzner", false},
		{"aws", false},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			if got := IsLocalProvider(tt.provider); got != tt.want {
				t.Errorf("IsLocalProvider(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestIsValidProvider(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{"hetzner", true},
		{"local", true},
		{"homelab", true},
		{"docker", true},
		{"proxmox", true},
		{"aws", true},
		{"digitalocean", true},
		{"centron", true},
		{"ionos", true},
		{"centron-managed", false},
		{"ionos-managed", false},
		{"unknown", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			if got := IsValidProvider(tt.provider); got != tt.want {
				t.Errorf("IsValidProvider(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestIsValidNodeType(t *testing.T) {
	tests := []struct {
		nodeType string
		want     bool
	}{
		{"main", true},
		{"worker", true},
		{"edge", true},
		{"MAIN", true},
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.nodeType, func(t *testing.T) {
			if got := IsValidNodeType(tt.nodeType); got != tt.want {
				t.Errorf("IsValidNodeType(%q) = %v, want %v", tt.nodeType, got, tt.want)
			}
		})
	}
}

func TestIsValidServiceType_MatchesStackKitServiceTypes(t *testing.T) {
	tests := []struct {
		serviceType string
		want        bool
	}{
		{"reverse-proxy", true},
		{"service", true},
		{"backend", true},
		{"database", true},
		{"cache", true},
		{"queue", true},
		{"paas", true},
		{"docker", true},
		{"monitoring", true},
		{"backup", true},
		{"auth", true},
		{"vpn", true},
		{"storage", true},
		{"media", true},
		{"automation", true},
		{"development", true},
		{"custom", true},
		{"unknown", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.serviceType, func(t *testing.T) {
			if got := IsValidServiceType(tt.serviceType); got != tt.want {
				t.Errorf("IsValidServiceType(%q) = %v, want %v", tt.serviceType, got, tt.want)
			}
		})
	}
}

func TestValidateSSHKeyPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"empty", "", false}, // Empty is valid
		{"valid path", "/home/user/.ssh/id_rsa", false},
		{"windows path", "C:\\Users\\user\\.ssh\\id_rsa", false},
		{"path traversal", "../../../etc/passwd", true},
		{"etc passwd", "/etc/passwd", true},
		{"etc shadow", "/etc/shadow", true},
		{"windows system", "C:\\Windows\\System32\\config", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSSHKeyPath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSSHKeyPath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestValidatePath(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		allowedBase string
		wantErr     bool
	}{
		{"empty path", "", "", true},
		{"simple path no base", "data/file.txt", "", false},
		{"path traversal no base", "../secret", "", true},
		{"double dot in name", "file..name.txt", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePath(tt.path, tt.allowedBase)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePath(%q, %q) error = %v, wantErr %v", tt.path, tt.allowedBase, err, tt.wantErr)
			}
		})
	}
}

func TestGetDefaultImage(t *testing.T) {
	tests := []struct {
		svcType string
		want    string
	}{
		{"reverse-proxy", "traefik:v3.0"},
		{"database", "postgres:16-alpine"},
		{"cache", "redis:7-alpine"},
		{"queue", "rabbitmq:3-management-alpine"},
		{"monitor", "grafana/grafana:latest"},
		{"monitoring", "grafana/grafana:latest"},
		{"storage", "minio/minio:latest"},
		{"ingress", "nginx:stable-alpine"},
		{"web", "nginx:stable-alpine"},
		{"api", "traefik:v3.0"},
		{"unknown", ""},
		{"custom", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.svcType, func(t *testing.T) {
			if got := GetDefaultImage(tt.svcType); got != tt.want {
				t.Errorf("GetDefaultImage(%q) = %q, want %q", tt.svcType, got, tt.want)
			}
		})
	}
}

func TestGetDefaultPort(t *testing.T) {
	tests := []struct {
		svcType string
		want    int
	}{
		{"reverse-proxy", 80},
		{"database", 5432},
		{"cache", 6379},
		{"queue", 5672},
		{"monitor", 3000},
		{"monitoring", 3000},
		{"storage", 9000},
		{"ingress", 80},
		{"web", 80},
		{"api", 8080},
		{"unknown", 0},
		{"custom", 0},
		{"", 0},
	}

	for _, tt := range tests {
		t.Run(tt.svcType, func(t *testing.T) {
			if got := GetDefaultPort(tt.svcType); got != tt.want {
				t.Errorf("GetDefaultPort(%q) = %d, want %d", tt.svcType, got, tt.want)
			}
		})
	}
}
