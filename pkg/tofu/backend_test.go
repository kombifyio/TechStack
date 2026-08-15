package tofu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *BackendConfig
		wantErr bool
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
		},
		{
			name: "valid local backend",
			config: &BackendConfig{
				Type: BackendLocal,
			},
			wantErr: false,
		},
		{
			name: "valid S3 backend",
			config: &BackendConfig{
				Type: BackendS3,
				S3: &S3BackendConfig{
					Bucket: "my-bucket",
					Key:    "terraform.tfstate",
					Region: "eu-central-1",
				},
			},
			wantErr: false,
		},
		{
			name: "S3 backend missing bucket",
			config: &BackendConfig{
				Type: BackendS3,
				S3: &S3BackendConfig{
					Key: "terraform.tfstate",
				},
			},
			wantErr: true,
		},
		{
			name: "S3 backend missing key",
			config: &BackendConfig{
				Type: BackendS3,
				S3: &S3BackendConfig{
					Bucket: "my-bucket",
				},
			},
			wantErr: true,
		},
		{
			name: "S3 backend missing config",
			config: &BackendConfig{
				Type: BackendS3,
			},
			wantErr: true,
		},
		{
			name: "valid GCS backend",
			config: &BackendConfig{
				Type: BackendGCS,
				GCS: &GCSBackendConfig{
					Bucket: "my-gcs-bucket",
				},
			},
			wantErr: false,
		},
		{
			name: "GCS backend missing bucket",
			config: &BackendConfig{
				Type: BackendGCS,
				GCS:  &GCSBackendConfig{},
			},
			wantErr: true,
		},
		{
			name: "valid AzureRM backend",
			config: &BackendConfig{
				Type: BackendAzureRM,
				AzureRM: &AzureRMBackendConfig{
					StorageAccountName: "mystorageaccount",
					ContainerName:      "tfstate",
					Key:                "terraform.tfstate",
				},
			},
			wantErr: false,
		},
		{
			name: "AzureRM backend missing storage account",
			config: &BackendConfig{
				Type: BackendAzureRM,
				AzureRM: &AzureRMBackendConfig{
					ContainerName: "tfstate",
					Key:           "terraform.tfstate",
				},
			},
			wantErr: true,
		},
		{
			name: "valid HTTP backend",
			config: &BackendConfig{
				Type: BackendHTTP,
				HTTP: &HTTPBackendConfig{
					Address: "https://state.example.com/state",
				},
			},
			wantErr: false,
		},
		{
			name: "HTTP backend missing address",
			config: &BackendConfig{
				Type: BackendHTTP,
				HTTP: &HTTPBackendConfig{},
			},
			wantErr: true,
		},
		{
			name: "valid Consul backend",
			config: &BackendConfig{
				Type: BackendConsul,
				Consul: &ConsulBackendConfig{
					Address: "consul.example.com:8500",
					Path:    "terraform/state",
				},
			},
			wantErr: false,
		},
		{
			name: "unsupported backend type",
			config: &BackendConfig{
				Type: "unknown",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBackendConfig_GenerateHCL(t *testing.T) {
	tests := []struct {
		name        string
		config      *BackendConfig
		wantErr     bool
		mustContain []string
	}{
		{
			name: "local backend",
			config: &BackendConfig{
				Type: BackendLocal,
				Local: &LocalBackendConfig{
					Path: "/var/lib/terraform/state.tfstate",
				},
			},
			wantErr: false,
			mustContain: []string{
				`backend "local"`,
				`path = "/var/lib/terraform/state.tfstate"`,
			},
		},
		{
			name: "S3 backend with all options",
			config: &BackendConfig{
				Type: BackendS3,
				S3: &S3BackendConfig{
					Bucket:        "techstack-state",
					Key:           "homelab/terraform.tfstate",
					Region:        "eu-central-1",
					Encrypt:       true,
					DynamoDBTable: "terraform-locks",
				},
			},
			wantErr: false,
			mustContain: []string{
				`backend "s3"`,
				`bucket = "techstack-state"`,
				`key    = "homelab/terraform.tfstate"`,
				`region = "eu-central-1"`,
				`encrypt = true`,
				`dynamodb_table = "terraform-locks"`,
			},
		},
		{
			name: "S3 backend for MinIO",
			config: &BackendConfig{
				Type: BackendS3,
				S3: &S3BackendConfig{
					Bucket:                    "tfstate",
					Key:                       "state.tfstate",
					Endpoint:                  "http://minio.local:9000",
					SkipCredentialsValidation: true,
					SkipMetadataAPICheck:      true,
					ForcePathStyle:            true,
				},
			},
			wantErr: false,
			mustContain: []string{
				`endpoint = "http://minio.local:9000"`,
				`skip_credentials_validation = true`,
				`skip_metadata_api_check = true`,
				`force_path_style = true`,
			},
		},
		{
			name: "GCS backend",
			config: &BackendConfig{
				Type: BackendGCS,
				GCS: &GCSBackendConfig{
					Bucket:  "my-gcs-bucket",
					Prefix:  "terraform/state",
					Project: "my-project",
				},
			},
			wantErr: false,
			mustContain: []string{
				`backend "gcs"`,
				`bucket = "my-gcs-bucket"`,
				`prefix = "terraform/state"`,
				`project = "my-project"`,
			},
		},
		{
			name: "AzureRM backend",
			config: &BackendConfig{
				Type: BackendAzureRM,
				AzureRM: &AzureRMBackendConfig{
					StorageAccountName: "mystorageaccount",
					ContainerName:      "tfstate",
					Key:                "prod/terraform.tfstate",
					ResourceGroupName:  "rg-terraform",
					UseMSI:             true,
				},
			},
			wantErr: false,
			mustContain: []string{
				`backend "azurerm"`,
				`storage_account_name = "mystorageaccount"`,
				`container_name       = "tfstate"`,
				`key                  = "prod/terraform.tfstate"`,
				`resource_group_name  = "rg-terraform"`,
				`use_msi = true`,
			},
		},
		{
			name: "HTTP backend",
			config: &BackendConfig{
				Type: BackendHTTP,
				HTTP: &HTTPBackendConfig{
					Address:       "https://state.example.com/state",
					LockAddress:   "https://state.example.com/lock",
					UnlockAddress: "https://state.example.com/unlock",
				},
			},
			wantErr: false,
			mustContain: []string{
				`backend "http"`,
				`address = "https://state.example.com/state"`,
				`lock_address   = "https://state.example.com/lock"`,
				`unlock_address = "https://state.example.com/unlock"`,
			},
		},
		{
			name: "Consul backend",
			config: &BackendConfig{
				Type: BackendConsul,
				Consul: &ConsulBackendConfig{
					Address:    "consul.local:8500",
					Path:       "terraform/mystack/state",
					Scheme:     "https",
					Datacenter: "dc1",
					Lock:       true,
				},
			},
			wantErr: false,
			mustContain: []string{
				`backend "consul"`,
				`address = "consul.local:8500"`,
				`path    = "terraform/mystack/state"`,
				`scheme = "https"`,
				`datacenter = "dc1"`,
				`lock = true`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hcl, err := tt.config.GenerateHCL()
			if (err != nil) != tt.wantErr {
				t.Errorf("GenerateHCL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}

			// Check that HCL contains required strings
			for _, s := range tt.mustContain {
				if !strings.Contains(hcl, s) {
					t.Errorf("GenerateHCL() output missing %q\nGot:\n%s", s, hcl)
				}
			}

			// Check that it starts with terraform block
			if !strings.HasPrefix(hcl, "terraform {") {
				t.Errorf("GenerateHCL() should start with 'terraform {', got:\n%s", hcl)
			}
		})
	}
}

func TestBackendConfig_WriteBackendFile(t *testing.T) {
	tmpDir := t.TempDir()

	config := &BackendConfig{
		Type: BackendS3,
		S3: &S3BackendConfig{
			Bucket:  "test-bucket",
			Key:     "test.tfstate",
			Region:  "us-east-1",
			Encrypt: true,
		},
	}

	err := config.WriteBackendFile(tmpDir)
	if err != nil {
		t.Fatalf("WriteBackendFile() error = %v", err)
	}

	// Check that file was created
	backendPath := filepath.Join(tmpDir, "backend.tf")
	content, err := os.ReadFile(backendPath)
	if err != nil {
		t.Fatalf("Failed to read backend file: %v", err)
	}

	// Verify content
	if !strings.Contains(string(content), `backend "s3"`) {
		t.Error("backend.tf should contain S3 backend configuration")
	}
	if !strings.Contains(string(content), `bucket = "test-bucket"`) {
		t.Error("backend.tf should contain bucket name")
	}
}

func TestBackendConfigFromJSON(t *testing.T) {
	jsonData := `{
		"type": "s3",
		"s3": {
			"bucket": "my-bucket",
			"key": "terraform.tfstate",
			"region": "eu-west-1",
			"encrypt": true
		}
	}`

	config, err := BackendConfigFromJSON([]byte(jsonData))
	if err != nil {
		t.Fatalf("BackendConfigFromJSON() error = %v", err)
	}

	if config.Type != BackendS3 {
		t.Errorf("expected type S3, got %s", config.Type)
	}
	if config.S3.Bucket != "my-bucket" {
		t.Errorf("expected bucket 'my-bucket', got %s", config.S3.Bucket)
	}
	if !config.S3.Encrypt {
		t.Error("expected encrypt to be true")
	}
}

func TestDefaultS3BackendConfig(t *testing.T) {
	config := DefaultS3BackendConfig("test-bucket", "prod/state.tfstate", "eu-central-1")

	if config.Type != BackendS3 {
		t.Errorf("expected type S3, got %s", config.Type)
	}
	if config.S3.Bucket != "test-bucket" {
		t.Errorf("expected bucket 'test-bucket', got %s", config.S3.Bucket)
	}
	if config.S3.Key != "prod/state.tfstate" {
		t.Errorf("expected key 'prod/state.tfstate', got %s", config.S3.Key)
	}
	if config.S3.Region != "eu-central-1" {
		t.Errorf("expected region 'eu-central-1', got %s", config.S3.Region)
	}
	if !config.S3.Encrypt {
		t.Error("expected encrypt to be true by default")
	}
}

func TestMinIOBackendConfig(t *testing.T) {
	config := MinIOBackendConfig("http://minio.local:9000", "tfstate", "state.tfstate")

	if config.Type != BackendS3 {
		t.Errorf("expected type S3, got %s", config.Type)
	}
	if config.S3.Endpoint != "http://minio.local:9000" {
		t.Errorf("expected endpoint 'http://minio.local:9000', got %s", config.S3.Endpoint)
	}
	if !config.S3.ForcePathStyle {
		t.Error("MinIO config should have ForcePathStyle = true")
	}
	if !config.S3.SkipCredentialsValidation {
		t.Error("MinIO config should have SkipCredentialsValidation = true")
	}
}

func TestDigitalOceanSpacesBackendConfig(t *testing.T) {
	config := DigitalOceanSpacesBackendConfig("fra1", "my-spaces-bucket", "terraform.tfstate")

	if config.Type != BackendS3 {
		t.Errorf("expected type S3, got %s", config.Type)
	}
	if config.S3.Endpoint != "https://fra1.digitaloceanspaces.com" {
		t.Errorf("expected DigitalOcean Spaces endpoint, got %s", config.S3.Endpoint)
	}
	if config.S3.Region != "fra1" {
		t.Errorf("expected region 'fra1', got %s", config.S3.Region)
	}
}
