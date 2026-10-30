param(
    [string]$Version = "",
    [string]$InstallDir = "",
    [string]$StateDir = "",
    [string]$ExpectedRevision = "",
    [string]$SpecTemplatesPath = "",
    # gRPC uses LocalPort + 3; the isolated WebView probe uses LocalPort + 10.
    # Override when 5260/5263 are held by another workload (e.g. the compose dev stack).
    [ValidateRange(1024, 65525)]
    [int]$LocalPort = 5260,
    # portable: stage installer into an isolated user directory.
    # setup: the shipped kombify-Techstack-Setup.exe (WiX/Burn chaining the
    # per-machine MSI) into its product location, then its uninstaller.
    [ValidateSet("portable", "setup")]
    [string]$Installer = "portable",
    [string]$SetupPath = "",
    # Blocks non-loopback traffic of the client, runtime and WebView2 with
    # Windows Firewall rules through the offline proof. Update acquisition
    # subsequently removes only this smoke's rules. Requires elevation.
    [switch]$BlockOutbound,
    # Receives runtime and PostgreSQL logs before the isolated state is removed.
    [string]$DiagnosticsDir = "",
    # Public unsigned installers authenticated by production-signed manifests.
    # Both versions/revisions must identify public releases; the installed
    # verifier and shell perform acquisition, application and recovery.
    [ValidateSet("none", "public-feed", "rollback")]
    [string]$UpdateProof = "none",
    [string]$UpdateSetupPath = "",
    # Retired: the current recovery consumer intentionally refuses loopback feeds.
    [string]$UpdateManifestPath = "",
    [string]$UpdateVersion = "",
    [string]$UpdateRevision = "",
    # Authenticated full-B2 context and real host facts, supplied only by the serial wrapper.
    [string]$B2ContextPath = "",
    [string]$B2HostPath = "",
    # Install/inspect/uninstall only; never launches the shell or local database.
    [switch]$PackageOnly,
    [switch]$SkipPackage,
    [switch]$KeepState,
    [switch]$KeepInstall
)

$ErrorActionPreference = "Stop"
if ($PSVersionTable.PSVersion.Major -ge 7) {
    $PSNativeCommandUseErrorActionPreference = $true
}

