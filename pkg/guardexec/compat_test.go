package guardexec

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// The control plane sends a command as encoding/json of the generated struct;
// the executor decodes the very same bytes with protojson. The two encodings
// must agree, or every admitted command would be refused at the boundary.
func TestControlPlaneCommandJSONIsExecutableByTheExecutor(t *testing.T) {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	command := &agentpb.StackKitCommand{
		CommandId: "cmd-compat", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY,
		WorkingDirectory: "/srv/stack", SpecPath: "stack.yaml", TimeoutSeconds: 600, OwnerApproved: true,
		CandidateSpecJson: []byte(`{"name":"demo"}`), ExpectedPlanHash: "sha256:" + digest,
		Release: &agentpb.StackKitReleasePin{Version: "v0.16.0", PlatformOs: "linux", PlatformArch: "amd64", ArchiveSha256: digest, ReleaseIndexSha256: digest},
	}
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &agentpb.StackKitCommand{}
	if err := (protojson.UnmarshalOptions{}).Unmarshal(raw, decoded); err != nil {
		t.Fatalf("the executor cannot decode what the control plane sends: %v", err)
	}
	if !proto.Equal(command, decoded) {
		t.Fatalf("the executor decoded a different command:\n%v\n%v", command, decoded)
	}
}
