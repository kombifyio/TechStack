//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
	"github.com/kombifyio/techstack/pkg/ril/hostpin"
	"github.com/kombifyio/techstack/pkg/ril/nodeproof"
	"golang.org/x/crypto/ssh"
)

// Existing enrollment is daemon-owned and supplies lookup hints only. The
// control plane returns the identity only after verifying the physical host
// key against its existing lease-bound pin over the fixed TLS origin.
func bootstrapExistingHostedRIL(ctx context.Context) error {
	if os.Geteuid() != 0 || nodeproof.RequireHostedProfile() != nil {
		return errors.New("RIL hosted profile unavailable")
	}
	raw, err := readSecretFile(defaultEnrollmentFile)
	if err != nil {
		return errors.New("RIL host enrollment hints unavailable")
	}
	enrollment, err := parseAgentEnrollment(raw)
	if err != nil {
		return errors.New("RIL host enrollment hints invalid")
	}
	tenant, server := firstCLIValue(enrollment.TenantID, enrollment.ChannelBootstrap.TenantID), firstCLIValue(enrollment.ServerID, enrollment.ChannelBootstrap.ServerID)
	if tenant == "" || server == "" || (enrollment.TenantID != "" && enrollment.ChannelBootstrap.TenantID != "" && enrollment.TenantID != enrollment.ChannelBootstrap.TenantID) ||
		(enrollment.ServerID != "" && enrollment.ChannelBootstrap.ServerID != "" && enrollment.ServerID != enrollment.ChannelBootstrap.ServerID) {
		return errors.New("RIL host enrollment hints invalid")
	}

	dir, err := os.OpenRoot("/etc/ssh")
	if err != nil {
		return errors.New("RIL host key unavailable")
	}
	defer dir.Close()
	const name = "ssh_host_ed25519_key"
	before, err := dir.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 {
		return errors.New("RIL host key unavailable")
	}
	f, err := dir.Open(name)
	if err != nil {
		return errors.New("RIL host key unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return errors.New("RIL host key changed")
	}
	stat, ok := opened.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Nlink != 1 {
		return errors.New("RIL host key unavailable")
	}
	key, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(key) == 0 || len(key) > 65536 {
		return errors.New("RIL host key unavailable")
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return errors.New("RIL host key unavailable")
	}
	request, err := hostpin.NewRequest(tenant, server, signer, time.Now())
	if err != nil {
		return errors.New("RIL host proof unavailable")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	const origin = "https://api.kombify.io" + hostpin.Path
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, origin, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	dial := (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return nodeproof.WithHostedProfileDial(ctx, network, address, dial)
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(httpRequest)
	if err != nil {
		return errors.New("RIL host authority unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Request == nil || response.Request.URL.String() != origin {
		return errors.New("RIL host authority denied")
	}
	responseBytes, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if err != nil || len(responseBytes) == 0 || len(responseBytes) > 8192 {
		return errors.New("RIL host authority response invalid")
	}
	var result hostpin.Response
	decoder := json.NewDecoder(bytes.NewReader(responseBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF || hostpin.ValidateResponse(request, result, time.Now()) != nil {
		return errors.New("RIL host authority response invalid")
	}
	if err := response.Body.Close(); err != nil {
		return errors.New("RIL host authority response unavailable")
	}
	if nodeproof.RequireHostedProfile() != nil {
		return errors.New("RIL hosted profile unavailable")
	}
	state, err := privatefs.Open("/var/lib/techstack-guard-executor")
	if err != nil {
		return err
	}
	defer state.Close()
	if err := nodeproof.PinLocalIdentity(state, nodeproof.Identity{TenantID: result.TenantID, OwnerID: result.OwnerID, ServerID: result.ServerID, StackID: result.StackID}); err != nil {
		return err
	}
	etc, err := privatefs.Open("/etc/techstack")
	if err != nil {
		return err
	}
	defer etc.Close()
	return privatefs.Replace(etc, "ril-hosted-enrollment", []byte("kombify.ril-hosted-enrollment.v1\n"), 0o600)
}
