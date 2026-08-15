param(
    [Parameter(Mandatory = $true)][string]$ExecutablePath,
    [string]$ExpectedSubjectPattern = ""
)

$ErrorActionPreference = "Stop"
if (!(Test-Path -LiteralPath $ExecutablePath -PathType Leaf)) {
    throw "Windows executable not found: $ExecutablePath"
}
$signature = Get-AuthenticodeSignature -LiteralPath $ExecutablePath
if ($signature.Status -ne [Management.Automation.SignatureStatus]::Valid -or !$signature.SignerCertificate) {
    throw "Authenticode signature is not valid: $($signature.Status) $($signature.StatusMessage)"
}
if ($ExpectedSubjectPattern -and $signature.SignerCertificate.Subject -notmatch $ExpectedSubjectPattern) {
    throw "Authenticode signer subject does not match the pinned release identity."
}
Write-Host "Authenticode verification passed: $($signature.SignerCertificate.Subject)"

