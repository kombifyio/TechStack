#requires -Version 7.5

param(
    [Parameter(Mandatory = $true)][string]$OutputPath,
    [string]$ControllerCatalogOutputPath = "",
    [string]$ReleaseTag = "v0.39.7",
    [string]$ReleaseVersion = "0.39.7",
    [string]$LinuxArchiveSHA256 = "2c37b0f4826edda93495ef2bcd636373eb864ba62a5481954a65b48b0fa08985",
    [string]$WindowsArchiveSHA256 = "84f35c5b0bbafbefd0044e9e58a59505221bbd15bf86438c8e766667ad7379e3",
    [string]$ReleaseIndexSHA256 = "3b1dd48a0f2aab86b2216c5979082578f3391b3c82316a9a62ce10570c98a8de",
    [string]$CompatibilityManifestSHA256 = "af7a05324713d9ec5ad3608dc4d6c8de3bcb3ed3c4124367b7b3991fe64cd91d",
    # Directory holding <kit>/stack-spec.yaml produced by the SAME pinned
    # StackKits release on Linux. Supplied in CI; when empty the script falls
    # back to generating them with the Windows CLI, which is what a developer
    # running this by hand on a non-elevated workstation gets.
    [string]$SpecTemplatesPath = ""
)

$ErrorActionPreference = "Stop"

# StackKits checks object ownership against TokenOwner, which can differ from
# TokenUser on elevated Windows. Its separate DACL check still grants access
# only to TokenUser; aligning ownership here does not change those access rules.
# Best-effort on purpose: where the two already match this is a no-op, and
# Set-Acl refuses SIDs the caller is not a member of, so a failure here must
# not fail the build - the CLI still enforces its own check either way.
function Set-CustodyOwner {
    param([Parameter(Mandatory = $true)][string]$Path)
    try {
        $tokenOwner = [Security.Principal.WindowsIdentity]::GetCurrent().Owner
        $acl = Get-Acl -LiteralPath $Path
        if ($acl.GetOwner([Security.Principal.SecurityIdentifier]) -eq $tokenOwner) { return }
        $acl.SetOwner($tokenOwner)
        Set-Acl -LiteralPath $Path -AclObject $acl
    } catch {
        Write-Host "custody owner alignment skipped for ${Path}: $($_.Exception.Message)"
    }
}

