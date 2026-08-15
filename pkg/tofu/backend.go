// Package tofu provides remote state backend configuration for OpenTofu.
// IAC-7: Remote State Backend Support
package tofu

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BackendType represents the type of remote state backend.
type BackendType string

const (
	// BackendLocal uses local file system (default).
	BackendLocal BackendType = "local"
	// BackendS3 uses AWS S3 or S3-compatible storage (MinIO, DigitalOcean Spaces).
	BackendS3 BackendType = "s3"
	// BackendGCS uses Google Cloud Storage.
	BackendGCS BackendType = "gcs"
	// BackendAzureRM uses Azure Blob Storage.
	BackendAzureRM BackendType = "azurerm"
	// BackendHTTP uses a generic HTTP backend.
	BackendHTTP BackendType = "http"
	// BackendConsul uses HashiCorp Consul.
	BackendConsul BackendType = "consul"
)

// BackendConfig represents configuration for a remote state backend.
type BackendConfig struct {
	// Type specifies the backend type.
	Type BackendType `json:"type"`

	// S3Config holds S3-specific configuration.
	S3 *S3BackendConfig `json:"s3,omitempty"`

	// GCSConfig holds GCS-specific configuration.
	GCS *GCSBackendConfig `json:"gcs,omitempty"`

	// AzureRM holds Azure-specific configuration.
	AzureRM *AzureRMBackendConfig `json:"azurerm,omitempty"`

	// HTTP holds HTTP backend configuration.
	HTTP *HTTPBackendConfig `json:"http,omitempty"`

	// Consul holds Consul backend configuration.
	Consul *ConsulBackendConfig `json:"consul,omitempty"`

	// Local holds local backend configuration.
	Local *LocalBackendConfig `json:"local,omitempty"`
}

// S3BackendConfig configures an S3-compatible backend.
type S3BackendConfig struct {
	// Bucket name for state storage.
	Bucket string `json:"bucket"`
	// Key is the path within the bucket (e.g., "techstack/terraform.tfstate").
	Key string `json:"key"`
	// Region is the AWS region (e.g., "eu-central-1").
	Region string `json:"region,omitempty"`
	// Endpoint is for S3-compatible storage (MinIO, DigitalOcean Spaces).
	Endpoint string `json:"endpoint,omitempty"`
	// AccessKey for authentication (prefer environment variables).
	AccessKey string `json:"access_key,omitempty"`
	// SecretKey for authentication (prefer environment variables).
	SecretKey string `json:"secret_key,omitempty"`
	// Encrypt enables server-side encryption.
	Encrypt bool `json:"encrypt"`
	// DynamoDBTable for state locking (optional).
	DynamoDBTable string `json:"dynamodb_table,omitempty"`
	// SkipCredentialsValidation for S3-compatible storage.
	SkipCredentialsValidation bool `json:"skip_credentials_validation,omitempty"`
	// SkipMetadataAPICheck for S3-compatible storage.
	SkipMetadataAPICheck bool `json:"skip_metadata_api_check,omitempty"`
	// ForcePathStyle for S3-compatible storage.
	ForcePathStyle bool `json:"force_path_style,omitempty"`
}

// GCSBackendConfig configures a Google Cloud Storage backend.
type GCSBackendConfig struct {
	// Bucket name for state storage.
	Bucket string `json:"bucket"`
	// Prefix is the path within the bucket.
	Prefix string `json:"prefix,omitempty"`
	// Credentials is the path to service account JSON (prefer GOOGLE_CREDENTIALS env).
	Credentials string `json:"credentials,omitempty"`
	// Project is the GCP project ID.
	Project string `json:"project,omitempty"`
}

// AzureRMBackendConfig configures an Azure Blob Storage backend.
type AzureRMBackendConfig struct {
	// StorageAccountName is the Azure storage account name.
	StorageAccountName string `json:"storage_account_name"`
	// ContainerName is the blob container name.
	ContainerName string `json:"container_name"`
	// Key is the blob name for state file.
	Key string `json:"key"`
	// ResourceGroupName for the storage account.
	ResourceGroupName string `json:"resource_group_name,omitempty"`
	// SubscriptionID for the Azure subscription.
	SubscriptionID string `json:"subscription_id,omitempty"`
	// TenantID for Azure AD authentication.
	TenantID string `json:"tenant_id,omitempty"`
	// ClientID for service principal authentication.
	ClientID string `json:"client_id,omitempty"`
	// ClientSecret for service principal authentication.
	ClientSecret string `json:"client_secret,omitempty"`
	// UseMSI enables Managed Service Identity.
	UseMSI bool `json:"use_msi,omitempty"`
}

// HTTPBackendConfig configures a generic HTTP backend.
type HTTPBackendConfig struct {
	// Address is the HTTP endpoint URL.
	Address string `json:"address"`
	// LockAddress is the URL for state locking (optional).
	LockAddress string `json:"lock_address,omitempty"`
	// UnlockAddress is the URL for state unlocking (optional).
	UnlockAddress string `json:"unlock_address,omitempty"`
	// Username for basic authentication.
	Username string `json:"username,omitempty"`
	// Password for basic authentication.
	Password string `json:"password,omitempty"`
	// SkipCertVerification disables TLS certificate verification.
	SkipCertVerification bool `json:"skip_cert_verification,omitempty"`
}

