$ErrorActionPreference = "Stop"

$reset = Join-Path $PSScriptRoot "reset-windows-client-state.ps1"
$uninstall = Join-Path $PSScriptRoot "uninstall-windows-client.ps1"
$smoke = Join-Path $PSScriptRoot "windows-client-installed-smoke.ps1"
$stateRoot = [IO.Path]::GetFullPath((Join-Path $env:LOCALAPPDATA "kombify")).TrimEnd("\", "/")
$installRoot = [IO.Path]::GetFullPath((Join-Path $env:LOCALAPPDATA "Programs\kombify")).TrimEnd("\", "/")
$id = [Guid]::NewGuid().ToString("N")
$safeState = Join-Path $stateRoot "techstack-client-path-contract-$id"
$safeInstall = Join-Path $installRoot "techstack-path-contract-$id"

function Assert-Rejected([string]$Name, [scriptblock]$Action) {
    $rejected = $false
    try {
        & $Action
    } catch {
        $rejected = $true
    }
    if (!$rejected) {
        throw "$Name was not rejected."
    }
    Write-Host "PASS reject $Name"
}

# These calls stop at path validation. PreserveCredentials + WhatIf guarantee
# that the reset helper cannot mutate either Credential Manager or file state.
Assert-Rejected "state root reset" {
    & $reset -StateDir $stateRoot -InstallDir $safeInstall -PreserveCredentials -WhatIf
}
Assert-Rejected "state sibling reset" {
    & $reset -StateDir "${stateRoot}-outside\victim" -InstallDir $safeInstall -PreserveCredentials -WhatIf
}
Assert-Rejected "install root reset" {
    & $reset -StateDir $safeState -InstallDir $installRoot -PreserveCredentials -WhatIf
}

# A missing isolated path must be accepted without requiring its parent to
# exist. This is the clean-account precondition used by hosted Windows runners.
& $reset -StateDir $safeState -InstallDir $safeInstall -PreserveCredentials -WhatIf

Assert-Rejected "install root uninstall" {
    & $uninstall -InstallDir $installRoot -StateDir $safeState -WhatIf
}
Assert-Rejected "state root uninstall" {
    & $uninstall -InstallDir $safeInstall -StateDir $stateRoot -WhatIf
}
Assert-Rejected "install sibling uninstall" {
    & $uninstall -InstallDir "${installRoot}-outside\victim" -StateDir $safeState -WhatIf
}

# The installed smoke has no override that can authorize deletion of normal
# product state or binaries. Both calls fail before package/build/process work.
Assert-Rejected "product state smoke" {
    & $smoke -SkipPackage -KeepState -KeepInstall -StateDir (Join-Path $stateRoot "techstack-client") -InstallDir $safeInstall
}
Assert-Rejected "product install smoke" {
    & $smoke -SkipPackage -KeepState -KeepInstall -StateDir $safeState -InstallDir (Join-Path $installRoot "techstack")
}

$resetSource = Get-Content -Raw -LiteralPath $reset
$smokeSource = Get-Content -Raw -LiteralPath $smoke
if ($resetSource.Contains('$exe -like "$installRoot*"')) {
    throw "Reset still uses the broad product-process prefix."
}
if (!$resetSource.Contains('Stop-WindowsClientStateProcesses -StateDir $resolvedStateDir -InstallDir $resolvedInstallDir')) {
    throw "Reset does not bind process shutdown to the requested install and state directories."
}
if ($smokeSource.Contains("AllowProductState")) {
    throw "Installed smoke still exposes a destructive product-state override."
}

Write-Host "Windows client path-safety contract passed."
