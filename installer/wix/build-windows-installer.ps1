#requires -Version 7.5

<##
.SYNOPSIS
Build the WiX v4 MSI and Burn setup.exe from the staged Windows client.

.DESCRIPTION
The stage is produced by scripts/package-windows-client.ps1. This script does
not build or copy product binaries itself. It creates:

  kombify-Techstack-x64.msi
  kombify-Techstack-Setup.exe

The outputs are deliberately unsigned. Signing is a separate release concern
and must never prevent an alpha/public OSS package from being produced.
##>

param(
    [Parameter(Mandatory = $true)]
    [string]$StageDir,
    [Parameter(Mandatory = $true)]
    [string]$Version,
    [string]$OutputDir = "dist\windows-client"
)

$ErrorActionPreference = "Stop"
if ($PSVersionTable.PSVersion.Major -ge 7) {
    $PSNativeCommandUseErrorActionPreference = $true
}

function Resolve-RequiredPath([string]$Path, [string]$Description) {
    if ([string]::IsNullOrWhiteSpace($Path)) {
        throw "$Description is required."
    }
    $resolved = Resolve-Path -LiteralPath $Path -ErrorAction Stop
    return $resolved.Path
}

function Invoke-Wix([string[]]$Arguments) {
    & wix @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "WiX build failed with exit code $LASTEXITCODE."
    }
}

function ConvertTo-WixIdentifier([string]$Value) {
    $identifier = [regex]::Replace($Value, "[^A-Za-z0-9_]", "_")
    if ($identifier -notmatch "^[A-Za-z_]") {
        $identifier = "_$identifier"
    }
    return $identifier
}

function Escape-Xml([string]$Value) {
    return [System.Security.SecurityElement]::Escape($Value)
}

