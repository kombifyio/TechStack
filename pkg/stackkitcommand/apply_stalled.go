package stackkitcommand

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/secrets"
)

// ApplyStalledReason is the machine-readable reason a node reports when it ended
// a StackKits apply because the run stopped reporting progress, as opposed to
// the wall-clock deadline that ends a slow one. The node writes it into the
// failed command's error text and the controller reads it back from there, so
// both ends share this one definition.
const ApplyStalledReason = "stackkit_apply_stalled"

const (
	applyStalledPrefix = "StackKits apply stopped responding (" + ApplyStalledReason + " "
	// MaxApplyStallOwnerLen bounds the runtime-owner label carried in the reason.
	MaxApplyStallOwnerLen = 200
)

var applyStalledPattern = regexp.MustCompile(
	`^` + regexp.QuoteMeta(applyStalledPrefix) + `owner=(\S*) position=(\d{1,9}) total=(\d{1,9}) idle_seconds=(\d{1,9})\)`,
)

// ApplyStall names the last runtime owner a stalled apply had reported and how
// long it then stayed silent.
type ApplyStall struct {
	Owner    string
	Position int
	Total    int
	Idle     time.Duration
}

// maxApplyStallNumber is the most digits ParseApplyStall reads for a number.
const maxApplyStallNumber = 999_999_999

// Message is the failed command's error text. It carries only the closed
// reason, a redacted and sanitized owner label and numbers, never CLI error
// text. The node redacts a failed command's error text before it leaves, and
// the controller reads the reason back from the result, so a message that did
// not survive that redaction as a readable reason would lose the stalled
// reason and its retryability. Such a message names no owner instead.
func (stall ApplyStall) Message() string {
	message := stall.format(stall.Owner)
	if _, readable := ParseApplyStall(secrets.Redact(message)); !readable {
		message = stall.format("")
	}
	return message
}

func (stall ApplyStall) format(owner string) string {
	bounded := func(n int64) int64 { return min(max(n, 0), maxApplyStallNumber) }
	return fmt.Sprintf("%sowner=%s position=%d total=%d idle_seconds=%d)",
		applyStalledPrefix, SafeEventLabel(owner), bounded(int64(stall.Position)), bounded(int64(stall.Total)),
		bounded(int64(stall.Idle/time.Second)))
}

// ParseApplyStall reads the stall back from a failed command's stderr. Only the
// last line counts: the node appends its own error text after whatever the CLI
// wrote, so earlier output cannot pose as the reason.
func ParseApplyStall(stderr string) (ApplyStall, bool) {
	lines := strings.Split(strings.TrimRight(stderr, " \t\r\n"), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	match := applyStalledPattern.FindStringSubmatch(last)
	if match == nil {
		return ApplyStall{}, false
	}
	position, _ := strconv.Atoi(match[2])
	total, _ := strconv.Atoi(match[3])
	idle, _ := strconv.Atoi(match[4])
	return ApplyStall{Owner: match[1], Position: position, Total: total, Idle: time.Duration(idle) * time.Second}, true
}

// SafeEventLabel reduces an identity from a rollout event to a bounded token of
// path-safe characters, so it can travel in a log field or an error text
// without carrying free-form content. Reducing is not redaction: the value goes
// through the secret redaction first, so a secret is replaced before the
// reduction and the cut can leave a recognisable piece of it.
func SafeEventLabel(value string) string {
	var out strings.Builder
	for _, r := range secrets.Redact(strings.TrimSpace(value)) {
		if out.Len() >= MaxApplyStallOwnerLen {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r == '/', r == ':', r == '@', r == '+':
			out.WriteRune(r)
		default:
			out.WriteByte('_')
		}
	}
	if out.Len() == 0 {
		return "unknown"
	}
	return out.String()
}
