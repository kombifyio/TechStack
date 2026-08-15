package sso

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret-key-for-sso-verification-32bytes"

// createTestToken generates a test JWT token with the given claims
func createTestToken(t *testing.T, secret string, claims *ssoCustomClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("failed to create test token: %v", err)
	}
	return tokenString
}

func TestNewVerifier(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr error
	}{
		{
			name: "valid config",
			cfg: Config{
				Secret:       testSecret,
				AllowedTools: []string{"kombifystack"},
			},
			wantErr: nil,
		},
		{
			name: "missing secret",
			cfg: Config{
				Secret:       "",
				AllowedTools: []string{"kombifystack"},
			},
			wantErr: ErrMissingSecret,
		},
		{
			name: "no allowed tools is valid",
			cfg: Config{
				Secret: testSecret,
			},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := NewVerifier(tt.cfg)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("NewVerifier() error = %v, want %v", err, tt.wantErr)
				}
				if v != nil {
					t.Error("NewVerifier() should return nil on error")
				}
			} else {
				if err != nil {
					t.Errorf("NewVerifier() unexpected error: %v", err)
				}
				if v == nil {
					t.Error("NewVerifier() should return verifier")
				}
			}
		})
	}
}

func TestVerifier_Verify(t *testing.T) {
	now := time.Now()

	validClaims := &ssoCustomClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		},
		Email: "test@example.com",
		Name:  "Test User",
		Tool:  "kombifystack",
	}

	tests := []struct {
		name         string
		cfg          Config
		tokenFunc    func(t *testing.T) string
		wantErr      error
		wantSub      string
		wantEmail    string
		wantTool     string
		wantIdentity *StackIdentity
	}{
		{
			name: "valid token",
			cfg: Config{
				Secret:       testSecret,
				AllowedTools: []string{"kombifystack"},
			},
			tokenFunc: func(t *testing.T) string {
				return createTestToken(t, testSecret, validClaims)
			},
			wantErr:      nil,
			wantSub:      "user-123",
			wantEmail:    "test@example.com",
			wantTool:     "kombifystack",
			wantIdentity: nil,
		},
		{
			name: "valid token with access token",
			cfg: Config{
				Secret:       testSecret,
				AllowedTools: []string{"kombifystack"},
			},
			tokenFunc: func(t *testing.T) string {
				claims := &ssoCustomClaims{
					RegisteredClaims: jwt.RegisteredClaims{
						Subject:   "user-456",
						IssuedAt:  jwt.NewNumericDate(now),
						ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
					},
					Email:       "admin@example.com",
					Name:        "Admin User",
					Tool:        "kombifystack",
					AccessToken: "cloud-access-token-xyz",
				}
				return createTestToken(t, testSecret, claims)
			},
			wantErr:      nil,
			wantSub:      "user-456",
			wantEmail:    "admin@example.com",
			wantTool:     "kombifystack",
			wantIdentity: nil,
		},
		{
			name: "valid token with stack identity",
			cfg: Config{
				Secret:       testSecret,
				AllowedTools: []string{"kombifystack"},
			},
			tokenFunc: func(t *testing.T) string {
				claims := &ssoCustomClaims{
					RegisteredClaims: jwt.RegisteredClaims{
						Subject:   "user-789",
						IssuedAt:  jwt.NewNumericDate(now),
						ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
					},
					Email: "identity@example.com",
					Name:  "Identity User",
					Tool:  "kombifystack",
					StackIdentity: &StackIdentity{
						Name:           "Soulrack",
						CharacterID:    "robot",
						AnimationStyle: "identity-glitch",
					},
				}
				return createTestToken(t, testSecret, claims)
			},
			wantErr:   nil,
			wantSub:   "user-789",
			wantEmail: "identity@example.com",
			wantTool:  "kombifystack",
			wantIdentity: &StackIdentity{
				Name:           "Soulrack",
				CharacterID:    "robot",
				AnimationStyle: "identity-glitch",
			},
		},
		{
			name: "expired token",
			cfg: Config{
				Secret:       testSecret,
				AllowedTools: []string{"kombifystack"},
			},
			tokenFunc: func(t *testing.T) string {
				claims := &ssoCustomClaims{
					RegisteredClaims: jwt.RegisteredClaims{
						Subject:   "user-123",
						IssuedAt:  jwt.NewNumericDate(now.Add(-10 * time.Minute)),
						ExpiresAt: jwt.NewNumericDate(now.Add(-5 * time.Minute)),
					},
					Email: "test@example.com",
					Name:  "Test User",
					Tool:  "kombifystack",
				}
				return createTestToken(t, testSecret, claims)
			},
			wantErr: ErrTokenExpired,
		},
		{
			name: "invalid tool",
			cfg: Config{
				Secret:       testSecret,
				AllowedTools: []string{"othertool"},
			},
			tokenFunc: func(t *testing.T) string {
				return createTestToken(t, testSecret, validClaims)
			},
			wantErr: ErrInvalidTool,
		},
		{
			name: "no tool restriction",
			cfg: Config{
				Secret:       testSecret,
				AllowedTools: []string{}, // no restriction
			},
			tokenFunc: func(t *testing.T) string {
				return createTestToken(t, testSecret, validClaims)
			},
			wantErr:  nil,
			wantSub:  "user-123",
			wantTool: "kombifystack",
		},
		{
			name: "wrong secret",
			cfg: Config{
				Secret:       testSecret,
				AllowedTools: []string{"kombifystack"},
			},
			tokenFunc: func(t *testing.T) string {
				return createTestToken(t, "wrong-secret-key-different", validClaims)
			},
			wantErr: ErrTokenInvalid,
		},
		{
			name: "empty token",
			cfg: Config{
				Secret:       testSecret,
				AllowedTools: []string{"kombifystack"},
			},
			tokenFunc: func(t *testing.T) string {
				return ""
			},
			wantErr: ErrTokenInvalid,
		},
		{
			name: "malformed token",
			cfg: Config{
				Secret:       testSecret,
				AllowedTools: []string{"kombifystack"},
			},
			tokenFunc: func(t *testing.T) string {
				return "not.a.valid.jwt.token"
			},
			wantErr: ErrTokenInvalid,
		},
		{
			name: "missing subject",
			cfg: Config{
				Secret:       testSecret,
				AllowedTools: []string{"kombifystack"},
			},
			tokenFunc: func(t *testing.T) string {
				claims := &ssoCustomClaims{
					RegisteredClaims: jwt.RegisteredClaims{
						Subject:   "", // missing
						IssuedAt:  jwt.NewNumericDate(now),
						ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
					},
					Email: "test@example.com",
					Name:  "Test User",
					Tool:  "kombifystack",
				}
				return createTestToken(t, testSecret, claims)
			},
			wantErr: ErrMissingClaims,
		},
		{
			name: "missing email",
			cfg: Config{
				Secret:       testSecret,
				AllowedTools: []string{"kombifystack"},
			},
			tokenFunc: func(t *testing.T) string {
				claims := &ssoCustomClaims{
					RegisteredClaims: jwt.RegisteredClaims{
						Subject:   "user-123",
						IssuedAt:  jwt.NewNumericDate(now),
						ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
					},
					Email: "", // missing
					Name:  "Test User",
					Tool:  "kombifystack",
				}
				return createTestToken(t, testSecret, claims)
			},
			wantErr: ErrMissingClaims,
		},
		{
			name: "missing tool",
			cfg: Config{
				Secret:       testSecret,
				AllowedTools: []string{"kombifystack"},
			},
			tokenFunc: func(t *testing.T) string {
				claims := &ssoCustomClaims{
					RegisteredClaims: jwt.RegisteredClaims{
						Subject:   "user-123",
						IssuedAt:  jwt.NewNumericDate(now),
						ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
					},
					Email: "test@example.com",
					Name:  "Test User",
					Tool:  "", // missing
				}
				return createTestToken(t, testSecret, claims)
			},
			wantErr: ErrMissingClaims,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := NewVerifier(tt.cfg)
			if err != nil {
				t.Fatalf("failed to create verifier: %v", err)
			}

			tokenString := tt.tokenFunc(t)
			payload, err := v.Verify(tokenString)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("Verify() error = %v, want %v", err, tt.wantErr)
				}
				if payload != nil {
					t.Error("Verify() should return nil payload on error")
				}
			} else {
				if err != nil {
					t.Fatalf("Verify() unexpected error: %v", err)
				}
				if payload == nil {
					t.Fatal("Verify() should return payload")
				}
				if payload.Sub != tt.wantSub {
					t.Errorf("payload.Sub = %q, want %q", payload.Sub, tt.wantSub)
				}
				if tt.wantEmail != "" && payload.Email != tt.wantEmail {
					t.Errorf("payload.Email = %q, want %q", payload.Email, tt.wantEmail)
				}
				if payload.Tool != tt.wantTool {
					t.Errorf("payload.Tool = %q, want %q", payload.Tool, tt.wantTool)
				}
				if tt.wantIdentity == nil {
					if payload.StackIdentity != nil {
						t.Errorf("payload.StackIdentity = %#v, want nil", payload.StackIdentity)
					}
				} else if payload.StackIdentity == nil || *payload.StackIdentity != *tt.wantIdentity {
					t.Errorf("payload.StackIdentity = %#v, want %#v", payload.StackIdentity, tt.wantIdentity)
				}
			}
		})
	}
}

