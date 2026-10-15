package guardexec

import (
	"context"

	"github.com/kombifyio/techstack/pkg/drivehealth"
)

// DriveProbeBackend is an optional read-only executor capability, allowing old
// backends to refuse collection without broadening the host execution interface.
type DriveProbeBackend interface {
	ProbeDrive(context.Context, string, string, string) ([]byte, error)
}

type driveProbeResponse struct {
	Output []byte `json:"output"`
	Reason string `json:"reason,omitempty"`
}

// ProbeDrive executes the package's bounded fixed commands as the executor.
func (h *Host) ProbeDrive(ctx context.Context, kind, path, deviceType string) ([]byte, error) {
	return drivehealth.ExecProbe(ctx, kind, path, deviceType)
}

// ProbeDrive carries a fixed observation across the peer-authorized socket.
func (r *Remote) ProbeDrive(ctx context.Context, kind, path, deviceType string) ([]byte, error) {
	if err := drivehealth.ValidateProbe(kind, path, deviceType); err != nil {
		return nil, &drivehealth.ProbeError{Reason: "invalid_device"}
	}
	var response driveProbeResponse
	if err := r.Client.CallInto(ctx, OpProbeDrive, ProbeDriveParams{Kind: kind, DevicePath: path, DeviceType: deviceType}, &response); err != nil {
		return nil, &drivehealth.ProbeError{Reason: "executor_unavailable"}
	}
	if response.Reason != "" {
		return response.Output, &drivehealth.ProbeError{Reason: response.Reason}
	}
	return response.Output, nil
}
