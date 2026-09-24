package devicereadiness

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ProbeScript is the observation the executor runs on the device. It is
// embedded rather than fetched: the whole point is a device that cannot reach
// anything, so a probe it has to download first would be useless.
//
//go:embed probe.sh
var ProbeScript string

// ProbeEnvironment names the endpoints the probe should test reachability
// against. They are passed in rather than compiled in so the probe stays
// independent of the deployment's origins, and they travel to the device by
// role so nothing about the report depends on a hostname.
type ProbeEnvironment struct {
	ControlPlaneHost  string
	PackageMirrorHost string
	ImageRegistryHost string
}

// Command renders the probe invocation for a shell on the device. The script
// itself is delivered on standard input by the executor; this is only the
// environment that parameterises it.
func (e ProbeEnvironment) Command() string {
	assignments := make([]string, 0, 3)
	if host := shellSafeHost(e.ControlPlaneHost); host != "" {
		assignments = append(assignments, "KOMBIFY_PROBE_CONTROL_PLANE="+host)
	}
	if host := shellSafeHost(e.PackageMirrorHost); host != "" {
		assignments = append(assignments, "KOMBIFY_PROBE_PACKAGE_MIRROR="+host)
	}
	if host := shellSafeHost(e.ImageRegistryHost); host != "" {
		assignments = append(assignments, "KOMBIFY_PROBE_IMAGE_REGISTRY="+host)
	}
	return strings.Join(append(assignments, "sh -s"), " ")
}

// shellSafeHost drops anything that is not a plain host or host:port. The
// executor builds a command line for a device it does not yet trust, so an
// endpoint is either obviously safe to interpolate or it is not sent at all.
func shellSafeHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == ':':
		default:
			return ""
		}
	}
	return value
}

// wireFacts is the probe's own output shape. It differs from Facts in exactly
// one place: the device reports the time it believes it is, and the executor
// turns that into a skew. A device with no time source cannot measure its own
// error, so it must not be asked to.
type wireFacts struct {
	Facts
	Clock struct {
		Observed     bool  `json:"observed"`
		DeviceEpoch  int64 `json:"deviceEpoch"`
		Synchronized *bool `json:"synchronized"`
	} `json:"clock"`
}

// DecodeFacts turns one probe run's output into facts, measuring the clock skew
// against observedAt, which is the executor's own clock at the moment the probe
// returned.
//
// A probe that produced nothing usable is an error rather than empty facts: the
// caller must be able to tell "the device is in a bad state" from "we failed to
// look".
func DecodeFacts(raw []byte, observedAt time.Time) (Facts, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return Facts{}, fmt.Errorf("device readiness probe returned no output")
	}
	// A login shell can print a banner or a warning before the payload, which
	// would otherwise make a perfectly good probe unparseable.
	if start := strings.Index(trimmed, "{"); start > 0 {
		trimmed = trimmed[start:]
	}

	var wire wireFacts
	if err := json.Unmarshal([]byte(trimmed), &wire); err != nil {
		return Facts{}, fmt.Errorf("device readiness probe returned unparsable output: %w", err)
	}

	facts := wire.Facts
	facts.ObservedAt = observedAt.UTC()
	facts.Clock = ClockFacts{
		Observed:     wire.Clock.Observed,
		Synchronized: wire.Clock.Synchronized,
	}
	if wire.Clock.Observed && wire.Clock.DeviceEpoch > 0 {
		skew := time.Unix(wire.Clock.DeviceEpoch, 0).Sub(observedAt).Seconds()
		facts.Clock.SkewSeconds = &skew
	}
	return facts, nil
}
