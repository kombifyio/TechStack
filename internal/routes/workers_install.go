//nolint:goconst
package routes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/httpx"
)

// linuxInstallerArtifactSHA256 pins the authoritative repository install.sh
// after CRLF-to-LF normalization. The runtime image copies that exact artifact;
// serving any partial, stale, or locally substituted shell fragment fails
// closed until the contract digest is deliberately updated with the installer.
const linuxInstallerArtifactSHA256 = "d0cc7838b671dd37338c775c57bf6270ebe157a736f700ec186cee090457d273"

func (h workerRouteHandlers) installScript(e *httpx.Event) error {
	return serveLinuxInstallScript(e, h.installScriptPaths())
}

func serveLinuxInstallScript(e *httpx.Event, paths []string) error {
	content, found := firstValidLinuxInstaller(paths)
	e.Response.Header().Set("Cache-Control", "no-store")
	if !found {
		return e.String(
			http.StatusServiceUnavailable,
			"kombify-TechStack Linux installer artifact unavailable; deployment package is incomplete\n",
		)
	}

	e.Response.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	e.Response.Header().Set("Content-Disposition", "inline; filename=\"install.sh\"")
	return e.String(http.StatusOK, string(content))
}

func firstValidLinuxInstaller(paths []string) ([]byte, bool) {
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err == nil && validLinuxInstallerArtifact(content) {
			return content, true
		}
	}
	return nil, false
}

func validLinuxInstallerArtifact(content []byte) bool {
	normalized := bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	digest := sha256.Sum256(normalized)
	return hex.EncodeToString(digest[:]) == linuxInstallerArtifactSHA256
}

func (h workerRouteHandlers) installScriptPaths() []string {
	return []string{
		"install.sh",
		"./install.sh",
		"/app/install.sh",
	}
}

// installScriptPS serves the Windows PowerShell installer at GET /install.ps1.
// The wizard's Windows one-liner (irm <server>/install.ps1 | iex) depends on
// this route; without it the Windows worker bootstrap 404s (bead
// kombify-Techstack-8as). Mirrors installScript: prefer the on-disk install.ps1,
// fall back to an embedded minimal bootstrap.
func (h workerRouteHandlers) installScriptPS(e *httpx.Event) error {
	for _, p := range h.installScriptPSPaths() {
		content, err := os.ReadFile(p)
		if err == nil {
			e.Response.Header().Set("Content-Type", "text/plain; charset=utf-8")
			e.Response.Header().Set("Content-Disposition", "inline; filename=\"install.ps1\"")
			return e.String(http.StatusOK, string(content))
		}
	}
	e.Response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	return e.String(http.StatusOK, getEmbeddedInstallScriptPS())
}

func (h workerRouteHandlers) installScriptPSPaths() []string {
	return []string{
		"install.ps1",
		"./install.ps1",
		"/app/install.ps1",
	}
}

func getClientIP(e *httpx.Event) string {
	// proxy headers
	if ip := e.Request.Header.Get("X-Real-IP"); ip != "" {
		return strings.TrimSpace(ip)
	}
	if xff := e.Request.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	addr := e.Request.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}

// getEmbeddedInstallScriptPS returns a minimal Windows PowerShell worker
// bootstrap, used when no on-disk install.ps1 is present. It mirrors the POSIX
// installer contract by registering the worker via KOMBI_SERVER/KOMBI_TOKEN.
// Note: no backtick characters — this Go raw string is backtick-delimited, and
// a PowerShell backtick would terminate it. Keep the script backtick-free.
func getEmbeddedInstallScriptPS() string {
	return `#Requires -Version 5.1
$ErrorActionPreference = "Stop"

Write-Host "kombifyTechstack Worker Bootstrap" -ForegroundColor Green
Write-Host "============================"

$server = $env:KOMBI_SERVER
$token = $env:KOMBI_TOKEN

if ([string]::IsNullOrWhiteSpace($server)) {
    Write-Host "Error: KOMBI_SERVER environment variable is required" -ForegroundColor Red
    Write-Host "Usage: $env:KOMBI_SERVER='http://<server>'; $env:KOMBI_TOKEN='<token>'; irm http://<server>/install.ps1 | iex"
    exit 1
}
if ([string]::IsNullOrWhiteSpace($token)) {
    Write-Host "Error: KOMBI_TOKEN environment variable is required" -ForegroundColor Red
    Write-Host "Copy the full one-liner from the kombify-TechStack wizard."
    exit 1
}

$arch = if ([Environment]::Is64BitOperatingSystem) { "amd64" } else { "386" }
if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { $arch = "arm64" }
$hostName = $env:COMPUTERNAME
if ([string]::IsNullOrWhiteSpace($hostName)) { $hostName = "worker" }

$server = $server.TrimEnd("/")
Write-Host ("Detected: windows/{0}" -f $arch)
Write-Host ("Server: {0}" -f $server)

if ($server -match "^https?://(localhost|127\.)") {
    Write-Host "Hint: If you run this on another machine/VM, localhost refers to that machine." -ForegroundColor Yellow
    Write-Host "Use the kombifyTechstack host reachable IP/hostname instead (e.g. http://<host-ip>:5260)." -ForegroundColor Yellow
}

$registerUrl = "$server/api/v1/workers/register"
$payload = @{ token = $token; hostname = $hostName; os = "windows"; arch = $arch } | ConvertTo-Json -Compress

Write-Host ("Registering worker with {0}" -f $registerUrl)
try {
    $resp = Invoke-RestMethod -Method Post -Uri $registerUrl -ContentType "application/json" -Body $payload -TimeoutSec 10
} catch {
    Write-Host ("Worker registration failed: {0}" -f $_.Exception.Message) -ForegroundColor Red
    exit 1
}

if ($resp.accepted -eq $true) {
    Write-Host "Worker registered and accepted." -ForegroundColor Green
} else {
    Write-Host "Worker registered but pending approval in the UI." -ForegroundColor Yellow
    Write-Host "Open the kombify-TechStack dashboard and approve the worker to continue."
}

Write-Host ""
Write-Host "Next steps:"
Write-Host "1. Open kombify-TechStack and approve the pending worker."
Write-Host "2. Continue the rollout from the stack creation screen."
Write-Host ""
`
}
