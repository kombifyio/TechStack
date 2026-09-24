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
    # Windows Firewall rules for the whole run. Requires elevation.
    [switch]$BlockOutbound,
    # Receives runtime and PostgreSQL logs before the isolated state is removed.
    [string]$DiagnosticsDir = "",
    # Update proof (setup installer only): a newer Setup.exe and its update
    # manifest signed with the production update key for the package URL
    # https://127.0.0.1:18443/kombify-Techstack-Setup.exe. The installed client
    # must stage it through its own update check, apply it on the next start
    # and come back with the same identity and data.
    [string]$UpdateSetupPath = "",
    [string]$UpdateManifestPath = "",
    [string]$UpdateVersion = "",
    [string]$UpdateRevision = "",
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
$previousStateOverride = [Environment]::GetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR")
$previousWebViewArguments = [Environment]::GetEnvironmentVariable("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS")
$previousSmokeBaseUrl = [Environment]::GetEnvironmentVariable("TECHSTACK_SMOKE_BASE_URL")
$previousSmokeCdpUrl = [Environment]::GetEnvironmentVariable("TECHSTACK_SMOKE_CDP_URL")

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
$updateRequested = ![string]::IsNullOrWhiteSpace($UpdateSetupPath)
if ($updateRequested) {
    if ($Installer -ne "setup") { throw "The update proof requires -Installer setup." }
    foreach ($required in @($UpdateManifestPath, $UpdateVersion, $UpdateRevision)) {
        if ([string]::IsNullOrWhiteSpace($required)) { throw "The update proof requires -UpdateManifestPath, -UpdateVersion and -UpdateRevision." }
    }
    if ($UpdateRevision -notmatch '^[0-9a-f]{40}$') { throw "-UpdateRevision must be a full source revision." }
    $UpdateSetupPath = (Resolve-Path -LiteralPath $UpdateSetupPath).Path
    $UpdateManifestPath = (Resolve-Path -LiteralPath $UpdateManifestPath).Path
}
$updateChannelPort = 18443
$updateChannel = $null
$updateCertificate = $null
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
  await page.goto(`${baseURL}/stacks/new`, { waitUntil: "load" });
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

