param(
    [string]$Version = "",
    [string]$InstallDir = "",
    [string]$StateDir = "",
    # Local HTTP port for the runtime under test; gRPC uses LocalPort + 3.
    # Override when 5260/5263 are held by another workload (e.g. the compose dev stack).
    [ValidateRange(1024, 65532)]
    [int]$LocalPort = 5260,
    [switch]$SkipPackage,
    [switch]$KeepState,
    [switch]$KeepInstall
)

$ErrorActionPreference = "Stop"
if ($PSVersionTable.PSVersion.Major -ge 7) {
    $PSNativeCommandUseErrorActionPreference = $true
}

$smokeId = [Guid]::NewGuid().ToString("N")
if ([string]::IsNullOrWhiteSpace($Version)) {
    $Version = "0.0.0-smoke.$($smokeId.Substring(0, 12))"
}
if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    $InstallDir = Join-Path $env:LOCALAPPDATA "Programs\kombify\techstack-smoke-$smokeId"
}
if ([string]::IsNullOrWhiteSpace($StateDir)) {
    $StateDir = Join-Path $env:LOCALAPPDATA "kombify\techstack-client-smoke-$smokeId"
}

function Get-NormalizedPath([string]$Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) {
        throw "Path is required."
    }
    return [IO.Path]::GetFullPath($Path).TrimEnd("\", "/")
}

function Test-StrictChildPath([string]$Path, [string]$Root) {
    $normalizedPath = Get-NormalizedPath $Path
    $normalizedRoot = Get-NormalizedPath $Root
    if ($normalizedPath.Equals($normalizedRoot, [StringComparison]::OrdinalIgnoreCase)) {
        return $false
    }
    $rootPrefix = $normalizedRoot + [IO.Path]::DirectorySeparatorChar
    return $normalizedPath.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase)
}

function Assert-NoReparsePointInBoundary([string]$Path, [string]$Root) {
    $normalizedPath = Get-NormalizedPath $Path
    $normalizedRoot = Get-NormalizedPath $Root
    $current = $normalizedPath
    while ($true) {
        if (Test-Path -LiteralPath $current) {
            $item = Get-Item -Force -LiteralPath $current
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Refusing a reparse-point path in the smoke boundary: $current"
            }
        }
        if ($current.Equals($normalizedRoot, [StringComparison]::OrdinalIgnoreCase)) {
            break
        }
        $parent = Get-NormalizedPath (Split-Path -Parent $current)
        $parentInsideBoundary = $parent.Equals($normalizedRoot, [StringComparison]::OrdinalIgnoreCase) -or
            (Test-StrictChildPath $parent $normalizedRoot)
        if ($parent.Equals($current, [StringComparison]::OrdinalIgnoreCase) -or !$parentInsideBoundary) {
            throw "Path escaped the smoke boundary while resolving ancestors: $Path"
        }
        $current = $parent
    }
}