function New-StageFragment([string]$StagePath, [string]$FragmentPath) {
    $files = @(Get-ChildItem -LiteralPath $StagePath -Recurse -File | Sort-Object FullName)
    if ($files.Count -eq 0) {
        throw "Windows client stage contains no files: $StagePath"
    }

    $directories = @{
        "" = [ordered]@{ Relative = ""; Id = "INSTALLFOLDER"; Name = ""; Files = [Collections.Generic.List[object]]::new(); Children = [Collections.Generic.List[string]]::new() }
    }
    $componentRefs = [Collections.Generic.List[string]]::new()

    foreach ($file in $files) {
        $relative = [IO.Path]::GetRelativePath($StagePath, $file.FullName).Replace("/", "\")
        $directoryRelative = Split-Path -Parent $relative
        if ($null -eq $directoryRelative) { $directoryRelative = "" }
        $current = ""
        foreach ($part in @($directoryRelative -split "\\" | Where-Object { $_ })) {
            $child = if ([string]::IsNullOrEmpty($current)) { $part } else { "$current\$part" }
            if (!$directories.ContainsKey($child)) {
                $id = ConvertTo-WixIdentifier "StageDirectory_$child"
                $directories[$child] = [ordered]@{ Relative = $child; Id = $id; Name = $part; Files = [Collections.Generic.List[object]]::new(); Children = [Collections.Generic.List[string]]::new() }
                $directories[$current].Children.Add($child)
            }
            $current = $child
        }
        $digest = [Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($relative))
        $suffix = ([BitConverter]::ToString($digest) -replace "-", "").ToLowerInvariant().Substring(0, 16)
        $componentId = "StageFile_$suffix"
        $directories[$current].Files.Add([ordered]@{ Relative = $relative; FullName = $file.FullName; ComponentId = $componentId; FileId = "${componentId}_Payload" })
        $componentRefs.Add($componentId)
    }

    $lines = [Collections.Generic.List[string]]::new()
    $lines.Add('<?xml version="1.0" encoding="utf-8"?>')
    $lines.Add('<Wix xmlns="http://wixtoolset.org/schemas/v4/wxs">')
    $lines.Add('  <Fragment>')
    $lines.Add('    <DirectoryRef Id="INSTALLFOLDER">')

    function Write-Directory([string]$Relative, [int]$Indent) {
        $directory = $directories[$Relative]
        $prefix = " " * $Indent
        if ($Relative -ne "") {
            $lines.Add(("{0}<Directory Id=`"{1}`" Name=`"{2}`">" -f $prefix, $directory.Id, (Escape-Xml $directory.Name)))
        }
        $componentIndent = if ($Relative -eq "") { $Indent } else { $Indent + 2 }
        $componentPrefix = " " * $componentIndent
        foreach ($file in $directory.Files) {
            $lines.Add(("{0}<Component Id=`"{1}`" Guid=`"*`">" -f $componentPrefix, $file.ComponentId))
            $lines.Add(("{0}  <File Id=`"{1}`" Source=`"{2}`" KeyPath=`"yes`" />" -f $componentPrefix, $file.FileId, (Escape-Xml $file.FullName)))
            $lines.Add(("{0}</Component>" -f $componentPrefix))
        }
        foreach ($child in $directory.Children) {
            Write-Directory $child ($componentIndent)
        }
        if ($Relative -ne "") {
            $lines.Add(("{0}</Directory>" -f $prefix))
        }
    }

    Write-Directory "" 6
    $lines.Add('    </DirectoryRef>')
    $lines.Add('    <ComponentGroup Id="StageFiles">')
    foreach ($componentId in $componentRefs) {
        $lines.Add(("      <ComponentRef Id=`"{0}`" />" -f $componentId))
    }
    $lines.Add('    </ComponentGroup>')
    $lines.Add('  </Fragment>')
    $lines.Add('</Wix>')
    [IO.File]::WriteAllLines($FragmentPath, $lines, [Text.UTF8Encoding]::new($false))
}

$root = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$stage = Resolve-RequiredPath $StageDir "StageDir"
$output = if ([IO.Path]::IsPathRooted($OutputDir)) {
    [IO.Path]::GetFullPath($OutputDir)
} else {
    [IO.Path]::GetFullPath((Join-Path $root $OutputDir))
}
$intermediate = Join-Path $output "wix-intermediate"
$stageFragment = Join-Path $intermediate "stage-files.wxs"
$msiPath = Join-Path $output "kombify-Techstack-x64.msi"
$bundlePath = Join-Path $output "kombify-Techstack-Setup.exe"
$msiSource = Join-Path $root "installer\wix\TechStack.wxs"
$bundleSource = Join-Path $root "installer\wix\TechStack.Bundle.wxs"

foreach ($required in @(
    (Join-Path $stage "kombify-techstack-client.exe"),
    (Join-Path $stage "techstack.exe"),
    (Join-Path $stage "Assets\kombify-navy.ico")
)) {
    if (!(Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Windows client stage is incomplete; missing $required"
    }
}

New-Item -ItemType Directory -Force -Path $output | Out-Null
if (Test-Path -LiteralPath $intermediate) {
    Remove-Item -LiteralPath $intermediate -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $intermediate | Out-Null
Remove-Item -LiteralPath $msiPath, $bundlePath -Force -ErrorAction SilentlyContinue
New-StageFragment -StagePath $stage -FragmentPath $stageFragment

Write-Host "Building MSI from stage: $stage"
Invoke-Wix @(
    "build",
    "-arch", "x64",
    "-ext", "WixToolset.Firewall.wixext",
    "-intermediatefolder", (Join-Path $intermediate "msi"),
    "-d", "TechStackVersion=$Version",
    "-d", "StageDir=$stage",
    $msiSource,
    $stageFragment,
    "-out", $msiPath
)

Write-Host "Building Burn setup.exe from MSI: $msiPath"
Invoke-Wix @(
    "build",
    "-arch", "x64",
    "-ext", "WixToolset.Bal.wixext",
    "-intermediatefolder", (Join-Path $intermediate "bundle"),
    "-d", "TechStackVersion=$Version",
    "-d", "StageDir=$stage",
    "-d", "MsiPath=$msiPath",
    $bundleSource,
    "-out", $bundlePath
)

foreach ($artifact in @($msiPath, $bundlePath)) {
    if (!(Test-Path -LiteralPath $artifact -PathType Leaf)) {
        throw "WiX did not produce the expected artifact: $artifact"
    }
    $item = Get-Item -LiteralPath $artifact
    if ($item.Length -le 0) {
        throw "WiX produced an empty artifact: $artifact"
    }
    Write-Host ("  {0} ({1:N0} bytes)" -f $item.FullName, $item.Length)
}

Write-Host "WiX installer build completed (unsigned)."