function Start-UpdateChannel {
    $channelDir = Join-Path $DiagnosticsDir "update-channel"
    New-Item -ItemType Directory -Force -Path $channelDir | Out-Null
    Copy-Item -LiteralPath $UpdateSetupPath -Destination (Join-Path $channelDir "kombify-Techstack-Setup.exe") -Force
    Copy-Item -LiteralPath $UpdateManifestPath -Destination (Join-Path $channelDir "kombify-techstack-windows-update.json") -Force
    # A loopback HTTPS origin trusted only on this disposable host; the
    # manifest signature, not the transport, carries the update's authenticity.
    $script:updateCertificate = New-SelfSignedCertificate -Subject "CN=127.0.0.1" `
        -TextExtension @("2.5.29.17={text}IPAddress=127.0.0.1") `
        -CertStoreLocation Cert:\LocalMachine\My -KeyExportPolicy Exportable -NotAfter (Get-Date).AddDays(1)
    $pfx = Join-Path ([IO.Path]::GetTempPath()) "kombify-update-channel-$smokeId.pfx"
    $cer = Join-Path ([IO.Path]::GetTempPath()) "kombify-update-channel-$smokeId.cer"
    Export-PfxCertificate -Cert $script:updateCertificate -FilePath $pfx `
        -Password (ConvertTo-SecureString -String $smokeId -Force -AsPlainText) | Out-Null
    Export-Certificate -Cert $script:updateCertificate -FilePath $cer | Out-Null
    Import-Certificate -FilePath $cer -CertStoreLocation Cert:\LocalMachine\Root | Out-Null
    $server = Join-Path ([IO.Path]::GetTempPath()) "kombify-update-channel-$smokeId.cjs"
    @'
const https = require("node:https");
const { readFileSync } = require("node:fs");
const path = require("node:path");
const dir = process.env.CHANNEL_DIR;
https.createServer({ pfx: readFileSync(process.env.CHANNEL_PFX), passphrase: process.env.CHANNEL_PASSPHRASE }, (req, res) => {
  const name = path.basename(new URL(req.url, "https://127.0.0.1").pathname);
  try { const body = readFileSync(path.join(dir, name)); res.writeHead(200); res.end(body); }
  catch { res.writeHead(404); res.end(); }
}).listen(Number(process.env.CHANNEL_PORT), "127.0.0.1");
'@ | Set-Content -LiteralPath $server -Encoding UTF8
    $env:CHANNEL_DIR = $channelDir
    $env:CHANNEL_PFX = $pfx
    $env:CHANNEL_PASSPHRASE = $smokeId
    $env:CHANNEL_PORT = "$updateChannelPort"
    $script:updateChannel = Start-Process -FilePath node -ArgumentList @($server) -WindowStyle Hidden -PassThru
    foreach ($name in @("CHANNEL_DIR", "CHANNEL_PFX", "CHANNEL_PASSPHRASE", "CHANNEL_PORT")) {
        [Environment]::SetEnvironmentVariable($name, $null)
    }
    Wait-Endpoint "https://127.0.0.1:$updateChannelPort/kombify-techstack-windows-update.json" -TimeoutSeconds 30 | Out-Null
    Write-Host "[windows-client-installed-smoke] update channel serves $UpdateVersion on loopback HTTPS"
}

function Stop-UpdateChannel {
    if ($script:updateChannel) {
        Stop-Process -Id $script:updateChannel.Id -Force -ErrorAction SilentlyContinue
    }
    if ($script:updateCertificate) {
        $thumbprint = $script:updateCertificate.Thumbprint
        foreach ($store in @("Cert:\LocalMachine\My", "Cert:\LocalMachine\Root")) {
            Get-ChildItem -Path $store | Where-Object Thumbprint -eq $thumbprint | Remove-Item -Force -ErrorAction SilentlyContinue
        }
    }
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

Assert-AppDependencies

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

$installed = $false
try {
    Stop-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir
    & $resetScript -StateDir $StateDir -InstallDir $InstallDir -IncludeClientConfig -Discard

    if ($Installer -eq "setup") {
        Invoke-ShippedSetup -Action install
    } else {
        & $installScript -InstallDir $InstallDir -StateDir $StateDir -Mode local -NoShortcuts `
            -LocalUiUrl "$localOrigin/" `
            -LocalOnboardingUrl "$localOrigin/client/local?client=windows"
    }
    $installed = $true
    if ($updateRequested) {
        Start-UpdateChannel
        New-Item -ItemType Directory -Force -Path $StateDir | Out-Null
        @{ mode = "local"; updateManifestUrl = "https://127.0.0.1:$updateChannelPort/kombify-techstack-windows-update.json"; updateChannel = "stable" } |
            ConvertTo-Json | Set-Content -LiteralPath (Join-Path $StateDir "client.json") -Encoding UTF8
    }
    if (!(Test-Path -LiteralPath $clientExe) -or !(Test-Path -LiteralPath $runtimeExe)) {
        throw "The $Installer installer did not install the client and runtime into $InstallDir"
    }
    Write-Host "[windows-client-installed-smoke] installed through the $Installer installer at $InstallDir"
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

    $restartVersion = $Version
    $restartRevision = $ExpectedRevision
    if ($updateRequested) {
        $stageDescriptor = Join-Path $StateDir "updates\$UpdateVersion\stage.json"
        $stageDeadline = (Get-Date).AddMinutes(5)
        while (!(Test-Path -LiteralPath $stageDescriptor) -and (Get-Date) -lt $stageDeadline) {
            Start-Sleep -Seconds 2
        }
        if (!(Test-Path -LiteralPath $stageDescriptor)) {
            throw "The installed client did not stage the signed update $UpdateVersion."
        }
        Write-Host "[windows-client-installed-smoke] installed client verified and staged update $UpdateVersion"
        $restartVersion = $UpdateVersion
        $restartRevision = $UpdateRevision
    }

    Stop-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir
    Move-Item -LiteralPath $runtimeLog -Destination (Join-Path $StateDir "runtime\techstack-runtime-first-start.log")
    $process = Start-Process -FilePath $clientExe -WindowStyle Hidden -PassThru
    Write-Host "[windows-client-installed-smoke] restarted client pid=$($process.Id)"
    if ($updateRequested) {
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
    Wait-LogLine -Path $runtimeLog -Pattern "local device session restored"
    if ($updateRequested) {
        Wait-LogLine -Path $runtimeLog -Pattern "client update applied: $Version -> $UpdateVersion"
        if (Test-Path -LiteralPath (Join-Path $StateDir "updates\pending.json")) {
            throw "The applied update left its pending marker behind."
        }
        Write-Host "[windows-client-installed-smoke] update $Version -> $UpdateVersion applied by the installed client"
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
        $SetupPath = $UpdateSetupPath
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
    Write-Host "[windows-client-installed-smoke] ok"
} finally {
    Stop-SmokeProcesses -InstallDir $InstallDir -StateDir $StateDir
    Save-SmokeDiagnostics
    if ($updateRequested) {
        Stop-UpdateChannel
    }
    if ($BlockOutbound) {
        Disable-OutboundBlock
    }
    [Environment]::SetEnvironmentVariable("TECHSTACK_CLIENT_STATE_DIR", $previousStateOverride)
    [Environment]::SetEnvironmentVariable("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", $previousWebViewArguments)
    [Environment]::SetEnvironmentVariable("TECHSTACK_SMOKE_BASE_URL", $previousSmokeBaseUrl)
    [Environment]::SetEnvironmentVariable("TECHSTACK_SMOKE_CDP_URL", $previousSmokeCdpUrl)
    if (!$KeepState) {
        & $resetScript -StateDir $StateDir -InstallDir $InstallDir -IncludeClientConfig -Discard
    }
    if ($Installer -eq "setup") {
        if ($installed -and !$KeepInstall) {
            Invoke-ShippedSetup -Action uninstall
        }
    } elseif (!$KeepInstall -and (Test-Path -LiteralPath $InstallDir)) {
        $resolvedInstallDir = Get-NormalizedPath ((Resolve-Path -LiteralPath $InstallDir).Path)
        if (!(Test-StrictChildPath $resolvedInstallDir $allowedInstallRoot) -or
            $resolvedInstallDir.Equals($defaultProductInstall, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to remove smoke install outside its isolated root: $resolvedInstallDir"
        }
        Assert-NoReparsePointInBoundary $resolvedInstallDir $allowedInstallRoot
        Remove-Item -LiteralPath $resolvedInstallDir -Recurse -Force
    }
}
