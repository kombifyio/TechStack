// Package schema - Security policies for kombifyTechstack configurations
package schema

// #PortPolicy enforces port usage rules
#PortPolicy: {
	// Reserved ports that cannot be used by custom services
	_reservedPorts: [22, 80, 443, 5260, 5263, 5265]

	// Well-known service ports
	_wellKnownPorts: {
		"reverse-proxy": [80, 443]
		"database":      [5432, 3306, 27017]
		"monitoring":    [3000, 9090, 9100]
	}
}

// #NetworkPolicy defines network security constraints
#NetworkPolicy: {
	// Subnet validation - only private network ranges (RFC 1918)
	allowedCIDRs: [
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
	]
}
