// multi-node addon - Enables multi-node deployment patterns
// Activated when more than one node is present

package addons

#MultiNode: {
	metadata: {
		name:        "multi-node"
		displayName: "Multi-Node"
		description: "Multi-node deployment patterns and service distribution"
		version:     "1.0.0"
		priority:    50
	}

	// Node roles and responsibilities
	nodeRoles: {
		main: {
			description: "Primary control plane and core services"
			services: [
				"traefik",      // Ingress
				"pocketbase",   // Database
			]
			canRunWorkloads: bool | *true
		}
		worker: {
			description: "Worker nodes for distributed workloads"
			services: [...string] | *[]  // Dynamically assigned
			canRunWorkloads: true
		}
		storage: {
			description: "Dedicated storage nodes"
			services: [
				"minio",
				"nfs-server",
			]
			canRunWorkloads: bool | *false
		}
	}

	// Service distribution strategy
	distribution: {
		// Strategy for placing services across nodes
		strategy: "spread" | "binpack" | "random" | *"spread"
		
		// Service placement constraints
		constraints: {
			// Services that must run on main node
			mainOnly: [
				"pocketbase",  // SQLite single-writer
			]
			
			// Services that should spread across all nodes
			spreadAll: [
				"node-exporter",
				"promtail",
			]
			
			// Services with node affinity
			affinity: {
				// Storage services
				storage: ["minio", "nfs-server"]
			}
		}
	}

	// Inter-node communication
	networking: {
		// Overlay network for multi-node
		overlay: {
			enabled: bool | *true
			type: "wireguard" | "vxlan" | "flannel" | *"wireguard"
			subnet: string | *"10.244.0.0/16"
		}
		
		// Service discovery
		discovery: {
			type: "dns" | "consul" | "mdns" | *"dns"
			// Internal domain for service discovery
			domain: string | *"svc.local"
		}
		
		// Node-to-node encryption
		encryption: {
			enabled: bool | *true
			type: "wireguard" | "ipsec" | *"wireguard"
		}
	}

	// Storage considerations for multi-node
	storage: {
		// Shared storage options
		shared: {
			enabled: bool | *false
			type: "nfs" | "ceph" | "minio" | *"nfs"
		}
		
		// Data replication
		replication: {
			enabled: bool | *false
			factor: int | *2  // Replicate to 2 nodes
		}
	}

	// Monitoring configuration for multi-node
	monitoring: {
		// Collect metrics from all nodes
		nodeMetrics: {
			enabled: bool | *true
			exporters: [
				"node-exporter",
				"cadvisor",
			]
		}
		
		// Centralized logging
		logging: {
			enabled: bool | *true
			collector: "promtail" | "fluentd" | "vector" | *"promtail"
			aggregator: "loki" | "elasticsearch" | *"loki"
		}
	}

	// Scaling configuration
	scaling: {
		// Horizontal pod autoscaling
		hpa: {
			enabled: bool | *false
			minReplicas: int | *1
			maxReplicas: int | *10
			targetCPUUtilization: int | *70
		}
		
		// Node autoscaling (for cloud providers)
		nodeAutoscaling: {
			enabled: bool | *false
			minNodes: int | *2
			maxNodes: int | *10
		}
	}

	// Health and readiness
	health: {
		// Node health checks
		nodeHealthCheck: {
			interval: string | *"30s"
			timeout:  string | *"10s"
			unhealthyThreshold: int | *3
		}
		
		// Service health checks
		serviceHealthCheck: {
			enabled: bool | *true
			interval: string | *"15s"
		}
	}
}
