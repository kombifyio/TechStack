package discovery

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Prober performs deep discovery via SSH.
type Prober struct {
	timeout  time.Duration
	hostKeys *HostKeyStore
}

// HostKeyStore provides TOFU (Trust On First Use) host key management.
// On first connection, the host key is stored. On subsequent connections,
// it's verified against the stored key.
type HostKeyStore struct {
	storePath string
	keys      map[string]string // host:port -> base64 encoded key
	mu        sync.RWMutex
}

// NewHostKeyStore creates a new host key store.
func NewHostKeyStore(storePath string) *HostKeyStore {
	if storePath == "" {
		storePath = filepath.Join("data", "known_hosts")
	}
	store := &HostKeyStore{
		storePath: storePath,
		keys:      make(map[string]string),
	}
	store.load()
	return store
}

// load reads stored host keys from disk.
func (s *HostKeyStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.storePath)
	if err != nil {
		return // File doesn't exist yet - that's fine
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			s.keys[parts[0]] = parts[1]
		}
	}
}

// save writes host keys to disk.
func (s *HostKeyStore) save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Ensure directory exists
	dir := filepath.Dir(s.storePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create host key directory: %w", err)
	}

	var lines []string
	lines = append(lines, "# kombifyTechstack known hosts (TOFU - Trust On First Use)")
	for host, key := range s.keys {
		lines = append(lines, fmt.Sprintf("%s %s", host, key))
	}

	return os.WriteFile(s.storePath, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

// GetCallback returns an SSH host key callback with TOFU behavior.
// Returns nil on first use (trusts the key) or error if key changed.
func (s *HostKeyStore) GetCallback(host string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		keyStr := base64.StdEncoding.EncodeToString(key.Marshal())

		s.mu.Lock()
		defer s.mu.Unlock()

		storedKey, exists := s.keys[host]
		if !exists {
			// TOFU: First connection, trust and store the key
			s.keys[host] = keyStr
			if err := s.save(); err != nil {
				// Log but don't fail - security > convenience
				fmt.Printf("Warning: failed to save host key: %v\n", err)
			}
			return nil
		}

		// Verify the key matches
		if storedKey != keyStr {
			return fmt.Errorf("SECURITY WARNING: host key for %s has changed! "+
				"This could indicate a man-in-the-middle attack. "+
				"If you trust this change, delete the old key from %s",
				host, s.storePath)
		}

		return nil
	}
}

// NewProber creates a new deep discovery prober.
func NewProber(timeout time.Duration) *Prober {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Prober{
		timeout:  timeout,
		hostKeys: NewHostKeyStore(""),
	}
}

// NewProberWithHostKeyStore creates a prober with a custom host key store.
func NewProberWithHostKeyStore(timeout time.Duration, store *HostKeyStore) *Prober {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if store == nil {
		store = NewHostKeyStore("")
	}
	return &Prober{
		timeout:  timeout,
		hostKeys: store,
	}
}

