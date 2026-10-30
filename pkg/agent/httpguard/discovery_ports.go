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

// webContainerPorts are container ports that conventionally speak plain HTTP.
// A published port outside this set, the TLS set and the catalog's default is
// reported without a scheme instead of guessing one.
var webContainerPorts = map[int]bool{
	80: true, 3000: true, 3001: true, 3456: true, 4533: true, 5000: true, 5055: true, 6767: true,
	7878: true, 8000: true, 8080: true, 8081: true, 8096: true, 8123: true, 8181: true, 8384: true,
	8686: true, 8787: true, 8888: true, 8989: true, 9000: true, 9090: true, 9696: true, 13378: true,
}

// tlsContainerPorts are container ports that conventionally speak HTTPS: a
// browser must open them with https://, so prefixing http:// breaks the link.
var tlsContainerPorts = map[int]bool{443: true, 8443: true, 9443: true}

// webSchemeForContainerPort is the URL scheme a container port is known to
// speak, or empty when nothing proves a web UI behind it.
func webSchemeForContainerPort(containerPort int) string {
	switch {
	case tlsContainerPorts[containerPort]:
		return "https"
	case webContainerPorts[containerPort]:
		return "http"
	}
	return ""
}

// publishedAddress picks the one address worth showing for a discovered
// container when the HTTP probe proved none (a proven address is set before
// this runs and wins), in this order: the mapping of the catalog's default web
// port; else the lowest published mapping of a known web or TLS web container port
// (a database, DNS or MQTT port published beside the UI must not outrank it);
// else the lowest published port without a scheme, because nothing proves a
// web UI there. The scheme follows the container port, never the host port,
// and is https for a known TLS port. The host part is the bind address when
// it is specific, else the host's own address; with neither it stays empty
// because a guessed host would be a false claim.
func publishedAddress(service Service, host Host) string {
	if len(service.publishedPorts) == 0 {
		return ""
	}
	entry := appcatalog.Match(serviceSubject(service))
	chosen, scheme, found := service.publishedPorts[0], "", false
	for _, webPort := range entry.WebPorts() {
		for _, port := range service.publishedPorts {
			if port.containerPort == webPort {
				// The catalog proves a web UI here; only a known TLS port changes the scheme.
				chosen, scheme, found = port, "http", true
				if webSchemeForContainerPort(port.containerPort) == "https" {
					scheme = "https"
				}
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		for _, port := range service.publishedPorts {
			if candidate := webSchemeForContainerPort(port.containerPort); candidate != "" {
				chosen, scheme = port, candidate
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
