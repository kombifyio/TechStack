package homeassistant

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// BaselineSettings are local owner settings, never authority supplied by an
// artifact. Empty settings preserve the manufacturer's existing value.
type BaselineSettings struct {
	Language    string `json:"language,omitempty"`
	TimeZone    string `json:"time_zone,omitempty"`
	InternalURL string `json:"internal_url,omitempty"`
	ExternalURL string `json:"external_url,omitempty"`
}

type InstanceBinding struct {
	BindingRef, Origin, ManagementScope string
	Core                                *Client
	Supervisor                          *Supervisor
	Custody                             OperationCustody
	Archives                            ArchiveStore
	DependencyAssessmentRef             string
	Settings                            BaselineSettings
	ConfigureGranted                    bool
	RecoveryBackupOperationID           string
	mu                                  sync.Mutex
}

type BaselineObservation struct {
	Core      Observation       `json:"core"`
	Applied   map[string]string `json:"applied"`
	Preserved []string          `json:"preserved"`
	Gaps      []string          `json:"gaps"`
}

func (s BaselineSettings) values() (map[string]string, error) {
	if s.TimeZone != "" {
		if _, err := time.LoadLocation(s.TimeZone); err != nil {
			return nil, errors.New("invalid baseline timezone")
		}
	}
	for _, raw := range []string{s.InternalURL, s.ExternalURL} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return nil, errors.New("baseline access address must be an HTTP(S) origin")
		}
	}
	return map[string]string{"language": s.Language, "time_zone": s.TimeZone, "internal_url": s.InternalURL, "external_url": s.ExternalURL}, nil
}

// ReconcileBaseline never bootstraps native authentication. Existing/imported
// bindings only observe. Each field retains a durable write intent and last
// owned value, so recovery and later passes cannot overwrite user changes.
func (b *InstanceBinding) ReconcileBaseline(ctx context.Context) (BaselineObservation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := BaselineObservation{Applied: map[string]string{}}
	if b.Core == nil || b.BindingRef == "" {
		return out, ErrUnauthorized
	}
	var err error
	out.Core, err = b.Core.Observe(ctx)
	if err != nil {
		return out, err
	}
	if b.Origin != "new" || b.ManagementScope != "managed" {
		return out, nil
	}
	if !b.ConfigureGranted || b.Custody == nil || b.Supervisor == nil {
		return out, ErrUnauthorized
	}
	if _, err = b.Supervisor.Observe(ctx); err != nil {
		return out, err
	}
	values, err := b.Settings.values()
	if err != nil {
		return out, err
	}
	for _, field := range []string{"language", "time_zone", "internal_url", "external_url"} {
		wanted := values[field]
		if wanted == "" {
			continue
		}
		var current map[string]any
		if err = b.Core.json(ctx, http.MethodGet, "/api/config", nil, &current); err != nil {
			return out, err
		}
		key := "baseline/" + operationName(b.BindingRef) + "/" + field
		fresh, receipt, e := b.Custody.Claim(ctx, key)
		if e != nil {
			return out, e
		}
		if fresh {
			receipt = map[string]any{"before": current[field], "desired": wanted, "state": "intent"}
			if e = b.Custody.Save(ctx, key, receipt); e != nil {
				return out, e
			}
		}
		state := str(receipt, "state")
		if state == "" {
			return out, errors.New("baseline claim requires recovery review")
		}
		if state == "released" {
			out.Preserved = append(out.Preserved, field)
			continue
		}
		prior := receipt["desired"]
		// An interrupted first dispatch may still show its before-value. A
		// completed dispatch may only change the value it last owned.
		owned := current[field] == prior || (state == "intent" && current[field] == receipt["before"])
		if !owned {
			receipt["state"] = "released"
			if e = b.Custody.Save(ctx, key, receipt); e != nil {
				return out, e
			}
			out.Preserved = append(out.Preserved, field)
			continue
		}
		if current[field] != wanted {
			receipt = map[string]any{"before": current[field], "desired": wanted, "state": "intent"}
			if e = b.Custody.Save(ctx, key, receipt); e != nil {
				return out, e
			}
			if e = b.Core.websocket(ctx, map[string]any{"type": "config/core/update", field: wanted}, nil); e != nil {
				return out, e
			}
			if e = b.Core.json(ctx, http.MethodGet, "/api/config", nil, &current); e != nil {
				return out, e
			}
			if current[field] != wanted {
				return out, errors.New("native baseline setting was not observed")
			}
		}
		receipt["state"] = "applied"
		if e = b.Custody.Save(ctx, key, receipt); e != nil {
			return out, e
		}
		out.Applied[field] = wanted
	}
	// These need independent transport, restoration and connector evidence.
	// A loaded integration or successful Core read is not that proof.
	if !b.verifyTLSAccess(ctx) {
		out.Gaps = append(out.Gaps, "tls_proxy_verification")
	}
	if !b.verifyRetainedRecovery(ctx, out.Core.CoreVersion) {
		out.Gaps = append(out.Gaps, "off_instance_recovery_verification")
	}
	out.Gaps = append(out.Gaps, "companion_connector_authorization")
	return out, nil
}

// A working authenticated HTTPS request proves the owner's existing ingress;
// no proxy headers or HA YAML are widened merely to make a probe succeed.
func (b *InstanceBinding) verifyTLSAccess(ctx context.Context) bool {
	endpoint := b.Core.Endpoint()
	if b.Settings.ExternalURL != "" {
		endpoint = b.Settings.ExternalURL
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" {
		return false
	}
	client, err := NewLocalClient(ctx, endpoint, b.Core.token)
	if err != nil {
		return false
	}
	defer client.Close()
	_, err = client.Observe(ctx)
	return err == nil
}

func (b *InstanceBinding) verifyRetainedRecovery(ctx context.Context, version string) bool {
	if b.Custody == nil || b.Archives == nil || b.RecoveryBackupOperationID == "" {
		return false
	}
	_, receipt, err := b.Custody.Claim(ctx, b.backupKey(b.RecoveryBackupOperationID))
	if err != nil || receipt["complete"] != true {
		return false
	}
	archive, err := archiveReceipt(receipt)
	if err != nil || b.Archives.Verify(ctx, archive) != nil {
		return false
	}
	if _, err = b.Custody.ReadRecoveryKey(ctx, b.backupKey(b.RecoveryBackupOperationID)); err != nil {
		return false
	}
	_, proof, err := b.Custody.Claim(ctx, "verified-recovery/"+archive.SHA256)
	return err == nil && proof["verified"] == true && str(proof, "archive_sha256") == archive.SHA256 && str(proof, "core_version") == version
}
