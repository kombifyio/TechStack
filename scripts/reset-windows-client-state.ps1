param(
    [string]$StateDir = "$env:LOCALAPPDATA\kombify\techstack-client",
    [string]$InstallDir = "$env:LOCALAPPDATA\Programs\kombify\techstack",
    [switch]$IncludeClientConfig,
    [Alias("PreserveCloudCredentials")]
    [switch]$PreserveCredentials,
    [switch]$Discard,
    [switch]$WhatIf
)

$ErrorActionPreference = "Stop"

function Get-NormalizedPath([string]$Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) {
        throw "Path is required."
    }
    return [IO.Path]::GetFullPath($Path).TrimEnd("\", "/")
}

function Test-StrictChildPath([string]$Path, [string]$Root) {
    $normalizedPath = Get-NormalizedPath $Path
    $normalizedRoot = Get-NormalizedPath $Root
    if ($normalizedPath.Equals($normalizedRoot, [StringComparison]::OrdinalIgnoreCase)) {
        return $false
    }
    $rootPrefix = $normalizedRoot + [IO.Path]::DirectorySeparatorChar
    return $normalizedPath.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase)
}

function Assert-NoReparsePointInBoundary([string]$Path, [string]$Root) {
    $normalizedPath = Get-NormalizedPath $Path
    $normalizedRoot = Get-NormalizedPath $Root
    $current = $normalizedPath
    while ($true) {
        if (Test-Path -LiteralPath $current) {
            $item = Get-Item -Force -LiteralPath $current
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Refusing a reparse-point path in the reset boundary: $current"
            }
        }
        if ($current.Equals($normalizedRoot, [StringComparison]::OrdinalIgnoreCase)) {
            break
        }
        $parent = Get-NormalizedPath (Split-Path -Parent $current)
        $parentInsideBoundary = $parent.Equals($normalizedRoot, [StringComparison]::OrdinalIgnoreCase) -or
            (Test-StrictChildPath $parent $normalizedRoot)
        if ($parent.Equals($current, [StringComparison]::OrdinalIgnoreCase) -or !$parentInsideBoundary) {
            throw "Path escaped the reset boundary while resolving ancestors: $Path"
        }
        $current = $parent
    }
}

$resolvedStateDir = Get-NormalizedPath $StateDir
$resolvedInstallDir = Get-NormalizedPath $InstallDir
$allowedRoot = Get-NormalizedPath (Join-Path $env:LOCALAPPDATA "kombify")
$allowedInstallRoot = Get-NormalizedPath (Join-Path $env:LOCALAPPDATA "Programs\kombify")
if (!(Test-StrictChildPath $resolvedStateDir $allowedRoot)) {
    throw "Refusing to reset state outside $allowedRoot`: $resolvedStateDir"
}
if (!(Test-StrictChildPath $resolvedInstallDir $allowedInstallRoot)) {
    throw "Refusing to stop a client outside $allowedInstallRoot`: $resolvedInstallDir"
}

