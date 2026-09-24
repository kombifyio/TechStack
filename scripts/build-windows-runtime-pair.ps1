#requires -Version 7.5

param(
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][ValidatePattern('^[0-9a-f]{40}$')][string]$SourceRevision,
    [Parameter(Mandatory = $true)][string]$WindowsOutputPath,
    [Parameter(Mandatory = $true)][string]$LinuxOutputPath
)

$ErrorActionPreference = "Stop"
if ($PSVersionTable.PSVersion.Major -ge 7) {
    $PSNativeCommandUseErrorActionPreference = $true
}
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$frontendBuildDir = Join-Path $root "app\build-static"
$frontendEmbedDir = Join-Path $root "internal\frontend\dist"
$WindowsOutputPath = if ([IO.Path]::IsPathFullyQualified($WindowsOutputPath)) {
    [IO.Path]::GetFullPath($WindowsOutputPath)
} else {
    [IO.Path]::GetFullPath((Join-Path $root $WindowsOutputPath))
}
$LinuxOutputPath = if ([IO.Path]::IsPathFullyQualified($LinuxOutputPath)) {
    [IO.Path]::GetFullPath($LinuxOutputPath)
} else {
    [IO.Path]::GetFullPath((Join-Path $root $LinuxOutputPath))
}

New-Item -ItemType Directory -Force -Path `
    (Split-Path -Parent $WindowsOutputPath), `
    (Split-Path -Parent $LinuxOutputPath) | Out-Null
if (Test-Path $frontendBuildDir) { Remove-Item -Recurse -Force $frontendBuildDir }
if (Test-Path $frontendEmbedDir) { Remove-Item -Recurse -Force $frontendEmbedDir }

$previousCGOEnabled = $env:CGO_ENABLED
$previousGoWork = $env:GOWORK
$previousDesktopStatic = $env:TECHSTACK_DESKTOP_STATIC
$previousViteDesktopStatic = $env:VITE_TECHSTACK_DESKTOP_STATIC
try {
    $env:CGO_ENABLED = "0"
    $env:GOWORK = "off"
    $env:TECHSTACK_DESKTOP_STATIC = "1"
    $env:VITE_TECHSTACK_DESKTOP_STATIC = "1"
    Push-Location (Join-Path $root "app")
    try {
        pnpm run build
        if ($LASTEXITCODE -ne 0) { throw "frontend desktop static build failed with exit code $LASTEXITCODE" }
    } finally {
        Pop-Location
    }

    New-Item -ItemType Directory -Force -Path $frontendEmbedDir | Out-Null
    Copy-Item -Recurse -Force -Path (Join-Path $frontendBuildDir "*") -Destination $frontendEmbedDir
    Push-Location $root
    try {
        go build -buildvcs=false -tags techstack_static_ui -trimpath `
            -ldflags "-s -w -X main.version=$Version -X main.buildRevision=$SourceRevision" `
            -o $WindowsOutputPath ./cmd/techstack
        if ($LASTEXITCODE -ne 0) { throw "Windows runtime build failed with exit code $LASTEXITCODE" }

        $previousGOOS = $env:GOOS
        $previousGOARCH = $env:GOARCH
        try {
            $env:GOOS = "linux"
            $env:GOARCH = "amd64"
            go build -buildvcs=false -tags techstack_static_ui -trimpath `
                -ldflags "-s -w -X main.version=$Version -X main.buildRevision=$SourceRevision" `
                -o $LinuxOutputPath ./cmd/techstack
            if ($LASTEXITCODE -ne 0) { throw "Linux runtime build failed with exit code $LASTEXITCODE" }
        } finally {
            $env:GOOS = $previousGOOS
            $env:GOARCH = $previousGOARCH
        }
    } finally {
        Pop-Location
    }
} finally {
    $env:CGO_ENABLED = $previousCGOEnabled
    $env:GOWORK = $previousGoWork
    $env:TECHSTACK_DESKTOP_STATIC = $previousDesktopStatic
    $env:VITE_TECHSTACK_DESKTOP_STATIC = $previousViteDesktopStatic
    if (Test-Path $frontendEmbedDir) { Remove-Item -Recurse -Force $frontendEmbedDir }
    if (Test-Path $frontendBuildDir) { Remove-Item -Recurse -Force $frontendBuildDir }
}
