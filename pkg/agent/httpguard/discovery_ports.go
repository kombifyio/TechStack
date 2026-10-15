package httpguard

import (
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/pkg/appcatalog"
)

// publishedPort is one container port Docker published on the host.
type publishedPort struct {
	bind          string // host bind address; empty or a wildcard when unspecified
	hostPort      int
	containerPort int
}

// parsePublishedPorts reads the TCP mappings out of `docker ps`'s Ports
// summary, e.g. "0.0.0.0:8080->80/tcp, :::8080->80/tcp, 5432/tcp". A port that
// is only exposed inside the container, a UDP port and a port range are not
// reported: only ports that are actually published are evidence of a reachable
// address. IPv4 and IPv6 twins of one mapping collapse into one.
func parsePublishedPorts(summary string) []publishedPort {
	seen := map[[2]int]bool{}
	var ports []publishedPort
	for _, entry := range strings.Split(summary, ",") {
		entry = strings.TrimSpace(entry)
		left, right, found := strings.Cut(entry, "->")
		if !found || !strings.HasSuffix(right, "/tcp") {
			continue
		}
		containerPort, err := strconv.Atoi(strings.TrimSuffix(right, "/tcp"))
		if err != nil {
			continue
		}
		bind, portText := "", left
		if colon := strings.LastIndex(left, ":"); colon >= 0 {
			bind, portText = strings.Trim(left[:colon], "[]"), left[colon+1:]
		}
		hostPort, err := strconv.Atoi(portText)
		if err != nil || hostPort < 1 || hostPort > 65535 || containerPort < 1 {
			continue
		}
		key := [2]int{hostPort, containerPort}
		if seen[key] {
			continue
		}
		seen[key] = true
		ports = append(ports, publishedPort{bind: bind, hostPort: hostPort, containerPort: containerPort})
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].hostPort < ports[j].hostPort })
	return ports
}

// webContainerPorts are container ports that conventionally speak HTTP. A
// published port outside this set and the catalog's default is reported
// without a scheme instead of guessing one.
var webContainerPorts = map[int]bool{
	80: true, 3000: true, 3001: true, 5000: true, 8000: true, 8080: true, 8081: true,
	8096: true, 8123: true, 8384: true, 8787: true, 8888: true, 9000: true, 9090: true,
}

// publishedAddress picks the one address worth showing for a discovered
// container: the mapping of the catalog's default web port when there is one,
// otherwise the lowest published port. The host part is the bind address when
// it is specific, else the host's own address; with neither it stays empty
// because a guessed host would be a false claim.
func publishedAddress(service Service, host Host) string {
	if len(service.publishedPorts) == 0 {
		return ""
	}
	entry := appcatalog.Match(appcatalog.Subject{
		Name: service.Name, Image: service.Image, Platform: service.PlatformType,
		ComposeService: service.RuntimeIdentity["service"],
	})
	chosen := service.publishedPorts[0]
	scheme := ""
	if webContainerPorts[chosen.containerPort] {
		scheme = "http"
	}
	if entry != nil && entry.WebPort > 0 {
		for _, port := range service.publishedPorts {
			if port.containerPort == entry.WebPort {
				chosen, scheme = port, "http"
				break
			}
		}
	}
	address := hostForPublishedPort(chosen.bind, host)
	if address == "" {
		return ""
	}
	endpoint := net.JoinHostPort(address, strconv.Itoa(chosen.hostPort))
	if scheme != "" {
		return scheme + "://" + endpoint
	}
	return endpoint
}

func hostForPublishedPort(bind string, host Host) string {
	if ip := net.ParseIP(bind); ip != nil && !ip.IsUnspecified() {
		return ip.String()
	}
	for _, candidate := range []string{host.PrivateIP, host.LocalIP, host.PublicIP} {
		if ip := net.ParseIP(strings.TrimSpace(candidate)); ip != nil {
			return ip.String()
		}
	}
	return ""
}