// Probe performs deep discovery on a device via SSH.
func (p *Prober) Probe(ctx context.Context, req *ProbeRequest) (*ProbeResult, error) {
	start := time.Now()
	result := &ProbeResult{
		DeviceID: GenerateDeviceID("", req.IP),
		IP:       req.IP,
		Status:   ProbeStatusProbing,
	}

	port := req.SSHPort
	if port == 0 {
		port = 22
	}

	timeout := p.timeout
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}

	// Build SSH config with TOFU host key verification
	hostAddr := fmt.Sprintf("%s:%d", req.IP, port)
	config := &ssh.ClientConfig{
		User:            req.SSHUser,
		HostKeyCallback: p.hostKeys.GetCallback(hostAddr),
		Timeout:         timeout,
	}

	// Add auth methods
	var authMethods []ssh.AuthMethod
	if req.SSHPrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(req.SSHPrivateKey))
		if err != nil {
			result.Status = ProbeStatusFailed
			result.Error = fmt.Sprintf("failed to parse private key: %v", err)
			result.Duration = time.Since(start)
			return result, err
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if req.SSHPassword != "" {
		authMethods = append(authMethods, ssh.Password(req.SSHPassword))
	}
	config.Auth = authMethods

	// Connect
	addr := fmt.Sprintf("%s:%d", req.IP, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		result.Status = ProbeStatusFailed
		result.Error = fmt.Sprintf("SSH connection failed: %v", err)
		result.Duration = time.Since(start)
		return result, err
	}
	defer client.Close()

	// Gather system information
	sysInfo := &SystemInfo{}

	// Get OS info
	if osInfo, err := p.runCommand(client, "cat /etc/os-release 2>/dev/null || cat /etc/system-release 2>/dev/null || uname -s"); err == nil {
		p.parseOSInfo(osInfo, sysInfo)
	}

	// Get kernel version
	if kernel, err := p.runCommand(client, "uname -r"); err == nil {
		sysInfo.Kernel = strings.TrimSpace(kernel)
	}

	// Get architecture
	if arch, err := p.runCommand(client, "uname -m"); err == nil {
		sysInfo.Arch = strings.TrimSpace(arch)
	}

	// Get CPU cores
	if cpuInfo, err := p.runCommand(client, "nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null"); err == nil {
		cores := strings.TrimSpace(cpuInfo)
		fmt.Sscanf(cores, "%d", &sysInfo.CPUCores)
	}

	// Get memory in MB
	if memInfo, err := p.runCommand(client, "grep MemTotal /proc/meminfo 2>/dev/null || sysctl -n hw.memsize 2>/dev/null"); err == nil {
		p.parseMemory(memInfo, sysInfo)
	}

	// Get disk space
	if diskInfo, err := p.runCommand(client, "df -BG / 2>/dev/null | tail -1 | awk '{print $4}'"); err == nil {
		diskStr := strings.TrimSpace(strings.TrimSuffix(diskInfo, "G"))
		fmt.Sscanf(diskStr, "%d", &sysInfo.DiskGB)
	}

	// Check Docker status
	if dockerVersion, err := p.runCommand(client, "docker version --format '{{.Server.Version}}' 2>/dev/null"); err == nil && strings.TrimSpace(dockerVersion) != "" {
		sysInfo.DockerStatus = "running"
	} else if _, err := p.runCommand(client, "which docker 2>/dev/null"); err == nil {
		sysInfo.DockerStatus = "installed"
	} else {
		sysInfo.DockerStatus = "not_installed"
	}

	result.System = sysInfo
	result.Status = ProbeStatusDone
	result.Duration = time.Since(start)

	return result, nil
}

// runCommand executes a command via SSH and returns the output.
func (p *Prober) runCommand(client *ssh.Client, cmd string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	output, err := session.CombinedOutput(cmd)
	if err != nil {
		return "", err
	}

	return string(output), nil
}

// parseOSInfo extracts OS information from /etc/os-release or similar.
func (p *Prober) parseOSInfo(output string, info *SystemInfo) {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), "\"")

		switch key {
		case "ID":
			info.Distribution = value
			info.OS = "linux"
		case "VERSION_ID":
			info.Version = value
		case "PRETTY_NAME":
			if info.Distribution == "" {
				info.Distribution = value
			}
		}
	}

	// Fallback for uname output
	if info.OS == "" {
		lower := strings.ToLower(output)
		if strings.Contains(lower, "linux") {
			info.OS = "linux"
		} else if strings.Contains(lower, "darwin") {
			info.OS = "darwin"
		}
	}
}

// parseMemory extracts memory from /proc/meminfo or sysctl output.
func (p *Prober) parseMemory(output string, info *SystemInfo) {
	output = strings.TrimSpace(output)

	// Linux: "MemTotal:       16384000 kB"
	if strings.HasPrefix(output, "MemTotal:") {
		var kb int
		_, err := fmt.Sscanf(output, "MemTotal: %d kB", &kb)
		if err == nil {
			info.MemoryMB = kb / 1024
			return
		}
	}

	// macOS: bytes from sysctl
	var bytes int64
	_, err := fmt.Sscanf(output, "%d", &bytes)
	if err == nil && bytes > 0 {
		info.MemoryMB = int(bytes / 1024 / 1024)
	}
}

// QuickProbe performs a lightweight SSH connectivity test without full system probing.
func (p *Prober) QuickProbe(ctx context.Context, ip string, port int, user, password, privateKey string) error {
	if port == 0 {
		port = 22
	}

	hostAddr := fmt.Sprintf("%s:%d", ip, port)
	config := &ssh.ClientConfig{
		User:            user,
		HostKeyCallback: p.hostKeys.GetCallback(hostAddr),
		Timeout:         5 * time.Second,
	}

	var authMethods []ssh.AuthMethod
	if privateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(privateKey))
		if err != nil {
			return fmt.Errorf("invalid private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if password != "" {
		authMethods = append(authMethods, ssh.Password(password))
	}
	config.Auth = authMethods

	addr := fmt.Sprintf("%s:%d", ip, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return fmt.Errorf("SSH connection failed: %w", err)
	}
	client.Close()

	return nil
}
