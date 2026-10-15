param(
    [switch]$NoInstall,
    [switch]$Service,
    [switch]$StageOnly,
    [switch]$Help
)

# kombify Techstack Windows Installer
# Usage (wizard one-liner):
#   $env:KOMBI_SERVER='http://<host>:5260'; $env:KOMBI_TOKEN='<token>'; irm https://install.techstack.kombify.io/install.ps1 | iex
# Usage (direct):
#   .\install.ps1 [-NoInstall] [-Service]
#
# Env fallbacks (used when piped through iex, where -switches cannot be passed):
#   $env:TECHSTACK_NO_INSTALL=1        # registration only, skip binary
#   $env:TECHSTACK_AS_SERVICE=1        # install + register Windows Service
#   $env:TECHSTACK_INSTALL_BINARY=1    # after worker bootstrap, also install the binary
#   $env:TECHSTACK_VERSION=v0.6.0      # pin a release (escape hatch)
#   $env:TECHSTACK_ALLOW_STALE=1       # allow a release below the supported baseline
#   $env:INSTALL_DIR=<path>            # override install directory

$ErrorActionPreference = "Stop"

$Repo = "kombifyio/TechStack"
$BinaryName = "techstack"
# MIN_SUPPORTED_VERSION: oldest release whose install/agent contract this
# installer supports. A dormant mirror serving an older "latest" is refused
# unless TECHSTACK_ALLOW_STALE=1. Keep in lockstep with install.sh.
$MinSupportedVersion = if ($env:MIN_SUPPORTED_VERSION) { $env:MIN_SUPPORTED_VERSION } else { "0.6.0" }
$InstallDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\kombify\techstack" }

# Merge env fallbacks into the switch flags (iex path can only pass env).
if ($env:TECHSTACK_NO_INSTALL -eq "1") { $NoInstall = $true }
if ($env:TECHSTACK_AS_SERVICE -eq "1") { $Service = $true }
if ($env:TECHSTACK_STAGE_ONLY -eq "1") { $StageOnly = $true }

$script:WorkerRegistered = $false

function Show-Usage {
    Write-Host "Usage: install.ps1 [-NoInstall] [-Service]"
    Write-Host ""
    Write-Host "Options:"
    Write-Host "  -NoInstall   Skip binary installation (registration only)"
    Write-Host "  -Service     Install and register a Windows Service"
    Write-Host "  -StageOnly   Stage the verified server runtime only; no PATH, service or enrollment changes"
}

function Register-WorkerIfEnvSet {
    $server = $env:KOMBI_SERVER
    $token = $env:KOMBI_TOKEN
    if ([string]::IsNullOrWhiteSpace($server) -or [string]::IsNullOrWhiteSpace($token)) {
        return
    }

    $server = $server.TrimEnd("/")
    $arch = if ([Environment]::Is64BitOperatingSystem) { "amd64" } else { "386" }
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { $arch = "arm64" }
    $hostName = $env:COMPUTERNAME
    if ([string]::IsNullOrWhiteSpace($hostName)) { $hostName = "worker" }

    Write-Host "Registering worker with kombify Techstack..." -ForegroundColor Cyan
    Write-Host ("Server: {0}" -f $server) -ForegroundColor Cyan

    if ($server -match "^https?://(localhost|127\.)") {
        Write-Host "Hint: On a worker/VM, localhost refers to that machine, not your kombify Techstack host." -ForegroundColor Yellow
        Write-Host "Use the host reachable IP/hostname instead (e.g. http://<host-ip>:5260)." -ForegroundColor Yellow
    }

    $registerUrl = "$server/api/v1/workers/register"
    $payload = @{ token = $token; hostname = $hostName; os = "windows"; arch = $arch } | ConvertTo-Json -Compress

    try {
        $resp = Invoke-RestMethod -Method Post -Uri $registerUrl -ContentType "application/json" -Body $payload -TimeoutSec 10
    } catch {
        Write-Host ("Worker registration failed: could not reach {0}" -f $registerUrl) -ForegroundColor Red
        Write-Host ("  {0}" -f $_.Exception.Message) -ForegroundColor Red
        exit 1
    }

    if ($resp.accepted -eq $true) {
        Write-Host "Worker registered and accepted." -ForegroundColor Green
    } else {
        Write-Host "Worker registered but pending approval in the UI." -ForegroundColor Yellow
        Write-Host "Open the kombify Techstack dashboard and approve the worker to continue."
    }
    $script:WorkerRegistered = $true
}

