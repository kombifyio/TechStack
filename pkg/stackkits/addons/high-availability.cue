// high-availability addon - Configures HA patterns for multi-node deployments
// Activated when 3+ nodes with HA configuration

package addons

#HighAvailability: {
	metadata: {
		name:        "high-availability"
		displayName: "High Availability"
		description: "Service replicas, leader election, failover configuration"
		version:     "1.0.0"
		priority:    95
	}

	// HA requirements
	requirements: {
		minNodes: 3  // Minimum nodes for HA
		minMainNodes: 2  // At least 2 main nodes for quorum
	}

	// Replica configuration
	replicas: {
		// Default replica counts per service type
		defaults: {
			stateless: 2  // Web servers, API services
			stateful:  1  // Databases - use native replication
			cache:     1  // Redis - use sentinel for HA
		}

		// Service-specific overrides
		services: {
			traefik: {
				replicas: 2
				antiAffinity: true  // Spread across nodes
			}
			pocketbase: {
				replicas: 1  // SQLite doesn't support multi-master
				strategy: "primary-backup"
			}
		}
	}

	// Leader election configuration
	leaderElection: {
		enabled: true
		// Lock types
		lockType: "lease" | "endpoints" | "configmaps" | *"lease"
		// Lease duration
		leaseDuration: string | *"15s"
		renewDeadline: string | *"10s"
		retryPeriod:   string | *"2s"
	}

	// Health checks for HA
	healthChecks: {
		// Aggressive health checking for fast failover
		livenessProbe: {
			initialDelaySeconds: 10
			periodSeconds:       5
			failureThreshold:    3
			timeoutSeconds:      3
		}
		readinessProbe: {
			initialDelaySeconds: 5
			periodSeconds:       3
			failureThreshold:    2
			timeoutSeconds:      2
		}
	}

	// Service mesh / Load balancing
	loadBalancing: {
		// Algorithm for distributing traffic
		algorithm: "round-robin" | "least-connections" | "ip-hash" | *"round-robin"
		
		// Sticky sessions configuration
		stickySessions: {
			enabled:  bool | *false
			duration: string | *"1h"
		}

		// Circuit breaker
		circuitBreaker: {
			enabled:           bool | *true
			failureThreshold:  int | *5
			recoveryTimeout:   string | *"30s"
			halfOpenRequests:  int | *3
		}
	}

	// Database HA patterns
	database: {
		postgres: {
			// Use native streaming replication
			replicationMode: "streaming"
			synchronousCommit: "on"
			maxWalSenders: 3
			walLevel: "replica"
		}
		sqlite: {
			// SQLite doesn't support multi-master
			// Use Litestream for replication
			replication: {
				type: "litestream"
				destination: string | *"s3://backup-bucket/litestream"
			}
		}
	}

	// Failover configuration
	failover: {
		// Automatic failover
		automatic: bool | *true
		
		// Time before declaring node as failed
		nodeFailureTimeout: string | *"30s"
		
		// Graceful shutdown timeout
		gracePeriod: string | *"30s"
		
		// Drain timeout for load balancer
		drainTimeout: string | *"15s"
	}

	// Node anti-affinity rules
	antiAffinity: {
		// Required anti-affinity for critical services
		required: [
			"traefik",
			"postgres",
		]
		// Preferred anti-affinity (best-effort)
		preferred: [
			"redis",
			"pocketbase",
		]
	}

	// Backup configuration for HA
	backup: {
		// More frequent backups in HA mode
		frequency: string | *"1h"
		retention: int | *168  // 7 days of hourly backups
		
		// Cross-region backup
		crossRegion: {
			enabled: bool | *false
			region:  string | *""
		}
	}
}
