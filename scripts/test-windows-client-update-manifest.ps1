#requires -Version 7.5

param(
    [Parameter(Mandatory = $true)][string]$ManifestPath,
    [Parameter(Mandatory = $true)][string]$PublicKeyPath,
    [string]$PackagePath = "",
    [string]$ExpectedKeyId = ""
)

$ErrorActionPreference = "Stop"

function Get-CanonicalManifestBytes($Manifest) {
    $payload = @(
        "kombify.windows-update.v1"
        $Manifest.version
        $Manifest.channel
        $Manifest.package_url
        $Manifest.package_sha256
        [string]$Manifest.package_size
        $Manifest.published_at
        $Manifest.signature.key_id
    ) -join "`n"
    return [Text.Encoding]::UTF8.GetBytes("$payload`n")
}

function ConvertFrom-Base64Url([string]$Value) {
    if ($Value -notmatch '^[A-Za-z0-9_-]+$') {
        throw "Manifest signature is not base64url."
    }
    $normalized = $Value.Replace('-', '+').Replace('_', '/')
    switch ($normalized.Length % 4) {
        2 { $normalized += '==' }
        3 { $normalized += '=' }
        1 { throw "Manifest signature has invalid base64url length." }
    }
    return [Convert]::FromBase64String($normalized)
}

if (!(Test-Path -LiteralPath $ManifestPath -PathType Leaf)) {
    throw "Update manifest not found: $ManifestPath"
}
if (!(Test-Path -LiteralPath $PublicKeyPath -PathType Leaf)) {
    throw "Update public key not found: $PublicKeyPath"
}
$manifest = Get-Content -LiteralPath $ManifestPath -Raw | ConvertFrom-Json -DateKind String
$expectedTopLevel = @('schema_version', 'version', 'channel', 'package_url', 'package_sha256', 'package_size', 'published_at', 'signature')
$actualTopLevel = @($manifest.PSObject.Properties.Name)
if (@(Compare-Object $expectedTopLevel $actualTopLevel).Count -ne 0) {
    throw "Update manifest contains missing or unknown top-level fields."
}
$expectedSignature = @('algorithm', 'key_id', 'value')
$actualSignature = @($manifest.signature.PSObject.Properties.Name)
if (@(Compare-Object $expectedSignature $actualSignature).Count -ne 0) {
    throw "Update manifest contains missing or unknown signature fields."
}
if ($manifest.schema_version -ne '1' -or $manifest.signature.algorithm -ne 'RS256') {
    throw "Unsupported Windows update manifest schema or signature algorithm."
}
if ($manifest.version -notmatch '^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$') {
    throw "Manifest version is not SemVer."
}
if ($manifest.channel -notmatch '^[a-z0-9][a-z0-9._-]{1,31}$') {
    throw "Manifest channel is invalid."
}
if ($manifest.signature.key_id -notmatch '^[a-zA-Z0-9][a-zA-Z0-9._-]{2,63}$') {
    throw "Manifest key id is invalid."
}
if ($ExpectedKeyId -and $manifest.signature.key_id -ne $ExpectedKeyId) {
    throw "Manifest key id does not match the pinned release key."
}
$download = [Uri]$manifest.package_url
if (!$download.IsAbsoluteUri -or $download.Scheme -ne 'https' -or !$download.Host -or $download.UserInfo -or $download.Fragment) {
    throw "Manifest package URL must be absolute HTTPS without user-info or fragment."
}
if ($manifest.package_sha256 -notmatch '^[a-f0-9]{64}$' -or [long]$manifest.package_size -le 0) {
    throw "Manifest package digest or size is invalid."
}
$published = [DateTimeOffset]::MinValue
if (![DateTimeOffset]::TryParse([string]$manifest.published_at, [ref]$published)) {
    throw "Manifest published_at is invalid."
}

$rsa = [Security.Cryptography.RSA]::Create()
try {
    $rsa.ImportFromPem((Get-Content -LiteralPath $PublicKeyPath -Raw))
    $valid = $rsa.VerifyData(
        (Get-CanonicalManifestBytes $manifest),
        (ConvertFrom-Base64Url ([string]$manifest.signature.value)),
        [Security.Cryptography.HashAlgorithmName]::SHA256,
        [Security.Cryptography.RSASignaturePadding]::Pkcs1
    )
    if (!$valid) {
        throw "Windows update manifest signature is invalid."
    }
} finally {
    $rsa.Dispose()
}

if ($PackagePath) {
    if (!(Test-Path -LiteralPath $PackagePath -PathType Leaf)) {
        throw "Manifest package not found: $PackagePath"
    }
    $package = Get-Item -LiteralPath $PackagePath
    $digest = (Get-FileHash -LiteralPath $package.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($digest -ne $manifest.package_sha256 -or [long]$package.Length -ne [long]$manifest.package_size) {
        throw "Windows update package does not match the signed manifest."
    }
}

Write-Host "Windows client update manifest verification passed."