function Get-Arch {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { return "arm64" }
    if ([Environment]::Is64BitOperatingSystem) { return "x86_64" }
    throw "Unsupported architecture: 32-bit Windows is not published."
}

# Compare-Semver A B: -1 if A<B, 0 if equal, 1 if A>B. Strips leading v and any
# -prerelease suffix; missing/non-numeric segments default to 0.
function Compare-Semver([string]$a, [string]$b) {
    $pa = ($a -replace '^v', '').Split('-')[0].Split('.')
    $pb = ($b -replace '^v', '').Split('-')[0].Split('.')
    for ($i = 0; $i -lt 3; $i++) {
        $fa = 0; $fb = 0
        if ($i -lt $pa.Count) { [int]::TryParse($pa[$i], [ref]$fa) | Out-Null }
        if ($i -lt $pb.Count) { [int]::TryParse($pb[$i], [ref]$fb) | Out-Null }
        if ($fa -lt $fb) { return -1 }
        if ($fa -gt $fb) { return 1 }
    }
    return 0
}

function Get-LatestVersion {
    if ($env:TECHSTACK_VERSION) {
        Write-Host ("Using pinned version: {0}" -f $env:TECHSTACK_VERSION) -ForegroundColor Cyan
        return $env:TECHSTACK_VERSION
    }

    Write-Host "Fetching latest version..." -ForegroundColor Cyan
    try {
        $release = Invoke-RestMethod -Uri ("https://api.github.com/repos/{0}/releases/latest" -f $Repo) -TimeoutSec 15
        $latest = $release.tag_name
    } catch {
        $latest = $null
    }

    if ([string]::IsNullOrWhiteSpace($latest)) {
        Write-Host ("Failed to fetch the latest release from {0}." -f $Repo) -ForegroundColor Red
        Write-Host "Pin a known version and retry:" -ForegroundColor Yellow
        Write-Host ("    `$env:TECHSTACK_VERSION='v{0}'; irm https://install.techstack.kombify.io/install.ps1 | iex" -f $MinSupportedVersion)
        exit 1
    }

    if ((Compare-Semver $latest $MinSupportedVersion) -lt 0) {
        if ($env:TECHSTACK_ALLOW_STALE -eq "1") {
            Write-Host ("Warning: {0} is below the supported baseline {1}; proceeding (TECHSTACK_ALLOW_STALE=1)." -f $latest, $MinSupportedVersion) -ForegroundColor Yellow
        } else {
            Write-Host ("Refusing to install stale release {0} (below baseline {1})." -f $latest, $MinSupportedVersion) -ForegroundColor Red
            Write-Host "The public release mirror appears to be behind. Options:" -ForegroundColor Yellow
            Write-Host "    - Pin a version:        `$env:TECHSTACK_VERSION='v<x.y.z>'"
            Write-Host "    - Override intentionally: `$env:TECHSTACK_ALLOW_STALE='1'"
            exit 1
        }
    }

    Write-Host ("Latest version: {0}" -f $latest) -ForegroundColor Green
    return $latest
}

