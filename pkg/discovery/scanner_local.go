package discovery

import (
	"fmt"
	"net"
)

// GetLocalInterfaces returns all local network interfaces with their IPs and subnets.
func (s *Scanner) GetLocalInterfaces() ([]NetworkInterface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to get interfaces: %w", err)
	}

	var result []NetworkInterface
	for _, iface := range ifaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		ni := NetworkInterface{
			Name: iface.Name,
			MAC:  iface.HardwareAddr.String(),
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			// Only IPv4 for now
			if ipNet.IP.To4() == nil {
				continue
			}

			ni.IPs = append(ni.IPs, ipNet.IP.String())
			ni.Subnets = append(ni.Subnets, ipNet.String())
		}

		if len(ni.IPs) > 0 {
			result = append(result, ni)
		}
	}

	return result, nil
}

// GetLocalSubnets returns all local subnets in CIDR notation.
func (s *Scanner) GetLocalSubnets() ([]string, error) {
	ifaces, err := s.GetLocalInterfaces()
	if err != nil {
		return nil, err
	}

	var subnets []string
	for _, iface := range ifaces {
		subnets = append(subnets, iface.Subnets...)
	}
	return subnets, nil
}

// GetLocalIPs returns all local IP addresses.
func (s *Scanner) GetLocalIPs() ([]string, error) {
	ifaces, err := s.GetLocalInterfaces()
	if err != nil {
		return nil, err
	}

	var ips []string
	for _, iface := range ifaces {
		ips = append(ips, iface.IPs...)
	}
	return ips, nil
}
