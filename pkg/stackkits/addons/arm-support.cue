// arm-support addon - Provides ARM64-specific configurations
// Activated when ARM architecture nodes are detected

package addons

#ARMSupport: {
	metadata: {
		name:        "arm-support"
		displayName: "ARM Support"
		description: "ARM64-compatible images and ARM-specific resource limits"
		version:     "1.0.0"
		priority:    90
	}

	// ARM-compatible container images
	images: {
		// Standard ARM64 images with multi-arch support
		nginx:           "nginx:alpine"                                // Multi-arch
		postgres:        "postgres:15-alpine"                          // Multi-arch
		redis:           "redis:7-alpine"                              // Multi-arch
		traefik:         "traefik:v3"                                  // Multi-arch
		otelCollector:   "otel/opentelemetry-collector-contrib:latest" // Multi-arch
		victoriaMetrics: "victoriametrics/victoria-metrics:latest"     // Multi-arch
		grafana:         "grafana/grafana:latest"                      // Multi-arch
		cadvisor:        "gcr.io/cadvisor/cadvisor:latest"             // Multi-arch

		// ARM-specific alternatives for x86-only images
		minio:         "minio/minio:latest" // Multi-arch
		elasticsearch: "elasticsearch:8"    // No ARM, requires alternative
	}

	// Resource adjustments for ARM (typically more efficient)
	resources: {
		// ARM chips often have better power efficiency
		// Adjust default resource requests accordingly
		memoryMultiplier: 0.9 // ARM often uses less memory
		cpuMultiplier:    1.0 // CPU performance varies by chip
	}

	// Build configurations for ARM
	build: {
		platform: "linux/arm64"
		// Cross-compilation settings if building from x86
		qemu: {
			enabled: bool | *false
			image:   string | *"tonistiigi/binfmt:latest"
		}
	}

	// Node labels for ARM nodes
	nodeLabels: {
		"kubernetes.io/arch":               "arm64"
		"node.kubernetes.io/instance-type": "arm"
	}

	// Constraints for service scheduling
	constraints: {
		// Services that don't support ARM
		unsupportedServices: [...string] | *[]

		// Services that require ARM-specific images
		armOnlyImages: [...string] | *[]
	}
}
