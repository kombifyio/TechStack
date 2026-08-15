package discovery

import (
	"fmt"
	"time"
)

// generateScanID creates a unique scan identifier.
func generateScanID() string {
	return fmt.Sprintf("scan-%d", time.Now().UnixNano())
}