// ConsulBackendConfig configures a HashiCorp Consul backend.
type ConsulBackendConfig struct {
	// Address is the Consul server address.
	Address string `json:"address"`
	// Path is the path in Consul KV store.
	Path string `json:"path"`
	// Scheme is http or https.
	Scheme string `json:"scheme,omitempty"`
	// Datacenter for Consul.
	Datacenter string `json:"datacenter,omitempty"`
	// Token for ACL authentication.
	Token string `json:"token,omitempty"`
	// Lock enables state locking.
	Lock bool `json:"lock"`
}

// LocalBackendConfig configures a local backend.
type LocalBackendConfig struct {
	// Path to the state file.
	Path string `json:"path,omitempty"`
	// WorkspaceDir for workspace state files.
	WorkspaceDir string `json:"workspace_dir,omitempty"`
}

// Validate validates the backend configuration.
func (c *BackendConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("backend config is nil")
	}

	switch c.Type {
	case BackendLocal:
		// Local backend has no required fields
		return nil

	case BackendS3:
		return validateS3Backend(c.S3)

	case BackendGCS:
		return validateGCSBackend(c.GCS)

	case BackendAzureRM:
		return validateAzureRMBackend(c.AzureRM)

	case BackendHTTP:
		return validateHTTPBackend(c.HTTP)

	case BackendConsul:
		return validateConsulBackend(c.Consul)

	default:
		return fmt.Errorf("unsupported backend type: %s", c.Type)
	}
}

func validateS3Backend(c *S3BackendConfig) error {
	if c == nil {
		return fmt.Errorf("s3 backend config is required for type 's3'")
	}
	if c.Bucket == "" {
		return fmt.Errorf("s3 bucket is required")
	}
	if c.Key == "" {
		return fmt.Errorf("s3 key is required")
	}
	return nil
}

func validateGCSBackend(c *GCSBackendConfig) error {
	if c == nil {
		return fmt.Errorf("gcs backend config is required for type 'gcs'")
	}
	if c.Bucket == "" {
		return fmt.Errorf("gcs bucket is required")
	}
	return nil
}

func validateAzureRMBackend(c *AzureRMBackendConfig) error {
	if c == nil {
		return fmt.Errorf("azurerm backend config is required for type 'azurerm'")
	}
	if c.StorageAccountName == "" {
		return fmt.Errorf("azure storage account name is required")
	}
	if c.ContainerName == "" {
		return fmt.Errorf("azure container name is required")
	}
	if c.Key == "" {
		return fmt.Errorf("azure key is required")
	}
	return nil
}

func validateHTTPBackend(c *HTTPBackendConfig) error {
	if c == nil {
		return fmt.Errorf("http backend config is required for type 'http'")
	}
	if c.Address == "" {
		return fmt.Errorf("http address is required")
	}
	return nil
}

func validateConsulBackend(c *ConsulBackendConfig) error {
	if c == nil {
		return fmt.Errorf("consul backend config is required for type 'consul'")
	}
	if c.Address == "" {
		return fmt.Errorf("consul address is required")
	}
	if c.Path == "" {
		return fmt.Errorf("consul path is required")
	}
	return nil
}

// GenerateHCL generates the backend HCL configuration block.
func (c *BackendConfig) GenerateHCL() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("terraform {\n")

	switch c.Type {
	case BackendLocal:
		writeLocalBackend(&sb, c.Local)

	case BackendS3:
		writeS3Backend(&sb, c.S3)

	case BackendGCS:
		writeGCSBackend(&sb, c.GCS)

	case BackendAzureRM:
		writeAzureRMBackend(&sb, c.AzureRM)

	case BackendHTTP:
		writeHTTPBackend(&sb, c.HTTP)

	case BackendConsul:
		writeConsulBackend(&sb, c.Consul)
	}

	sb.WriteString("}\n")
	return sb.String(), nil
}

func writeLocalBackend(sb *strings.Builder, c *LocalBackendConfig) {
	sb.WriteString("  backend \"local\" {\n")
	if c != nil && c.Path != "" {
		fmt.Fprintf(sb, "    path = %q\n", c.Path)
	}
	sb.WriteString("  }\n")
}

func writeS3Backend(sb *strings.Builder, c *S3BackendConfig) {
	sb.WriteString("  backend \"s3\" {\n")
	fmt.Fprintf(sb, "    bucket = %q\n", c.Bucket)
	fmt.Fprintf(sb, "    key    = %q\n", c.Key)
	if c.Region != "" {
		fmt.Fprintf(sb, "    region = %q\n", c.Region)
	}
	if c.Endpoint != "" {
		fmt.Fprintf(sb, "    endpoint = %q\n", c.Endpoint)
	}
	if c.Encrypt {
		sb.WriteString("    encrypt = true\n")
	}
	if c.DynamoDBTable != "" {
		fmt.Fprintf(sb, "    dynamodb_table = %q\n", c.DynamoDBTable)
	}
	if c.SkipCredentialsValidation {
		sb.WriteString("    skip_credentials_validation = true\n")
	}
	if c.SkipMetadataAPICheck {
		sb.WriteString("    skip_metadata_api_check = true\n")
	}
	if c.ForcePathStyle {
		sb.WriteString("    force_path_style = true\n")
	}
	sb.WriteString("  }\n")
}

