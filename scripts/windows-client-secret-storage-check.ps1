$ErrorActionPreference = "Stop"

$root = Resolve-Path (Join-Path $PSScriptRoot "..")
$programPath = Join-Path $root "clients\windows\Kombify.TechStack.Client\Program.cs"
$storePath = Join-Path $root "clients\windows\Kombify.TechStack.Client\WindowsCredentialStore.cs"
$resetPath = Join-Path $root "scripts\reset-windows-client-state.ps1"
$smokePath = Join-Path $root "scripts\windows-client-installed-smoke.ps1"

$program = Get-Content -Raw -LiteralPath $programPath
$store = Get-Content -Raw -LiteralPath $storePath
$reset = Get-Content -Raw -LiteralPath $resetPath
$smoke = Get-Content -Raw -LiteralPath $smokePath

$requiredProgramFragments = @(
    "WindowsCredentialStore.Write(accessTarget",
    "WindowsCredentialStore.Write(refreshTarget",
    'credentialStore = "windows-credential-manager"'
)
foreach ($fragment in $requiredProgramFragments) {
    if (!$program.Contains($fragment)) {
        throw "Windows client secret-custody contract missing: $fragment"
    }
}
if ($program.Contains("response = JsonSerializer.Deserialize<JsonElement>(body)")) {
    throw "Windows client still persists the complete token-bearing authorization response."
}
foreach ($fragment in @("CredWriteW", "CredentialPersistLocalMachine", "CryptographicOperations.ZeroMemory")) {
    if (!$store.Contains($fragment)) {
        throw "Windows Credential Manager adapter contract missing: $fragment"
    }
}
foreach ($fragment in @(
    "LocalRuntimeSessionCredentialTarget",
    "LocalDeviceCredentialTarget",
    "WindowsCredentialStore.Read(target)",
    "ClientConfig.CredentialTarget"
)) {
    if (!$program.Contains($fragment)) {
        throw "Local Windows client secret-custody contract missing: $fragment"
    }
}
if (!$reset.Contains("Get-CredentialTargetSuffix")) {
    throw "Windows reset does not namespace Credential Manager targets by state directory."
}
foreach ($fragment in @("Get-StateScopedCredentialTarget", "Refusing to run installed smoke against the normal product state")) {
    if (!$smoke.Contains($fragment)) {
        throw "Installed Windows smoke isolation contract missing: $fragment"
    }
}
foreach ($forbiddenPath in @("device-token.txt", "session-secret.txt")) {
    if ($program.Contains($forbiddenPath)) {
        throw "Local Windows client still persists a plaintext secret at $forbiddenPath"
    }
}
foreach ($target in @(
    "kombify/techstack/cloud/stack/access-token",
    "kombify/techstack/cloud/stack/refresh-token",
    "kombify/techstack/local/runtime-session-secret",
    "kombify/techstack/local/device-session-token"
)) {
    if (!$reset.Contains($target)) {
        throw "Windows reset does not delete credential target: $target"
    }
}

Write-Host "Windows client secret-storage contract passed."
