// Package auth provides trusted device and IP authentication for kombifyTechstack.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"time"
)

// ============================================================================
// Device Trust
// ============================================================================

// DeviceIDLength is the length of a device fingerprint
const DeviceIDLength = 64

// TrustedDevice represents a trusted device for auto-login
type TrustedDevice struct {
	ID        string     `json:"id"`
	UserID    string     `json:"user"`
	DeviceID  string     `json:"device_id"`
	Name      string     `json:"name"`
	Type      string     `json:"type"` // desktop, laptop, mobile, tablet, other
	UserAgent string     `json:"user_agent"`
	Active    bool       `json:"active"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	LastSeen  time.Time  `json:"last_seen"`
	Created   time.Time  `json:"created"`
}

// TrustedIP represents a trusted IP address for auto-login
type TrustedIP struct {
	ID        string     `json:"id"`
	UserID    string     `json:"user"`
	IPAddress string     `json:"ip_address"`
	CIDR      *int       `json:"cidr,omitempty"`
	Name      string     `json:"name"`
	Active    bool       `json:"active"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	LastUsed  time.Time  `json:"last_used"`
	Created   time.Time  `json:"created"`
}

// GenerateDeviceID creates a unique device fingerprint from client attributes.
// The fingerprint is a SHA256 hash of the combined attributes.
func GenerateDeviceID(userAgent, acceptLanguage, screenResolution string, additionalData ...string) string {
	// Combine all identifying information
	data := strings.Join(append([]string{userAgent, acceptLanguage, screenResolution}, additionalData...), "|")

	// Add entropy to make the fingerprint unique per user session request
	entropy := make([]byte, 16)
	rand.Read(entropy)
	data += "|" + base64.StdEncoding.EncodeToString(entropy)

	hash := sha256.Sum256([]byte(data))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// ValidateDeviceID checks if a device ID has the expected format
func ValidateDeviceID(deviceID string) bool {
	// Should be base64url encoded SHA256 hash (43 characters without padding)
	if len(deviceID) < 32 || len(deviceID) > 128 {
		return false
	}

	// Try to decode - if it fails, the format is invalid
	_, err := base64.RawURLEncoding.DecodeString(deviceID)
	return err == nil
}

// IsDeviceExpired checks if a trusted device has expired
func (d *TrustedDevice) IsDeviceExpired() bool {
	if d.ExpiresAt == nil {
		return false // Never expires
	}
	return time.Now().After(*d.ExpiresAt)
}

// IsDeviceTrusted checks if a device is trusted (active and not expired)
func (d *TrustedDevice) IsDeviceTrusted() bool {
	return d.Active && !d.IsDeviceExpired()
}

// ============================================================================
// IP Trust
// ============================================================================

// ValidateIPAddress checks if the IP address is valid
func ValidateIPAddress(ip string) bool {
	return net.ParseIP(ip) != nil
}

// IsIPExpired checks if a trusted IP has expired
func (t *TrustedIP) IsIPExpired() bool {
	if t.ExpiresAt == nil {
		return false // Never expires
	}
	return time.Now().After(*t.ExpiresAt)
}

// IsIPTrusted checks if an IP is trusted (active and not expired)
func (t *TrustedIP) IsIPTrusted() bool {
	return t.Active && !t.IsIPExpired()
}

// MatchesIP checks if the given IP matches this trusted IP entry
// Supports both exact match and CIDR range matching
func (t *TrustedIP) MatchesIP(clientIP string) bool {
	if !t.IsIPTrusted() {
		return false
	}

	// Parse client IP
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}

	// Exact match
	trustedIP := net.ParseIP(t.IPAddress)
	if trustedIP == nil {
		return false
	}

	// If no CIDR, do exact match
	if t.CIDR == nil || *t.CIDR == 0 {
		return ip.Equal(trustedIP)
	}

	// CIDR range match
	cidrNotation := fmt.Sprintf("%s/%d", t.IPAddress, *t.CIDR)
	_, network, err := net.ParseCIDR(cidrNotation)
	if err != nil {
		return false
	}

	return network.Contains(ip)
}

// ============================================================================
// Trust Token Generation
// ============================================================================

// TrustTokenLength is the length of a trust token (used for device registration)
const TrustTokenLength = 32

// GenerateTrustToken creates a random token for device trust registration
func GenerateTrustToken() (string, error) {
	bytes := make([]byte, TrustTokenLength)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate trust token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// DeviceTrustRequest represents a request to trust a new device
type DeviceTrustRequest struct {
	DeviceID   string `json:"device_id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	UserAgent  string `json:"user_agent"`
	ExpiryDays *int   `json:"expiry_days,omitempty"` // nil = never expires
}

// IPTrustRequest represents a request to trust a new IP
type IPTrustRequest struct {
	IPAddress  string `json:"ip_address"`
	CIDR       *int   `json:"cidr,omitempty"`
	Name       string `json:"name"`
	ExpiryDays *int   `json:"expiry_days,omitempty"` // nil = never expires
}

// Validate validates a DeviceTrustRequest
func (r *DeviceTrustRequest) Validate() error {
	if !ValidateDeviceID(r.DeviceID) {
		return fmt.Errorf("invalid device_id format")
	}
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(r.Name) > 100 {
		return fmt.Errorf("name must be 100 characters or less")
	}
	validTypes := map[string]bool{"desktop": true, "laptop": true, "mobile": true, "tablet": true, "other": true}
	if r.Type != "" && !validTypes[r.Type] {
		return fmt.Errorf("invalid device type")
	}
	return nil
}

// Validate validates an IPTrustRequest
func (r *IPTrustRequest) Validate() error {
	if !ValidateIPAddress(r.IPAddress) {
		return fmt.Errorf("invalid IP address")
	}
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(r.Name) > 100 {
		return fmt.Errorf("name must be 100 characters or less")
	}
	if r.CIDR != nil && (*r.CIDR < 0 || *r.CIDR > 128) {
		return fmt.Errorf("CIDR must be between 0 and 128")
	}
	return nil
}
