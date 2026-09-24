package homeassistant

import (
	"context"
	"io"
)

type NativeBackup interface {
	Observe(context.Context) (PlatformObservation, error)
	BackupAllowed() bool
	SubmitFullBackup(context.Context, string, string) (Submission, error)
	ObserveJob(context.Context, string) (bool, error)
	FindBackup(context.Context, string) (*Backup, error)
	BackupInfo(context.Context, string) (Backup, error)
	DownloadFullBackup(context.Context, string) (io.ReadCloser, error)
}

func (b *MigrationBinding) sourceBackup() NativeBackup {
	if b.SourceBackup != nil {
		return b.SourceBackup
	}
	if b.SourceSupervisor != nil {
		return b.SourceSupervisor
	}
	return nil
}
