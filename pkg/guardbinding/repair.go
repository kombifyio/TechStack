package guardbinding

import "time"

// Binding pin reports a Guard sends with its posture. They name whether the
// root pin file holds a binding signing key. They carry no key material.
const (
	BindingPinPresent    = "present"
	BindingPinAbsent     = "absent"
	BindingPinUnreadable = "unreadable"
)

// BindingPinsIntroduced is the instant enrollment started pinning a binding
// signing key. A Guard paired earlier has no pin until it is paired again.
var BindingPinsIntroduced = time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)

// CodeBindingRepairRequired is the stable identifier the server read model
// uses when mutating commands stay refused until the server is paired again.
const CodeBindingRepairRequired = "guard_binding_repair_required"

// BindingRepairRequired reports whether this server must be paired again
// before mutating commands can run. A present pin clears it. An absent or
// unreadable pin requires it. With no report, only a Guard paired before
// BindingPinsIntroduced requires it; a zero paired-at does not.
func BindingRepairRequired(report string, pairedAt time.Time) bool {
	switch report {
	case BindingPinPresent:
		return false
	case BindingPinAbsent, BindingPinUnreadable:
		return true
	default:
		return !pairedAt.IsZero() && pairedAt.Before(BindingPinsIntroduced)
	}
}
