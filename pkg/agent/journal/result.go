package journal

import (
	"encoding/json"
	"errors"

	"google.golang.org/protobuf/proto"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// ErrResultTooLarge means a result cannot be made to fit its bound even after
// dropping logs and events. The caller records an outcome_unknown instead of
// claiming either success or failure.
var ErrResultTooLarge = errors.New("result exceeds the journal bound")

const maxRetainedStderr = 64 << 10

// BoundResult returns the immutable, bounded form of a result: the exact JSON
// that is persisted, digested and delivered. Transient bearer material in
// SensitiveResultJson is never written to disk. When the result is too large,
// the stderr tail and then the rollout events are dropped and truncated is
// set; the typed command result is never cut.
func BoundResult(result *agentpb.StackKitResult, max int64) (payload []byte, truncated, sensitiveOmitted bool, err error) {
	bounded := proto.Clone(result).(*agentpb.StackKitResult)
	if len(bounded.SensitiveResultJson) != 0 {
		bounded.SensitiveResultJson = nil
		sensitiveOmitted = true
	}
	encode := func() ([]byte, error) { return json.Marshal(bounded) }
	payload, err = encode()
	if err != nil {
		return nil, false, sensitiveOmitted, err
	}
	if int64(len(payload)) <= max {
		return payload, false, sensitiveOmitted, nil
	}
	if len(bounded.Stderr) > maxRetainedStderr {
		bounded.Stderr = "<stderr truncated by kombify Guard journal>\n" + bounded.Stderr[len(bounded.Stderr)-maxRetainedStderr:]
		truncated = true
		if payload, err = encode(); err != nil {
			return nil, false, sensitiveOmitted, err
		}
		if int64(len(payload)) <= max {
			return payload, truncated, sensitiveOmitted, nil
		}
	}
	if len(bounded.EventsJsonl) != 0 {
		bounded.EventsJsonl = nil
		truncated = true
		if payload, err = encode(); err != nil {
			return nil, false, sensitiveOmitted, err
		}
		if int64(len(payload)) <= max {
			return payload, truncated, sensitiveOmitted, nil
		}
	}
	return nil, true, sensitiveOmitted, ErrResultTooLarge
}