# Retain both token identities and path ownership when a native init fails.
function Write-CustodyDiagnostics {
    param([Parameter(Mandatory = $true)][string]$Label, [string[]]$Paths)
    try {
        $id = [Security.Principal.WindowsIdentity]::GetCurrent()
        Write-Host "[custody:$Label] token user  = $($id.User.Value)"
        Write-Host "[custody:$Label] token owner = $($id.Owner.Value)"
        Write-Host "[custody:$Label] STACKKIT_CUSTODY_DIR = '$($env:STACKKIT_CUSTODY_DIR)'"
        Write-Host "[custody:$Label] USERPROFILE = '$($env:USERPROFILE)'"
        foreach ($candidate in $Paths) {
            if ([string]::IsNullOrWhiteSpace($candidate)) { continue }
            if (Test-Path -LiteralPath $candidate) {
                $acl = Get-Acl -LiteralPath $candidate
                $sid = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
                Write-Host "[custody:$Label] $candidate -> owner $($acl.Owner) ($sid)"
            } else {
                Write-Host "[custody:$Label] $candidate -> absent"
            }
        }
    } catch {
        Write-Host "[custody:$Label] diagnostics failed: $($_.Exception.Message)"
    }
}
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ("techstack-stackkit-bundle-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $tempRoot | Out-Null
Set-CustodyOwner -Path $tempRoot
$releaseBase = "https://github.com/kombifyio/StackKits/releases/download/$ReleaseTag"
$linuxName = "stackkits-basement-kit_${ReleaseVersion}_linux_amd64.tar.gz"
$windowsName = "stackkits-basement-kit_${ReleaseVersion}_windows_amd64.zip"
$linuxArchive = Join-Path $tempRoot $linuxName
$windowsArchive = Join-Path $tempRoot $windowsName
$compatibilityManifest = Join-Path $tempRoot "stackkits-compatibility-v1.json"
$linuxRelease = Join-Path $tempRoot "linux-release"
$windowsRelease = Join-Path $tempRoot "windows-release"
$bundleRoot = Join-Path $tempRoot "bundle"
$stackKitRoot = Join-Path $bundleRoot ".stackkit"

function Assert-SHA256([string]$Path, [string]$Expected) {
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
    if ($actual -ne $Expected.ToLowerInvariant()) {
        throw "StackKits release digest mismatch for $(Split-Path -Leaf $Path): $actual"
    }
}

try {
    New-Item -ItemType Directory -Force -Path $tempRoot, $linuxRelease, $windowsRelease, $stackKitRoot | Out-Null
    Invoke-WebRequest -UseBasicParsing "$releaseBase/$linuxName" -OutFile $linuxArchive
    Invoke-WebRequest -UseBasicParsing "$releaseBase/$windowsName" -OutFile $windowsArchive
    Invoke-WebRequest -UseBasicParsing "$releaseBase/stackkits-compatibility-v1.json" -OutFile $compatibilityManifest
    Assert-SHA256 $linuxArchive $LinuxArchiveSHA256
    Assert-SHA256 $windowsArchive $WindowsArchiveSHA256
    Assert-SHA256 $compatibilityManifest $CompatibilityManifestSHA256
    $compatibility = Get-Content -Raw -LiteralPath $compatibilityManifest | ConvertFrom-Json
    if ($compatibility.schemaVersion -ne "stackkits-compatibility/v1" -or $compatibility.release.tag -ne $ReleaseTag) {
        throw "StackKits compatibility manifest does not match $ReleaseTag."
    }
    tar --extract --gzip --file $linuxArchive --directory $linuxRelease
    if ($LASTEXITCODE -ne 0) { throw "Could not extract the pinned Linux StackKits release." }
    Expand-Archive -LiteralPath $windowsArchive -DestinationPath $windowsRelease

    $linuxBinary = Join-Path $linuxRelease "stackkit"
    $windowsBinary = Join-Path $windowsRelease "stackkit.exe"
    if (!(Test-Path -LiteralPath $linuxBinary -PathType Leaf) -or !(Test-Path -LiteralPath $windowsBinary -PathType Leaf)) {
        throw "Pinned StackKits release is missing its platform executable."
    }

    if (![string]::IsNullOrWhiteSpace($ControllerCatalogOutputPath)) {
        $resolvedCatalog = [IO.Path]::GetFullPath($ControllerCatalogOutputPath)
        if (Test-Path -LiteralPath $resolvedCatalog) { Remove-Item -Recurse -Force -LiteralPath $resolvedCatalog }
        New-Item -ItemType Directory -Force -Path $resolvedCatalog | Out-Null
        # `foundation`, not `base`: StackKits renamed the shared CUE schema
        # package in 300b5e6b and ships no `base` directory any more, so this
        # list threw on the first entry for every pinned release since.
        foreach ($catalogEntry in @("foundation", "basement-kit", "modules", "cue.mod", "addons")) {
            $catalogSource = Join-Path $windowsRelease $catalogEntry
            if (!(Test-Path -LiteralPath $catalogSource -PathType Container)) {
                throw "Pinned StackKits release is missing controller catalog directory $catalogEntry."
            }
            Copy-Item -Recurse -Force -LiteralPath $catalogSource -Destination (Join-Path $resolvedCatalog $catalogEntry)
        }
        Copy-Item -Force -LiteralPath (Join-Path $windowsRelease "LICENSE") -Destination (Join-Path $resolvedCatalog "LICENSE")
        Copy-Item -Force -LiteralPath $compatibilityManifest -Destination (Join-Path $resolvedCatalog "stackkits-compatibility-v1.json")
        $controllerBinaryDir = Join-Path $resolvedCatalog "bin"
        New-Item -ItemType Directory -Force -Path $controllerBinaryDir | Out-Null
        Copy-Item -Force -LiteralPath $windowsBinary -Destination (Join-Path $controllerBinaryDir "stackkit.exe")
    }

    $binaryDir = Join-Path $stackKitRoot "bin"
    New-Item -ItemType Directory -Force -Path $binaryDir | Out-Null
    Copy-Item -LiteralPath $linuxBinary -Destination (Join-Path $binaryDir "stackkit")
    Copy-Item -LiteralPath $compatibilityManifest -Destination (Join-Path $stackKitRoot "stackkits-compatibility-v1.json")

    $prebuiltTemplates = ""
    if (-not [string]::IsNullOrWhiteSpace($SpecTemplatesPath)) {
        $prebuiltTemplates = [IO.Path]::GetFullPath($SpecTemplatesPath)
        if (-not (Test-Path -LiteralPath $prebuiltTemplates -PathType Container)) {
            throw "SpecTemplatesPath '$prebuiltTemplates' does not exist."
        }
        Write-Host "Using spec templates prepared by the pinned Linux release: $prebuiltTemplates"
    }

    foreach ($kit in @("basement-kit", "cloud-kit", "modern-homelab")) {
        $templateWork = Join-Path $tempRoot ("template-work-" + $kit)
        New-Item -ItemType Directory -Force -Path $templateWork | Out-Null
        Set-CustodyOwner -Path $templateWork
        # Take the custody location out of the guesswork: the CLI offers this
        # override, so point it at a directory this script creates and owns.
        $custodyDir = Join-Path $templateWork ".stackkit-custody"
        New-Item -ItemType Directory -Force -Path $custodyDir | Out-Null
        Set-CustodyOwner -Path $custodyDir
        $env:STACKKIT_CUSTODY_DIR = $custodyDir
        Write-CustodyDiagnostics -Label $kit -Paths @(
            [IO.Path]::GetTempPath(),
            $tempRoot,
            $templateWork,
            $custodyDir,
            (Join-Path $env:USERPROFILE ".stackkit"),
            (Join-Path $env:LOCALAPPDATA "stackkit")
        )
        if ($prebuiltTemplates -ne "") {
            # The templates are platform-independent YAML produced by the same
            # pinned release, so the Windows CLI never has to run here. That
            # matters: StackKits refuses to write custody state on an elevated
            # runner (kombify-Techstack-uxss), and every attempt to satisfy
            # that check from this side failed, while the container build has
            # been generating these three templates on Linux all along.
            $source = Join-Path $prebuiltTemplates ($kit + "/stack-spec.yaml")
            if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
                throw "prepared spec template for $kit is missing at $source"
            }
            Copy-Item -Force -LiteralPath $source -Destination (Join-Path $templateWork "stack-spec.yaml")
        } else {
        Push-Location $templateWork
        try {
            # Keep native catalog defaults aligned with the Docker template stage.
            $authoringArgs = switch ($kit) {
                { $_ -in @("basement-kit", "cloud-kit") } { @("--api-version", "stackkit/v2alpha2", "--catalog-defaults") }
                "modern-homelab" { @("--api-version", "stackkit/v2alpha1", "--compute-tier", "standard") }
                default { throw "Unsupported StackKits template kit: $kit" }
            }
            & $windowsBinary --no-log init $kit --non-interactive --name techstack-spec-template --owner-source=local --owner-email owner@smoke.stackkit.cc --owner-username owner @authoringArgs --domain template.invalid
            if ($LASTEXITCODE -ne 0) { throw "StackKits template initialization failed for $kit." }
        } finally {
            Pop-Location
        }
        }
        $templateDir = Join-Path $stackKitRoot ("spec-templates\" + $kit)
        New-Item -ItemType Directory -Force -Path $templateDir | Out-Null
        Copy-Item -LiteralPath (Join-Path $templateWork "stack-spec.yaml") -Destination (Join-Path $templateDir "stack-spec.yaml")
        if (![string]::IsNullOrWhiteSpace($ControllerCatalogOutputPath)) {
            $controllerTemplateDir = Join-Path $resolvedCatalog ("spec-templates\" + $kit)
            New-Item -ItemType Directory -Force -Path $controllerTemplateDir | Out-Null
            Copy-Item -LiteralPath (Join-Path $templateWork "stack-spec.yaml") -Destination (Join-Path $controllerTemplateDir "stack-spec.yaml")
        }
    }

    $binarySHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $binaryDir "stackkit")).Hash.ToLowerInvariant()
    $pin = [ordered]@{
        schemaVersion = "techstack.stackkit-release-pin/v2"
        kit = "basement-kit"
        version = $ReleaseTag
        platform = [ordered]@{ os = "linux"; arch = "amd64" }
        archiveSha256 = $LinuxArchiveSHA256
        indexSha256 = $ReleaseIndexSHA256
        binarySha256 = $binarySHA256
        binaryPath = "/app/.stackkit/bin/stackkit"
    }
    $pin | ConvertTo-Json -Depth 4 -Compress | Set-Content -Encoding UTF8 -LiteralPath (Join-Path $stackKitRoot "stackkits-release-pin.json")

    $resolvedOutput = [IO.Path]::GetFullPath($OutputPath)
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $resolvedOutput) | Out-Null
    if (Test-Path -LiteralPath $resolvedOutput) { Remove-Item -Force -LiteralPath $resolvedOutput }
    $gitTar = Join-Path (Split-Path -Parent (Split-Path -Parent (Get-Command git).Source)) "usr\bin\tar.exe"
    if (!(Test-Path -LiteralPath $gitTar -PathType Leaf)) {
        throw "Git for Windows tar is required to preserve executable Linux bundle permissions."
    }
    $gitTools = Split-Path -Parent $gitTar
    $cygpath = Join-Path $gitTools "cygpath.exe"
    $tarOutput = (& $cygpath -u $resolvedOutput).Trim()
    $tarRoot = (& $cygpath -u $bundleRoot).Trim()
    $previousPath = $env:PATH
    try {
        $env:PATH = "$gitTools;$previousPath"
        & $gitTar --create --gzip --mode=0755 --file $tarOutput --directory $tarRoot .stackkit
    } finally {
        $env:PATH = $previousPath
    }
    if ($LASTEXITCODE -ne 0 -or !(Test-Path -LiteralPath $resolvedOutput -PathType Leaf)) {
        throw "Could not create the StackKits Linux release bundle."
    }
    Write-Host "StackKits Linux bundle: $resolvedOutput"
} finally {
    if (Test-Path -LiteralPath $tempRoot) { Remove-Item -Recurse -Force -LiteralPath $tempRoot }
}
