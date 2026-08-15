// Package backup provides backup functionality including S3 storage and scheduling.
// This file contains unit tests for s3.go
package backup

import (
	"testing"
)

// =============================================================================
// Sprint 9: S3Config Validation Tests (TD-11)
// =============================================================================

func TestNewS3Client_MissingBucket(t *testing.T) {
	cfg := S3Config{
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:    "us-east-1",
	}

	_, err := NewS3Client(cfg)
	if err == nil {
		t.Error("Expected error for missing bucket")
	}
}

func TestNewS3Client_MissingAccessKey(t *testing.T) {
	cfg := S3Config{
		Bucket:    "my-bucket",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:    "us-east-1",
	}

	_, err := NewS3Client(cfg)
	if err == nil {
		t.Error("Expected error for missing access key")
	}
}

func TestNewS3Client_MissingSecretKey(t *testing.T) {
	cfg := S3Config{
		Bucket:    "my-bucket",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		Region:    "us-east-1",
	}

	_, err := NewS3Client(cfg)
	if err == nil {
		t.Error("Expected error for missing secret key")
	}
}

func TestNewS3Client_MissingCredentials(t *testing.T) {
	cfg := S3Config{
		Bucket: "my-bucket",
		Region: "us-east-1",
	}

	_, err := NewS3Client(cfg)
	if err == nil {
		t.Error("Expected error for missing credentials")
	}
}

func TestNewS3Client_DefaultRegion(t *testing.T) {
	cfg := S3Config{
		Bucket:    "my-bucket",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		// Region not specified
	}

	client, err := NewS3Client(cfg)
	if err != nil {
		t.Fatalf("NewS3Client failed: %v", err)
	}

	// Region should default to us-east-1
	// We can't directly access the internal config, but the client should be created
	if client == nil {
		t.Error("Expected non-nil client")
	}
}

// =============================================================================
// Sprint 9: S3Client Helper Methods Tests (TD-11)
// =============================================================================

func TestS3Client_FullKey_WithPrefix(t *testing.T) {
	cfg := S3Config{
		Bucket:    "my-bucket",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:    "us-east-1",
		Prefix:    "backups/",
	}

	client, err := NewS3Client(cfg)
	if err != nil {
		t.Fatalf("NewS3Client failed: %v", err)
	}

	// Test fullKey with prefix
	key := client.fullKey("test.tar.gz")
	expected := "backups/test.tar.gz"
	if key != expected {
		t.Errorf("Expected key '%s', got '%s'", expected, key)
	}
}

func TestS3Client_FullKey_NoPrefix(t *testing.T) {
	cfg := S3Config{
		Bucket:    "my-bucket",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:    "us-east-1",
		Prefix:    "", // No prefix
	}

	client, err := NewS3Client(cfg)
	if err != nil {
		t.Fatalf("NewS3Client failed: %v", err)
	}

	// Test fullKey without prefix
	key := client.fullKey("test.tar.gz")
	expected := "test.tar.gz"
	if key != expected {
		t.Errorf("Expected key '%s', got '%s'", expected, key)
	}
}

func TestS3Client_FullKey_NestedPath(t *testing.T) {
	cfg := S3Config{
		Bucket:    "my-bucket",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:    "us-east-1",
		Prefix:    "techstack/backups/",
	}

	client, err := NewS3Client(cfg)
	if err != nil {
		t.Fatalf("NewS3Client failed: %v", err)
	}

	key := client.fullKey("2026/01/backup.tar.gz")
	expected := "techstack/backups/2026/01/backup.tar.gz"
	if key != expected {
		t.Errorf("Expected key '%s', got '%s'", expected, key)
	}
}

func TestS3Client_Bucket(t *testing.T) {
	cfg := S3Config{
		Bucket:    "my-test-bucket",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:    "us-east-1",
	}

	client, err := NewS3Client(cfg)
	if err != nil {
		t.Fatalf("NewS3Client failed: %v", err)
	}

	if client.Bucket() != "my-test-bucket" {
		t.Errorf("Expected bucket 'my-test-bucket', got '%s'", client.Bucket())
	}
}

