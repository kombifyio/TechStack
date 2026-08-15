#requires -Version 7.5

param(
    [string]$Configuration = "Release",
    [string]$Runtime = "win-x64",
    [string]$Version = "",
    [string]$RuntimeExe = "",
    [string]$OutputDir = "dist\windows-client",
    [string]$UpdateManifestPrivateKeyPath = "",
    [string]$UpdateManifestPublicKeyPath = "",
    [string]$UpdateManifestKeyId = "",
    [string]$UpdateDownloadUrl = "",
    [switch]$RequireSignedUpdateManifest,
    [switch]$RequireAuthenticode,
    [string]$ExpectedAuthenticodeSubjectPattern = ""
)

$ErrorActionPreference = "Stop"
if ($PSVersionTable.PSVersion.Major -ge 7) {
    $PSNativeCommandUseErrorActionPreference = $true
}
$hasManifestInputs = @(
    $UpdateManifestPrivateKeyPath,
    $UpdateManifestPublicKeyPath,
    $UpdateManifestKeyId,
    $UpdateDownloadUrl
) | Where-Object { ![string]::IsNullOrWhiteSpace($_) }
if ($hasManifestInputs.Count -gt 0 -and $hasManifestInputs.Count -ne 4) {
    throw "Signed update manifest requires private/public key paths, key id, and download URL together."
}
if ($RequireSignedUpdateManifest -and $hasManifestInputs.Count -ne 4) {
    throw "Release packaging requires a signed update manifest, but signing inputs are incomplete."
}
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
$sourceRevision = (& git -C $root rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $sourceRevision -notmatch '^[0-9a-f]{40}$') {
    throw "Windows client packaging requires an exact Git source revision."
}
# Pre-1.0 packaging produces the requested artifacts only. The optional
# client checks remain independent diagnostics and are not release dependencies.
$project = Join-Path $root "clients\windows\Kombify.TechStack.Client\Kombify.TechStack.Client.csproj"
$publishDir = Join-Path $root "dist\windows-client\native"
$stageDir = Join-Path $root "dist\windows-client\stage"
$zipDir = Join-Path $root $OutputDir
$frontendBuildDir = Join-Path $root "app\build-static"
$frontendEmbedDir = Join-Path $root "internal\frontend\dist"

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
    -p:Version=$Version
if ($LASTEXITCODE -ne 0) { throw "dotnet publish failed with exit code $LASTEXITCODE" }
Copy-Item -Recurse -Force -Path (Join-Path $publishDir "*") -Destination $stageDir

$runtimeDestination = Join-Path $stageDir "techstack.exe"
if (-not [string]::IsNullOrWhiteSpace($RuntimeExe)) {
    $runtimeExePath = Resolve-Path $RuntimeExe
    Copy-Item -Force -Path $runtimeExePath -Destination $runtimeDestination
} else {
    $env:CGO_ENABLED = "0"
    $env:GOWORK = "off"
    if (Test-Path $frontendBuildDir) { Remove-Item -Recurse -Force $frontendBuildDir }
    if (Test-Path $frontendEmbedDir) { Remove-Item -Recurse -Force $frontendEmbedDir }
    Push-Location (Join-Path $root "app")
    try {
        $env:TECHSTACK_DESKTOP_STATIC = "1"
        $env:VITE_TECHSTACK_DESKTOP_STATIC = "1"
        pnpm run build
        if ($LASTEXITCODE -ne 0) { throw "frontend desktop static build failed with exit code $LASTEXITCODE" }
    } finally {
        Pop-Location
        Remove-Item Env:\TECHSTACK_DESKTOP_STATIC -ErrorAction SilentlyContinue
        Remove-Item Env:\VITE_TECHSTACK_DESKTOP_STATIC -ErrorAction SilentlyContinue
    }
    New-Item -ItemType Directory -Force -Path $frontendEmbedDir | Out-Null
    Copy-Item -Recurse -Force -Path (Join-Path $frontendBuildDir "*") -Destination $frontendEmbedDir
    try {
        go build -buildvcs=false -tags techstack_static_ui -trimpath `
            -ldflags "-s -w -X main.version=$Version -X main.buildRevision=$sourceRevision" `
            -o $runtimeDestination ./cmd/techstack
        if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
        $previousGOOS = $env:GOOS
        $previousGOARCH = $env:GOARCH
        try {
            $env:GOOS = "linux"
            $env:GOARCH = "amd64"
            go build -buildvcs=false -tags techstack_static_ui -trimpath `
                -ldflags "-s -w -X main.version=$Version -X main.buildRevision=$sourceRevision" `
                -o (Join-Path $stageDir "techstack-linux-amd64") ./cmd/techstack
            if ($LASTEXITCODE -ne 0) { throw "Linux Agent cross-build failed with exit code $LASTEXITCODE" }
        } finally {
            $env:GOOS = $previousGOOS
            $env:GOARCH = $previousGOARCH
        }
    } finally {
        if (Test-Path $frontendEmbedDir) { Remove-Item -Recurse -Force $frontendEmbedDir }
        if (Test-Path $frontendBuildDir) { Remove-Item -Recurse -Force $frontendBuildDir }
    }
}

