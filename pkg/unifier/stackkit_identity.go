package unifier

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	baseKitDefaultAdminEmail     = "admin@example.com"
	baseKitAdminPasswordBytes    = 24
	baseKitPocketIDStaticBytes   = 32
	baseKitPocketIDEncryptBytes  = 24
	baseKitDefaultLocalDomain    = "home.localhost"
	baseKitTinyAuthServicePrefix = "auth"
	baseKitPocketIDServicePrefix = "id"
)

func populateBaseKitIdentityTFVars(vars *BaseKitTFVars, config *IaCConfig) error {
	if vars == nil {
		return fmt.Errorf("base-kit tfvars are nil")
	}

	adminEmail := strings.TrimSpace(config.AdminEmail)
	if adminEmail == "" {
		adminEmail = baseKitDefaultAdminEmail
	}
	vars.AdminEmail = adminEmail

	adminPassword := strings.TrimSpace(config.AdminPasswordPlaintext)
	if adminPassword == "" {
		generated, err := randomBase64Secret(baseKitAdminPasswordBytes)
		if err != nil {
			return fmt.Errorf("generate base-kit admin password: %w", err)
		}
		adminPassword = generated
	}
	vars.AdminPasswordPlaintext = adminPassword

	tinyAuthUsers := strings.TrimSpace(config.TinyAuthUsers)
	if tinyAuthUsers == "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash base-kit TinyAuth password: %w", err)
		}
		tinyAuthUsers = fmt.Sprintf("%s:%s", adminEmail, string(hash))
	}
	vars.TinyAuthUsers = tinyAuthUsers

	pocketIDStaticAPIKey := strings.TrimSpace(config.PocketIDStaticAPIKey)
	if pocketIDStaticAPIKey == "" {
		generated, err := randomBase64Secret(baseKitPocketIDStaticBytes)
		if err != nil {
			return fmt.Errorf("generate PocketID static API key: %w", err)
		}
		pocketIDStaticAPIKey = generated
	}
	vars.PocketIDStaticAPIKey = pocketIDStaticAPIKey

	pocketIDEncryptionKey := strings.TrimSpace(config.PocketIDEncryptionKey)
	if pocketIDEncryptionKey == "" {
		generated, err := randomBase64Secret(baseKitPocketIDEncryptBytes)
		if err != nil {
			return fmt.Errorf("generate PocketID encryption key: %w", err)
		}
		pocketIDEncryptionKey = generated
	}
	vars.PocketIDEncryptionKey = pocketIDEncryptionKey

	authURL, idURL := baseKitIdentityURLs(vars)
	vars.TinyAuthAppURL = authURL
	vars.PocketIDAppURL = idURL
	if vars.EnableHTTPS {
		vars.TinyAuthSecureCookie = "true"
	} else {
		vars.TinyAuthSecureCookie = "false"
	}

	return nil
}

func baseKitIdentityURLs(vars *BaseKitTFVars) (authURL string, idURL string) {
	domain := strings.TrimSpace(vars.Domain)
	if domain == "" {
		domain = baseKitDefaultLocalDomain
	}

	authHost := baseKitServiceHost(baseKitTinyAuthServicePrefix, domain, vars.SubdomainPrefix)
	idHost := baseKitServiceHost(baseKitPocketIDServicePrefix, domain, vars.SubdomainPrefix)
	proto := "http"
	if vars.EnableHTTPS {
		proto = "https"
	}
	return proto + "://" + authHost, proto + "://" + idHost
}

func baseKitServiceHost(service, domain, prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix != "" {
		return fmt.Sprintf("%s-%s.%s", prefix, service, domain)
	}
	return fmt.Sprintf("%s.%s", service, domain)
}

func randomBase64Secret(byteLen int) (string, error) {
	if byteLen < 16 {
		return "", fmt.Errorf("byteLen must be >= 16, got %d", byteLen)
	}
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(buf), nil
}
