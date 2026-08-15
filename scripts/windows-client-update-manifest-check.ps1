#requires -Version 7.5

$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..")
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) "kombify-windows-update-$([Guid]::NewGuid().ToString('N'))"
$package = Join-Path $tempRoot "client.zip"
$privateKey = Join-Path $tempRoot "private.pem"
$publicKey = Join-Path $tempRoot "public.pem"
$manifest = Join-Path $tempRoot "update.json"
$tampered = Join-Path $tempRoot "tampered.json"

try {
    New-Item -ItemType Directory -Force -Path $tempRoot | Out-Null
    [IO.File]::WriteAllBytes($package, [Security.Cryptography.RandomNumberGenerator]::GetBytes(4096))
    $rsa = [Security.Cryptography.RSA]::Create(3072)
    try {
        [IO.File]::WriteAllText($privateKey, $rsa.ExportRSAPrivateKeyPem())
        [IO.File]::WriteAllText($publicKey, $rsa.ExportSubjectPublicKeyInfoPem())
    } finally {
        $rsa.Dispose()
    }

    & (Join-Path $PSScriptRoot "new-windows-client-update-manifest.ps1") `
        -PackagePath $package `
        -Version "0.0.0-contract.1" `
        -DownloadUrl "https://releases.kombify.io/windows/client.zip" `
        -PrivateKeyPath $privateKey `
        -KeyId "windows-contract-1" `
        -PublishedAt "2026-07-10T00:00:00Z" `
        -OutputPath $manifest
    & (Join-Path $PSScriptRoot "test-windows-client-update-manifest.ps1") `
        -ManifestPath $manifest `
        -PublicKeyPath $publicKey `
        -PackagePath $package `
        -ExpectedKeyId "windows-contract-1"

    $changed = Get-Content -LiteralPath $manifest -Raw | ConvertFrom-Json -DateKind String
    $changed.package_sha256 = '0' * 64
    $changed | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $tampered -Encoding UTF8
    $rejected = $false
    try {
        & (Join-Path $PSScriptRoot "test-windows-client-update-manifest.ps1") `
            -ManifestPath $tampered `
            -PublicKeyPath $publicKey `
            -PackagePath $package `
            -ExpectedKeyId "windows-contract-1"
    } catch {
        $rejected = $true
    }
    if (!$rejected) {
        throw "Tampered Windows update manifest was accepted."
    }
    Write-Host "Windows client signed-update contract passed (valid + tamper rejection)."
} finally {
    if (Test-Path -LiteralPath $tempRoot) {
        $resolvedTemp = (Resolve-Path -LiteralPath $tempRoot).Path
        $allowedTemp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
        if (!$resolvedTemp.StartsWith($allowedTemp, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to clean update test directory outside temp root: $resolvedTemp"
        }
        Remove-Item -LiteralPath $resolvedTemp -Recurse -Force
    }
}