function Install-Binary([string]$version) {
    if ((Get-Arch) -ne "x86_64") {
        throw "The public runtime transport supports Windows x64. Use the published Compose image for other server platforms."
    }
    if ($version -notmatch '^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
        throw "A stable numeric public release tag is required."
    }
    # The Local client ZIP transports a separate server runtime. Never install
    # the desktop client or use this unsigned transport as a managed Guard seed.
    $productVersion = $version -replace '^v', ''
    $asset = "kombify-techstack-client_${productVersion}_Windows_x86_64.zip"
    $releaseUrl = "https://github.com/$Repo/releases/download/v$productVersion"
    $downloadUrl = "$releaseUrl/$asset"
    $tmpDir = Join-Path ([IO.Path]::GetTempPath()) ("techstack-install-" + [Guid]::NewGuid())
    New-Item -ItemType Directory -Path $tmpDir | Out-Null
    try {
        $archive = Join-Path $tmpDir "archive.zip"
        Invoke-WebRequest -Uri $downloadUrl -OutFile $archive -TimeoutSec 300
        $expected = $env:TECHSTACK_BINARY_SHA256
        $digestSource = "TECHSTACK_BINARY_SHA256"
        if ([string]::IsNullOrWhiteSpace($expected)) {
            $checksumResponse = Invoke-WebRequest -Uri "$releaseUrl/SHA256SUMS.txt" -TimeoutSec 60
            $checksums = if ($checksumResponse.Content -is [byte[]]) { [Text.Encoding]::UTF8.GetString($checksumResponse.Content) } else { [string]$checksumResponse.Content }
            $matches = @($checksums -split '\r?\n' | Where-Object { $_ -match ('^[0-9a-fA-F]{64}\s+\*?' + [regex]::Escape($asset) + '$') })
            if ($matches.Count -ne 1) { throw "SHA256SUMS.txt must name exactly one checksum for $asset. Supply an independently obtained TECHSTACK_BINARY_SHA256 archive pin." }
            $expected = ($matches[0] -split '\s+')[0]
            $digestSource = "release SHA256SUMS.txt (transfer integrity only)"
        }
        if ($expected -notmatch '^[0-9a-fA-F]{64}$') { throw "TECHSTACK_BINARY_SHA256 must be a SHA-256 archive digest." }
        $actual = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $expected.ToLowerInvariant()) { throw "Release archive checksum mismatch; no runtime was installed." }
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        $zip = [IO.Compression.ZipFile]::OpenRead($archive)
        try {
            $entries = @($zip.Entries | Where-Object { $_.FullName -ceq "techstack.exe" })
            if ($entries.Count -ne 1) { throw "The Local public ZIP has no unique Windows server runtime. Use its published desktop installer for the client, or Compose for the server." }
            $staged = Join-Path $tmpDir "techstack.exe"
            [IO.Compression.ZipFileExtensions]::ExtractToFile($entries[0], $staged)
        } finally { $zip.Dispose() }
        $versionOutput = & $staged version
        if ($LASTEXITCODE -ne 0 -or ($versionOutput -join "`n") -notmatch ('(?m)^kombifyTechstack v' + [regex]::Escape($productVersion) + '\r?$')) {
            throw "The staged server runtime does not report the selected public version."
        }
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
        $exeTarget = Join-Path $InstallDir "techstack.exe"
        Copy-Item -LiteralPath $staged -Destination $exeTarget -Force
        @{
            role = "techstack-server"; version = $productVersion; releaseUrl = $releaseUrl
            asset = $asset; archiveSha256 = $actual; digestSource = $digestSource
            entry = "techstack.exe"; runtimeSha256 = (Get-FileHash -LiteralPath $exeTarget -Algorithm SHA256).Hash.ToLowerInvariant()
        } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $InstallDir "techstack-install-receipt.json") -Encoding UTF8
        if (-not $StageOnly) { Add-ToUserPath $InstallDir }
        return $exeTarget
    } catch {
        throw "Public server runtime staging failed for ${downloadUrl}: $($_.Exception.Message). Use the published Compose server bundle or the Windows desktop installer for its respective role."
    } finally { Remove-Item -LiteralPath $tmpDir -Recurse -Force }
}

