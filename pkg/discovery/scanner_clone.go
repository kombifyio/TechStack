package discovery

// cloneScanResultLocked deep-copies a ScanResult for safe cross-goroutine progress callbacks.
// Callers must hold any locks protecting concurrent access to the input.
func cloneScanResultLocked(in *ScanResult) *ScanResult {
	if in == nil {
		return nil
	}
	out := *in
	if in.Errors != nil {
		out.Errors = append([]string(nil), in.Errors...)
	}
	if in.Devices != nil {
		out.Devices = make([]DiscoveredDevice, len(in.Devices))
		for i := range in.Devices {
			out.Devices[i] = cloneDevice(in.Devices[i])
		}
	}
	return &out
}

func cloneDevice(d DiscoveredDevice) DiscoveredDevice {
	out := d
	if d.Ports != nil {
		out.Ports = append([]int(nil), d.Ports...)
	}
	if d.Services != nil {
		out.Services = append([]DiscoveredService(nil), d.Services...)
	}
	if d.RolesSuggested != nil {
		out.RolesSuggested = append([]DeviceRole(nil), d.RolesSuggested...)
	}
	if d.Metadata != nil {
		out.Metadata = make(map[string]string, len(d.Metadata))
		for k, v := range d.Metadata {
			out.Metadata[k] = v
		}
	}
	if d.System != nil {
		sys := *d.System
		out.System = &sys
	}
	return out
}
