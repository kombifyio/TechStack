#requires -Version 7.5

param(
    [Parameter(Mandatory = $true)][string]$PackagePath,
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][string]$DownloadUrl,
    [Parameter(Mandatory = $true)][string]$PrivateKeyPath,
    [Parameter(Mandatory = $true)][string]$KeyId,
    [string]$Channel = "stable",
    [string]$PublishedAt = ([DateTimeOffset]::UtcNow.ToString("O")),
    [string]$OutputPath = ""
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

if (!(Test-Path -LiteralPath $PackagePath -PathType Leaf)) {
    throw "Windows client package not found: $PackagePath"
}
if ($Version -notmatch '^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$') {
    throw "Version must be SemVer: $Version"
}
if ($Channel -notmatch '^[a-z0-9][a-z0-9._-]{1,31}$') {
    throw "Channel is invalid: $Channel"
}
if ($KeyId -notmatch '^[a-zA-Z0-9][a-zA-Z0-9._-]{2,63}$') {
    throw "KeyId is invalid."
}
$download = [Uri]$DownloadUrl
if (!$download.IsAbsoluteUri -or $download.Scheme -ne 'https' -or !$download.Host -or $download.UserInfo -or $download.Fragment) {
    throw "DownloadUrl must be an absolute HTTPS URL without user-info or fragment."
}
$published = [DateTimeOffset]::MinValue
if (![DateTimeOffset]::TryParse($PublishedAt, [ref]$published)) {
    throw "PublishedAt must be an ISO-8601 timestamp."
}
if (!(Test-Path -LiteralPath $PrivateKeyPath -PathType Leaf)) {
    throw "Private signing key not found: $PrivateKeyPath"
}

$package = Get-Item -LiteralPath $PackagePath
$manifest = [ordered]@{
    schema_version = "1"
    version = $Version
    channel = $Channel
    package_url = $download.AbsoluteUri
    package_sha256 = (Get-FileHash -LiteralPath $package.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    package_size = [long]$package.Length
    published_at = $published.ToUniversalTime().ToString("O")
    signature = [ordered]@{
        algorithm = "RS256"
        key_id = $KeyId
        value = ""
    }
}

$rsa = [Security.Cryptography.RSA]::Create()
try {
    $rsa.ImportFromPem((Get-Content -LiteralPath $PrivateKeyPath -Raw))
    $signature = $rsa.SignData(
        (Get-CanonicalManifestBytes $manifest),
        [Security.Cryptography.HashAlgorithmName]::SHA256,
        [Security.Cryptography.RSASignaturePadding]::Pkcs1
    )
    $manifest.signature.value = [Convert]::ToBase64String($signature).TrimEnd('=').Replace('+', '-').Replace('/', '_')
} finally {
    $rsa.Dispose()
}

if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    $OutputPath = "$($package.FullName).update.json"
}
$outputDirectory = Split-Path -Parent ([IO.Path]::GetFullPath($OutputPath))
if ($outputDirectory) {
    New-Item -ItemType Directory -Force -Path $outputDirectory | Out-Null
}
$manifest | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $OutputPath -Encoding UTF8
Write-Host "Signed Windows client update manifest: $OutputPath"