func TestVerifier_VerifyRS256Rejected(t *testing.T) {
	// Ensure RS256 tokens are rejected (we only accept HS256)
	v, err := NewVerifier(Config{
		Secret:       testSecret,
		AllowedTools: []string{"kombifystack"},
	})
	if err != nil {
		t.Fatalf("failed to create verifier: %v", err)
	}

	// Create a token header that claims RS256 but is actually tampered
	// This tests that we reject non-HMAC algorithms
	maliciousToken := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1c2VyLTEyMyIsImVtYWlsIjoidGVzdEBleGFtcGxlLmNvbSIsIm5hbWUiOiJUZXN0IiwidG9vbCI6ImtvbWJpZnlzdGFjayIsImlhdCI6MTcwMDAwMDAwMCwiZXhwIjoxOTAwMDAwMDAwfQ.invalid"

	_, err = v.Verify(maliciousToken)
	if err == nil {
		t.Error("expected error for RS256 token, got nil")
	}
	if !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestVerifyFromEnv(t *testing.T) {
	v, err := VerifyFromEnv(testSecret, []string{"kombifystack"})
	if err != nil {
		t.Fatalf("VerifyFromEnv() error: %v", err)
	}
	if v == nil {
		t.Error("VerifyFromEnv() should return verifier")
	}

	_, err = VerifyFromEnv("", []string{"kombifystack"})
	if !errors.Is(err, ErrMissingSecret) {
		t.Errorf("VerifyFromEnv() with empty secret should return ErrMissingSecret, got %v", err)
	}
}

func TestErrorHelpers(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		isExpired bool
		isInvalid bool
		isInvTool bool
	}{
		{"ErrTokenExpired", ErrTokenExpired, true, false, false},
		{"ErrTokenInvalid", ErrTokenInvalid, false, true, false},
		{"ErrInvalidTool", ErrInvalidTool, false, false, true},
		{"wrapped expired", errors.New("test"), false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if IsExpired(tt.err) != tt.isExpired {
				t.Errorf("IsExpired() = %v, want %v", IsExpired(tt.err), tt.isExpired)
			}
			if IsInvalid(tt.err) != tt.isInvalid {
				t.Errorf("IsInvalid() = %v, want %v", IsInvalid(tt.err), tt.isInvalid)
			}
			if IsInvalidTool(tt.err) != tt.isInvTool {
				t.Errorf("IsInvalidTool() = %v, want %v", IsInvalidTool(tt.err), tt.isInvTool)
			}
		})
	}
}

func TestClockSkew(t *testing.T) {
	now := time.Now()

	// Token expired 10 seconds ago
	claims := &ssoCustomClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			IssuedAt:  jwt.NewNumericDate(now.Add(-1 * time.Minute)),
			ExpiresAt: jwt.NewNumericDate(now.Add(-10 * time.Second)),
		},
		Email: "test@example.com",
		Name:  "Test User",
		Tool:  "kombifystack",
	}

	tokenString := createTestToken(t, testSecret, claims)

	// With default 30s clock skew, should still be valid
	v1, _ := NewVerifier(Config{
		Secret:       testSecret,
		AllowedTools: []string{"kombifystack"},
		ClockSkew:    30 * time.Second,
	})
	_, err := v1.Verify(tokenString)
	if err != nil {
		t.Errorf("expected token to be valid with 30s clock skew, got: %v", err)
	}

	// With 0 clock skew (using 1 nanosecond as minimum), should fail
	v2, _ := NewVerifier(Config{
		Secret:       testSecret,
		AllowedTools: []string{"kombifystack"},
		ClockSkew:    1 * time.Nanosecond,
	})
	_, err = v2.Verify(tokenString)
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired with minimal clock skew, got: %v", err)
	}
}
