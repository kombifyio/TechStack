package backupstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// HomeAssistantArchive uses the existing scoped backup target credentials. It
// has no delete/overwrite API: original migration archives are retained until a
// separate, explicitly authorized retention operation is implemented.
type HomeAssistantArchive struct {
	client *s3.Client
	bucket string
}
type ArchiveReceipt struct {
	ObjectKey string `json:"object_key"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
}

func NewHomeAssistantArchive(ctx context.Context, c Credentials) (*HomeAssistantArchive, error) {
	c = normalizeCredentials(c)
	if err := validateCredentials(c); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(c.Endpoint, "https://") {
		return nil, errors.New("backup archive requires an HTTPS object target")
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, "")), config.WithRegion("auto"))
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.Endpoint); o.UsePathStyle = true })
	return &HomeAssistantArchive{client: client, bucket: c.Bucket}, nil
}

// Put downloads to a bounded temporary file to hash before conditional create.
// The bytes are already native HA encrypted backup bytes; the recovery key is
// never included in the object or its metadata. A lost PUT receipt is reconciled
// by reading the exact object and comparing its content hash, never replacing it.
func (a *HomeAssistantArchive) Put(ctx context.Context, operationID string, encrypted io.Reader) (ArchiveReceipt, error) {
	if operationID == "" || encrypted == nil {
		return ArchiveReceipt{}, errors.New("archive operation and encrypted stream required")
	}
	name := sha256.Sum256([]byte(operationID))
	key := "home-assistant/migrations/" + hex.EncodeToString(name[:]) + ".tar"
	f, err := os.CreateTemp("", "ha-encrypted-archive-*")
	if err != nil {
		return ArchiveReceipt{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(encrypted, 32*1024*1024*1024+1))
	if err != nil {
		return ArchiveReceipt{}, err
	}
	if n == 0 || n > 32*1024*1024*1024 {
		return ArchiveReceipt{}, errors.New("encrypted archive is empty or exceeds 32 GiB")
	}
	receipt := ArchiveReceipt{ObjectKey: key, SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: n}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return ArchiveReceipt{}, err
	}
	_, putErr := a.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(a.bucket), Key: aws.String(key), Body: f, ContentLength: aws.Int64(n), IfNoneMatch: aws.String("*"), Metadata: map[string]string{"sha256": receipt.SHA256}})
	if err := a.Verify(ctx, receipt); err != nil {
		if putErr != nil {
			return ArchiveReceipt{}, errors.New("archive write unconfirmed; retained operation must be reconciled")
		}
		return ArchiveReceipt{}, err
	}
	return receipt, nil
}
func (a *HomeAssistantArchive) Open(ctx context.Context, receipt ArchiveReceipt) (io.ReadCloser, error) {
	if !strings.HasPrefix(receipt.ObjectKey, "home-assistant/migrations/") || strings.Contains(receipt.ObjectKey, "..") || len(receipt.SHA256) != 64 || receipt.Bytes <= 0 {
		return nil, errors.New("invalid archive receipt")
	}
	out, err := a.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(a.bucket), Key: aws.String(receipt.ObjectKey)})
	if err != nil {
		return nil, errors.New("retained archive unavailable")
	}
	return out.Body, nil
}
func (a *HomeAssistantArchive) Verify(ctx context.Context, receipt ArchiveReceipt) error {
	r, err := a.Open(ctx, receipt)
	if err != nil {
		return err
	}
	defer r.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, receipt.Bytes+1))
	if err != nil {
		return err
	}
	if n != receipt.Bytes || hex.EncodeToString(h.Sum(nil)) != receipt.SHA256 {
		return errors.New("retained archive content does not match source digest")
	}
	return nil
}