func writeGCSBackend(sb *strings.Builder, c *GCSBackendConfig) {
	sb.WriteString("  backend \"gcs\" {\n")
	fmt.Fprintf(sb, "    bucket = %q\n", c.Bucket)
	if c.Prefix != "" {
		fmt.Fprintf(sb, "    prefix = %q\n", c.Prefix)
	}
	if c.Project != "" {
		fmt.Fprintf(sb, "    project = %q\n", c.Project)
	}
	sb.WriteString("  }\n")
}

func writeAzureRMBackend(sb *strings.Builder, c *AzureRMBackendConfig) {
	sb.WriteString("  backend \"azurerm\" {\n")
	fmt.Fprintf(sb, "    storage_account_name = %q\n", c.StorageAccountName)
	fmt.Fprintf(sb, "    container_name       = %q\n", c.ContainerName)
	fmt.Fprintf(sb, "    key                  = %q\n", c.Key)
	if c.ResourceGroupName != "" {
		fmt.Fprintf(sb, "    resource_group_name  = %q\n", c.ResourceGroupName)
	}
	if c.UseMSI {
		sb.WriteString("    use_msi = true\n")
	}
	sb.WriteString("  }\n")
}

func writeHTTPBackend(sb *strings.Builder, c *HTTPBackendConfig) {
	sb.WriteString("  backend \"http\" {\n")
	fmt.Fprintf(sb, "    address = %q\n", c.Address)
	if c.LockAddress != "" {
		fmt.Fprintf(sb, "    lock_address   = %q\n", c.LockAddress)
	}
	if c.UnlockAddress != "" {
		fmt.Fprintf(sb, "    unlock_address = %q\n", c.UnlockAddress)
	}
	if c.SkipCertVerification {
		sb.WriteString("    skip_cert_verification = true\n")
	}
	sb.WriteString("  }\n")
}

func writeConsulBackend(sb *strings.Builder, c *ConsulBackendConfig) {
	sb.WriteString("  backend \"consul\" {\n")
	fmt.Fprintf(sb, "    address = %q\n", c.Address)
	fmt.Fprintf(sb, "    path    = %q\n", c.Path)
	if c.Scheme != "" {
		fmt.Fprintf(sb, "    scheme = %q\n", c.Scheme)
	}
	if c.Datacenter != "" {
		fmt.Fprintf(sb, "    datacenter = %q\n", c.Datacenter)
	}
	if c.Lock {
		sb.WriteString("    lock = true\n")
	}
	sb.WriteString("  }\n")
}

// WriteBackendFile writes the backend configuration to a file.
func (c *BackendConfig) WriteBackendFile(dir string) error {
	hcl, err := c.GenerateHCL()
	if err != nil {
		return fmt.Errorf("failed to generate backend HCL: %w", err)
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	backendPath := filepath.Join(dir, "backend.tf")
	if err := os.WriteFile(backendPath, []byte(hcl), 0644); err != nil {
		return fmt.Errorf("failed to write backend file: %w", err)
	}

	return nil
}

// BackendConfigFromJSON parses a backend configuration from JSON.
func BackendConfigFromJSON(data []byte) (*BackendConfig, error) {
	var config BackendConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse backend config: %w", err)
	}
	return &config, nil
}

// DefaultS3BackendConfig returns a default S3 backend configuration.
func DefaultS3BackendConfig(bucket, key, region string) *BackendConfig {
	return &BackendConfig{
		Type: BackendS3,
		S3: &S3BackendConfig{
			Bucket:  bucket,
			Key:     key,
			Region:  region,
			Encrypt: true,
		},
	}
}

// MinIOBackendConfig returns a configuration for MinIO (S3-compatible).
func MinIOBackendConfig(endpoint, bucket, key string) *BackendConfig {
	return &BackendConfig{
		Type: BackendS3,
		S3: &S3BackendConfig{
			Bucket:                    bucket,
			Key:                       key,
			Endpoint:                  endpoint,
			Encrypt:                   false,
			SkipCredentialsValidation: true,
			SkipMetadataAPICheck:      true,
			ForcePathStyle:            true,
		},
	}
}

// DigitalOceanSpacesBackendConfig returns a configuration for DigitalOcean Spaces.
func DigitalOceanSpacesBackendConfig(region, bucket, key string) *BackendConfig {
	endpoint := fmt.Sprintf("https://%s.digitaloceanspaces.com", region)
	return &BackendConfig{
		Type: BackendS3,
		S3: &S3BackendConfig{
			Bucket:                    bucket,
			Key:                       key,
			Region:                    region,
			Endpoint:                  endpoint,
			Encrypt:                   false,
			SkipCredentialsValidation: true,
			SkipMetadataAPICheck:      true,
		},
	}
}
