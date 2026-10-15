package backupstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type fakeTargetObjectAPI struct {
	calls       []string
	written     []byte
	readPayload []byte
	headErr     error
	deleteErr   error
	putErrs     []error
}

func (f *fakeTargetObjectAPI) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.calls = append(f.calls, "put:"+aws.ToString(input.Bucket)+":"+aws.ToString(input.Key))
	if len(f.putErrs) > 0 {
		err := f.putErrs[0]
		f.putErrs = f.putErrs[1:]
		return nil, err
	}
	f.written, _ = io.ReadAll(input.Body)
	return &s3.PutObjectOutput{}, nil
}
func (f *fakeTargetObjectAPI) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.calls = append(f.calls, "get:"+aws.ToString(input.Bucket)+":"+aws.ToString(input.Key))
	payload := f.readPayload
	if payload == nil {
		payload = f.written
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(payload))}, nil
}
func (f *fakeTargetObjectAPI) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.calls = append(f.calls, "delete:"+aws.ToString(input.Bucket)+":"+aws.ToString(input.Key))
	return &s3.DeleteObjectOutput{}, f.deleteErr
}
func (f *fakeTargetObjectAPI) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.calls = append(f.calls, "head:"+aws.ToString(input.Bucket)+":"+aws.ToString(input.Key))
	return nil, f.headErr
}

func TestTargetVerifierAttestsExactWriteReadDeleteLifecycle(t *testing.T) {
	client := &fakeTargetObjectAPI{headErr: &smithy.GenericAPIError{Code: "NotFound", Message: "gone"}}
	verifier := &TargetVerifier{client: client, now: func() time.Time { return time.Date(2026, 8, 6, 11, 0, 0, 0, time.UTC) }, random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, attestationPayloadBytes))}
	evidence := testCustodyEvidence()
	verified, err := verifier.Verify(context.Background(), testCredentials(), evidence)
	if err != nil {
		t.Fatal(err)
	}
	if string(verified.AttestationEvidence) == "" || verified.ObservedAt.Equal(evidence.ObservedAt) {
		t.Fatalf("verification did not add fresh attestation: %#v", verified)
	}
	if len(client.calls) != 4 || !strings.HasPrefix(client.calls[0], "put:") || !strings.HasPrefix(client.calls[1], "get:") || !strings.HasPrefix(client.calls[2], "delete:") || !strings.HasPrefix(client.calls[3], "head:") {
		t.Fatalf("unexpected target lifecycle: %v", client.calls)
	}
	for _, forbidden := range []string{"secret", "access-a", "repo-password", "acct.r2"} {
		if strings.Contains(string(verified.AttestationEvidence), forbidden) {
			t.Fatalf("attestation leaked %q", forbidden)
		}
	}
}

func TestTargetVerifierFailsOnContentMismatchAndCleansUp(t *testing.T) {
	client := &fakeTargetObjectAPI{readPayload: []byte("wrong")}
	verifier := &TargetVerifier{client: client, now: time.Now, random: bytes.NewReader(bytes.Repeat([]byte{0x1}, attestationPayloadBytes))}
	_, err := verifier.Verify(context.Background(), testCredentials(), testCustodyEvidence())
	if err == nil || !strings.Contains(err.Error(), "content mismatch") {
		t.Fatalf("expected mismatch, got %v", err)
	}
	if len(client.calls) != 3 || !strings.HasPrefix(client.calls[2], "delete:") {
		t.Fatalf("failed sentinel was not cleaned up: %v", client.calls)
	}
}

func TestTargetVerifierRequiresObservedDeletion(t *testing.T) {
	client := &fakeTargetObjectAPI{}
	verifier := &TargetVerifier{client: client, now: time.Now, random: bytes.NewReader(bytes.Repeat([]byte{0x1}, attestationPayloadBytes))}
	if _, err := verifier.Verify(context.Background(), testCredentials(), testCustodyEvidence()); err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("target that retained sentinel was accepted: %v", err)
	}
}

func TestTargetVerifierRejectsPriorAttestationAndUnknownHeadErrors(t *testing.T) {
	client := &fakeTargetObjectAPI{headErr: errors.New("network unavailable")}
	verifier := &TargetVerifier{client: client, now: time.Now, random: bytes.NewReader(bytes.Repeat([]byte{0x1}, attestationPayloadBytes))}
	evidence := testCustodyEvidence()
	evidence.AttestationEvidence = []byte("old")
	if _, err := verifier.Verify(context.Background(), testCredentials(), evidence); err == nil || len(client.calls) != 0 {
		t.Fatalf("prior attestation was accepted: %v calls=%v", err, client.calls)
	}
	evidence.AttestationEvidence = nil
	if _, err := verifier.Verify(context.Background(), testCredentials(), evidence); err == nil || !strings.Contains(err.Error(), "deletion") {
		t.Fatalf("unknown head error was accepted: %v", err)
	}
}

func testCustodyEvidence() CustodyEvidence {
	return CustodyEvidence{BindingEvidence: []byte("binding"), TargetEvidence: []byte("target"), ObservedAt: time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)}
}

// Regression (managed Cloud lane 2026-10-03): a just-created per-stack R2
// token answered 401 Unauthorized to the first sentinel write.
func TestTargetVerifierWaitsForANewTokenToBeAccepted(t *testing.T) {
	unauthorized := &smithy.GenericAPIError{Code: "Unauthorized", Message: "not yet valid"}
	client := &fakeTargetObjectAPI{putErrs: []error{unauthorized, unauthorized}, headErr: &smithy.GenericAPIError{Code: "NotFound", Message: "gone"}}
	var slept time.Duration
	verifier := &TargetVerifier{
		client: client, now: func() time.Time { return time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC) },
		random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, attestationPayloadBytes)),
		sleep:  func(_ context.Context, d time.Duration) error { slept += d; return nil },
	}
	verified, err := verifier.Verify(context.Background(), testCredentials(), testCustodyEvidence())
	if err != nil {
		t.Fatalf("a token that becomes valid within the budget must attest: %v", err)
	}
	if len(verified.AttestationEvidence) == 0 || slept == 0 || slept > credentialPropagationBudget {
		t.Fatalf("attestation=%q slept=%s", verified.AttestationEvidence, slept)
	}

	denied := &fakeTargetObjectAPI{putErrs: []error{&smithy.GenericAPIError{Code: "AccessDenied", Message: "no"}}}
	verifier.client, verifier.random, slept = denied, bytes.NewReader(bytes.Repeat([]byte{0x2a}, attestationPayloadBytes)), 0
	if _, err := verifier.Verify(context.Background(), testCredentials(), testCustodyEvidence()); err == nil || slept != 0 {
		t.Fatalf("403 must fail at once without waiting: err=%v slept=%s", err, slept)
	}
}