function Add-ToUserPath([string]$dir) {
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($null -eq $userPath) { $userPath = "" }
    if (($userPath -split ';') -notcontains $dir) {
        $newPath = if ([string]::IsNullOrEmpty($userPath)) { $dir } else { "$userPath;$dir" }
        [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
        $env:Path = "$env:Path;$dir"
        Write-Host ("Added {0} to your user PATH (restart terminals to pick it up)." -f $dir) -ForegroundColor Cyan
    }
}

function Install-WindowsService([string]$exePath) {
    if (-not (Test-Path $exePath)) {
        Write-Host "Cannot install service: binary not found." -ForegroundColor Yellow
        return
    }
    $isAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)
    if (-not $isAdmin) {
        Write-Host "Windows Service installation requires an elevated (Administrator) PowerShell." -ForegroundColor Yellow
        Write-Host ("Re-run from an admin terminal: {0} then '.\\install.ps1 -Service'" -f $exePath)
        return
    }

    $serviceName = "techstack"
    Write-Host "Installing Windows Service..." -ForegroundColor Cyan
    $existing = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
    if ($existing) {
        Write-Host "Service already exists; updating binary path." -ForegroundColor Cyan
        & sc.exe config $serviceName binPath= ('"{0}" serve' -f $exePath) | Out-Null
    } else {
        New-Service -Name $serviceName -BinaryPathName ('"{0}" serve' -f $exePath) -DisplayName "kombify Techstack" -StartupType Automatic | Out-Null
    }
    Write-Host "Windows Service 'techstack' installed." -ForegroundColor Green
    Write-Host "  Start:  Start-Service techstack"
    Write-Host "  Status: Get-Service techstack"
}

# ---- Main ---------------------------------------------------------------------

if ($Help) { Show-Usage; return }

Write-Host "----------------------------------------" -ForegroundColor Cyan
Write-Host "   kombify Techstack Installer (Windows)" -ForegroundColor Cyan
Write-Host "   The Hybrid Infrastructure Unifier" -ForegroundColor Cyan
Write-Host "----------------------------------------" -ForegroundColor Cyan
Write-Host ""

if (-not $StageOnly -and $env:KOMBI_SERVER -and ($Service -or $env:TECHSTACK_INSTALL_BINARY -eq "1") -and -not $NoInstall) {
    throw "A managed Guard must come from its enrolling control plane's signed release. This public installer stages the server runtime; use the supported Linux Guard enrollment path."
}
if (-not $StageOnly) { Register-WorkerIfEnvSet }

if ($script:WorkerRegistered -and -not $Service -and $env:TECHSTACK_INSTALL_BINARY -ne "1") {
    Write-Host "Worker bootstrap complete." -ForegroundColor Green
    Write-Host "Set `$env:TECHSTACK_INSTALL_BINARY='1' or pass -Service to also install the TechStack binary."
    return
}

if ($NoInstall) {
    Write-Host "Skipping install (-NoInstall)." -ForegroundColor Green
    return
}

$version = Get-LatestVersion
$exePath = Install-Binary $version
if ($StageOnly) { Write-Host "Verified server runtime staged at $exePath; no client, service, PATH or enrollment changes."; return }

try {
    $versionOut = & $exePath version 2>$null
    Write-Host "Verification successful:" -ForegroundColor Green
    Write-Host $versionOut
} catch {
    Write-Host ("Warning: could not run {0} version" -f $exePath) -ForegroundColor Yellow
}

if ($Service) {
    Install-WindowsService $exePath
}

Write-Host ""
Write-Host "----------------------------------------" -ForegroundColor Green
Write-Host "Installation complete!" -ForegroundColor Green
Write-Host "Next steps:"
Write-Host ("  1. Run '{0} version' to verify" -f $BinaryName)
Write-Host ("  2. Run '{0}' to see available commands" -f $BinaryName)
if ($Service) { Write-Host "  3. Run 'Start-Service techstack' to start the service" }
Write-Host ("  4. Visit https://github.com/{0} for documentation" -f $Repo)
Write-Host "----------------------------------------" -ForegroundColor Green
