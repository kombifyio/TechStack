param(
    [string]$InstallDir = "$env:LOCALAPPDATA\Programs\kombify\techstack",
    [ValidateSet("local", "cloud", "server")]
    [string]$Mode = "local",
    [string]$ServerUrl = "",
    [string]$CloudUrl = "https://kombify.io/device",
    [string]$CloudDeviceUrl = "https://kombify.io/device",
    [string]$CloudUiUrl = "https://techstack.kombify.io/login?manual=1&client=windows",
    [string]$LocalUiUrl = "http://127.0.0.1:5260/",
    [string]$LocalOnboardingUrl = "http://127.0.0.1:5260/client/local?client=windows",
    [string]$StateDir = "$env:LOCALAPPDATA\kombify\techstack-client",
    [string]$RuntimeDataDir = "",
    [int]$RuntimeStartupTimeoutSeconds = 120,
    [switch]$NoRuntimeStart,
    [switch]$NoShortcuts,
    [switch]$DesktopShortcut,
    [switch]$Launch
)

$ErrorActionPreference = "Stop"
$sourceDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$clientExe = Join-Path $sourceDir "kombify-techstack-client.exe"
$runtimeExe = Join-Path $sourceDir "techstack.exe"

if (!(Test-Path $clientExe)) { throw "kombify-techstack-client.exe not found: $clientExe" }
if (!(Test-Path $runtimeExe)) { throw "techstack.exe not found: $runtimeExe" }
if ($Mode -eq "server" -and [string]::IsNullOrWhiteSpace($ServerUrl)) { throw "Mode server requires -ServerUrl." }

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Get-ChildItem -Path $sourceDir -File | Where-Object { $_.Name -ne "install-windows-client.ps1" } | ForEach-Object {
    Copy-Item -Force -Path $_.FullName -Destination (Join-Path $InstallDir $_.Name)
}

foreach ($dir in @("Assets", "runtimes")) {
    $source = Join-Path $sourceDir $dir
    if (Test-Path $source) { Copy-Item -Recurse -Force -Path $source -Destination $InstallDir }
}

New-Item -ItemType Directory -Force -Path $stateDir | Out-Null
if ([string]::IsNullOrWhiteSpace($RuntimeDataDir)) {
    $RuntimeDataDir = Join-Path $stateDir "runtime"
}
New-Item -ItemType Directory -Force -Path $RuntimeDataDir | Out-Null
$config = [ordered]@{
    mode = $Mode
    cloudUrl = $CloudUrl
    cloudDeviceUrl = $CloudDeviceUrl
    cloudUiUrl = $CloudUiUrl
    cloudDeviceCodeEndpoint = "https://app.kombify.io/api/v1/tools/auth/device-code"
    cloudDevicePollEndpoint = "https://app.kombify.io/api/v1/tools/auth/device-code/poll"
    toolName = "stack"
    localUiUrl = $LocalUiUrl
    localOnboardingUrl = $LocalOnboardingUrl
    runtimeExecutablePath = (Join-Path $InstallDir "techstack.exe")
    runtimeDataDir = $RuntimeDataDir
    runtimeStartupTimeoutSeconds = $RuntimeStartupTimeoutSeconds
    autoStartRuntime = -not $NoRuntimeStart.IsPresent
}
if ($Mode -eq "server") { $config.serverUrl = $ServerUrl }
$config | ConvertTo-Json -Depth 4 | Set-Content -Encoding UTF8 -Path (Join-Path $stateDir "client.json")

if (!$NoShortcuts) {
    $programsDir = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\kombify"
    New-Item -ItemType Directory -Force -Path $programsDir | Out-Null
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut((Join-Path $programsDir "kombify TechStack Client.lnk"))
    $shortcut.TargetPath = Join-Path $InstallDir "kombify-techstack-client.exe"
    $shortcut.WorkingDirectory = $InstallDir
    $shortcut.Description = "kombify TechStack Client"
    $shortcut.IconLocation = Join-Path $InstallDir "kombify-techstack-client.exe"
    $shortcut.Save()

    if ($DesktopShortcut) {
        $desktopShortcut = $shell.CreateShortcut((Join-Path ([Environment]::GetFolderPath("Desktop")) "kombify TechStack Client.lnk"))
        $desktopShortcut.TargetPath = Join-Path $InstallDir "kombify-techstack-client.exe"
        $desktopShortcut.WorkingDirectory = $InstallDir
        $desktopShortcut.Description = "kombify TechStack Client"
        $desktopShortcut.IconLocation = Join-Path $InstallDir "kombify-techstack-client.exe"
        $desktopShortcut.Save()
    }
}

Write-Host "kombify TechStack Client installed at $InstallDir"
if ($Launch) { Start-Process -FilePath (Join-Path $InstallDir "kombify-techstack-client.exe") }
