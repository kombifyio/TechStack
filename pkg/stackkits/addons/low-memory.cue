// low-memory addon - Optimizes for nodes with limited RAM
// Activated when nodes have < 4GB RAM

package addons

#LowMemory: {
	metadata: {
		name:        "low-memory"
		displayName: "Low Memory Mode"
		description: "Excludes heavy services, optimized resource limits for constrained environments"
		version:     "1.0.0"
		priority:    80
	}

	// Memory thresholds
	thresholds: {
		// Define what constitutes "low memory"
		maxNodeMemoryMB: 4096 // 4GB
		minNodeMemoryMB: 1024 // 1GB minimum

		// Warning thresholds
		warningMemoryMB: 2048 // Warn if only 2GB available
	}

	// Services to exclude in low-memory environments
	excludedServices: [
		"elasticsearch",
		"victoriametrics", // Optional retention backend
		"grafana",         // Optional monitoring UI
		"minio",           // S3 storage
	]

	// Lightweight alternatives
	alternatives: {
		victoriametrics: "none" // Keep collector-only monitoring
		grafana:         "none" // Skip monitoring UI
		minio:           "local-storage"
	}

	// Resource limits for low-memory mode
	resourceLimits: {
		// Default container memory limits
		defaultMemoryLimitMB:   256
		maxMemoryLimitMB:       512
		defaultMemoryRequestMB: 64

		// Service-specific overrides
		services: {
			traefik: {
				memoryLimitMB:   128
				memoryRequestMB: 32
			}
			redis: {
				memoryLimitMB:   128
				memoryRequestMB: 32
			}
			postgres: {
				memoryLimitMB:   256
				memoryRequestMB: 64
			}
		}
	}

	// Swap configuration for low-memory systems
	swap: {
		enabled:    bool | *true
		sizeMB:     int | *1024 // 1GB swap
		swappiness: int | *10   // Low swappiness
		vfsCache:   int | *50   // Reduce VFS cache pressure
	}

	// Docker/Podman settings for low memory
	containerRuntime: {
		// Limit concurrent containers
		maxConcurrentPulls: 1
		// Aggressive cleanup
		pruneOnStartup: true
		// Logging limits
		logMaxSize:  "10m"
		logMaxFiles: 2
	}

	// System optimizations
	system: {
		// Kernel parameters for low memory
		vm: {
			overcommitMemory:     int | *1  // Allow overcommit
			overcommitRatio:      int | *50 // 50% overcommit
			minFreeKbytes:        int | *32768
			dirtyRatio:           int | *10
			dirtyBackgroundRatio: int | *5
		}
	}
}
