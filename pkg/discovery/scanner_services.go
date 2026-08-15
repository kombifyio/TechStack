package discovery

import "fmt"

// identifyService maps a port number to a service name.
func identifyService(port int) DiscoveredService {
	services := map[int]string{
		22:   "ssh",
		80:   "http",
		443:  "https",
		2375: "docker",
		2376: "docker-tls",
		5260: "techstack",
		5263: "techstack-grpc",
		6443: "kubernetes",
		8006: "proxmox",
		8080: "http-alt",
		9090: "prometheus",
		9100: "node-exporter",
	}

	name := services[port]
	if name == "" {
		name = fmt.Sprintf("unknown-%d", port)
	}

	return DiscoveredService{Name: name, Port: port}
}
