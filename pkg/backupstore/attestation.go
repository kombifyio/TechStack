package backupstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

const (
	attestationPayloadBytes = 32
	attestationKeyPrefix    = ".kombify/target-attestation/"
)

type targetObjectAPI interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

// TargetVerifier proves that the exact credentials in durable custody can
// write, read back, and delete an object in the exact managed bucket. It never
// logs or returns the credentials or sentinel content.
type TargetVerifier struct {
	client targetObjectAPI
	now    func() time.Time
	random io.Reader
}

func NewTargetVerifier(ctx context.Context, custody Credentials) (*TargetVerifier, error) {
	custody = normalizeCredentials(custody)
	if err := validateCredentials(custody); err != nil {
		return nil, err
	}
	awsConfig, err := config.LoadDefaultConfig(ctx,
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(custody.AccessKeyID, custody.SecretAccessKey, "")),
		config.WithRegion("auto"),
	)
	if err != nil {
		return nil, fmt.Errorf("load managed backup target client: %w", err)
	}
	client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(custody.Endpoint)
		options.UsePathStyle = true
	})
	return &TargetVerifier{client: client, now: time.Now, random: rand.Reader}, nil
}

// Verify completes custody evidence only after the target passes the full
// sentinel lifecycle. Existing attestation is rejected so callers cannot
// silently refresh or bless untraceable evidence.
func (v *TargetVerifier) Verify(ctx context.Context, custody Credentials, evidence CustodyEvidence) (verified CustodyEvidence, err error) {
	if v == nil || v.client == nil || v.now == nil || v.random == nil {
		return CustodyEvidence{}, errors.New("managed backup target verifier is not configured")
	}
	custody = normalizeCredentials(custody)
	if validateErr := validateCredentials(custody); validateErr != nil {
		return CustodyEvidence{}, validateErr
	}
	if len(evidence.BindingEvidence) == 0 || len(evidence.TargetEvidence) == 0 || len(evidence.AttestationEvidence) != 0 || evidence.ObservedAt.IsZero() {
		return CustodyEvidence{}, errors.New("managed backup custody evidence is incomplete or already attested")
	}

	payload := make([]byte, attestationPayloadBytes)
	if _, err := io.ReadFull(v.random, payload); err != nil {
		return CustodyEvidence{}, fmt.Errorf("generate managed backup target sentinel: %w", err)
	}
	key := attestationKeyPrefix + hex.EncodeToString(payload)
	objectCreated := false
	defer func() {
		if !objectCreated || err == nil {
			return
		}
		// Best-effort compensation for a failed verification. The primary error
		// remains authoritative; no credential or object content is exposed.
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelCleanup()
		_, _ = v.client.DeleteObject(cleanupContext, &s3.DeleteObjectInput{Bucket: aws.String(custody.Bucket), Key: aws.String(key)})
	}()

	if _, err = v.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(custody.Bucket), Key: aws.String(key), Body: bytes.NewReader(payload),
		ContentLength: aws.Int64(int64(len(payload))), ContentType: aws.String("application/octet-stream"),
	}); err != nil {
		return CustodyEvidence{}, fmt.Errorf("write managed backup target sentinel: %w", err)
	}
	objectCreated = true

	readResult, err := v.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(custody.Bucket), Key: aws.String(key)})
	if err != nil {
		return CustodyEvidence{}, fmt.Errorf("read managed backup target sentinel: %w", err)
	}
	if readResult == nil || readResult.Body == nil {
		return CustodyEvidence{}, errors.New("read managed backup target sentinel: target returned no content")
	}
	readPayload, readErr := io.ReadAll(io.LimitReader(readResult.Body, attestationPayloadBytes+1))
	closeErr := readResult.Body.Close()
	if readErr != nil {
		return CustodyEvidence{}, fmt.Errorf("read managed backup target sentinel content: %w", readErr)
	}
	if closeErr != nil {
		return CustodyEvidence{}, fmt.Errorf("close managed backup target sentinel content: %w", closeErr)
	}
	if !bytes.Equal(readPayload, payload) {
		return CustodyEvidence{}, errors.New("managed backup target sentinel content mismatch")
	}

	if _, err = v.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(custody.Bucket), Key: aws.String(key)}); err != nil {
		return CustodyEvidence{}, fmt.Errorf("delete managed backup target sentinel: %w", err)
	}
	objectCreated = false
	if _, headErr := v.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(custody.Bucket), Key: aws.String(key)}); !isObjectNotFound(headErr) {
		if headErr == nil {
			return CustodyEvidence{}, errors.New("managed backup target sentinel still exists after delete")
		}
		return CustodyEvidence{}, fmt.Errorf("verify managed backup target sentinel deletion: %w", headErr)
	}

	observedAt := v.now().UTC()
	payloadDigest := sha256.Sum256(payload)
	attestationDigest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%x\x00%s", custody.StackID, custody.Bucket, payloadDigest, observedAt.Format(time.RFC3339Nano))))
	evidence.AttestationEvidence = []byte(fmt.Sprintf("sha256:%x", attestationDigest))
	evidence.ObservedAt = observedAt
	return evidence, nil
}

func isObjectNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	switch apiError.ErrorCode() {
	case "NotFound", "NoSuchKey", "404":
		return true
	default:
		return false
	}
}
