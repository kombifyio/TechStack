//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
)

const maxPublishedEnrollmentBytes = 1 << 20

// enrollAsCustodyOwner pairs a privilege-split Guard again when root runs the
// enrollment, as the install command does after a detach. The redemption runs
// in a child as the daemon user, which reads the capability from stdin, so the
// identity, journal and enrollment leaves it writes stay the daemon's. Its
// enrollment lands in the state directory, because /etc/techstack is not
// writable for that user; root then publishes it at the configured path as its
// own file, the one `guard layout apply` adopts: the executor pins the new
// binding keys from it (they are derived per worker, and a pairing creates a
// new worker) and the file goes to the daemon. It reports false when the state
// directory is not a split Guard's.
func enrollAsCustodyOwner(ctx context.Context, flags *flag.FlagSet, stateDir, enrollmentFile string, resumable bool, token []byte, stdout io.Writer) (bool, error) {
	credential, name, ok := splitCustodyOwner(stateDir)
	if !ok {
		return false, nil
	}
	if err := absolutePaths(&stateDir, &enrollmentFile); err != nil {
		return true, err
	}
	args := append(custodyChildArgs("enroll", flags, "token-file", "state-dir", "enrollment-file"),
		"--state-dir="+stateDir, "--enrollment-file="+enrollmentFile, "--token-file=-")
	if err := runAsCustodyOwner(ctx, credential, args, token, nil, stdout); err != nil {
		return true, fmt.Errorf("the enrollment as %s did not complete: %w", name, err)
	}
	if resumable {
		return true, nil
	}
	if err := publishRootEnrollment(stateDir, enrollmentFile, credential.Uid); err != nil {
		return true, fmt.Errorf("publish the enrollment for the privilege-split Guard: %w", err)
	}
	// Root-owned cloud-init files the daemon user could not rewrite.
	hostidentity.RedactBootstrapResidue(string(token), hostidentity.CloudInitResiduePaths)
	_, _ = fmt.Fprintln(os.Stderr, "kombify Guard runs privilege-split: `techstack guard layout apply --activate` (the install command runs it) pins the new binding keys and restarts it")
	return true, nil
}

// publishRootEnrollment copies the enrollment the daemon user wrote into its
// state directory to the configured path as root's own owner-only file. The leaf
// is opened beneath the state directory and must be the daemon user's
// single-link regular file, so a planted link cannot make root copy anything
// else.
func publishRootEnrollment(stateDir, configured string, owner uint32) error {
	if configured == "" || hostidentity.LocateEnrollment(configured, stateDir) == configured {
		return nil // the configured path is already the newest enrollment
	}
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(hostidentity.EnrollmentFileName)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != owner || stat.Nlink != 1 || info.Size() > maxPublishedEnrollmentBytes {
		return fmt.Errorf("%s in %s is not the daemon's own regular file", hostidentity.EnrollmentFileName, stateDir)
	}
	file, err := root.OpenFile(hostidentity.EnrollmentFileName, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("%s changed while it was published", hostidentity.EnrollmentFileName)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxPublishedEnrollmentBytes+1))
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > maxPublishedEnrollmentBytes {
		return fmt.Errorf("%s is empty or exceeds its bound", hostidentity.EnrollmentFileName)
	}
	return hostidentity.WriteFileAtomic(configured, raw, 0o600)
}
