package homeassistant

import (
	"context"
	"errors"
)

// createNativeArchive journals native dispatch and verifies encrypted off-instance bytes.
func createNativeArchive(ctx context.Context, key string, s NativeBackup, core *Client, custody OperationCustody, archives ArchiveStore, dependencyRef string) (map[string]any, error) {
	fresh, receipt, err := custody.Claim(ctx, key)
	if err != nil {
		return nil, err
	}
	if receipt["complete"] == true {
		archive, err := archiveReceipt(receipt)
		if err != nil {
			return nil, err
		}
		if err = archives.Verify(ctx, archive); err != nil {
			return nil, err
		}
		if _, err = custody.ReadRecoveryKey(ctx, key); err != nil {
			return nil, err
		}
		return receipt, nil
	}
	var password string
	if fresh {
		password, err = custody.RecoveryKey(ctx, key)
	} else {
		password, err = custody.ReadRecoveryKey(ctx, key)
	}
	if err != nil {
		return nil, err
	}
	name := operationName(key)
	slug := str(receipt, "slug")
	job := str(receipt, "job_id")
	if fresh {
		out, submitErr := s.SubmitFullBackup(ctx, name, password)
		if submitErr != nil {
			if errors.Is(submitErr, ErrUnavailable) {
				return pending(), nil
			}
			return nil, submitErr
		}
		receipt = map[string]any{"slug": out.Slug, "job_id": out.JobID}
		if err = custody.Save(ctx, key, receipt); err != nil {
			return nil, err
		}
		slug = out.Slug
		job = out.JobID
	}
	if job != "" {
		done, err := s.ObserveJob(ctx, job)
		if err != nil {
			return nil, err
		}
		if !done {
			return pending(), nil
		}
	}
	if slug == "" {
		found, err := s.FindBackup(ctx, name)
		if err != nil {
			return nil, err
		}
		if found == nil {
			return pending(), nil
		}
		slug = found.Slug
	}
	info, err := s.BackupInfo(ctx, slug)
	if err != nil {
		return nil, err
	}
	platform, err := s.Observe(ctx)
	if err != nil {
		return nil, err
	}
	entityIDs, err := core.EntityIDs(ctx)
	if err != nil {
		return nil, err
	}
	reader, err := s.DownloadFullBackup(ctx, slug)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	archive, err := archives.Put(ctx, key, reader)
	if err != nil {
		return nil, err
	}
	out := proof("native_full_backup", "encrypted", "off_instance", "immutable", "integrity_verified", "key_separate_custody", "configuration_inventory_retained", "versions_retained", "external_dependencies_accounted")
	out["archive_ref"] = archive.ObjectKey
	out["archive_sha256"] = archive.SHA256
	out["archive_bytes"] = archive.Bytes
	out["recovery_key_ref"] = key
	out["slug"] = slug
	out["backup_info"] = info
	out["platform_observation"] = platform
	out["entity_ids"] = entityIDs
	out["dependency_assessment_ref"] = dependencyRef
	out["complete"] = true
	if err = custody.Save(ctx, key, out); err != nil {
		return nil, err
	}
	return out, nil
}
