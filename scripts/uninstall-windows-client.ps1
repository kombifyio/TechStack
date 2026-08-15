param(
    [string]$InstallDir = "$env:LOCALAPPDATA\Programs\kombify\techstack",
    [string]$StateDir = "$env:LOCALAPPDATA\kombify\techstack-client",
    [switch]$RemoveState,
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
                throw "Refusing a reparse-point path in the uninstall boundary: $current"
            }
        }
        if ($current.Equals($normalizedRoot, [StringComparison]::OrdinalIgnoreCase)) {
            break
        }
        $parent = Get-NormalizedPath (Split-Path -Parent $current)
        $parentInsideBoundary = $parent.Equals($normalizedRoot, [StringComparison]::OrdinalIgnoreCase) -or
            (Test-StrictChildPath $parent $normalizedRoot)
        if ($parent.Equals($current, [StringComparison]::OrdinalIgnoreCase) -or !$parentInsideBoundary) {
            throw "Path escaped the uninstall boundary while resolving ancestors: $Path"
        }
        $current = $parent
    }
}

$allowedInstallRoot = Get-NormalizedPath (Join-Path $env:LOCALAPPDATA "Programs\kombify")
$allowedStateRoot = Get-NormalizedPath (Join-Path $env:LOCALAPPDATA "kombify")
$resolvedInstall = Get-NormalizedPath $InstallDir
$resolvedState = Get-NormalizedPath $StateDir
if (!(Test-StrictChildPath $resolvedInstall $allowedInstallRoot)) {
    throw "Refusing to uninstall outside $allowedInstallRoot`: $resolvedInstall"
}
if (!(Test-StrictChildPath $resolvedState $allowedStateRoot)) {
    throw "Refusing to remove or preserve state outside $allowedStateRoot`: $resolvedState"
}
Assert-NoReparsePointInBoundary $resolvedInstall $allowedInstallRoot
Assert-NoReparsePointInBoundary $resolvedState $allowedStateRoot

$installPrefix = "$resolvedInstall$([IO.Path]::DirectorySeparatorChar)"
$statePrefix = "$resolvedState$([IO.Path]::DirectorySeparatorChar)"
$processes = Get-CimInstance Win32_Process | Where-Object {
    ($_.ExecutablePath -and "$($_.ExecutablePath)".StartsWith($installPrefix, [StringComparison]::OrdinalIgnoreCase)) -or
    ($_.ExecutablePath -and "$($_.ExecutablePath)".StartsWith($statePrefix, [StringComparison]::OrdinalIgnoreCase)) -or
    ($_.Name -eq "msedgewebview2.exe" -and $_.CommandLine -like "*$resolvedState*")
}
foreach ($process in $processes) {
    if ($WhatIf) {
        Write-Host "Would stop process $($process.ProcessId) $($process.Name)"
    } else {
        Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
    }
}

$shortcutPaths = @(
    (Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\kombify\kombify TechStack Client.lnk"),
    (Join-Path ([Environment]::GetFolderPath("Desktop")) "kombify TechStack Client.lnk")
)
foreach ($shortcutPath in $shortcutPaths) {
    if (!(Test-Path -LiteralPath $shortcutPath)) { continue }
    $removeShortcut = $false
    try {
        $shell = New-Object -ComObject WScript.Shell
        $target = $shell.CreateShortcut($shortcutPath).TargetPath
        $removeShortcut = $target -and [IO.Path]::GetFullPath($target).StartsWith($installPrefix, [StringComparison]::OrdinalIgnoreCase)
    } catch {
        $removeShortcut = $false
    }
    if ($removeShortcut) {
        if ($WhatIf) { Write-Host "Would remove shortcut $shortcutPath" }
        else { Remove-Item -LiteralPath $shortcutPath -Force }
    }
}

if (Test-Path -LiteralPath $resolvedInstall) {
    if ($WhatIf) {
        Write-Host "Would remove Windows client binaries from $resolvedInstall"
    } else {
        Remove-Item -LiteralPath $resolvedInstall -Recurse -Force
    }
}

if ($RemoveState) {
    $resetScript = Join-Path $PSScriptRoot "reset-windows-client-state.ps1"
    if (!(Test-Path -LiteralPath $resetScript)) {
        throw "State reset helper is missing: $resetScript"
    }
    if ($WhatIf) {
        & $resetScript -StateDir $resolvedState -InstallDir $resolvedInstall -IncludeClientConfig -Discard -WhatIf
        Write-Host "Would remove remaining client state root $resolvedState"
    } else {
        & $resetScript -StateDir $resolvedState -InstallDir $resolvedInstall -IncludeClientConfig -Discard
        if (Test-Path -LiteralPath $resolvedState) {
            Remove-Item -LiteralPath $resolvedState -Recurse -Force
        }
    }
    Write-Host "kombify TechStack Client uninstalled with state removal."
} else {
    Write-Host "kombify TechStack Client uninstalled; state and Credential Manager entries preserved at $resolvedState"
}
