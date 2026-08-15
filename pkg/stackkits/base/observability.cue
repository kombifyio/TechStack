// Package base - Observability (logging, health checks, metrics)
package base

// #LoggingConfig defines logging settings
#LoggingConfig: {
    // Log driver
    driver: "journald" | "json-file" | "syslog" | *"journald"
    
    // Log format
    format: "json" | "text" | *"json"
    
    // Log retention period
    retention: =~"^[0-9]+[dhm]$" | *"7d"
    
    // Default log level
    level: "debug" | "info" | "warn" | "error" | *"info"
    
    // Maximum log file size (for json-file driver)
    maxSize: =~"^[0-9]+(k|m|g)$" | *"100m"
    
    // Maximum number of log files to keep
    maxFiles: int & >=1 | *3
    
    // Compress rotated logs
    compress: bool | *true
    
    // Remote logging (optional, for future integration)
    remote?: #RemoteLogging
}

#RemoteLogging: {
    // Enable remote logging
    enabled: bool | *false
    
    // Remote endpoint URL
    endpoint: string
    
    // Protocol
    protocol: "syslog" | "http" | "grpc" | *"syslog"
    
    // Authentication (if required)
    authRef?: =~"^secret://"
    
    // TLS verification
    tlsVerify: bool | *true
}

// #HealthCheck defines container health check configuration
#HealthCheck: {
    // Enable health checks
    enabled: bool | *true
    
    // Check interval
    interval: =~"^[0-9]+[sm]$" | *"30s"
    
    // Check timeout
    timeout: =~"^[0-9]+[sm]$" | *"10s"
    
    // Number of retries before marking unhealthy
    retries: int & >=1 | *3
    
    // Grace period for container startup
    startPeriod: =~"^[0-9]+[sm]$" | *"60s"
    
    // Check type
    type: "http" | "tcp" | "exec" | "grpc" | *"http"
    
    // HTTP-specific settings
    if type == "http" {
        http: #HTTPHealthCheck
    }
    
    // TCP-specific settings
    if type == "tcp" {
        tcp: #TCPHealthCheck
    }
    
    // Exec-specific settings
    if type == "exec" {
        exec: #ExecHealthCheck
    }
}

#HTTPHealthCheck: {
    // Health check path
    path: string | *"/health"
    
    // Expected status code
    status: int | *200
    
    // Expected response body (partial match)
    bodyContains?: string
    
    // Custom headers
    headers?: [string]: string
    
    // Use HTTPS
    https: bool | *false
}

#TCPHealthCheck: {
    // Port to check
    port: int & >0 & <=65535
}

#ExecHealthCheck: {
    // Command to execute
    command: [...string]
    
    // Expected exit code (0 = healthy)
    exitCode: int | *0
}

// #MetricsConfig defines OpenMetrics-compatible scrape settings.
#MetricsConfig: {
    // Enable metrics collection
    enabled: bool | *false
    
    // Metrics port
    port: int & >0 & <=65535 | *9100
    
    // Metrics path
    path: string | *"/metrics"
    
    // Scrape interval
    scrapeInterval: =~"^[0-9]+[sm]$" | *"15s"
    
    // Node exporter (host metrics)
    nodeExporter: #NodeExporterConfig
    
    // cAdvisor (container metrics)
    cadvisor?: #CAdvisorConfig
}

#NodeExporterConfig: {
    // Enable node exporter
    enabled: bool | *false
    
    // Port for node exporter
    port: int | *9100
    
    // Collectors to enable
    collectors: [...string] | *["cpu", "diskstats", "filesystem", "loadavg", "meminfo", "netdev", "time"]
    
    // Collectors to disable
    disabledCollectors: [...string] | *["wifi", "infiniband", "nfs"]
}

#CAdvisorConfig: {
    // Enable cAdvisor
    enabled: bool | *false
    
    // Port for cAdvisor
    port: int | *8081
    
    // Expose as internal service only
    exposure: "internal"
}

// #MonitoringConfig defines the Monitoring 2.0 compatibility lane.
// The target contract is OTLP via OpenTelemetry Collector; this lane keeps
// legacy agents and embedded query fallback working during migration.
#MonitoringConfig: {
    // Enable the monitoring subsystem
    enabled: bool | *true

    // Agent collector configuration
    agent: #MonitoringAgentConfig

    // TSDB storage configuration (Core + Standalone)
    storage: #MonitoringStorageConfig

    // Alerting configuration (evaluated on Core)
    alerting: #AlertingConfig
}

