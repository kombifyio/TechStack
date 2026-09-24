package stackkitcommand

import (
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// JobID recovers the parent of a lifecycle command named job-operation[-attempt].
// It removes one recognized operation only; unrelated direct command IDs are
// retained, and a parent's own operation-like suffix must not be stripped.
func JobID(commandID string) string {
	original := strings.TrimSpace(commandID)
	value := original
	if separator := strings.LastIndexByte(value, '-'); separator >= 0 {
		base, ordinal := value[:separator], value[separator+1:]
		if attempt, err := strconv.ParseUint(ordinal, 10, 32); err == nil && attempt > 0 && strconv.FormatUint(attempt, 10) == ordinal {
			value = base
		}
	}
	for number, name := range agentpb.StackKitOperation_name {
		if number == 0 {
			continue
		}
		suffix := "-" + strings.ToLower(strings.TrimPrefix(name, "STACKKIT_OPERATION_"))
		if parent, ok := strings.CutSuffix(value, suffix); ok && parent != "" {
			return parent
		}
	}
	return original
}
