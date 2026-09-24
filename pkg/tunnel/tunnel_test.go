package tunnel

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/logger"
)

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{
			name:    "valid https URL",
			url:     "https://abc-def-ghi.trycloudflare.com",
			wantErr: false,
		},
		{
			name:    "valid http URL with IP",
			url:     "http://192.168.1.100:5260",
			wantErr: false,
		},
		{
			name:    "localhost rejected",
			url:     "http://localhost:5260",
			wantErr: true,
		},
		{
			name:    "localhost uppercase rejected",
			url:     "http://LOCALHOST:5260",
			wantErr: true,
		},
		{
			name:    "127.0.0.1 rejected",
			url:     "http://127.0.0.1:5260",
			wantErr: true,
		},
		{
			name:    "127.0.0.100 rejected (loopback range)",
			url:     "http://127.0.0.100:5260",
			wantErr: true,
		},
		{
			name:    "empty URL rejected",
			url:     "",
			wantErr: true,
		},
		{
			name:    "Tailscale IP valid",
			url:     "https://100.100.100.100:5260",
			wantErr: false,
		},
		{
			name:    "unspecified IP rejected",
			url:     "http://0.0.0.0:5260",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestURLPattern(t *testing.T) {
	tunnel := NewCloudflareTunnel(8080, logger.NewNop())

	testCases := []struct {
		input    string
		expected string
	}{
		{
			input:    "INF |  https://random-words-1234.trycloudflare.com",
			expected: "https://random-words-1234.trycloudflare.com",
		},
		{
			input:    "https://abc-def-ghi.trycloudflare.com is your tunnel URL",
			expected: "https://abc-def-ghi.trycloudflare.com",
		},
		{
			input:    "INF Connection established",
			expected: "",
		},
	}

	for _, tc := range testCases {
		match := tunnel.urlPattern.FindString(tc.input)
		if match != tc.expected {
			t.Errorf("Expected %q, got %q for input %q", tc.expected, match, tc.input)
		}
	}
}

func TestResolverCustomURL(t *testing.T) {
	config := Config{
		Mode:      NetworkModeCustom,
		CustomURL: "https://my-homelab.example.com:5260",
		LocalPort: 5260,
	}
	log := logger.NewNop()

	resolver := NewRegistryURLResolver(config, log)
	ctx := context.Background()

	url, err := resolver.GetRegistryURL(ctx)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if url != "https://my-homelab.example.com:5260" {
		t.Errorf("Expected custom URL, got %q", url)
	}
}

func TestResolverAutoUsesSaaSExternalURL(t *testing.T) {
	// On a hosted SaaS instance (Render etc.) the resolver must prefer the
	// operator-configured public URL over LAN scanning, otherwise workers
	// receive an unreachable RFC1918 address such as 10.x or 192.168.x.
	t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "https://techstack.kombify.io")
	t.Setenv("TECHSTACK_PUBLIC_URL", "")
	t.Setenv("RENDER_EXTERNAL_URL", "")
	t.Setenv("RENDER_EXTERNAL_HOSTNAME", "")

	resolver := NewRegistryURLResolver(Config{Mode: NetworkModeAuto, LocalPort: 5260}, logger.NewNop())

	url, err := resolver.GetRegistryURL(context.Background())
	if err != nil {
		t.Fatalf("expected SaaS external URL, got error: %v", err)
	}
	if url != "https://techstack.kombify.io" {
		t.Errorf("expected https://techstack.kombify.io, got %q", url)
	}
	if mode := resolver.GetCurrentMode(); mode != NetworkModeCustom {
		t.Errorf("expected mode custom, got %s", mode)
	}
}

func TestResolverAutoUsesRenderExternalHostnameFallback(t *testing.T) {
	t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "")
	t.Setenv("TECHSTACK_PUBLIC_URL", "")
	t.Setenv("RENDER_EXTERNAL_URL", "")
	t.Setenv("RENDER_EXTERNAL_HOSTNAME", "techstack-prd.onrender.com")

	resolver := NewRegistryURLResolver(Config{Mode: NetworkModeAuto, LocalPort: 5260}, logger.NewNop())

	url, err := resolver.GetRegistryURL(context.Background())
	if err != nil {
		t.Fatalf("expected fallback to RENDER_EXTERNAL_HOSTNAME, got error: %v", err)
	}
	if url != "https://techstack-prd.onrender.com" {
		t.Errorf("expected https://techstack-prd.onrender.com, got %q", url)
	}
}

