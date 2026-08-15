// Package hooks contains PocketBase lifecycle hooks and bootstrap logic.
package hooks

import (
	"fmt"
	"os"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// Environment variable names for user credentials
const (
	// Superuser (PocketBase admin panel access)
	EnvSuperuserEmail    = "TECHSTACK_SUPERUSER_EMAIL"
	EnvSuperuserPassword = "TECHSTACK_SUPERUSER_PASSWORD"

	// Admin user (kombifyTechstack app admin in users collection)
	EnvAdminEmail    = "TECHSTACK_ADMIN_EMAIL"
	EnvAdminPassword = "TECHSTACK_ADMIN_PASSWORD"

	// Developer user (kombifyTechstack app developer in users collection)
	EnvDeveloperEmail    = "TECHSTACK_DEVELOPER_EMAIL"
	EnvDeveloperPassword = "TECHSTACK_DEVELOPER_PASSWORD"
)

// Default credentials (for development only!)
const (
	DefaultSuperuserEmail = "superuser@techstack.local"

	DefaultAdminEmail = "admin@techstack.local"

	DefaultDeveloperEmail = "developer@techstack.local"
)

// BootstrapUsers creates the initial users on first startup.
// This includes:
// - PocketBase superuser (for admin panel at /_/)
// - kombifyTechstack admin user (in users collection, role=admin)
// - kombifyTechstack developer user (in users collection, role=developer)
func BootstrapUsers(app core.App) error {
	isDev := isDevEnvironment()
	shouldBootstrap, err := shouldBootstrapUsers(isDev)
	if err != nil {
		return err
	}
	if !shouldBootstrap {
		fmt.Println("ℹ️  User bootstrap skipped: explicit credentials not provided outside development")
		return nil
	}

	// 1. Create superuser (PocketBase admin)
	suEmail, suPassword, suPasswordKnown, err := ensureSuperuser(app, isDev)
	if err != nil {
		return fmt.Errorf("create superuser: %w", err)
	}

	// 2. Create admin user in users collection
	adminID, adminEmail, adminPassword, adminPasswordKnown, err := ensureAppUser(app, "admin", isDev)
	if err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	// 3. Create developer user in users collection
	devID, devEmail, devPassword, devPasswordKnown, err := ensureAppUser(app, "developer", isDev)
	if err != nil {
		return fmt.Errorf("create developer user: %w", err)
	}

	// 4. Ensure auth_config record exists (marks setup as complete, prevents wizard)
	if err := ensureAuthConfig(app); err != nil {
		return fmt.Errorf("ensure auth config: %w", err)
	}

	// 5. Store known bootstrap credentials in the Wallet (copyable in UI)
	if err := ensureSystemCredentialsInWallet(app,
		isDev,
		adminID, devID,
		suEmail, suPassword, suPasswordKnown,
		adminEmail, adminPassword, adminPasswordKnown,
		devEmail, devPassword, devPasswordKnown,
	); err != nil {
		return fmt.Errorf("store system credentials in wallet: %w", err)
	}

	return nil
}

// BootstrapOAuthProviders configures PocketBase OAuth2 providers from environment variables.
// If TECHSTACK_OAUTH_{PROVIDER}_CLIENT_ID and _CLIENT_SECRET are both set,
// the provider is enabled on the users collection.
func BootstrapOAuthProviders(app core.App) error {
	type providerEnv struct {
		name   string
		envID  string
		envSec string
	}

	providers := []providerEnv{
		{"google", "TECHSTACK_OAUTH_GOOGLE_CLIENT_ID", "TECHSTACK_OAUTH_GOOGLE_CLIENT_SECRET"},
		{"github", "TECHSTACK_OAUTH_GITHUB_CLIENT_ID", "TECHSTACK_OAUTH_GITHUB_CLIENT_SECRET"},
	}

	var toApply []core.OAuth2ProviderConfig
	for _, p := range providers {
		clientID := strings.TrimSpace(os.Getenv(p.envID))
		clientSecret := strings.TrimSpace(os.Getenv(p.envSec))
		if clientID != "" && clientSecret != "" {
			toApply = append(toApply, core.OAuth2ProviderConfig{
				Name:         p.name,
				ClientId:     clientID,
				ClientSecret: clientSecret,
			})
		}
	}

	if len(toApply) == 0 {
		return nil
	}

	collection, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		return fmt.Errorf("find users collection for OAuth2 config: %w", err)
	}

	for _, newCfg := range toApply {
		updated := false
		for i, existing := range collection.OAuth2.Providers {
			if existing.Name == newCfg.Name {
				collection.OAuth2.Providers[i].ClientId = newCfg.ClientId
				collection.OAuth2.Providers[i].ClientSecret = newCfg.ClientSecret
				updated = true
				break
			}
		}
		if !updated {
			collection.OAuth2.Providers = append(collection.OAuth2.Providers, newCfg)
		}
		fmt.Printf("✓ OAuth2 provider configured from env: %s\n", newCfg.Name)
	}

	collection.OAuth2.Enabled = true
	if err := app.Save(collection); err != nil {
		return fmt.Errorf("save OAuth2 config: %w", err)
	}

	return nil
}

