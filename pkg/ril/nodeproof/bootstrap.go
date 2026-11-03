package nodeproof

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
)

const PurposeKeysFile = "ril-purpose-keys.json"
const purposeKeysOrigin = "https://api.kombify.io/.well-known/kombify-ril-purpose-keys"

// RequireHostedProfile rechecks the root-owned current applied-profile
// authority. Callers must invoke it immediately at their actual network dial.
func RequireHostedProfile() error { return admitHostedProfile("/var/lib/techstack-guard-executor") }

// RefreshPurposeKeys is invoked by the privileged installer/refresh unit, never
// by a command. The immutable Gateway origin and TLS authenticate the public
// snapshot; redirects and response-selected URLs are not accepted.
func RefreshPurposeKeys(ctx context.Context, dir string) error {
	if os.Geteuid() != 0 {
		return errors.New("RIL key refresh requires root")
	}
	if err := RequireHostedProfile(); err != nil {
		return errors.New("RIL applied hosted profile unavailable")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	dial := (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return WithHostedProfileDial(ctx, network, address, dial)
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return refreshPurposeKeys(ctx, dir, client)
}
func refreshPurposeKeys(ctx context.Context, dir string, client *http.Client) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, purposeKeysOrigin, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("RIL key source unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Request == nil || response.Request.URL.String() != purposeKeysOrigin {
		return errors.New("RIL key source refused")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) == 0 || len(raw) > 65536 {
		return errors.New("RIL key source response invalid")
	}
	incoming, err := parseKeyFile(raw)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	if incoming.IssuedAt > now || incoming.ValidUntil <= now || incoming.ValidUntil-incoming.IssuedAt > 45 || incoming.ValidUntil <= incoming.IssuedAt {
		return errors.New("RIL key snapshot time invalid")
	}
	root, err := privatefs.Open(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	old, err := privatefs.ReadFile(root, PurposeKeysFile)
	if err == nil {
		previous, parseErr := parseKeyFile(old)
		if parseErr != nil {
			return errors.New("existing RIL key custody invalid")
		}
		if incoming.Generation < previous.Generation {
			return errors.New("RIL key generation rollback")
		}
		if incoming.Generation == previous.Generation {
			previous.IssuedAt, previous.ValidUntil = 0, 0
			incoming.IssuedAt, incoming.ValidUntil = 0, 0
			priorBytes, e0 := json.Marshal(previous)
			if e0 != nil {
				return e0
			}
			incomingBytes, e0 := json.Marshal(incoming)
			if e0 != nil {
				return e0
			}
			a, e1 := canonical(priorBytes)
			b, e2 := canonical(incomingBytes)
			if e1 != nil || e2 != nil || !bytes.Equal(a, b) {
				return errors.New("RIL key generation conflict")
			}
			// Same generation can refresh only the authenticated time window.
			return privatefs.Replace(root, PurposeKeysFile, raw, 0o600)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return privatefs.Replace(root, PurposeKeysFile, raw, 0o600)
}
