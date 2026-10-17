#requires -Version 7.5

param(
    [string]$Configuration = "Release",
    [string]$Runtime = "win-x64",
    [string]$Version = "",
    [ValidateSet("Local", "Cloud")][string]$DesktopEdition = "Local",
    [string]$RuntimeExe = "",
    [string]$LinuxRuntimeExe = "",
    [string]$GuardReleaseDirectory = "",
    [string]$GuardReleaseTool = "",
    [string]$OutputDir = "dist\windows-client",
    # Spec templates produced by the same pinned StackKits release on Linux.
    # Passed through to the bundle builder so the Windows CLI never has to
    # generate them here; see kombify-Techstack-uxss.
    [string]$SpecTemplatesPath = ""
)

$ErrorActionPreference = "Stop"
if ($PSVersionTable.PSVersion.Major -ge 7) {
    $PSNativeCommandUseErrorActionPreference = $true
}
$hasRuntimeInputs = @($RuntimeExe, $LinuxRuntimeExe) |
    Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
if ($hasRuntimeInputs.Count -ne 0 -and $hasRuntimeInputs.Count -ne 2) {
    throw "Prebuilt Windows and Linux runtimes must be supplied together."
}
if ($DesktopEdition -eq "Cloud" -and $hasRuntimeInputs.Count -ne 0) {
    throw "The Cloud installer cannot contain a local product runtime."
}
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
$sourceRevision = (& git -C $root rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $sourceRevision -notmatch '^[0-9a-f]{40}$') {
    throw "Windows client packaging requires an exact Git source revision."
}
$publicProfile = Test-Path -LiteralPath (Join-Path $root ".techstack-public-export.json")
if ($DesktopEdition -eq "Local" -and $publicProfile -and [string]::IsNullOrWhiteSpace($GuardReleaseDirectory)) {
    throw "Public Local packaging requires the exact-source signed Guard publication."
}
$signedGuardDirectory = ""
if (-not [string]::IsNullOrWhiteSpace($GuardReleaseDirectory)) {
    if ($DesktopEdition -ne "Local" -or [string]::IsNullOrWhiteSpace($GuardReleaseTool)) {
        throw "A signed Guard seed requires Local packaging and its canonical release verifier."
    }
    $signedGuardDirectory = (Resolve-Path -LiteralPath $GuardReleaseDirectory).Path
    $verifier = (Resolve-Path -LiteralPath $GuardReleaseTool).Path
    & $verifier verify $signedGuardDirectory --expected-source $sourceRevision --require-runtime-bundles
    if ($LASTEXITCODE -ne 0) { throw "The signed Guard publication does not match this source/runtime." }
    $signedManifest = Get-Content -Raw -LiteralPath (Join-Path $signedGuardDirectory "release-manifest.json") | ConvertFrom-Json
}
# Pre-1.0 packaging produces the requested artifacts only. The optional
# client checks remain independent diagnostics and are not release dependencies.
$project = Join-Path $root "clients\windows\Kombify.TechStack.Client\Kombify.TechStack.Client.csproj"
$editionDir = if ($DesktopEdition -eq "Cloud") { "windows-client-cloud" } else { "windows-client" }
if ($DesktopEdition -eq "Cloud" -and $OutputDir -eq "dist\windows-client") { $OutputDir = "dist\windows-client-cloud" }
$publishDir = Join-Path $root "dist\$editionDir\native"
$stageDir = Join-Path $root "dist\$editionDir\stage"
$zipDir = Join-Path $root $OutputDir
$safeDistRoot = [IO.Path]::GetFullPath((Join-Path $root "dist"))
foreach ($target in @($publishDir, $stageDir)) {
    $absolute = [IO.Path]::GetFullPath($target)
    if (!$absolute.StartsWith($safeDistRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Packaging target escapes the workspace dist directory: $absolute"
    }
}

if ([string]::IsNullOrWhiteSpace($Version)) {
    $versionFile = Join-Path $root "VERSION"
    $Version = if (Test-Path $versionFile) { (Get-Content -Raw $versionFile).Trim() } else { "0.0.0-local" }
}

if (Test-Path $publishDir) { Remove-Item -Recurse -Force $publishDir }
if (Test-Path $stageDir) { Remove-Item -Recurse -Force $stageDir }
New-Item -ItemType Directory -Force -Path $publishDir, $stageDir, $zipDir | Out-Null

dotnet publish $project -c $Configuration -r $Runtime --self-contained true -o $publishDir `
    -p:PublishSingleFile=true `
    -p:IncludeNativeLibrariesForSelfExtract=true `
    -p:EnableCompressionInSingleFile=true `
    -p:Version=$Version `
    -p:DesktopEdition=$DesktopEdition
if ($LASTEXITCODE -ne 0) { throw "dotnet publish failed with exit code $LASTEXITCODE" }
Copy-Item -Recurse -Force -Path (Join-Path $publishDir "*") -Destination $stageDir

if ($DesktopEdition -eq "Cloud") {
    & (Join-Path $PSScriptRoot "build-windows-cloud-updater.ps1") -Version $Version `
        -OutputPath (Join-Path $stageDir "kombify-techstack-cloud-updater.exe")
    if ($LASTEXITCODE -ne 0) { throw "Cloud update verifier packaging failed with exit code $LASTEXITCODE" }
    Copy-Item -LiteralPath (Join-Path $root "THIRD-PARTY-NOTICES.md") -Destination $stageDir
}