Copy-Item -Force -Path (Join-Path $root "scripts\install-windows-client.ps1") -Destination $stageDir
Copy-Item -Force -Path (Join-Path $root "install.sh") -Destination (Join-Path $stageDir "install.sh")
Copy-Item -Force -Path (Join-Path $root "install.ps1") -Destination (Join-Path $stageDir "install.ps1")
& (Join-Path $PSScriptRoot "new-windows-stackkit-bundle.ps1") `
    -OutputPath (Join-Path $stageDir "stackkit-release-linux-amd64.tar.gz") `
    -ControllerCatalogOutputPath (Join-Path $stageDir "stackkits")
if ($LASTEXITCODE -ne 0) { throw "StackKits Linux bundle build failed with exit code $LASTEXITCODE" }
Copy-Item -Force -Path (Join-Path $root "scripts\uninstall-windows-client.ps1") -Destination $stageDir
Copy-Item -Force -Path (Join-Path $root "scripts\reset-windows-client-state.ps1") -Destination $stageDir
Copy-Item -Force -Path (Join-Path $root "scripts\test-windows-client-update-manifest.ps1") -Destination $stageDir
Copy-Item -Force -Path (Join-Path $root "scripts\test-windows-client-authenticode.ps1") -Destination $stageDir
Copy-Item -Force -Path (Join-Path $root "clients\windows\README.md") -Destination (Join-Path $stageDir "README.md")
$stageAssetsDir = Join-Path $stageDir "Assets"
New-Item -ItemType Directory -Force -Path $stageAssetsDir | Out-Null
Copy-Item -Force -Path (Join-Path $root "clients\windows\Kombify.TechStack.Client\Assets\kombify-navy.ico") -Destination $stageAssetsDir

if ($RequireAuthenticode) {
    foreach ($executable in @(
        (Join-Path $stageDir "kombify-techstack-client.exe"),
        (Join-Path $stageDir "techstack.exe")
    )) {
        & (Join-Path $PSScriptRoot "test-windows-client-authenticode.ps1") `
            -ExecutablePath $executable `
            -ExpectedSubjectPattern $ExpectedAuthenticodeSubjectPattern
    }
}

$zipPath = Join-Path $zipDir ("kombify-techstack-client_{0}_Windows_x86_64.zip" -f $Version)
if (Test-Path $zipPath) { Remove-Item -Force $zipPath }
Compress-Archive -Path (Join-Path $stageDir "*") -DestinationPath $zipPath -Force
(Get-FileHash -Algorithm SHA256 $zipPath).Hash | Set-Content -Encoding ASCII -Path "$zipPath.sha256"

# The ZIP remains the portable/development artifact. The public Windows
# release also ships the WiX v4 per-machine MSI and Burn setup.exe, both built
# from this exact stage. Authenticode is intentionally not a packaging gate.
$installerBuilder = Join-Path $PSScriptRoot "..\installer\wix\build-windows-installer.ps1"
& $installerBuilder -StageDir $stageDir -Version $Version -OutputDir $OutputDir
if ($LASTEXITCODE -ne 0) { throw "WiX Windows installer build failed with exit code $LASTEXITCODE" }
$msiPath = Join-Path $zipDir "kombify-Techstack-x64.msi"
$setupPath = Join-Path $zipDir "kombify-Techstack-Setup.exe"
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
kombify Techstack Windows release $Version

The Windows MSI and Burn setup.exe in this release are intentionally
unsigned. Authenticode signing is optional for this alpha and never blocks
publication. Verify the downloaded assets against SHA256SUMS.txt before
installing.

Assets:
- kombify-Techstack-Setup.exe (user-facing installer)
- kombify-Techstack-x64.msi (per-machine MSI)
- $(Split-Path -Leaf $zipPath) (portable ZIP)
"@ | Set-Content -Encoding UTF8 -Path $unsignedMarker

if ($hasManifestInputs.Count -eq 4) {
    $updateManifestPath = "$zipPath.update.json"
    & (Join-Path $PSScriptRoot "new-windows-client-update-manifest.ps1") `
        -PackagePath $zipPath `
        -Version $Version `
        -DownloadUrl $UpdateDownloadUrl `
        -PrivateKeyPath $UpdateManifestPrivateKeyPath `
        -KeyId $UpdateManifestKeyId `
        -OutputPath $updateManifestPath
    & (Join-Path $PSScriptRoot "test-windows-client-update-manifest.ps1") `
        -ManifestPath $updateManifestPath `
        -PublicKeyPath $UpdateManifestPublicKeyPath `
        -PackagePath $zipPath `
        -ExpectedKeyId $UpdateManifestKeyId
}
Write-Host "Windows client package: $zipPath"