// ensureSuperuser creates or updates the PocketBase superuser.
// Returns (email, password, passwordKnown).
// In production, we do not rotate an existing password unless it is explicitly provided via env.
func ensureSuperuser(app core.App, isDev bool) (string, string, bool, error) {
	email := getEnvOrDefault(EnvSuperuserEmail, DefaultSuperuserEmail)
	provided := strings.TrimSpace(os.Getenv(EnvSuperuserPassword)) != ""

	superusersCollection, err := app.FindCollectionByNameOrId("_superusers")
	if err != nil {
		return email, "", false, fmt.Errorf("find superusers collection: %w", err)
	}

	existing, _ := app.FindAuthRecordByEmail(superusersCollection, email)
	if existing != nil {
		// Only rotate in dev, or if explicitly provided.
		if !isDev && !provided {
			fmt.Printf("✓ Superuser already exists\n")
			return email, "", false, nil
		}

		password, err := resolvePassword(EnvSuperuserPassword, email, "superuser", isDev)
		if err != nil {
			return email, "", false, err
		}
		existing.SetPassword(password)
		if err := app.Save(existing); err != nil {
			return email, "", false, fmt.Errorf("update superuser password: %w", err)
		}
		fmt.Printf("✓ Superuser updated: %s\n", email)
		return email, password, true, nil
	}

	password, err := resolvePassword(EnvSuperuserPassword, email, "superuser", isDev)
	if err != nil {
		return email, "", false, err
	}

	// Create superuser using the app's built-in method
	superuser := core.NewRecord(superusersCollection)
	superuser.Set("email", email)
	superuser.SetPassword(password)

	if err := app.Save(superuser); err != nil {
		// Try alternative: use direct creation logic
		if err2 := createSuperuserDirect(app, email, password); err2 != nil {
			return email, "", false, err2
		}
	}

	fmt.Printf("✓ Superuser created: %s\n", email)
	return email, password, true, nil
}

// createSuperuserDirect creates superuser using direct collection access
func createSuperuserDirect(app core.App, email, password string) error {
	superusersCollection, err := app.FindCollectionByNameOrId("_superusers")
	if err != nil {
		return fmt.Errorf("find superusers collection: %w", err)
	}

	// Check if email already exists
	existing, _ := app.FindAuthRecordByEmail(superusersCollection, email)
	if existing != nil {
		fmt.Printf("✓ Superuser already exists: %s\n", email)
		return nil
	}

	superuser := core.NewRecord(superusersCollection)
	superuser.Set("email", email)
	superuser.SetPassword(password)

	if err := app.Save(superuser); err != nil {
		return fmt.Errorf("save superuser: %w", err)
	}

	fmt.Printf("✓ Superuser created: %s\n", email)
	return nil
}

// ensureAppUser creates or updates a user in the users collection.
// Returns (id, email, password, passwordKnown).
// In production, we do not rotate an existing password unless it is explicitly provided via env.
func ensureAppUser(app core.App, role string, isDev bool) (string, string, string, bool, error) {
	var email string
	var envEmail, envPassword, defaultEmail string

	switch role {
	case "admin":
		envEmail, envPassword = EnvAdminEmail, EnvAdminPassword
		defaultEmail = DefaultAdminEmail
	case "developer":
		envEmail, envPassword = EnvDeveloperEmail, EnvDeveloperPassword
		defaultEmail = DefaultDeveloperEmail
	default:
		return "", "", "", false, fmt.Errorf("unknown role: %s", role)
	}

	email = getEnvOrDefault(envEmail, defaultEmail)
	provided := strings.TrimSpace(os.Getenv(envPassword)) != ""

	// Find or create in users collection
	usersCollection, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		return "", email, "", false, fmt.Errorf("find users collection: %w", err)
	}

	// Check if user already exists
	existing, _ := app.FindAuthRecordByEmail(usersCollection, email)
	if existing != nil {
		// Update metadata, and rotate password only in dev or when explicitly provided.
		existing.Set("name", role)
		existing.Set("verified", true)

		if isDev || provided {
			password, err := resolvePassword(envPassword, email, role, isDev)
			if err != nil {
				return existing.Id, email, "", false, err
			}
			existing.SetPassword(password)
			if err := app.Save(existing); err != nil {
				return existing.Id, email, "", false, fmt.Errorf("update existing user: %w", err)
			}
			fmt.Printf("✓ User updated: %s (role=%s)\n", email, role)
			return existing.Id, email, password, true, nil
		}

		if err := app.Save(existing); err != nil {
			return existing.Id, email, "", false, fmt.Errorf("update existing user: %w", err)
		}
		fmt.Printf("✓ User already exists: %s (role=%s)\n", email, role)
		return existing.Id, email, "", false, nil
	}

	password, err := resolvePassword(envPassword, email, role, isDev)
	if err != nil {
		return "", email, "", false, err
	}

	// Create new user
	user := core.NewRecord(usersCollection)
	user.Set("email", email)
	user.Set("name", role)
	user.Set("verified", true)
	user.SetPassword(password)

	if err := app.Save(user); err != nil {
		return "", email, "", false, fmt.Errorf("create user: %w", err)
	}

	fmt.Printf("✓ User created: %s (role=%s)\n", email, role)
	return user.Id, email, password, true, nil
}

