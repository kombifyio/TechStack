package discovery

// Scanner performs network discovery operations.
type Scanner struct {
	config *DiscoveryConfig
}

// NewScanner creates a new network scanner with the given configuration.
func NewScanner(config *DiscoveryConfig) *Scanner {
	if config == nil {
		config = DefaultConfig()
	}
	return &Scanner{config: config}
}