function Get-CredentialTargetSuffix([string]$ResolvedStateDir) {
    $defaultStateDir = [IO.Path]::GetFullPath(
        (Join-Path $env:LOCALAPPDATA "kombify\techstack-client")
    ).TrimEnd("\", "/")
    $stateDir = [IO.Path]::GetFullPath($ResolvedStateDir).TrimEnd("\", "/")
    if ($stateDir.Equals($defaultStateDir, [StringComparison]::OrdinalIgnoreCase)) {
        return ""
    }

    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $bytes = [Text.Encoding]::UTF8.GetBytes($stateDir.ToUpperInvariant())
        $digest = $sha.ComputeHash($bytes)
        $hex = ($digest | ForEach-Object { $_.ToString("x2") }) -join ""
        return "/state-$($hex.Substring(0, 16))"
    } finally {
        $sha.Dispose()
    }
}

$credentialTargetSuffix = Get-CredentialTargetSuffix $resolvedStateDir
$clientCredentialTargets = @(
    "kombify/techstack/cloud/stack/access-token",
    "kombify/techstack/cloud/stack/refresh-token",
    "kombify/techstack/local/runtime-session-secret",
    "kombify/techstack/local/device-session-token"
) | ForEach-Object { "$_$credentialTargetSuffix" }
if (!$PreserveCredentials) {
    foreach ($target in $clientCredentialTargets) {
        if ($WhatIf) {
            Write-Host "Would delete Windows Credential Manager target: $target"
            continue
        }
        $cmdkey = Start-Process `
            -FilePath "cmdkey.exe" `
            -ArgumentList @("/delete:$target") `
            -WindowStyle Hidden `
            -Wait `
            -PassThru
        if ($cmdkey.ExitCode -ne 0 -and $cmdkey.ExitCode -ne 1) {
            throw "cmdkey.exe failed deleting credential target $target with exit code $($cmdkey.ExitCode)"
        }
    }
}

if (!(Test-Path -LiteralPath $resolvedStateDir)) {
    Write-Host "No Windows client state found at $resolvedStateDir"
    return
}
Assert-NoReparsePointInBoundary $resolvedStateDir $allowedRoot

function Stop-WindowsClientStateProcesses([string]$StateDir, [string]$InstallDir) {
    $statePrefix = (Get-NormalizedPath $StateDir) + [IO.Path]::DirectorySeparatorChar
    $installPrefix = (Get-NormalizedPath $InstallDir) + [IO.Path]::DirectorySeparatorChar
    $processes = Get-CimInstance Win32_Process | Where-Object {
        $name = $_.Name
        $exe = [string]$_.ExecutablePath
        $cmd = [string]$_.CommandLine
        $installedExecutable = $exe -and $exe.StartsWith($installPrefix, [StringComparison]::OrdinalIgnoreCase)
        $stateExecutable = $exe -and $exe.StartsWith($statePrefix, [StringComparison]::OrdinalIgnoreCase)
        $stateCommand = $cmd -and $cmd.IndexOf($statePrefix, [StringComparison]::OrdinalIgnoreCase) -ge 0

        ($name -eq "kombify-techstack-client.exe" -and $installedExecutable) -or
        ($name -eq "techstack.exe" -and $installedExecutable) -or
        ($name -eq "msedgewebview2.exe" -and $stateCommand) -or
        ($name -eq "postgres.exe" -and $stateExecutable) -or
        ($name -eq "cmd.exe" -and $stateCommand -and $cmd.IndexOf("postgres.exe", [StringComparison]::OrdinalIgnoreCase) -ge 0)
    }

    foreach ($process in $processes) {
        Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
    }
}

$backupRoot = Join-Path $allowedRoot "techstack-client-backups"
$timestamp = "{0}-{1}" -f (Get-Date -Format "yyyyMMdd-HHmmss-fff"), ([Guid]::NewGuid().ToString("N").Substring(0, 8))
$backupDir = Join-Path $backupRoot $timestamp
$items = @("webview2", "runtime", "cloud-session.json", "connection-profile.json")
if ($IncludeClientConfig) {
    $items += "client.json"
}

$existingItems = @()
foreach ($item in $items) {
    $source = Join-Path $resolvedStateDir $item
    if (Test-Path -LiteralPath $source) {
        $existingItems += [pscustomobject]@{
            Name = $item
            Source = $source
            Target = (Join-Path $backupDir $item)
        }
    }
}

if ($existingItems.Count -eq 0) {
    Write-Host "Windows client state is already clean at $resolvedStateDir"
    return
}

if ($WhatIf) {
    if ($Discard) {
        Write-Host "Would permanently discard Windows client state below $resolvedStateDir"
    } else {
        Write-Host "Would back up Windows client state to $backupDir"
    }
    $existingItems | ForEach-Object { Write-Host ("- {0}" -f $_.Source) }
    return
}

Stop-WindowsClientStateProcesses -StateDir $resolvedStateDir -InstallDir $resolvedInstallDir
Start-Sleep -Milliseconds 500

if (!$Discard) {
    Assert-NoReparsePointInBoundary $backupRoot $allowedRoot
    New-Item -ItemType Directory -Force -Path $backupDir | Out-Null
}
foreach ($entry in $existingItems) {
    Assert-NoReparsePointInBoundary $entry.Source $allowedRoot
    $resolvedSource = (Resolve-Path -LiteralPath $entry.Source).Path
    $statePrefix = $resolvedStateDir + [IO.Path]::DirectorySeparatorChar
    if (!$resolvedSource.StartsWith($statePrefix, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to move path outside state dir: $resolvedSource"
    }
    if ($Discard) {
        Remove-Item -LiteralPath $resolvedSource -Recurse -Force
    } else {
        Move-Item -LiteralPath $resolvedSource -Destination $entry.Target
    }
}

if ($Discard) {
    Write-Host "Windows client state discarded below $resolvedStateDir"
} else {
    Write-Host "Windows client state backed up to $backupDir"
}
if ($IncludeClientConfig) {
    Write-Host "client.json was included in the backup."
} else {
    Write-Host "Kept client.json. Re-run with -IncludeClientConfig to reset install configuration too."
}
if (!$PreserveCredentials) {
    Write-Host "Removed state-scoped kombify TechStack credentials from Windows Credential Manager."
}