if ($DesktopEdition -eq "Local") {
$runtimeDestination = Join-Path $stageDir "techstack.exe"
$linuxRuntimeDestination = Join-Path $stageDir "techstack-linux-amd64"
if ($hasRuntimeInputs.Count -eq 2) {
    $runtimeExePath = Resolve-Path $RuntimeExe
    $linuxRuntimeExePath = Resolve-Path $LinuxRuntimeExe
    Copy-Item -Force -Path $runtimeExePath -Destination $runtimeDestination
    Copy-Item -Force -Path $linuxRuntimeExePath -Destination $linuxRuntimeDestination
} else {
    & (Join-Path $PSScriptRoot "build-windows-runtime-pair.ps1") `
        -Version $Version `
        -SourceRevision $sourceRevision `
        -WindowsOutputPath $runtimeDestination `
        -LinuxOutputPath $linuxRuntimeDestination
}

Copy-Item -Force -Path (Join-Path $root "scripts\install-windows-client.ps1") -Destination $stageDir
& (Join-Path $PSScriptRoot "new-windows-postgres-bundle.ps1") -OutputDirectory (Join-Path $stageDir "postgres")
Copy-Item -Force -Path (Join-Path $root "install.sh") -Destination (Join-Path $stageDir "install.sh")
Copy-Item -Force -Path (Join-Path $root "install.ps1") -Destination (Join-Path $stageDir "install.ps1")
$bundleArgs = @{
    OutputPath                  = (Join-Path $stageDir "stackkit-release-linux-amd64.tar.gz")
    ControllerCatalogOutputPath = (Join-Path $stageDir "stackkits")
}
if (-not [string]::IsNullOrWhiteSpace($SpecTemplatesPath)) {
    $bundleArgs.SpecTemplatesPath = $SpecTemplatesPath
}
if ($signedGuardDirectory -ne "" -and $signedManifest.version -ne $Version) {
    throw "The signed Guard version must match the product being packaged."
}
if ($signedGuardDirectory -ne "") {
    $bundleArgs.ControllerOnly = $true
    $bundleArgs.ExpectedReleaseTag = $signedManifest.stackkits.version
    $bundleArgs.ExpectedLinuxArchiveSHA256 = $signedManifest.stackkits.archiveSha256
}
& (Join-Path $PSScriptRoot "new-windows-stackkit-bundle.ps1") @bundleArgs
if ($LASTEXITCODE -ne 0) { throw "StackKits Linux bundle build failed with exit code $LASTEXITCODE" }
if ($signedGuardDirectory -ne "") {
    $seedDestination = Join-Path $stageDir "guard-release"
    New-Item -ItemType Directory -Force -Path $seedDestination | Out-Null
    foreach ($asset in @("release-manifest.json", "release-manifest.sigstore.json", "techstack-linux-amd64", "techstack-linux-amd64.sigstore.json", "techstack-linux-arm64", "techstack-linux-arm64.sigstore.json", "stackkit-runtime-linux-amd64.tar.gz", "stackkit-runtime-linux-arm64.tar.gz")) {
        Copy-Item -Force -LiteralPath (Join-Path $signedGuardDirectory $asset) -Destination (Join-Path $seedDestination $asset)
    }
    # The consumer serves the producer's exact archive, never a Windows repack.
    Copy-Item -Force -LiteralPath (Join-Path $seedDestination "stackkit-runtime-linux-amd64.tar.gz") -Destination $bundleArgs.OutputPath
    Copy-Item -Force -LiteralPath (Join-Path $seedDestination "techstack-linux-amd64") -Destination $linuxRuntimeDestination
    $metadataDirectory = [IO.Path]::GetFullPath((Join-Path $stageDir "signed-runtime-metadata"))
    if (!$metadataDirectory.StartsWith($safeDistRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Signed runtime metadata target escapes the workspace dist directory."
    }
    New-Item -ItemType Directory -Force -Path $metadataDirectory | Out-Null
    try {
        tar --extract --gzip --file $bundleArgs.OutputPath --directory $metadataDirectory .stackkit/stackkits-release-pin.json .stackkit/stackkits-use-case-catalog-v1.json .stackkit/stackkits-advanced-operations-v1.json
        if ($LASTEXITCODE -ne 0) { throw "Could not read the verified runtime catalogs." }
        $linuxPin = Get-Content -Raw -LiteralPath (Join-Path $metadataDirectory ".stackkit\stackkits-release-pin.json") | ConvertFrom-Json
        $controllerPlanner = Get-Content -Raw -LiteralPath (Join-Path $bundleArgs.ControllerCatalogOutputPath "stackkits-controller-address-planner.json") | ConvertFrom-Json
        if ($linuxPin.indexSha256 -ne $controllerPlanner.controller.indexSha256) {
            throw "The signed Guard and native controller pin different release indexes."
        }
        foreach ($catalog in @("stackkits-use-case-catalog-v1.json", "stackkits-advanced-operations-v1.json")) {
            Copy-Item -Force -LiteralPath (Join-Path $metadataDirectory ".stackkit\$catalog") -Destination (Join-Path $bundleArgs.ControllerCatalogOutputPath $catalog)
        }
    } finally {
        Remove-Item -LiteralPath $metadataDirectory -Recurse -Force
    }
}
Copy-Item -Force -Path (Join-Path $root "scripts\uninstall-windows-client.ps1") -Destination $stageDir
Copy-Item -Force -Path (Join-Path $root "scripts\reset-windows-client-state.ps1") -Destination $stageDir
} else {
    Copy-Item -Force -Path (Join-Path $root "scripts\install-windows-cloud-client.ps1") -Destination $stageDir
}
Copy-Item -Force -Path (Join-Path $root "clients\windows\README.md") -Destination (Join-Path $stageDir "README.md")
$stageAssetsDir = Join-Path $stageDir "Assets"
New-Item -ItemType Directory -Force -Path $stageAssetsDir | Out-Null
Copy-Item -Force -Path (Join-Path $root "clients\windows\Kombify.TechStack.Client\Assets\kombify-navy.ico") -Destination $stageAssetsDir

$zipName = if ($DesktopEdition -eq "Cloud") { "kombify-techstack-cloud-client_${Version}_Windows_x86_64.zip" } else { "kombify-techstack-client_${Version}_Windows_x86_64.zip" }
$zipPath = Join-Path $zipDir $zipName
if (Test-Path $zipPath) { Remove-Item -Force $zipPath }
Compress-Archive -Path (Join-Path $stageDir "*") -DestinationPath $zipPath -Force
(Get-FileHash -Algorithm SHA256 $zipPath).Hash | Set-Content -Encoding ASCII -Path "$zipPath.sha256"

# The ZIP remains the portable/development artifact. The public Windows
# release also ships the WiX v4 per-machine MSI and Burn setup.exe, both built
# from this exact stage. Authenticode is intentionally not a packaging gate.
$installerBuilder = Join-Path $PSScriptRoot "..\installer\wix\build-windows-installer.ps1"
& $installerBuilder -StageDir $stageDir -Version $Version -OutputDir $OutputDir -DesktopEdition $DesktopEdition
if ($LASTEXITCODE -ne 0) { throw "WiX Windows installer build failed with exit code $LASTEXITCODE" }
$suffix = if ($DesktopEdition -eq "Cloud") { "-Cloud" } else { "" }
$msiPath = Join-Path $zipDir "kombify-Techstack$suffix-x64.msi"
$setupPath = Join-Path $zipDir "kombify-Techstack$suffix-Setup.exe"
foreach ($installerArtifact in @($msiPath, $setupPath)) {
    if (!(Test-Path -LiteralPath $installerArtifact -PathType Leaf)) {
        throw "Missing WiX Windows release artifact: $installerArtifact"
    }
}

$releaseAssets = @($zipPath, $msiPath, $setupPath)
$checksumLines = foreach ($asset in $releaseAssets) {
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $asset).Hash.ToLowerInvariant()
    "$hash *$(Split-Path -Leaf $asset)"
}
$checksumPath = Join-Path $zipDir "SHA256SUMS.txt"
$checksumLines | Set-Content -Encoding ASCII -Path $checksumPath
$unsignedMarker = Join-Path $zipDir "UNSIGNED-WINDOWS-RELEASE.txt"
@"
kombify Techstack $DesktopEdition Windows release $Version

The Windows MSI and Burn setup.exe in this release are intentionally
unsigned on every release channel, including stable. No Authenticode
certificate is required. Verify the downloaded assets against SHA256SUMS.txt before
installing.

Assets:
- $(Split-Path -Leaf $setupPath) (user-facing installer)
- $(Split-Path -Leaf $msiPath) (per-machine MSI)
- $(Split-Path -Leaf $zipPath) (portable ZIP)
"@ | Set-Content -Encoding UTF8 -Path $unsignedMarker

Write-Host "Windows client package: $zipPath"