func ensureSystemCredentialsInWallet(
	app core.App,
	isDev bool,
	adminID, devID string,
	suEmail, suPassword string, suKnown bool,
	adminEmail, adminPassword string, adminKnown bool,
	devEmail, devPassword string, devKnown bool,
) error {
	if !isDev {
		return nil
	}

	walletCollection, err := app.FindCollectionByNameOrId("wallet")
	if err != nil {
		// Wallet collection may not exist in some dev states; don't hard-fail bootstrap.
		return nil
	}

	ensure := func(ownerID, serviceID, name, email, password string, known bool) error {
		if strings.TrimSpace(ownerID) == "" || !known {
			return nil
		}

		rec, _ := app.FindFirstRecordByFilter(
			"wallet",
			"owner_id = {:o} && service_id = {:sid}",
			map[string]any{"o": ownerID, "sid": serviceID},
		)
		if rec == nil {
			rec = core.NewRecord(walletCollection)
			rec.Set("owner_id", ownerID)
			rec.Set("service_id", serviceID)
		}
		rec.Set("name", name)
		rec.Set("kind", "password")
		rec.Set("username", email)
		rec.Set("secret", password)
		rec.Set("notes", "Bootstrap system account (generated/provided by kombifyTechstack)")
		rec.Set("auto_generated", true)
		return app.Save(rec)
	}

	// Admin sees superuser + admin.
	if err := ensure(adminID, "system:superuser", "PocketBase Superuser", suEmail, suPassword, suKnown); err != nil {
		return err
	}
	if err := ensure(adminID, "system:admin", "kombifyTechstack Admin", adminEmail, adminPassword, adminKnown); err != nil {
		return err
	}

	// Developer sees superuser + developer.
	if err := ensure(devID, "system:superuser", "PocketBase Superuser", suEmail, suPassword, suKnown); err != nil {
		return err
	}
	if err := ensure(devID, "system:developer", "kombifyTechstack Developer", devEmail, devPassword, devKnown); err != nil {
		return err
	}

	return nil
}

// ensureAuthConfig creates the auth_config singleton record if it doesn't exist.
// This prevents the first-run wizard from showing when users are auto-bootstrapped.
func ensureAuthConfig(app core.App) error {
	existing, _ := app.FindFirstRecordByFilter("auth_config", "id != ''", nil)
	if existing != nil {
		return nil
	}

	collection, err := app.FindCollectionByNameOrId("auth_config")
	if err != nil {
		return nil // Collection may not exist yet during early migrations
	}

	record := core.NewRecord(collection)
	record.Set("mode", "local")
	record.Set("allow_local_login", true)

	if err := app.Save(record); err != nil {
		return fmt.Errorf("save auth_config: %w", err)
	}

	fmt.Printf("✓ Auth config created (mode=local)\n")
	return nil
}

// Helper functions

func isDevEnvironment() bool {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("TECHSTACK_ENV")))
	if env == "" {
		env = strings.ToLower(strings.TrimSpace(os.Getenv("ENVIRONMENT")))
	}
	return env == "development" || env == "dev" || env == "local"
}

func shouldBootstrapUsers(isDev bool) (bool, error) {
	if isDev {
		return true, nil
	}

	passwordEnvKeys := []string{
		EnvSuperuserPassword,
		EnvAdminPassword,
		EnvDeveloperPassword,
	}

	provided := 0
	for _, key := range passwordEnvKeys {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			provided++
		}
	}

	if provided == 0 {
		return false, nil
	}
	if provided != len(passwordEnvKeys) {
		return false, fmt.Errorf("bootstrap users outside development requires %s, %s, and %s to all be set", EnvSuperuserPassword, EnvAdminPassword, EnvDeveloperPassword)
	}

	return true, nil
}

func getEnvOrDefault(key, defaultVal string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return defaultVal
}

// resolvePassword returns the password from env var or a development-only default.
func resolvePassword(envKey, email, role string, isDev bool) (string, error) {
	password := getEnvOrDefault(envKey, "")
	if password != "" {
		return password, nil
	}

	if isDev {
		password = devDefaultPassword(role)
		fmt.Printf("⚠️  Using development %s password for %s\n", role, email)
		return password, nil
	}

	return "", fmt.Errorf("%s must be set outside development for %s (%s)", envKey, role, email)
}

func devDefaultPassword(role string) string {
	return "dev-" + role + "-password-change-me"
}
