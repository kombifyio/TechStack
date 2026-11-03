//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kombifyio/techstack/internal/privatefs"
	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/ril/nodeproof"
)

// installHostedRILBootstrap derives local identity only from the authenticated,
// root-owned enrollment response; an unprivileged daemon cannot re-pin it.
func installHostedRILBootstrap(ctx context.Context, server, enrollmentPath string, result hostidentity.EnrollResult) error {
	if os.Geteuid() != 0 || !result.Accepted {
		return nil
	}
	u, err := url.Parse(server)
	if err != nil || result.Mode != hostidentity.ModeHostKey || u.Scheme != "https" || u.Host != "api.kombify.io" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (strings.TrimRight(u.Path, "/") != "" && strings.TrimRight(u.Path, "/") != "/v1/techstack") {
		return clearHostedRILBootstrap()
	}
	directory, err := os.OpenRoot(filepath.Dir(enrollmentPath))
	if err != nil {
		return err
	}
	defer directory.Close()
	leaf := filepath.Base(enrollmentPath)
	before, err := directory.Lstat(leaf)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("unsafe RIL enrollment custody")
	}
	f, err := directory.Open(leaf)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return fmt.Errorf("enrollment changed")
	}
	st, ok := opened.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || st.Nlink != 1 {
		return fmt.Errorf("enrollment is not root owned")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 {
		return fmt.Errorf("invalid enrollment")
	}
	enrollment, err := parseAgentEnrollment(raw)
	if err != nil {
		return err
	}
	choose := func(a, b string) (string, error) {
		if a != "" && b != "" && a != b {
			return "", fmt.Errorf("enrollment identity mismatch")
		}
		return firstCLIValue(a, b), nil
	}
	tenant, err := choose(enrollment.TenantID, enrollment.ChannelBootstrap.TenantID)
	if err != nil {
		return err
	}
	serverID, err := choose(enrollment.ServerID, enrollment.ChannelBootstrap.ServerID)
	if err != nil {
		return err
	}
	owner, err := choose(enrollment.OwnerID, enrollment.ChannelBootstrap.OwnerID)
	if err != nil {
		return err
	}
	stack, err := choose(enrollment.StackID, enrollment.ChannelBootstrap.StackID)
	if err != nil {
		return err
	}
	if tenant == "" || serverID == "" {
		return fmt.Errorf("hosted enrollment lacks RIL identity")
	}
	root, err := privatefs.Open("/var/lib/techstack-guard-executor")
	if err != nil {
		return err
	}
	defer root.Close()
	if err = nodeproof.PinLocalIdentity(root, nodeproof.Identity{TenantID: tenant, ServerID: serverID, OwnerID: owner, StackID: stack}); err != nil {
		return err
	}
	etc, err := privatefs.Open("/etc/techstack")
	if err != nil {
		return err
	}
	defer etc.Close()
	if err = privatefs.Replace(etc, "ril-hosted-enrollment", []byte("kombify.ril-hosted-enrollment.v1\n"), 0o600); err != nil {
		return err
	}
	return nodeproof.RefreshPurposeKeys(ctx, "/etc/techstack")
}

// A re-enrollment into a non-hosted authority removes only our own marker and
// invalidates the old RIL identity; a Full Local node starts no refresh traffic.
func clearHostedRILBootstrap() error {
	if _, err := os.Stat("/etc/techstack/ril-hosted-enrollment"); err == nil {
		root, err := privatefs.Open("/etc/techstack")
		if err != nil {
			return err
		}
		defer root.Close()
		if err = root.Remove("ril-hosted-enrollment"); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat("/var/lib/techstack-guard-executor/ril-local-identity.json"); err == nil {
		root, err := privatefs.Open("/var/lib/techstack-guard-executor")
		if err != nil {
			return err
		}
		defer root.Close()
		return nodeproof.PinLocalIdentity(root, nodeproof.Identity{})
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}