$smokeId = [Guid]::NewGuid().ToString("N")
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($ExpectedRevision)) {
    $ExpectedRevision = (& git -C $root rev-parse HEAD).Trim()
}
if ($ExpectedRevision -notmatch '^[0-9a-f]{40}$') {
    throw "Installed smoke requires an exact source revision."
}
if ([string]::IsNullOrWhiteSpace($Version)) {
    $Version = (& node (Join-Path $root "scripts/release-version.mjs") resolve $ExpectedRevision).Trim()
    if ($LASTEXITCODE -ne 0) { throw "Could not resolve the artifact version." }
}
$setupInstallDir = Join-Path $env:ProgramFiles "kombify\techstack"
if ($Installer -eq "setup") {
    if (![string]::IsNullOrWhiteSpace($InstallDir)) {
        throw "The shipped installer owns its install location; -InstallDir applies only to -Installer portable."
    }
    if ($LocalPort -ne 5260) {
        throw "The shipped installer uses the product's default local port 5260."
    }
    if (Test-Path -LiteralPath $setupInstallDir) {
        throw "Refusing the setup smoke: kombify Techstack is already installed at $setupInstallDir."
    }
} elseif ([string]::IsNullOrWhiteSpace($InstallDir)) {
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
if (!(Test-StrictChildPath $requestedState $allowedStateRoot)) {
    throw "Refusing installed smoke state outside $allowedStateRoot`: $requestedState"
}
if ($requestedState.Equals($defaultProductState, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to run installed smoke against the normal product state. Use an isolated StateDir."
}
Assert-NoReparsePointInBoundary $requestedState $allowedStateRoot
$StateDir = $requestedState
$stateInitiallyAbsent = !(Test-Path -LiteralPath $StateDir)
if ($Installer -eq "setup") {
    $InstallDir = Get-NormalizedPath $setupInstallDir
} else {
    $requestedInstall = Get-NormalizedPath $InstallDir
    if (!(Test-StrictChildPath $requestedInstall $allowedInstallRoot)) {
        throw "Refusing installed smoke binaries outside $allowedInstallRoot`: $requestedInstall"
    }
    if ($requestedInstall.Equals($defaultProductInstall, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to run installed smoke against the normal product installation. Use an isolated InstallDir."
    }
    Assert-NoReparsePointInBoundary $requestedInstall $allowedInstallRoot
    $InstallDir = $requestedInstall
}
$previousEnvironment = [ordered]@{
    TECHSTACK_CLIENT_STATE_DIR = [Environment]::GetEnvironmentVariable('TECHSTACK_CLIENT_STATE_DIR')
    WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS = [Environment]::GetEnvironmentVariable('WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS')
    TECHSTACK_SMOKE_BASE_URL = [Environment]::GetEnvironmentVariable('TECHSTACK_SMOKE_BASE_URL')
    TECHSTACK_SMOKE_CDP_URL = [Environment]::GetEnvironmentVariable('TECHSTACK_SMOKE_CDP_URL')
    TECHSTACK_SMOKE_MARKER_NAME = [Environment]::GetEnvironmentVariable('TECHSTACK_SMOKE_MARKER_NAME')
    TECHSTACK_SMOKE_MARKER_CREATE = [Environment]::GetEnvironmentVariable('TECHSTACK_SMOKE_MARKER_CREATE')
}

$localOrigin = "http://127.0.0.1:$LocalPort"
$localGrpcPort = $LocalPort + 3
$webViewDebugPort = $LocalPort + 10

$root = Resolve-Path (Join-Path $PSScriptRoot "..")
$appDir = Join-Path $root "app"
$resetScript = Join-Path $root "scripts\reset-windows-client-state.ps1"
$packageScript = Join-Path $root "scripts\package-windows-client.ps1"
$installScript = Join-Path $root "dist\windows-client\stage\install-windows-client.ps1"
if ([string]::IsNullOrWhiteSpace($SetupPath)) {
    $SetupPath = Join-Path $root "dist\windows-client\kombify-Techstack-Setup.exe"
}
$clientExe = Join-Path $InstallDir "kombify-techstack-client.exe"
$runtimeExe = Join-Path $InstallDir "techstack.exe"
$firewallGroup = "kombify-techstack-smoke-$smokeId"
$updateRequested = $UpdateProof -ne "none"
if ($PackageOnly -and ($Installer -ne 'setup' -or !$SkipPackage -or $KeepInstall -or $KeepState -or
    $BlockOutbound -or $updateRequested -or $B2ContextPath -or $B2HostPath)) {
    throw 'PackageOnly requires a prebuilt setup installer and complete cleanup; runtime, update and B2 options are excluded.'
}
if ($PackageOnly -and !$stateInitiallyAbsent) { throw 'PackageOnly requires a fresh isolated state directory.' }
if ($UpdateManifestPath) { throw "Loopback update manifests are retired; use -UpdateProof public-feed or rollback with published releases." }
if (!$updateRequested -and ($UpdateSetupPath -or $UpdateVersion -or $UpdateRevision)) {
    throw "Update arguments require -UpdateProof public-feed or rollback."
}
if ($updateRequested) {
    if ($KeepInstall -or $KeepState) { throw 'An update proof requires complete isolated cleanup.' }
    if (!$stateInitiallyAbsent) { throw 'An update proof requires a fresh isolated state directory.' }
    if ($Installer -ne "setup") { throw "The update proof requires -Installer setup." }
    foreach ($required in @($UpdateSetupPath, $UpdateVersion, $UpdateRevision)) {
        if ([string]::IsNullOrWhiteSpace($required)) { throw "The public update proof requires -UpdateSetupPath, -UpdateVersion and -UpdateRevision." }
    }
    if ($UpdateRevision -notmatch '^[0-9a-f]{40}$') { throw "-UpdateRevision must be a full source revision." }
    if ($Version -notmatch '^\d+\.\d+\.\d+$' -or $UpdateVersion -notmatch '^\d+\.\d+\.\d+$' -or
        [version]$UpdateVersion -le [version]$Version) { throw "The update must be a newer numeric public release." }
    $UpdateSetupPath = (Resolve-Path -LiteralPath $UpdateSetupPath).Path
}
$publicUpdateFeed = "https://github.com/kombifyio/TechStack/releases/latest/download/kombify-techstack-windows-update.json"
$clientConfigPath = Join-Path $StateDir "client.json"
$updatesDir = Join-Path $StateDir "updates"
$pendingPath = Join-Path $updatesDir "pending.json"
$updateObservations = $null
$reenableFirewallProfiles = @()
if ([string]::IsNullOrWhiteSpace($DiagnosticsDir)) {
    $DiagnosticsDir = Join-Path ([IO.Path]::GetTempPath()) "kombify-techstack-smoke-$smokeId"
}
New-Item -ItemType Directory -Force -Path $DiagnosticsDir | Out-Null

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

function Get-SmokeProcesses {
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

    return $processes
}

function Stop-SmokeProcesses {
    param([string]$InstallDir, [string]$StateDir)
    $processes = @(Get-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir)
    # Keep process handles until exit: termination is asynchronous on Windows.
    # Restarting against a WebView profile still held by the old process can
    # prevent WebView initialization before the local runtime even starts.
    $stopping = @($processes | ForEach-Object {
        Get-Process -Id $_.ProcessId -ErrorAction SilentlyContinue
    })
    foreach ($process in $stopping) {
        Stop-Process -InputObject $process -Force -ErrorAction SilentlyContinue
    }
    foreach ($process in $stopping) {
        try {
            if (!$process.WaitForExit(30000)) {
                throw "Smoke-owned process did not stop before restart: $($process.Id)"
            }
        } finally {
            $process.Dispose()
        }
    }
}

function Wait-Endpoint {
    param(
        [string]$Url,
        # The local runtime only binds its HTTP listener AFTER embedded Postgres
        # is ready (TECHSTACK_EMBEDDED_POSTGRES_START_TIMEOUT_SECONDS=120),
        # then control-plane migrations and the
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

function Initialize-SmokeCredentialReader {
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

    public static bool Exists(string target)
    {
        IntPtr pointer;
        if (!CredRead(target, 1, 0, out pointer)) {
            int error = Marshal.GetLastWin32Error();
            if (error == 1168) return false;
            throw new Win32Exception(error, "Credential existence check failed");
        }
        CredFree(pointer);
        return true;
    }

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

}

function Get-WindowsGenericCredentialSecret {
    param([Parameter(Mandatory = $true)][string]$Target)
    Initialize-SmokeCredentialReader
    return [KombifyCredentialReader]::Read($Target)
}

function Assert-WebViewDebugOwner {
    param([Parameter(Mandatory = $true)][int]$ClientProcessId)

    $listeners = @(Get-NetTCPConnection -LocalPort $webViewDebugPort -State Listen -ErrorAction Stop)
    $statePrefix = (Get-NormalizedPath $StateDir) + [IO.Path]::DirectorySeparatorChar
    foreach ($listener in $listeners) {
        if ($listener.LocalAddress -notin @("127.0.0.1", "::1")) {
            throw "WebView debugging must remain on loopback."
        }
        $owner = Get-CimInstance Win32_Process -Filter "ProcessId = $($listener.OwningProcess)"
        if ($owner.Name -ne "msedgewebview2.exe" -or
            !$owner.CommandLine -or $owner.CommandLine.IndexOf($statePrefix, [StringComparison]::OrdinalIgnoreCase) -lt 0) {
            throw "Debug port does not belong to the isolated WebView state."
        }
        $ancestorId = $owner.ParentProcessId
        for ($depth = 0; $depth -lt 16 -and $ancestorId -ne $ClientProcessId -and $ancestorId -gt 0; $depth++) {
            $ancestor = Get-CimInstance Win32_Process -Filter "ProcessId = $ancestorId"
            if (!$ancestor) { break }
            $ancestorId = $ancestor.ParentProcessId
        }
        if ($ancestorId -ne $ClientProcessId) {
            throw "Debug port owner is not a child of the installed client."
        }
    }
    if (!$listeners.Count) { throw "The installed WebView has no debugging listener." }
}

function Invoke-LocalUiSmoke {
    param(
        [Parameter(Mandatory = $true)][int]$ClientProcessId,
        [switch]$CreateMarker
    )

    $env:TECHSTACK_SMOKE_MARKER_CREATE = if ($CreateMarker) { "1" } else { "0" }
    Wait-Endpoint "http://127.0.0.1:$webViewDebugPort/json/version" | Out-Null
    Assert-WebViewDebugOwner -ClientProcessId $ClientProcessId
    $script = @'
const { chromium, expect } = require("@playwright/test");

(async () => {
  // Attach to the installed shell's actual WebView and its native session.
  // No test-created browser context, cookie, or manual sign-in is involved.
  const browser = await chromium.connectOverCDP(process.env.TECHSTACK_SMOKE_CDP_URL);
  const baseURL = process.env.TECHSTACK_SMOKE_BASE_URL || "http://127.0.0.1:5260";
  const context = browser.contexts()[0];
  const page = context.pages()[0] || await context.waitForEvent("page");
  await expect(page).toHaveURL(url => url.origin === baseURL &&
    /^\/(?:dashboard|stacks)(?:\/|$)/.test(url.pathname), { timeout: 30000 });
  await page.waitForLoadState("load");
  const identity = await page.evaluate(async () => {
    const response = await fetch("/api/v2/whoami", { credentials: "include" });
    if (!response.ok) throw new Error(`native session identity failed ${response.status}`);
    return response.json();
  });
  if (!identity.subject || identity.role !== "admin") {
    throw new Error("Native WebView has no local operator session");
  }
  // Retained state: the first start stores an encrypted Wallet secret (it
  // needs the runtime encryption key from Credential Manager); later starts
  // must still list it.
  const markerId = await page.evaluate(async ({ name, create }) => {
    if (create) {
      const csrf = await fetch("/api/v1/csrf", { credentials: "include" });
      if (!csrf.ok) throw new Error(`csrf token failed ${csrf.status}`);
      const token = csrf.headers.get("x-csrf-token") || (await csrf.json()).token;
      const created = await fetch("/api/v1/wallet", {
        method: "POST",
        credentials: "include",
        headers: { "content-type": "application/json", "x-csrf-token": token },
        body: JSON.stringify({ name, kind: "password", username: "retained-state", secret: crypto.randomUUID() }),
      });
      if (!created.ok) throw new Error(`encrypted wallet write failed ${created.status}`);
    }
    const listed = await fetch("/api/v1/wallet", { credentials: "include" });
    if (!listed.ok) throw new Error(`wallet read failed ${listed.status}`);
    const items = (await listed.json())?.data?.items || [];
    const marker = items.find(item => item.name === name && item.has_secret === true);
    if (!marker) throw new Error("retained encrypted wallet item is missing");
    return marker.id;
  }, { name: process.env.TECHSTACK_SMOKE_MARKER_NAME, create: process.env.TECHSTACK_SMOKE_MARKER_CREATE === "1" });
  await context.route("**/*", route => {
    const url = new URL(route.request().url());
    return url.origin === baseURL ? route.continue() : route.abort("internetdisconnected");
  });
  const failures = [];
  page.on("response", (response) => {
    if (response.url().includes("/_app/") && response.status() >= 400) {
      failures.push(`${response.status()} ${response.url()}`);
    }
  });
  page.on("requestfailed", (request) => {
    // net::ERR_ABORTED is a browser-side cancellation (a navigation replacing
    // the document, or SvelteKit dropping a superseded preload), not a missing
    // or unreachable asset. Missing assets surface as HTTP >= 400 above,
    // unreachable ones as connection or offline errors here, and broken pages
    // as pageerror or a missing landmark below.
    if (request.url().includes("/_app/") && request.failure()?.errorText !== "net::ERR_ABORTED") {
      failures.push(`${request.url()} ${JSON.stringify(request.failure())}`);
    }
  });
  page.on("pageerror", (error) => {
    failures.push(`pageerror ${error.message}`);
  });

  await page.reload({ waitUntil: "load" });
  await expect(page.getByTestId("stacks-dashboard")).toBeVisible({ timeout: 30000 });
  await page.goto(`${baseURL}/wallet`, { waitUntil: "load" });
  await expect(page.locator("[data-wallet-tab-nav]")).toBeVisible({ timeout: 30000 });
  await page.goto(`${baseURL}/stacks/new?target=new`, { waitUntil: "load" });
  await expect(page.locator('[data-testid="easy-wizard"]')).toBeVisible({ timeout: 30000 });
  await browser.close();

  if (failures.length > 0) {
    throw new Error(`frontend asset/page failures:\n${failures.join("\n")}`);
  }
  console.log(JSON.stringify({ ...identity, markerId }));
})().catch((error) => {
  console.error(error);
  process.exit(1);
});
'@
    $identityJson = Invoke-NodeSmoke $script
    Write-Host "[windows-client-installed-smoke] installed WebView device UI ok; external browser requests blocked"
    return ($identityJson | ConvertFrom-Json)
}

function Assert-InstalledRuntime {
    param([string]$ExpectVersion = $Version, [string]$ExpectRevision = $ExpectedRevision)
    $info = (Invoke-RestMethod -Uri "$localOrigin/api/v1/info" -TimeoutSec 10).data
    if ($info.environment -ne "local" -or $info.database_backend -ne "postgres" -or
        $info.revision -ne $ExpectRevision -or $info.version -ne $ExpectVersion) {
        throw "Installed runtime does not match the expected local PostgreSQL artifact ($ExpectVersion, $ExpectRevision)."
    }
    $auth = (Invoke-RestMethod -Uri "$localOrigin/api/v1/auth/mode" -TimeoutSec 10).data
    if ($auth.mode -ne 'local' -or $auth.edition -ne 'selfhost-oss' -or $auth.deployment_mode -ne 'self-hosted' -or
        $auth.cloud_auth_url -or $auth.portal_url -or !$auth.allow_local_login) {
        throw 'The installed runtime is not account-free selfhost OSS.'
    }
    if ($updateObservations) {
        $script:updateObservations.accountFree = @{ mode = $auth.mode; edition = $auth.edition
            deploymentMode = $auth.deployment_mode; cloudAuthUrlAbsent = !$auth.cloud_auth_url
            portalAbsent = !$auth.portal_url; offlineFirstStart = [bool]$BlockOutbound }
    }
    Write-Host "[windows-client-installed-smoke] artifact version=$($info.version) revision=$($info.revision) backend=$($info.database_backend)"
}

function Invoke-ShippedSetup {
    param([Parameter(Mandatory = $true)][string]$Action)

    $arguments = @("/quiet", "/norestart", "/log", (Join-Path $DiagnosticsDir "setup-$Action.log"))
    if ($Action -eq "uninstall") { $arguments = @("/uninstall") + $arguments }
    $setup = Start-Process -FilePath $SetupPath -ArgumentList $arguments -Wait -PassThru
    if ($setup.ExitCode -notin @(0, 3010)) {
        throw "kombify-Techstack-Setup.exe $Action failed with exit code $($setup.ExitCode)."
    }
}

function Assert-PrivateLANEnrollmentFirewall {
    param([string]$ExpectedShellVersion = "$Version+$ExpectedRevision")
    if ((Get-InstalledShellVersion) -ne $ExpectedShellVersion) {
        throw 'Enrollment firewall assertion requires the exact installed candidate shell.'
    }
    $rules = @(Get-NetFirewallRule -DisplayName 'kombify Techstack private LAN enrollment' -ErrorAction Stop)
    if ($rules.Count -ne 1) { throw 'Expected exactly one installed LAN enrollment firewall rule.' }
    $rule = $rules[0]
    $port = $rule | Get-NetFirewallPortFilter
    $address = $rule | Get-NetFirewallAddressFilter
    $remoteAddresses = @($address.RemoteAddress)
    if ([string]$rule.Profile -ne 'Private' -or [string]$rule.Enabled -ne 'True' -or
        [string]$rule.Direction -ne 'Inbound' -or [string]$rule.Action -ne 'Allow' -or
        [string]$port.Protocol -ne 'TCP' -or [string]$port.LocalPort -ne '5264' -or
        $remoteAddresses.Count -ne 1 -or [string]$remoteAddresses[0] -ne 'LocalSubnet') {
        throw 'Installed LAN enrollment firewall must allow only Private inbound TCP/5264 from LocalSubnet.'
    }
    if ((Get-InstalledShellVersion) -ne $ExpectedShellVersion) {
        throw 'Installed candidate changed while observing its enrollment firewall rule.'
    }
    [ordered]@{ shellProductVersion = $ExpectedShellVersion; profile = [string]$rule.Profile; protocol = [string]$port.Protocol;
        port = [string]$port.LocalPort; remoteAddress = $remoteAddresses } |
        ConvertTo-Json | Set-Content -LiteralPath (Join-Path $DiagnosticsDir 'installed-firewall.json') -Encoding utf8
    Write-Host '[windows-client-installed-smoke] installed LAN enrollment firewall is Private TCP/5264 LocalSubnet'
}

function Enable-OutboundBlock {
    $profiles = @(Get-NetFirewallProfile)
    $script:reenableFirewallProfiles = @($profiles | Where-Object { $_.Enabled -ne $true } | ForEach-Object { $_.Name })
    if ($script:reenableFirewallProfiles.Count -gt 0) {
        Set-NetFirewallProfile -Name $script:reenableFirewallProfiles -Enabled True
    }
    $webView = @(Get-ChildItem -Path (Join-Path ${env:ProgramFiles(x86)} "Microsoft\EdgeWebView\Application") `
        -Filter msedgewebview2.exe -Recurse -File -ErrorAction SilentlyContinue | ForEach-Object FullName)
    # Everything except 127.0.0.0/8 and ::1: the local UI, runtime and
    # embedded PostgreSQL stay reachable, every other destination is refused.
    $remote = @("0.0.0.0-126.255.255.255", "128.0.0.0-255.255.255.255", "::2-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")
    foreach ($program in @($clientExe, $runtimeExe) + $webView) {
        New-NetFirewallRule -DisplayName "kombify Techstack smoke offline $(Split-Path -Leaf $program)" `
            -Group $firewallGroup -Direction Outbound -Action Block -Profile Any `
            -Program $program -RemoteAddress $remote | Out-Null
    }
    Write-Host "[windows-client-installed-smoke] outbound network blocked for client, runtime and $($webView.Count) WebView2 executable(s)"
}

function Disable-OutboundBlock {
    Get-NetFirewallRule -Group $firewallGroup -ErrorAction SilentlyContinue | Remove-NetFirewallRule
    if ($script:reenableFirewallProfiles.Count -gt 0) {
        Set-NetFirewallProfile -Name $script:reenableFirewallProfiles -Enabled False
    }
}

# Atomic changes preserve unrelated settings and prevent the install
# coordinator's shell reading a partial isolated configuration.
function Set-SmokeClientConfig([hashtable]$Changes) {
    $config = if (Test-Path -LiteralPath $clientConfigPath) {
        Get-Content -Raw -LiteralPath $clientConfigPath | ConvertFrom-Json -AsHashtable
    } else { @{} }
    foreach ($key in $Changes.Keys) { $config[$key] = $Changes[$key] }
    $temporary = "$clientConfigPath.$smokeId.tmp"
    try {
        [IO.File]::WriteAllText($temporary, ($config | ConvertTo-Json -Depth 20))
        [IO.File]::Move($temporary, $clientConfigPath, $true)
    } finally { Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue }
}

function Assert-PublicUpdateFeed {
    $manifest = Invoke-RestMethod -Uri $publicUpdateFeed -TimeoutSec 30
    $digest = (Get-FileHash -Algorithm SHA256 -LiteralPath $UpdateSetupPath).Hash.ToLowerInvariant()
    if ($manifest.version -ne $UpdateVersion -or $manifest.channel -ne 'stable' -or
        $manifest.package_url -ne "https://github.com/kombifyio/TechStack/releases/download/v$UpdateVersion/kombify-Techstack-Setup.exe" -or
        $manifest.package_sha256 -ne $digest -or $manifest.package_size -ne (Get-Item -LiteralPath $UpdateSetupPath).Length) {
        throw "The public feed drifted from pinned update v$UpdateVersion; no other release is admitted."
    }
}

function Get-InstalledShellVersion {
    if (!(Test-Path -LiteralPath $clientExe)) { return '' }
    return [Diagnostics.FileVersionInfo]::GetVersionInfo($clientExe).ProductVersion
}

function Wait-SmokeRuntimeStopped {
    $deadline = (Get-Date).AddSeconds(45)
    $statePrefix = (Get-NormalizedPath $StateDir) + [IO.Path]::DirectorySeparatorChar
    do {
        $ports = @(Get-NetTCPConnection -LocalPort $LocalPort, $localGrpcPort -State Listen -ErrorAction SilentlyContinue)
        $postgres = @(Get-CimInstance Win32_Process -Filter "Name = 'postgres.exe'" | Where-Object {
            $_.ExecutablePath -and $_.ExecutablePath.StartsWith($statePrefix, [StringComparison]::OrdinalIgnoreCase)
        })
        if (!$ports.Count -and !$postgres.Count) { return }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    throw 'The local runtime or embedded PostgreSQL still runs; update admission would defer.'
}

function Read-SmokePendingUpdate {
    # Cursor replacement is atomic, and successful recovery deletes it. A
    # deletion between existence check and read is a normal commit transition.
    try { $raw = [IO.File]::ReadAllText($pendingPath) }
    catch [IO.FileNotFoundException] { return $null }
    catch [IO.DirectoryNotFoundException] { return $null }
    $pending = $raw | ConvertFrom-Json
    if ($pending.from -ne $Version -or $pending.to -ne $UpdateVersion -or
        $pending.phase -notin @('applying', 'rolling-back')) {
        throw 'The recovery cursor does not bind the pinned public releases.'
    }
    return $pending
}

function Wait-InstalledRollback {
    $snapshot = Join-Path $updatesDir 'snapshot'
    $sentinel = Join-Path $StateDir "runtime\rollback-sentinel-$smokeId.txt"
    $added = Join-Path $StateDir "runtime\rollback-added-$smokeId.txt"
    $original = "before-update-$smokeId"
    # This non-log sidecar proves snapshot restoration without modifying SQL.
    [IO.File]::WriteAllText($sentinel, $original)
    Set-SmokeClientConfig @{ autoStartRuntime = $false }
    $script:updateObservations.fault = 'autoStartRuntime=false in isolated client configuration'
    Start-Process -FilePath $clientExe -WindowStyle Hidden | Out-Null
    $applying = $false
    $rollingBack = $false
    $deadline = (Get-Date).AddMinutes(15)
    do {
        $pending = Read-SmokePendingUpdate
        if ($pending -and !$applying) {
            if ($pending.phase -ne 'applying' -or !(Test-Path -LiteralPath (Join-Path $snapshot (Split-Path -Leaf $sentinel)))) {
                throw 'Applying cursor and pre-update snapshot were not observed; restoration proof is inconclusive.'
            }
            [IO.File]::WriteAllText($sentinel, 'changed-after-snapshot')
            [IO.File]::WriteAllText($added, 'created-after-snapshot')
            $afterMutation = Read-SmokePendingUpdate
            if (!$afterMutation -or ($afterMutation.phase -eq 'rolling-back' -and
                (Get-InstalledShellVersion).Split('+')[0] -ne $UpdateVersion)) {
                throw 'Snapshot mutation was not proven to precede recovery of the original installation.'
            }
            $applying = $true
            $script:updateObservations.phases += 'applying'
        }
        if ($pending.phase -eq 'rolling-back' -and !$rollingBack) {
            $shellVersion = Get-InstalledShellVersion
            if ($shellVersion.Split('+')[0] -ne $UpdateVersion) {
                throw 'The updated installed shell was not observed when it committed rollback.'
            }
            Assert-PrivateLANEnrollmentFirewall -ExpectedShellVersion "$UpdateVersion+$UpdateRevision"
            Assert-PublicUpdateFeed
            $rollingBack = $true
            $script:updateObservations.phases += 'rolling-back'
            $script:updateObservations.updatedShellProductVersion = $shellVersion
        }
        $rollbackLine = "client update rolled back to $Version; runtime data restored from the pre-update snapshot"
        $committed = $applying -and $rollingBack -and !$pending -and
            (Get-InstalledShellVersion).Split('+')[0] -eq $Version -and
            !(Test-Path -LiteralPath $snapshot) -and
            (Test-Path -LiteralPath $runtimeLog) -and
            (Select-String -LiteralPath $runtimeLog -Pattern $rollbackLine -SimpleMatch -Quiet)
        if ($committed) {
            if ((Get-Content -Raw -LiteralPath $sentinel) -cne $original -or (Test-Path -LiteralPath $added)) {
                throw 'Rollback did not restore the pre-update sentinel and remove the post-snapshot file.'
            }
            if (!(Test-Path -LiteralPath (Join-Path $updatesDir "$UpdateVersion\failed"))) {
                throw 'Rollback did not mark the failed update.'
            }
            Assert-PublicUpdateFeed
            $script:updateObservations.phases += 'recovery-committed'
            $script:updateObservations.snapshotSentinelRestored = $true
            $script:updateObservations.postSnapshotFileRemoved = $true
            $script:updateObservations.rollbackLog = $rollbackLine
            # Preserve the fault through original reinstall AND recovery commit.
            # Explicitly relaunch after stopping the coordinator-launched shell.
            Stop-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir
            Wait-SmokeRuntimeStopped
            Set-SmokeClientConfig @{ autoStartRuntime = $true }
            return Start-Process -FilePath $clientExe -WindowStyle Hidden -PassThru
        }
        Start-Sleep -Milliseconds 200
    } while ((Get-Date) -lt $deadline)
    throw 'The installed client did not commit automatic rollback of the pinned update.'
}

function Get-InstalledClientProcess {
    $installPrefix = (Get-NormalizedPath $InstallDir) + [IO.Path]::DirectorySeparatorChar
    return Get-CimInstance Win32_Process -Filter "Name = 'kombify-techstack-client.exe'" |
        Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($installPrefix, [StringComparison]::OrdinalIgnoreCase) } |
        Select-Object -First 1
}

function Save-SmokeDiagnostics {
    $runtimeRoot = Join-Path $StateDir "runtime"
    if (!(Test-Path -LiteralPath $runtimeRoot)) { return }
    Get-ChildItem -LiteralPath $runtimeRoot -Recurse -File -Include *.log, *.jsonl -ErrorAction SilentlyContinue |
        Where-Object { $_.FullName -notmatch '\\postgres\\data\\' -and $_.Length -lt 50MB } |
        ForEach-Object {
            $relative = $_.FullName.Substring($runtimeRoot.Length).TrimStart('\') -replace '[\\/]', '__'
            Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $DiagnosticsDir $relative) -Force
        }
}

if (!$PackageOnly) { Assert-AppDependencies }

foreach ($requiredPort in @($LocalPort, $localGrpcPort, $webViewDebugPort)) {
    $occupiedLocalRuntime = Get-NetTCPConnection -LocalPort $requiredPort -State Listen -ErrorAction SilentlyContinue
    if ($occupiedLocalRuntime) {
        $owners = ($occupiedLocalRuntime | Select-Object -ExpandProperty OwningProcess -Unique) -join ","
        throw "Refusing to run installed-client smoke while port $requiredPort is already in use (pid=$owners). Pass -LocalPort to use a free port pair."
    }
}

if (!$SkipPackage) {
    & $packageScript -Version $Version -SpecTemplatesPath $SpecTemplatesPath
}
$installerPath = if ($Installer -eq "setup") { $SetupPath } else { $installScript }
if (!(Test-Path -LiteralPath $installerPath)) {
    throw "Missing $Installer installer: $installerPath"
}
# Burn re-launches itself from a cache and resolves its payload from the
# original location, so it needs an absolute path.
if ($Installer -eq "setup") { $SetupPath = (Resolve-Path -LiteralPath $SetupPath).Path }

if ($B2ContextPath -or $B2HostPath) {
    if (!$B2ContextPath -or !$B2HostPath) { throw 'Both authenticated B2 context and actual host binding are required.' }
    & node (Join-Path $PSScriptRoot 'test-window/windows-b2-receipt.mjs') --require-full-context $B2ContextPath
    $b2Host = Get-Content -Raw -LiteralPath $B2HostPath | ConvertFrom-Json
    $machine = (Get-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\Microsoft\Cryptography' -Name MachineGuid).MachineGuid
    $actualHost = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData(
        [Text.Encoding]::UTF8.GetBytes("$machine/$env:GITHUB_RUN_ID/$env:GITHUB_RUN_ATTEMPT"))).ToLowerInvariant()
    $machine = $null
    if ($b2Host.fingerprint -ne $actualHost -or $b2Host.run -ne "$env:GITHUB_RUN_ID.$env:GITHUB_RUN_ATTEMPT" -or
        $b2Host.harness -ne $env:GITHUB_SHA) { throw 'The actual installed smoke host differs from native B2.' }
}
if (!$PackageOnly) { Initialize-SmokeCredentialReader }
$installed = $false
$flowPassed = $false
try {
    Stop-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir
    if (!$PackageOnly) {
        & $resetScript -StateDir $StateDir -InstallDir $InstallDir -IncludeClientConfig -Discard
    }

    if ($Installer -eq "setup") {
        Invoke-ShippedSetup -Action install
    } else {
        & $installScript -InstallDir $InstallDir -StateDir $StateDir -Mode local -NoShortcuts `
            -LocalUiUrl "$localOrigin/" `
            -LocalOnboardingUrl "$localOrigin/client/local?client=windows"
    }
    $installed = $true
    if ($updateRequested) {
        New-Item -ItemType Directory -Force -Path $StateDir | Out-Null
        Set-SmokeClientConfig @{ mode = 'local'; updateManifestUrl = $publicUpdateFeed; updateChannel = 'stable'; autoUpdate = $false }
        $updateObservations = [ordered]@{
            schema = 'techstack.installed-update-observations/v2'
            result = 'PENDING'; mode = $UpdateProof; repository = 'kombifyio/TechStack'
            binding = if ($B2ContextPath) { Get-Content -Raw -LiteralPath $B2ContextPath | ConvertFrom-Json -AsHashtable } else { $null }
            host = if ($B2HostPath) { Get-Content -Raw -LiteralPath $B2HostPath | ConvertFrom-Json -AsHashtable } else { $null }
            statePath = $StateDir; smokeId = $smokeId; initialStateAbsent = $stateInitiallyAbsent
            workflowRun = $env:GITHUB_RUN_ID; workflowAttempt = $env:GITHUB_RUN_ATTEMPT; harnessRevision = $env:GITHUB_SHA
            from = @{ tag = "v$Version"; version = $Version; publicSource = $ExpectedRevision
                setupSha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $SetupPath).Hash.ToLowerInvariant() }
            to = @{ tag = "v$UpdateVersion"; version = $UpdateVersion; publicSource = $UpdateRevision
                setupSha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $UpdateSetupPath).Hash.ToLowerInvariant() }
            phases = @(); startedAt = (Get-Date).ToUniversalTime().ToString('o')
            scope = if ($UpdateProof -eq 'rollback') { 'installed recovery with runtime startup disabled; no migration rollback claim' }
                else { 'installed public-feed upgrade and retained owner/Wallet state' }
        }
    }
    if (!(Test-Path -LiteralPath $clientExe) -or !(Test-Path -LiteralPath $runtimeExe)) {
        throw "The $Installer installer did not install the client and runtime into $InstallDir"
    }
    Write-Host "[windows-client-installed-smoke] installed through the $Installer installer at $InstallDir"
    # Update proofs start from an authenticated historical package. Check the
    # actual candidate after its installation, not the intentionally old baseline.
    if ($Installer -eq 'setup' -and !$updateRequested) { Assert-PrivateLANEnrollmentFirewall }
    if ($PackageOnly) {
        if (@(Get-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir).Count -or
            (Test-Path -LiteralPath (Join-Path $StateDir 'runtime'))) {
            throw 'Package-only installation unexpectedly started the product or created runtime state.'
        }
        Write-Host '[windows-client-installed-smoke] package-only checks passed; runtime and B2 not run'
        $flowPassed = $true
        return
    }
    if ($BlockOutbound) {
        Enable-OutboundBlock
    }

    $env:TECHSTACK_CLIENT_STATE_DIR = $StateDir
    $env:TECHSTACK_SMOKE_BASE_URL = $localOrigin
    $env:TECHSTACK_SMOKE_CDP_URL = "http://127.0.0.1:$webViewDebugPort"
    $env:TECHSTACK_SMOKE_MARKER_NAME = "installed-smoke-$smokeId"
    $env:WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS = "--remote-debugging-address=127.0.0.1 --remote-debugging-port=$webViewDebugPort"
    $process = Start-Process -FilePath $clientExe -WindowStyle Hidden -PassThru
    Write-Host "[windows-client-installed-smoke] started client pid=$($process.Id)"
    Wait-Endpoint "$localOrigin/api/v1/auth/mode" -TimeoutSeconds 240 | Out-Null
    Assert-InstalledRuntime
    $runtimeLog = Join-Path $StateDir "runtime\techstack-runtime.log"
    Wait-LogLine -Path $runtimeLog -Pattern "local device session restored"

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
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $deviceDigest = $sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($deviceToken))
        $expectedSubject = "device:" + ([BitConverter]::ToString($deviceDigest)).Replace("-", "").ToLowerInvariant()
    } finally { $sha.Dispose() }
    $firstIdentity = Invoke-LocalUiSmoke -ClientProcessId $process.Id -CreateMarker
    if ($firstIdentity.subject -ne $expectedSubject) {
        throw "Native WebView identity does not match this installation's device credential."
    }
    if ($updateRequested) {
        $updateObservations.originalShellProductVersion = Get-InstalledShellVersion
        if ($updateObservations.originalShellProductVersion.Split('+')[0] -ne $Version) {
            throw 'The original installed shell does not match the pinned public release.'
        }
        Assert-PublicUpdateFeed
        Stop-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir
        Wait-SmokeRuntimeStopped
        # Offline first start has passed. Only this run's outbound rules are
        # removed; the real public feed and signed recovery packages stay reachable.
        if ($BlockOutbound) { Get-NetFirewallRule -Group $firewallGroup -ErrorAction SilentlyContinue | Remove-NetFirewallRule }
        Set-SmokeClientConfig @{ autoUpdate = $true }
        $process = Start-Process -FilePath $clientExe -WindowStyle Hidden -PassThru
        Wait-Endpoint "$localOrigin/api/v1/auth/mode" | Out-Null
        Assert-InstalledRuntime
    }

    $restartVersion = $Version
    $restartRevision = $ExpectedRevision
    if ($updateRequested) {
        $stageDescriptor = Join-Path $StateDir "updates\$UpdateVersion\stage.json"
        $stagedManifest = Join-Path $StateDir "updates\$UpdateVersion\manifest.json"
        $stageDeadline = (Get-Date).AddMinutes(10)
        while ((!(Test-Path -LiteralPath $stageDescriptor) -or !(Test-Path -LiteralPath $stagedManifest)) -and (Get-Date) -lt $stageDeadline) {
            Start-Sleep -Seconds 2
        }
        if (!(Test-Path -LiteralPath $stageDescriptor) -or !(Test-Path -LiteralPath $stagedManifest)) {
            throw "The installed client did not stage the signed update $UpdateVersion."
        }
        Assert-PublicUpdateFeed
        $verifiedUpdate = & $runtimeExe client-update --staging-dir $updatesDir --channel stable --verify-staged-version $UpdateVersion | ConvertFrom-Json
        if ($LASTEXITCODE -ne 0 -or $verifiedUpdate.status -ne 'verified-update' -or
            $verifiedUpdate.package_sha256 -ne $updateObservations.to.setupSha256) {
            throw 'The installed verifier did not authenticate the pinned public update package.'
        }
        $verifiedRecovery = & $runtimeExe client-update --staging-dir (Join-Path $updatesDir 'rollback') --channel stable `
            --verify-staged-version $Version --recovery-version $Version | ConvertFrom-Json
        if ($LASTEXITCODE -ne 0 -or $verifiedRecovery.status -ne 'verified-recovery' -or
            $verifiedRecovery.package_sha256 -ne $updateObservations.from.setupSha256) {
            throw 'The installed verifier did not authenticate the original public recovery package.'
        }
        $updateObservations.signedPackageVerification = @{
            update = @{ status = $verifiedUpdate.status; sha256 = $verifiedUpdate.package_sha256; size = $verifiedUpdate.package_size }
            recovery = @{ status = $verifiedRecovery.status; sha256 = $verifiedRecovery.package_sha256; size = $verifiedRecovery.package_size }
        }
        $updateObservations.phases += 'signed-update-and-recovery-staged'
        Write-Host "[windows-client-installed-smoke] installed client verified and staged update $UpdateVersion"
        $restartVersion = $UpdateVersion
        $restartRevision = $UpdateRevision
    }

    Stop-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir
    if ($updateRequested) { Wait-SmokeRuntimeStopped }
    Move-Item -LiteralPath $runtimeLog -Destination (Join-Path $StateDir "runtime\techstack-runtime-first-start.log")
    if ($UpdateProof -eq 'rollback') {
        $process = Wait-InstalledRollback
        $restartVersion = $Version
        $restartRevision = $ExpectedRevision
    } else {
        $process = Start-Process -FilePath $clientExe -WindowStyle Hidden -PassThru
    }
    Write-Host "[windows-client-installed-smoke] restarted client pid=$($process.Id)"
    if ($UpdateProof -eq 'public-feed') {
        # The previous version applies the staged installer and restarts as
        # the new version; its runtime must come back on the retained data.
        $updateDeadline = (Get-Date).AddMinutes(10)
        $observedVersion = ""
        while ((Get-Date) -lt $updateDeadline) {
            try {
                $observedVersion = (Invoke-RestMethod -Uri "$localOrigin/api/v1/info" -TimeoutSec 3).data.version
                if ($observedVersion -eq $UpdateVersion) { break }
            } catch {
                $observedVersion = ""
            }
            Start-Sleep -Seconds 3
        }
        if ($observedVersion -ne $UpdateVersion) {
            throw "The client did not come back as $UpdateVersion after applying its staged update (last seen: $observedVersion)."
        }
        $process = Get-InstalledClientProcess
        if (!$process) { throw "The updated client is not running from $InstallDir." }
        $process = Get-Process -Id $process.ProcessId
    }
    Wait-Endpoint "$localOrigin/api/v1/auth/mode" | Out-Null
    Assert-InstalledRuntime -ExpectVersion $restartVersion -ExpectRevision $restartRevision
    if ($UpdateProof -eq 'public-feed') {
        Assert-PrivateLANEnrollmentFirewall -ExpectedShellVersion "$UpdateVersion+$UpdateRevision"
    }
    Wait-LogLine -Path $runtimeLog -Pattern "local device session restored"
    if ($UpdateProof -eq 'public-feed') {
        Wait-LogLine -Path $runtimeLog -Pattern "client update applied: $Version -> $UpdateVersion"
        if (Test-Path -LiteralPath (Join-Path $StateDir "updates\pending.json")) {
            throw "The applied update left its pending marker behind."
        }
        Write-Host "[windows-client-installed-smoke] update $Version -> $UpdateVersion applied by the installed client"
        Assert-PublicUpdateFeed
        $updateObservations.phases += 'update-committed'
    }
    $restartedIdentity = Invoke-LocalUiSmoke -ClientProcessId $process.Id
    if ($restartedIdentity.subject -ne $firstIdentity.subject -or
        $restartedIdentity.tenantId -ne $firstIdentity.tenantId -or
        $restartedIdentity.email -ne $firstIdentity.email) {
        throw "Local operator identity changed after client restart."
    }
    if ($restartedIdentity.markerId -ne $firstIdentity.markerId) {
        throw "Retained encrypted state changed after client restart."
    }
    Write-Host "[windows-client-installed-smoke] restart retained the operator identity and encrypted Wallet state"

    if ($updateRequested) {
        if (Test-Path -LiteralPath $pendingPath) { throw 'Recovery remains pending after the final runtime start.' }
        if ($UpdateProof -eq 'rollback' -and !(Test-Path -LiteralPath (Join-Path $updatesDir "$UpdateVersion\failed"))) {
            throw 'The recovered installation did not retain its failed-update marker.'
        }
        $updateObservations.finalShellProductVersion = Get-InstalledShellVersion
        if ($updateObservations.finalShellProductVersion.Split('+')[0] -ne $restartVersion) {
            throw 'The final installed shell and runtime versions differ.'
        }
        $updateObservations.finalRuntime = @{ version = $restartVersion; publicSource = $restartRevision }
        $updateObservations.identity = @{ subjectBefore = $firstIdentity.subject; subjectAfter = $restartedIdentity.subject
            tenantBefore = $firstIdentity.tenantId; tenantAfter = $restartedIdentity.tenantId
            markerBefore = $firstIdentity.markerId; markerAfter = $restartedIdentity.markerId }
        $updateObservations.identityRetained = $true
        $updateObservations.encryptedWalletMarkerRetained = $true
        $updateObservations.pendingConsumed = $true
        $updateObservations.result = 'PENDING_CLEANUP'
        $updateObservations.finishedAt = (Get-Date).ToUniversalTime().ToString('o')
        if ($UpdateProof -eq 'public-feed') { $SetupPath = $UpdateSetupPath }
    }
    if ($Installer -eq "setup" -and !$KeepInstall) {
        Stop-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir
        Invoke-ShippedSetup -Action uninstall
        $installed = $false
        if ((Test-Path -LiteralPath $clientExe) -or (Test-Path -LiteralPath $runtimeExe)) {
            throw "The shipped uninstaller left Techstack binaries in $InstallDir"
        }
        if (!(Test-Path -LiteralPath (Join-Path $StateDir "runtime\postgres\data\PG_VERSION"))) {
            throw "Uninstall removed the retained local PostgreSQL data."
        }
        if ((Get-WindowsGenericCredentialSecret -Target $deviceCredentialTarget).Trim() -ne $deviceToken) {
            throw "Uninstall removed the retained local device credential."
        }
        Write-Host "[windows-client-installed-smoke] uninstall removed the binaries and retained local data and credentials"
    }
    $flowPassed = $true
    Write-Host "[windows-client-installed-smoke] flow complete; reconciling cleanup"
} catch {
    if ($PackageOnly) {
        [ordered]@{ message = $_.Exception.Message; category = [string]$_.CategoryInfo.Category } |
            ConvertTo-Json | Set-Content -LiteralPath (Join-Path $DiagnosticsDir 'package-failure.json') -Encoding utf8
    }
    if ($updateObservations) {
        $updateObservations.result = 'FAIL'
        $updateObservations.failure = ($_.Exception.Message -split "`r?`n")[0]
        $updateObservations.finishedAt = (Get-Date).ToUniversalTime().ToString('o')
    }
    throw
} finally {
    # Every action gets its own guard: a diagnostics/uninstall/reset failure must
    # never prevent the remaining custody, firewall and process cleanup.
    $cleanupErrors = [Collections.Generic.List[string]]::new()
    $cleanup = [ordered]@{ result = 'PENDING'; errors = @(); errorDetails = @() }
    function Invoke-SmokeCleanup([string]$Name, [scriptblock]$Action) {
        try { & $Action | Out-Null } catch {
            $cleanupErrors.Add($Name)
            $cause = $_.Exception
            while ($cause.InnerException) { $cause = $cause.InnerException }
            # Keep errors actionable without logging credential values or messages.
            $cleanup.errorDetails += [ordered]@{ action = $Name; line = $_.InvocationInfo.ScriptLineNumber
                exceptionType = $cause.GetType().FullName; hresult = $cause.HResult
                win32 = if ($cause -is [ComponentModel.Win32Exception]) { $cause.NativeErrorCode } else { $null } }
        }
    }
    Invoke-SmokeCleanup 'stop-processes' { Stop-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir }
    Invoke-SmokeCleanup 'diagnostics' { Save-SmokeDiagnostics }
    if ($BlockOutbound) {
        Invoke-SmokeCleanup 'firewall-rules' {
            Get-NetFirewallRule -Group $firewallGroup -ErrorAction SilentlyContinue | Remove-NetFirewallRule
        }
        Invoke-SmokeCleanup 'firewall-profiles' {
            if ($script:reenableFirewallProfiles.Count -gt 0) {
                Set-NetFirewallProfile -Name $script:reenableFirewallProfiles -Enabled False
            }
        }
    }
    # PackageOnly returns before changing any product environment variable.
    if (!$PackageOnly) {
        foreach ($entry in $previousEnvironment.GetEnumerator()) {
            Invoke-SmokeCleanup ('environment-' + $entry.Key) {
                # PowerShell binds $null to an empty string; NullString restores absence.
                if ($null -eq $entry.Value) {
                    [Environment]::SetEnvironmentVariable($entry.Key, [NullString]::Value)
                } else {
                    [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value)
                }
            }
        }
    }
    if (!$KeepInstall) {
        Invoke-SmokeCleanup 'uninstall' {
            if ($Installer -eq 'setup') {
                # An installer may fail after laying down files, before $installed
                # was set. The initial setup location was required to be absent.
                if ($installed -or (Test-Path -LiteralPath $InstallDir)) {
                    if ($updateRequested -and (Test-Path -LiteralPath $clientExe) -and
                        (Get-InstalledShellVersion).Split('+')[0] -eq $UpdateVersion) { $script:SetupPath = $UpdateSetupPath }
                    Invoke-ShippedSetup -Action uninstall
                }
            } elseif (Test-Path -LiteralPath $InstallDir) {
                $resolvedInstallDir = Get-NormalizedPath ((Resolve-Path -LiteralPath $InstallDir).Path)
                if (!(Test-StrictChildPath $resolvedInstallDir $allowedInstallRoot) -or
                    $resolvedInstallDir.Equals($defaultProductInstall, [StringComparison]::OrdinalIgnoreCase)) {
                    throw 'Portable cleanup escaped its isolated boundary.'
                }
                Assert-NoReparsePointInBoundary $resolvedInstallDir $allowedInstallRoot
                Remove-Item -LiteralPath $resolvedInstallDir -Recurse -Force
            }
        }
    }
    # Stop again if an installer coordinator restarted the shell during uninstall.
    Invoke-SmokeCleanup 'stop-after-uninstall' { Stop-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir }
    if (!$KeepState) {
        if (!$PackageOnly) {
            Invoke-SmokeCleanup 'reset-state-and-credentials' {
                & $resetScript -StateDir $StateDir -InstallDir $InstallDir -IncludeClientConfig -Discard
            }
        }
        Invoke-SmokeCleanup 'remove-fresh-state' {
            if ($stateInitiallyAbsent -and (Test-Path -LiteralPath $StateDir)) {
                if (!(Test-StrictChildPath $StateDir $allowedStateRoot) -or $StateDir -eq $defaultProductState) {
                    throw 'State cleanup escaped its isolated boundary.'
                }
                Assert-NoReparsePointInBoundary $StateDir $allowedStateRoot
                Remove-Item -LiteralPath $StateDir -Recurse -Force
            }
        }
    }
    Invoke-SmokeCleanup 'readback' {
        $cleanup.processesAbsent = @(Get-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir).Count -eq 0
        $cleanup.portsFree = @(Get-NetTCPConnection -State Listen -ErrorAction Stop | Where-Object LocalPort -In @($LocalPort, $localGrpcPort, $webViewDebugPort)).Count -eq 0
        $cleanup.binariesAbsent = !(Test-Path -LiteralPath $clientExe) -and !(Test-Path -LiteralPath $runtimeExe)
        $cleanup.enrollmentFirewallAbsent = $Installer -ne 'setup' -or $KeepInstall -or
            @(Get-NetFirewallRule -DisplayName 'kombify Techstack private LAN enrollment' -ErrorAction SilentlyContinue).Count -eq 0
        $cleanup.firewallRulesAbsent = @(Get-NetFirewallRule -ErrorAction Stop | Where-Object Group -EQ $firewallGroup).Count -eq 0
        $cleanup.firewallProfilesRestored = @($script:reenableFirewallProfiles | ForEach-Object {
            Get-NetFirewallProfile -Name $_ | Where-Object Enabled
        }).Count -eq 0
        $cleanup.stateAbsent = !(Test-Path -LiteralPath $StateDir)
        $cleanup.credentialsAbsent = $false
        if ($PackageOnly) {
            # A package install never opens user custody. SSH logon sessions may
            # have no Credential Manager; do not claim an absence readback there.
            $cleanup.credentialsAbsent = $null
            $cleanup.credentialsNotUsed = $true
        } elseif ('KombifyCredentialReader' -as [type]) {
            $remaining = @(@('cloud/stack/access-token', 'cloud/stack/refresh-token', 'local/runtime-session-secret',
                'local/device-session-token', 'local/runtime-encryption-key') | ForEach-Object {
                $target = Get-StateScopedCredentialTarget -BaseTarget "kombify/techstack/$_" -StateDir $StateDir
                if ([KombifyCredentialReader]::Exists($target)) { $target }
            })
            $cleanup.credentialsAbsent = $remaining.Count -eq 0
        } else { throw 'Credential readback is unavailable.' }
        $cleanup.environmentMismatchNames = @($previousEnvironment.GetEnumerator() | Where-Object {
            ![object]::Equals([Environment]::GetEnvironmentVariable($_.Key), $_.Value)
        } | ForEach-Object { $_.Key })
        $cleanup.environmentRestored = $cleanup.environmentMismatchNames.Count -eq 0
        if (!$cleanup.processesAbsent -or !$cleanup.portsFree -or !$cleanup.firewallRulesAbsent -or !$cleanup.enrollmentFirewallAbsent -or
            !$cleanup.firewallProfilesRestored -or !$cleanup.environmentRestored -or
            (!$KeepInstall -and !$cleanup.binariesAbsent) -or (!$KeepState -and
            ((!$PackageOnly -and !$cleanup.credentialsAbsent) -or ($stateInitiallyAbsent -and !$cleanup.stateAbsent)))) {
            throw 'Smoke cleanup readback found residue.'
        }
    }
    Invoke-SmokeCleanup 'cleanup-receipt' {
        $cleanup.errors = $cleanupErrors.ToArray()
        $cleanup.result = if ($cleanupErrors.Count -eq 0) { 'PASS' } else { 'FAIL' }
        $cleanup | ConvertTo-Json -Depth 8 |
            Set-Content -LiteralPath (Join-Path $DiagnosticsDir 'cleanup-readback.json') -Encoding utf8
    }
    $cleanup.errors = $cleanupErrors.ToArray()
    $cleanup.result = if ($cleanupErrors.Count -eq 0) { 'PASS' } else { 'FAIL' }
    if ($PackageOnly) {
        $cleanup | ConvertTo-Json -Depth 8 |
            Set-Content -LiteralPath (Join-Path $DiagnosticsDir 'package-cleanup.json') -Encoding utf8
    }
    if ($updateObservations) {
        $updateObservations.cleanup = $cleanup
        $updateObservations.result = if ($flowPassed -and $cleanup.result -eq 'PASS') { 'PASS' } else { 'FAIL' }
        $updateObservations.finishedAt = (Get-Date).ToUniversalTime().ToString('o')
        $updateObservations | ConvertTo-Json -Depth 35 |
            Set-Content -LiteralPath (Join-Path $DiagnosticsDir 'update-observations.json') -Encoding UTF8
    }
    if ($cleanupErrors.Count -gt 0) { throw ('Smoke cleanup failed: ' + ($cleanupErrors -join ',')) }
}

# The native device lane admits success only after the full flow and cleanup.
if ($flowPassed -and !$PackageOnly) {
    Write-Host '[windows-client-installed-smoke] ok'
}
