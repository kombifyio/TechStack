package discovery

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
)

var maxIntAsUint64 = uint64(^uint(0) >> 1)

// estimateIPv4HostCount returns the number of usable hosts for an IPv4 CIDR.
func estimateIPv4HostCount(cidr string) (int, error) {
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0, fmt.Errorf("invalid CIDR: %w", err)
	}
	if ip.To4() == nil {
		return 0, fmt.Errorf("only IPv4 CIDRs are supported")
	}
	ones, bits := ipNet.Mask.Size()
	if bits != 32 {
		return 0, fmt.Errorf("only IPv4 CIDRs are supported")
	}
	hostBits := 32 - ones
	if hostBits <= 0 {
		return 1, nil
	}
	total := ipv4AddressCount(hostBits)
	if total >= 4 {
		return checkedHostCount(total - 2)
	}
	return checkedHostCount(total)
}

// sampleIPv4Hosts returns up to limit hosts sampled across the CIDR without enumerating all.
func sampleIPv4Hosts(cidr string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("invalid sample limit")
	}
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR: %w", err)
	}
	base := ip.Mask(ipNet.Mask).To4()
	if base == nil {
		return nil, fmt.Errorf("only IPv4 CIDRs are supported")
	}

	ones, bits := ipNet.Mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("only IPv4 CIDRs are supported")
	}
	hostBits := 32 - ones
	totalAddrs := ipv4AddressCount(hostBits)

	start := ipv4ToUint32(base)
	first := start
	count := totalAddrs
	if totalAddrs >= 4 {
		first = start + 1
		count = totalAddrs - 2
	}
	if count == 0 {
		return []string{}, nil
	}

	limit64 := uint64(limit)
	if limit64 > count {
		clamped, err := checkedHostCount(count)
		if err != nil {
			return nil, err
		}
		limit = clamped
		limit64 = count
	}
	step := count / limit64
	if step < 1 {
		step = 1
	}

	res := make([]string, 0, limit)
	for i := uint64(0); i < count && len(res) < limit; i += step {
		res = append(res, uint32ToIPv4String(first+uint32(i)))
	}
	return res, nil
}

func checkedHostCount(count uint64) (int, error) {
	if count > maxIntAsUint64 {
		return 0, fmt.Errorf("CIDR host count %d exceeds %d-bit platform limit", count, strconv.IntSize)
	}
	// #nosec G115 -- count is range-checked against max int for this platform above.
	return int(count), nil
}

func ipv4AddressCount(hostBits int) uint64 {
	if hostBits <= 0 {
		return 1
	}
	if hostBits >= 32 {
		return 1 << 32
	}
	return 1 << hostBits
}

func ipv4ToUint32(ip net.IP) uint32 {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0
	}
	return binary.BigEndian.Uint32(ip4)
}

func uint32ToIPv4String(v uint32) string {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return net.IP(b).String()
}

// expandSubnet returns all host IPs in a subnet.
func expandSubnet(cidr string) ([]string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR: %w", err)
	}

	var hosts []string
	for ip := ipNet.IP.Mask(ipNet.Mask); ipNet.Contains(ip); incrementIP(ip) {
		hosts = append(hosts, ip.String())
	}

	if len(hosts) > 2 {
		hosts = hosts[1 : len(hosts)-1]
	}

	return hosts, nil
}

// incrementIP increments an IP address by 1.
func incrementIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}