// #MonitoringAgentConfig controls the collectors running on each agent.
#MonitoringAgentConfig: {
    // Global metrics collection interval
    metricsInterval: =~"^[0-9]+[sm]$" | *"10s"

    // Which collectors to enable
    collectors: {
        // System metrics: CPU per-core, memory, swap, disk per-mount,
        // network per-interface, load average, host info
        system: bool | *true

        // Container metrics: Docker stats (CPU%, memory, network, block I/O)
        containers: bool | *true

        // Process monitoring: summary always on, patterns for specific processes
        processes: {
            enabled: bool | *true
            // Process name patterns to watch (regex). Empty = summary only.
            // Example: ["github-runner", "docker", "nginx", "postgres"]
            patterns: [...string] | *[]
        }
    }

    // Agent operating mode
    mode: "connected" | "standalone" | *"connected"
}

// #MonitoringStorageConfig defines TSDB storage parameters.
#MonitoringStorageConfig: {
    // Data retention period
    retention: =~"^[0-9]+[dwm]$" | *"15d"

    // TSDB data directory (relative to data_dir or absolute)
    dataDir: string | *"monitoring/tsdb"
}

// #AlertingConfig defines PromQL-compatible alerting rules evaluated against the active query backend.
#AlertingConfig: {
    // Enable alerting
    enabled: bool | *false

    // Evaluation interval for alert rules
    evalInterval: =~"^[0-9]+[sm]$" | *"30s"

    // Alert rules
    rules: [...#AlertRule]

    // Notification channels
    channels: [...#AlertChannel] | *[]
}

#AlertRule: {
    // Rule name (unique identifier)
    name: string

    // PromQL expression — fires when result is non-empty vector
    expr: string

    // Duration condition must hold before firing
    for: =~"^[0-9]+[smh]$" | *"5m"

    // Severity level
    severity: "info" | "warning" | "critical"

    // Alert message
    message: string

    // Additional labels for routing/grouping
    labels?: [string]: string
}

#AlertChannel: {
    // Channel type
    type: "telegram" | "webhook" | "email" | "slack" | "pagerduty"

    // Channel configuration (type-specific)
    // telegram: {bot_token_ref: "secret://...", chat_id: "..."}
    // webhook: {url: "https://..."}
    config: [string]: string
}

// #BackupConfig defines backup settings
#BackupConfig: {
    // Enable backups
    enabled: bool | *false
    
    // Backup schedule (cron format)
    schedule: =~"^[0-9* ,/-]+$" | *"0 3 * * *"
    
    // Retention period
    retention: =~"^[0-9]+[dwm]$" | *"7d"
    
    // Backup target
    target: #BackupTarget
    
    // What to backup
    include: [...string] | *["volumes", "configs"]
    
    // What to exclude
    exclude: [...string] | *[]
    
    // Pre-backup hooks
    preHooks?: [...string]
    
    // Post-backup hooks
    postHooks?: [...string]
    
    // Encryption
    encryption?: #BackupEncryption
}

#BackupTarget: {
    // Target type
    type: "local" | "s3" | "restic" | "sftp" | *"local"
    
    // Local path (for local target)
    if type == "local" {
        path: string | *"/var/backups/techstack"
    }
    
    // S3 configuration
    if type == "s3" {
        bucket:       string
        region:       string
        endpoint?:    string
        accessKeyRef: =~"^secret://"
        secretKeyRef: =~"^secret://"
    }
    
    // Restic repository
    if type == "restic" {
        repository:   string
        passwordRef:  =~"^secret://"
    }
}

#BackupEncryption: {
    // Enable encryption
    enabled: bool | *true
    
    // Encryption key reference
    keyRef: =~"^secret://"
    
    // Encryption algorithm
    algorithm: "aes-256-gcm" | "chacha20-poly1305" | *"aes-256-gcm"
}

// #UptimeCheck defines external uptime monitoring
#UptimeCheck: {
    // Enable uptime checks
    enabled: bool | *false
    
    // External monitoring service
    provider: "uptimerobot" | "pingdom" | "self-hosted" | *"self-hosted"
    
    // Check interval
    interval: =~"^[0-9]+[sm]$" | *"60s"
    
    // Endpoints to monitor
    endpoints: [...#UptimeEndpoint]
}

#UptimeEndpoint: {
    // Endpoint URL or address
    url: string
    
    // Check type
    type: "http" | "tcp" | "ping" | *"http"
    
    // Expected status (for HTTP)
    expectedStatus?: int
    
    // Keywords to check in response
    keywords?: [...string]
}