func TestResolverCustomURLInvalid(t *testing.T) {
	config := Config{
		Mode:      NetworkModeCustom,
		CustomURL: "http://localhost:5260",
		LocalPort: 5260,
	}
	log := logger.NewNop()

	resolver := NewRegistryURLResolver(config, log)
	ctx := context.Background()

	_, err := resolver.GetRegistryURL(ctx)
	if err == nil {
		t.Fatal("Expected error for localhost URL")
	}
}

func TestResolverCaching(t *testing.T) {
	config := Config{
		Mode:      NetworkModeCustom,
		CustomURL: "https://test.example.com:5260",
		LocalPort: 5260,
	}
	log := logger.NewNop()

	resolver := NewRegistryURLResolver(config, log)
	ctx := context.Background()

	// First call
	url1, err := resolver.GetRegistryURL(ctx)
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}

	// Change config (should not affect cached result)
	resolver.config.CustomURL = "https://changed.example.com:5260"

	// Second call should return cached value
	url2, err := resolver.GetRegistryURL(ctx)
	if err != nil {
		t.Fatalf("Second call failed: %v", err)
	}

	if url1 != url2 {
		t.Errorf("Expected cached URL %q, got %q", url1, url2)
	}
}

func TestGenerateInstallCommand(t *testing.T) {
	config := Config{
		Mode:      NetworkModeCustom,
		CustomURL: "https://my-homelab.example.com:5260",
		LocalPort: 5260,
	}
	log := logger.NewNop()

	resolver := NewRegistryURLResolver(config, log)
	ctx := context.Background()

	cmd, err := resolver.GenerateInstallCommand(ctx, "test-token-123")
	if err != nil {
		t.Fatalf("Failed to generate command: %v", err)
	}

	expected := `curl -fsSL 'https://my-homelab.example.com:5260/install.sh' | KOMBI_SERVER='https://my-homelab.example.com:5260' KOMBI_TOKEN='test-token-123' bash`
	if cmd != expected {
		t.Errorf("Expected:\n%s\nGot:\n%s", expected, cmd)
	}
}

func TestGenerateInstallCommandRejectsUnsafeToken(t *testing.T) {
	config := Config{
		Mode:      NetworkModeCustom,
		CustomURL: "https://my-homelab.example.com:5260",
		LocalPort: 5260,
	}
	resolver := NewRegistryURLResolver(config, logger.NewNop())

	if _, err := resolver.GenerateInstallCommand(context.Background(), `bad"; rm -rf /`); err == nil {
		t.Fatal("expected unsafe token to be rejected")
	}
}

func TestGenerateInstallCommandPS(t *testing.T) {
	config := Config{
		Mode:      NetworkModeCustom,
		CustomURL: "https://my-homelab.example.com:5260",
		LocalPort: 5260,
	}
	log := logger.NewNop()

	resolver := NewRegistryURLResolver(config, log)
	ctx := context.Background()

	cmd, err := resolver.GenerateInstallCommandPS(ctx, "test-token-123")
	if err != nil {
		t.Fatalf("Failed to generate command: %v", err)
	}

	expected := `$env:KOMBI_SERVER='https://my-homelab.example.com:5260'; $env:KOMBI_TOKEN='test-token-123'; irm 'https://my-homelab.example.com:5260/install.ps1' | iex`
	if cmd != expected {
		t.Errorf("Expected:\n%s\nGot:\n%s", expected, cmd)
	}
}

func TestGenerateInstallCommandPSRejectsUnsafeToken(t *testing.T) {
	config := Config{
		Mode:      NetworkModeCustom,
		CustomURL: "https://my-homelab.example.com:5260",
		LocalPort: 5260,
	}
	resolver := NewRegistryURLResolver(config, logger.NewNop())

	if _, err := resolver.GenerateInstallCommandPS(context.Background(), "bad'; Remove-Item C:\\"); err == nil {
		t.Fatal("expected unsafe PowerShell token to be rejected")
	}
}
