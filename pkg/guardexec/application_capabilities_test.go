package guardexec

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"testing"

	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

func TestRemoteApplicationAdmissionFollowsExecutorCapabilities(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsupported", true: "supported"}[admitted], func(t *testing.T) {
			remote := &Remote{Client: &Client{Dial: func(context.Context) (net.Conn, error) {
				client, executor := net.Pipe()
				go func() {
					defer executor.Close()
					var request Request
					if json.NewDecoder(executor).Decode(&request) != nil || request.Op != OpStackKitCapabilities {
						return
					}
					capabilities := []string{stackkitcommand.HostMaintenanceCapability}
					if admitted {
						capabilities = append(capabilities, stackkitcommand.ApplicationAdoptionCapability, stackkitcommand.ApplicationConnectorCapability)
					}
					payload, _ := json.Marshal(map[string]any{"capabilities": capabilities})
					_ = json.NewEncoder(executor).Encode(Frame{Type: FrameResult, Payload: payload})
				}()
				return client, nil
			}}}
			if got := slices.Contains(remote.ApplicationCapabilities(), stackkitcommand.ApplicationAdoptionCapability); got != admitted {
				t.Fatalf("application admission = %v, executor admission = %v", got, admitted)
			}
			if got := slices.Contains(remote.ApplicationCapabilities(), stackkitcommand.ApplicationConnectorCapability); got != admitted {
				t.Fatalf("connector admission=%v, executor admission=%v", got, admitted)
			}
			if slices.Contains(remote.HostMaintenanceCapabilities(), stackkitcommand.ApplicationConnectorCapability) {
				t.Fatal("connector capability escaped typed application admission")
			}
			if !slices.Contains(remote.HostMaintenanceCapabilities(), stackkitcommand.HostMaintenanceCapability) {
				t.Fatal("application capability relay lost host maintenance admission")
			}
			if slices.Contains(remote.HostMaintenanceCapabilities(), stackkitcommand.ApplicationAdoptionCapability) {
				t.Fatal("application admission was duplicated into host maintenance")
			}
		})
	}
}
