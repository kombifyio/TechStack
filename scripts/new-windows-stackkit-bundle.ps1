#requires -Version 7.5

param(
    [Parameter(Mandatory = $true)][string]$OutputPath,
    [string]$ControllerCatalogOutputPath = "",
    [string]$ReleaseTag = "v0.18.14",
    [string]$ReleaseVersion = "0.18.14",
    [string]$LinuxArchiveSHA256 = "9a74d1fb89dc7351298b5bf76467ab5271e5b7bc1a1467b6a490543bf8cb1f8b",
    [string]$WindowsArchiveSHA256 = "fd51088ce5d3d860be7faa3481e779c851f5cfbf9a7599a17f5909d4cac37bf6",
    [string]$ReleaseIndexSHA256 = "ad851938a9878adde80988fae744782f3f9092edf7d41ca9e0f4771af084b86a"
)

$ErrorActionPreference = "Stop"
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ("techstack-stackkit-bundle-" + [Guid]::NewGuid().ToString("N"))
$releaseBase = "https://github.com/kombifyio/StackKits/releases/download/$ReleaseTag"
$linuxName = "stackkits-basement-kit_${ReleaseVersion}_linux_amd64.tar.gz"
$windowsName = "stackkits-basement-kit_${ReleaseVersion}_windows_amd64.zip"
$linuxArchive = Join-Path $tempRoot $linuxName
$windowsArchive = Join-Path $tempRoot $windowsName
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
    Assert-SHA256 $linuxArchive $LinuxArchiveSHA256
    Assert-SHA256 $windowsArchive $WindowsArchiveSHA256
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
        foreach ($catalogEntry in @("base", "basement-kit", "modules", "cue.mod", "addons")) {
            $catalogSource = Join-Path $windowsRelease $catalogEntry
            if (!(Test-Path -LiteralPath $catalogSource -PathType Container)) {
                throw "Pinned StackKits release is missing controller catalog directory $catalogEntry."
            }
            Copy-Item -Recurse -Force -LiteralPath $catalogSource -Destination (Join-Path $resolvedCatalog $catalogEntry)
        }
        Copy-Item -Force -LiteralPath (Join-Path $windowsRelease "LICENSE") -Destination (Join-Path $resolvedCatalog "LICENSE")
        $controllerBinaryDir = Join-Path $resolvedCatalog "bin"
        New-Item -ItemType Directory -Force -Path $controllerBinaryDir | Out-Null
        Copy-Item -Force -LiteralPath $windowsBinary -Destination (Join-Path $controllerBinaryDir "stackkit.exe")
    }

    $binaryDir = Join-Path $stackKitRoot "bin"
    New-Item -ItemType Directory -Force -Path $binaryDir | Out-Null
    Copy-Item -LiteralPath $linuxBinary -Destination (Join-Path $binaryDir "stackkit")

    foreach ($kit in @("basement-kit", "cloud-kit")) {
        $templateWork = Join-Path $tempRoot ("template-work-" + $kit)
        New-Item -ItemType Directory -Force -Path $templateWork | Out-Null
        Push-Location $templateWork
        try {
            & $windowsBinary --no-log init $kit --non-interactive --name techstack-spec-template --owner-source=local --domain template.invalid
            if ($LASTEXITCODE -ne 0) { throw "StackKits template initialization failed for $kit." }
        } finally {
            Pop-Location
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