func TestS3Client_Prefix(t *testing.T) {
	cfg := S3Config{
		Bucket:    "my-bucket",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:    "us-east-1",
		Prefix:    "my-prefix/",
	}

	client, err := NewS3Client(cfg)
	if err != nil {
		t.Fatalf("NewS3Client failed: %v", err)
	}

	if client.Prefix() != "my-prefix/" {
		t.Errorf("Expected prefix 'my-prefix/', got '%s'", client.Prefix())
	}
}

func TestS3Client_Prefix_Empty(t *testing.T) {
	cfg := S3Config{
		Bucket:    "my-bucket",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:    "us-east-1",
		// No prefix
	}

	client, err := NewS3Client(cfg)
	if err != nil {
		t.Fatalf("NewS3Client failed: %v", err)
	}

	if client.Prefix() != "" {
		t.Errorf("Expected empty prefix, got '%s'", client.Prefix())
	}
}

// =============================================================================
// Sprint 9: S3Object Tests (TD-11)
// =============================================================================

func TestS3Object_AllFields(t *testing.T) {
	obj := S3Object{
		Key:  "backups/test.tar.gz",
		Size: 1024 * 1024 * 10, // 10 MB
		ETag: "d41d8cd98f00b204e9800998ecf8427e",
	}

	if obj.Key != "backups/test.tar.gz" {
		t.Errorf("Expected Key 'backups/test.tar.gz', got '%s'", obj.Key)
	}
	if obj.Size != 10*1024*1024 {
		t.Errorf("Expected Size 10MB, got %d", obj.Size)
	}
	if obj.ETag == "" {
		t.Error("Expected non-empty ETag")
	}
}

// =============================================================================
// Sprint 9: S3Config Tests (TD-11)
// =============================================================================

func TestS3Config_AllFields(t *testing.T) {
	cfg := S3Config{
		Bucket:    "production-bucket",
		Endpoint:  "https://minio.example.com",
		AccessKey: "admin",
		SecretKey: "supersecret",
		Region:    "eu-west-1",
		Prefix:    "techstack/prod/",
	}

	if cfg.Bucket != "production-bucket" {
		t.Errorf("Expected Bucket 'production-bucket', got '%s'", cfg.Bucket)
	}
	if cfg.Endpoint != "https://minio.example.com" {
		t.Errorf("Expected Endpoint 'https://minio.example.com', got '%s'", cfg.Endpoint)
	}
	if cfg.Region != "eu-west-1" {
		t.Errorf("Expected Region 'eu-west-1', got '%s'", cfg.Region)
	}
	if cfg.Prefix != "techstack/prod/" {
		t.Errorf("Expected Prefix 'techstack/prod/', got '%s'", cfg.Prefix)
	}
}

func TestNewS3Client_WithCustomEndpoint(t *testing.T) {
	cfg := S3Config{
		Bucket:    "my-bucket",
		Endpoint:  "http://localhost:9000", // MinIO local
		AccessKey: "minioadmin",
		SecretKey: "minioadmin",
		Region:    "us-east-1",
	}

	client, err := NewS3Client(cfg)
	if err != nil {
		t.Fatalf("NewS3Client with custom endpoint failed: %v", err)
	}
	if client == nil {
		t.Error("Expected non-nil client")
	}
}

// =============================================================================
// Sprint 9: DeleteMultiple Tests (TD-11)
// Note: These test the logic, not actual S3 operations
// =============================================================================

func TestDeleteMultiple_EmptyList(t *testing.T) {
	cfg := S3Config{
		Bucket:    "my-bucket",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Region:    "us-east-1",
	}

	client, err := NewS3Client(cfg)
	if err != nil {
		t.Fatalf("NewS3Client failed: %v", err)
	}

	// Delete empty list should succeed without doing anything
	err = client.DeleteMultiple(nil, []string{})
	if err != nil {
		t.Errorf("DeleteMultiple with empty list should succeed, got: %v", err)
	}
}
