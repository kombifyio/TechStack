// Package base provides the foundational CUE schemas for all StackKits.
// Every StackKit imports this package and inherits these defaults.
//
// Structure:
//   pkg/stackkits/base/
//   ├── doc.cue        # This file - package documentation
//   ├── system.cue     # System/host configuration defaults
//   ├── security.cue   # SSH, firewall, container security
//   ├── network.cue    # Network exposure, DNS, NTP
//   └── observability.cue # Logging, health checks, metrics
//
// Usage in StackKits:
//   import "github.com/kombihq/stackkits/base"
//
//   myStackKit: base.#BaseStackKit & {
//       // Override specific defaults here
//   }

package base
