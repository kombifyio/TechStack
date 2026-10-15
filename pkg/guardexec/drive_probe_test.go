package guardexec

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/kombifyio/techstack/pkg/drivehealth"
)

type driveProbeTestBackend struct {
	fakeBackend
	called bool
	reason string
}

func (b *driveProbeTestBackend) ProbeDrive(context.Context, string, string, string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.called = true
	if b.reason != "" {
		return []byte(`{"smart_status":{"passed":false}}`), &drivehealth.ProbeError{Reason: b.reason}
	}
	return []byte(`{"smart_status":{"passed":true}}`), nil
}

// The actual authorized socket refuses arbitrary argv/paths and preserves
// successful versus partially inaccessible observations across its codecs.
func TestPrivilegedDriveProbePreservesCollectionBoundary(t *testing.T) {
	backend := &driveProbeTestBackend{}
	client := serveTest(t, &Server{Backend: backend, AllowPeer: func(uid uint32) bool { return uid == uint32(os.Getuid()) }})
	for _, params := range []ProbeDriveParams{
		{Kind: "format", DevicePath: "/dev/nvme0n1"},
		{Kind: drivehealth.ProbeSMART, DevicePath: "/etc/shadow"},
		{Kind: drivehealth.ProbeSMART, DevicePath: "/dev/sda", DeviceType: "sat,--set=all"},
		{Kind: drivehealth.ProbeScan, DevicePath: "/dev/sda"},
	} {
		if _, err := client.Call(t.Context(), OpProbeDrive, params, nil); !IsCode(err, CodeNotAllowed) {
			t.Fatalf("unsafe probe admitted: %+v err=%v", params, err)
		}
	}
	backend.mu.Lock()
	called := backend.called
	backend.mu.Unlock()
	if called {
		t.Fatal("refused probe reached privileged execution")
	}
	remote := &Remote{Client: client}
	raw, err := remote.ProbeDrive(t.Context(), drivehealth.ProbeSMART, "/dev/sda", "ata")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		SMART struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || !result.SMART.Passed {
		t.Fatalf("successful observation corrupted: %s %v", raw, err)
	}
	backend.mu.Lock()
	backend.reason = "permission_denied"
	backend.mu.Unlock()
	raw, err = remote.ProbeDrive(t.Context(), drivehealth.ProbeSMART, "/dev/sda", "ata")
	if drivehealth.ErrorReason(err) != "permission_denied" {
		t.Fatalf("collection reason lost: %v", err)
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.SMART.Passed {
		t.Fatalf("partial hardware evidence discarded: %s %v", raw, err)
	}
}