$allowedStateRoot = Get-NormalizedPath (Join-Path $env:LOCALAPPDATA "kombify")
$allowedInstallRoot = Get-NormalizedPath (Join-Path $env:LOCALAPPDATA "Programs\kombify")
$defaultProductState = Get-NormalizedPath (Join-Path $allowedStateRoot "techstack-client")
$defaultProductInstall = Get-NormalizedPath (Join-Path $allowedInstallRoot "techstack")
$requestedState = Get-NormalizedPath $StateDir
$requestedInstall = Get-NormalizedPath $InstallDir
if (!(Test-StrictChildPath $requestedState $allowedStateRoot)) {
    throw "Refusing installed smoke state outside $allowedStateRoot`: $requestedState"
}
if (!(Test-StrictChildPath $requestedInstall $allowedInstallRoot)) {
    throw "Refusing installed smoke binaries outside $allowedInstallRoot`: $requestedInstall"
}
if ($requestedState.Equals($defaultProductState, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to run installed smoke against the normal product state. Use an isolated StateDir."
}
if ($requestedInstall.Equals($defaultProductInstall, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to run installed smoke against the normal product installation. Use an isolated InstallDir."
}
Assert-NoReparsePointInBoundary $requestedState $allowedStateRoot
Assert-NoReparsePointInBoundary $requestedInstall $allowedInstallRoot
$StateDir = $requestedState
$InstallDir = $requestedInstall
$previousStateOverride = [Environment]::GetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR")

$localOrigin = "http://127.0.0.1:$LocalPort"
$localGrpcPort = $LocalPort + 3

$root = Resolve-Path (Join-Path $PSScriptRoot "..")
$appDir = Join-Path $root "app"
$resetScript = Join-Path $root "scripts\reset-windows-client-state.ps1"
$packageScript = Join-Path $root "scripts\package-windows-client.ps1"
$installScript = Join-Path $root "dist\windows-client\stage\install-windows-client.ps1"
$clientExe = Join-Path $InstallDir "kombify-techstack-client.exe"

function Get-StateScopedCredentialTarget {
    param(
        [string]$BaseTarget,
        [string]$StateDir
    )

    $defaultState = [IO.Path]::GetFullPath(
        (Join-Path $env:LOCALAPPDATA "kombify\techstack-client")
    ).TrimEnd("\", "/")
    $state = [IO.Path]::GetFullPath($StateDir).TrimEnd("\", "/")
    if ($state.Equals($defaultState, [StringComparison]::OrdinalIgnoreCase)) {
        return $BaseTarget
    }

    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $bytes = [Text.Encoding]::UTF8.GetBytes($state.ToUpperInvariant())
        $digest = $sha.ComputeHash($bytes)
        $hex = ($digest | ForEach-Object { $_.ToString("x2") }) -join ""
        return "$BaseTarget/state-$($hex.Substring(0, 16))"
    } finally {
        $sha.Dispose()
    }
}

function Stop-SmokeProcesses {
    param(
        [string]$InstallDir,
        [string]$StateDir
    )

    $installPrefix = (Get-NormalizedPath $InstallDir) + [IO.Path]::DirectorySeparatorChar
    $statePrefix = (Get-NormalizedPath $StateDir) + [IO.Path]::DirectorySeparatorChar
    $processes = Get-CimInstance Win32_Process | Where-Object {
        $name = $_.Name
        $exe = [string]$_.ExecutablePath
        $cmd = [string]$_.CommandLine
        $installedExecutable = $exe -and $exe.StartsWith($installPrefix, [StringComparison]::OrdinalIgnoreCase)
        $stateExecutable = $exe -and $exe.StartsWith($statePrefix, [StringComparison]::OrdinalIgnoreCase)
        $stateCommand = $cmd -and $cmd.IndexOf($statePrefix, [StringComparison]::OrdinalIgnoreCase) -ge 0

        ($name -eq "kombify-techstack-client.exe" -and $installedExecutable) -or
        ($name -eq "techstack.exe" -and $installedExecutable) -or
        ($name -eq "msedgewebview2.exe" -and $stateCommand) -or
        ($name -eq "postgres.exe" -and $stateExecutable) -or
        ($name -eq "cmd.exe" -and $stateCommand -and $cmd.IndexOf("postgres.exe", [StringComparison]::OrdinalIgnoreCase) -ge 0)
    }

    foreach ($process in $processes) {
        Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
    }
}

function Wait-Endpoint {
    param(
        [string]$Url,
        # The local runtime only binds its HTTP listener AFTER embedded Postgres
        # is ready (TECHSTACK_EMBEDDED_POSTGRES_START_TIMEOUT_SECONDS=120, plus
        # binary download on a cold cache), then control-plane migrations and the
        # PocketBase data-layer bootstrap. The readiness poll must therefore
        # exceed the embedded-Postgres start budget with init headroom, or a slow
        # first start races the deadline and the endpoint reads as "refused"
        # while the runtime is still coming up.
        [int]$TimeoutSeconds = 180
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $last = $null
    while ((Get-Date) -lt $deadline) {
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri $Url -TimeoutSec 3
            if ($response.StatusCode -eq 200) {
                return $response.Content
            }
        } catch {
            $last = $_.Exception.Message
            Start-Sleep -Milliseconds 500
        }
    }

    throw "Endpoint did not become ready: $Url. Last error: $last"
}

function Wait-LogLine {
    param(
        [string]$Path,
        [string]$Pattern,
        [int]$TimeoutSeconds = 45
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if (Test-Path -LiteralPath $Path) {
            $match = Select-String -LiteralPath $Path -Pattern $Pattern -SimpleMatch -Quiet
            if ($match) {
                return
            }
        }
        Start-Sleep -Milliseconds 500
    }

    throw "Log line not found in $Path`: $Pattern"
}

function Assert-AppDependencies {
    Push-Location $appDir
    try {
        node -e "require.resolve('@playwright/test')" | Out-Null
    } catch {
        throw "Missing app Playwright dependencies. Run `pnpm --dir app install --frozen-lockfile` first."
    } finally {
        Pop-Location
    }
}

function Invoke-NodeSmoke {
    param(
        [string]$Script
    )

    Push-Location $appDir
    try {
        $Script | node -
        if ($LASTEXITCODE -ne 0) {
            throw "Node smoke failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }
}

function Get-WindowsGenericCredentialSecret {
    param([Parameter(Mandatory = $true)][string]$Target)

    if (-not ("KombifyCredentialReader" -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Text;

public static class KombifyCredentialReader
{
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct Credential
    {
        public UInt32 Flags;
        public UInt32 Type;
        public string TargetName;
        public string Comment;
        public System.Runtime.InteropServices.ComTypes.FILETIME LastWritten;
        public UInt32 CredentialBlobSize;
        public IntPtr CredentialBlob;
        public UInt32 Persist;
        public UInt32 AttributeCount;
        public IntPtr Attributes;
        public string TargetAlias;
        public string UserName;
    }

    [DllImport("Advapi32.dll", EntryPoint = "CredReadW", CharSet = CharSet.Unicode, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool CredRead(string target, UInt32 type, UInt32 flags, out IntPtr credential);

    [DllImport("Advapi32.dll", EntryPoint = "CredFree")]
    private static extern void CredFree(IntPtr buffer);

    public static string Read(string target)
    {
        IntPtr pointer;
        if (!CredRead(target, 1, 0, out pointer)) {
            throw new Win32Exception(Marshal.GetLastWin32Error(), "Credential not found: " + target);
        }
        byte[] bytes = null;
        try {
            var credential = Marshal.PtrToStructure<Credential>(pointer);
            bytes = new byte[credential.CredentialBlobSize];
            Marshal.Copy(credential.CredentialBlob, bytes, 0, bytes.Length);
            return Encoding.UTF8.GetString(bytes);
        } finally {
            if (bytes != null) Array.Clear(bytes, 0, bytes.Length);
            CredFree(pointer);
        }
    }
}
'@
    }

    return [KombifyCredentialReader]::Read($Target)
}

function Invoke-FirstRunUiSmoke {
    $script = @'
const { chromium, expect } = require("@playwright/test");

(async () => {
  const browser = await chromium.launch({ headless: true });
  const baseURL = process.env.TECHSTACK_SMOKE_BASE_URL || "http://127.0.0.1:5260";
  const context = await browser.newContext({ baseURL });
  const page = await context.newPage();
  const failures = [];
  page.on("response", (response) => {
    if (response.url().includes("/_app/") && response.status() >= 400) {
      failures.push(`${response.status()} ${response.url()}`);
    }
  });
  page.on("requestfailed", (request) => {
    if (request.url().includes("/_app/")) {
      failures.push(`${request.url()} ${JSON.stringify(request.failure())}`);
    }
  });
  page.on("pageerror", (error) => {
    failures.push(`pageerror ${error.message}`);
  });

  await page.goto("/client/local?client=windows", { waitUntil: "networkidle" });
  await expect(page.getByTestId("windows-local-admin-email")).toBeVisible({ timeout: 30000 });
  await page.getByTestId("windows-local-admin-email").fill("demo@techstack.local");
  await page.getByTestId("windows-local-admin-password").fill("DemoUser123!");
  await page.getByTestId("windows-local-setup-submit").click();
  await page.waitForURL(/\/stacks(?:$|[/?#])/, { timeout: 30000 });
  await expect(page.getByTestId("stacks-dashboard")).toBeVisible({ timeout: 30000 });
  await page.goto("/wallet", { waitUntil: "domcontentloaded" });
  await expect(page.locator("[data-wallet-tab-nav]")).toBeVisible({ timeout: 30000 });
  await page.goto("/stacks/new", { waitUntil: "domcontentloaded" });
  await expect(page.locator('[data-testid="easy-wizard"]')).toBeVisible({ timeout: 30000 });
  await browser.close();

  if (failures.length > 0) {
    throw new Error(`frontend asset/page failures:\n${failures.join("\n")}`);
  }
  console.log("[windows-client-installed-smoke] first-run UI ok");
})().catch((error) => {
  console.error(error);
  process.exit(1);
});
'@
    Invoke-NodeSmoke $script
}

function Invoke-LogoutUiSmoke {
    param(
        [string]$DeviceToken
    )

    $env:TECHSTACK_SMOKE_DEVICE_TOKEN = $DeviceToken
    $script = @'
const { chromium, expect } = require("@playwright/test");

(async () => {
  const token = process.env.TECHSTACK_SMOKE_DEVICE_TOKEN;
  if (!token) throw new Error("missing TECHSTACK_SMOKE_DEVICE_TOKEN");
  const browser = await chromium.launch({ headless: true });
  const baseURL = process.env.TECHSTACK_SMOKE_BASE_URL || "http://127.0.0.1:5260";
  const context = await browser.newContext({ baseURL });
  const deviceResp = await context.request.post("/api/v1/auth/device-session", {
    headers: { "X-TechStack-Device-Token": token },
  });
  if (!deviceResp.ok()) {
    throw new Error(`device session failed ${deviceResp.status()} ${await deviceResp.text()}`);
  }

  const page = await context.newPage();
  await page.goto("/client/local?client=windows", { waitUntil: "domcontentloaded" });
  await page.waitForURL(/\/stacks(?:$|[/?#])/, { timeout: 30000 });
  await expect(page.getByTestId("stacks-dashboard")).toBeVisible({ timeout: 30000 });
  await page.getByRole("button", { name: /demo@techstack\.local/i }).click();
  await page.getByRole("button", { name: "Logout" }).click();
  await page.waitForURL(/\/client\/local\?client=windows$/, { timeout: 30000 });
  await expect(page.getByText("Local owner sign-in")).toBeVisible({ timeout: 30000 });
  await expect(page.getByTestId("windows-local-existing-email")).toBeVisible({ timeout: 30000 });
  await browser.close();
  console.log("[windows-client-installed-smoke] logout return ok");
})().catch((error) => {
  console.error(error);
  process.exit(1);
});
'@

    try {
        Invoke-NodeSmoke $script
    } finally {
        Remove-Item Env:\TECHSTACK_SMOKE_DEVICE_TOKEN -ErrorAction SilentlyContinue
    }
}

Assert-AppDependencies

foreach ($requiredPort in @($LocalPort, $localGrpcPort)) {
    $occupiedLocalRuntime = Get-NetTCPConnection -LocalPort $requiredPort -State Listen -ErrorAction SilentlyContinue
    if ($occupiedLocalRuntime) {
        $owners = ($occupiedLocalRuntime | Select-Object -ExpandProperty OwningProcess -Unique) -join ","
        throw "Refusing to run installed-client smoke while port $requiredPort is already in use (pid=$owners). Pass -LocalPort to use a free port pair."
    }
}

if (!$SkipPackage) {
    & $packageScript -Version $Version
}
if (!(Test-Path -LiteralPath $installScript)) {
    throw "Missing staged installer: $installScript"
}

try {
    Stop-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir
    & $resetScript -StateDir $StateDir -InstallDir $InstallDir -IncludeClientConfig -Discard

    & $installScript -InstallDir $InstallDir -StateDir $StateDir -Mode local -NoShortcuts `
        -LocalUiUrl "$localOrigin/" `
        -LocalOnboardingUrl "$localOrigin/client/local?client=windows"
    if (!(Test-Path -LiteralPath $clientExe)) {
        throw "Missing installed client exe: $clientExe"
    }

    $env:TECHSTACK_CLIENT_STATE_DIR = $StateDir
    $env:TECHSTACK_SMOKE_BASE_URL = $localOrigin
    $process = Start-Process -FilePath $clientExe -WindowStyle Hidden -PassThru
    Write-Host "[windows-client-installed-smoke] started client pid=$($process.Id)"
    # Cold first start downloads the embedded Postgres binaries before the
    # runtime can bind; allow the full download + start budget plus init headroom.
    $mode = Wait-Endpoint "$localOrigin/api/v1/auth/mode" -TimeoutSeconds 240
    if ($mode -notmatch '"is_first_run"\s*:\s*true') {
        throw "Expected first-run local auth mode before setup. Response: $mode"
    }
    $info = (Invoke-WebRequest -UseBasicParsing -Uri "$localOrigin/api/v1/info" -TimeoutSec 10).Content
    if ($info -notmatch '"environment"\s*:\s*"local"') {
        throw "Expected the local runtime posture (environment=local) in /api/v1/info. Response: $info"
    }
    Invoke-FirstRunUiSmoke

    foreach ($forbiddenSecretFile in @("device-token.txt", "session-secret.txt")) {
        $forbiddenPath = Join-Path $StateDir "runtime\$forbiddenSecretFile"
        if (Test-Path -LiteralPath $forbiddenPath) {
            throw "Local secret was written to disk: $forbiddenPath"
        }
    }
    $deviceCredentialTarget = Get-StateScopedCredentialTarget `
        -BaseTarget "kombify/techstack/local/device-session-token" `
        -StateDir $StateDir
    $deviceToken = (Get-WindowsGenericCredentialSecret -Target $deviceCredentialTarget).Trim()
    if ($deviceToken.Length -lt 32) {
        throw "Local device token is unexpectedly short."
    }

    Stop-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir
    $process = Start-Process -FilePath $clientExe -WindowStyle Hidden -PassThru
    Write-Host "[windows-client-installed-smoke] restarted client pid=$($process.Id)"
    $mode = Wait-Endpoint "$localOrigin/api/v1/auth/mode"
    if ($mode -notmatch '"is_first_run"\s*:\s*false') {
        throw "Expected configured local auth mode after setup. Response: $mode"
    }

    $runtimeLog = Join-Path $StateDir "runtime\techstack-runtime.log"
    Wait-LogLine -Path $runtimeLog -Pattern "local device session restored"

    $headers = @{ "X-TechStack-Device-Token" = $deviceToken }
    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $deviceResponse = Invoke-WebRequest -UseBasicParsing -Method POST -Uri "$localOrigin/api/v1/auth/device-session" -Headers $headers -WebSession $session -TimeoutSec 10
    if ($deviceResponse.StatusCode -ne 200) {
        throw "Device session endpoint returned $($deviceResponse.StatusCode)."
    }
    $whoami = Invoke-WebRequest -UseBasicParsing -Uri "$localOrigin/api/v2/whoami" -WebSession $session -TimeoutSec 10
    if ($whoami.Content -notmatch "demo@techstack\.local") {
        throw "Device session whoami did not return the demo owner. Body: $($whoami.Content)"
    }

    Invoke-LogoutUiSmoke -DeviceToken $deviceToken
    Write-Host "[windows-client-installed-smoke] ok"
} finally {
    Stop-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir
    [Environment]::SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", $previousStateOverride)
    Remove-Item Env:\TECHSTACK_SMOKE_BASE_URL -ErrorAction SilentlyContinue
    if (!$KeepState) {
        & $resetScript -StateDir $StateDir -InstallDir $InstallDir -IncludeClientConfig -Discard
    }
    if (!$KeepInstall -and (Test-Path -LiteralPath $InstallDir)) {
        $resolvedInstallDir = Get-NormalizedPath ((Resolve-Path -LiteralPath $InstallDir).Path)
        if (!(Test-StrictChildPath $resolvedInstallDir $allowedInstallRoot) -or
            $resolvedInstallDir.Equals($defaultProductInstall, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to remove smoke install outside its isolated root: $resolvedInstallDir"
        }
        Assert-NoReparsePointInBoundary $resolvedInstallDir $allowedInstallRoot
        Remove-Item -LiteralPath $resolvedInstallDir -Recurse -Force
    }
}
