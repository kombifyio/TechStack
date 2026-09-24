# kombify TechStack Windows Client

Native Windows shell for kombify TechStack.

The reusable profile-validation and Windows secret-custody boundary lives in
`Kombify.Client.Shell`; Techstack-specific runtime supervision, routes,
branding and UI remain in `Kombify.TechStack.Client`.

- `kombify-techstack-client.exe` is the WinForms/WebView2 desktop app.
- `techstack.exe` remains the runtime/CLI binary.
- The client reads `%LOCALAPPDATA%\kombify\techstack-client\client.json`.
- The Windows taskbar/application icon uses the navy kombify mark.
- Cloud access and refresh tokens are stored only as per-user Generic
  Credentials in Windows Credential Manager. `cloud-session.json` contains
  non-secret connection metadata and credential target names only.
- The local runtime session-signing secret, the local device-session token
  and the runtime encryption key (`TECHSTACK_ENCRYPTION_KEY`, 32 bytes, used
  for Wallet, backup and StackKit custody) also live in Windows Credential
  Manager; none is written into the runtime data directory. A present but
  malformed encryption key stops the start instead of being replaced, because
  a new key could not read the retained encrypted data.
- `mode=local` starts the bundled `techstack.exe` when the local runtime is
  not already reachable, then opens `/client/local?client=windows`. That route
  uses the device session bootstrapped by the shell and opens the operator UI
  directly, without a mode picker, account creation or interactive login.
  Existing configurations pointing at the former default mode picker migrate
  to this local entry. Explicit Cloud and server configurations are preserved.
- Local mode runs the real Postgres-backed control plane against an embedded
  PostgreSQL 16 child process (`TECHSTACK_EMBEDDED_POSTGRES=1`,
  `internal/localdb/embedded_postgres.go`). The installer carries the pinned
  PostgreSQL archive in `postgres\`; the shell sets
  `TECHSTACK_EMBEDDED_POSTGRES_BUNDLE_DIR` to this directory. Startup extracts
  it into the user runtime directory without downloading binaries or writing
  to the installation directory. A missing bundle requires an installation
  repair. Data remains in the user runtime directory (`postgres\data`).
  There is no separate desktop data backend.
- `mode=cloud` opens the TechStack Cloud web UI in WebView2; the separate
  device-code URL remains available for Cloud tool-token authorization.
- `mode=server` first loads `/.well-known/kombify-client`, rejects unknown
  fields, insecure remote HTTP, and origin mismatches, then persists only the
  public profile as `connection-profile.json`. It never navigates directly to
  an unverified raw server URL.

The packaging and installer tools require PowerShell 7.5 or newer. This is a
tooling requirement only; the installed client does not depend on PowerShell.

Build and install locally:

```powershell
dotnet tool install --global wix --version 4.0.6
wix extension add WixToolset.Bal.wixext/4.0.6
pwsh -File .\scripts\package-windows-client.ps1
.\dist\windows-client\stage\install-windows-client.ps1 `
  -Launch
```

The same command also produces the public Windows release assets in
`dist\windows-client`:

- `kombify-Techstack-Setup.exe` — user-facing WiX/Burn installer
- `kombify-Techstack-x64.msi` — per-machine MSI
- `kombify-techstack-client_<version>_Windows_x86_64.zip` — portable artifact
- `SHA256SUMS.txt` and `UNSIGNED-WINDOWS-RELEASE.txt`

The MSI and setup.exe are intentionally unsigned for the current alpha. Verify
the hashes before installing; Authenticode signing is optional and never
blocks publication.

Cloud UI smoke:

```powershell
.\dist\windows-client\stage\install-windows-client.ps1 `
  -Mode cloud `
  -CloudUiUrl "https://techstack.kombify.io/login?manual=1&client=windows" `
  -Launch
```

Reset local test state without uninstalling:

```powershell
.\dist\windows-client\stage\reset-windows-client-state.ps1
```

The reset helper moves WebView2/session/runtime data into
`%LOCALAPPDATA%\kombify\techstack-client-backups\<timestamp>` and keeps
`client.json` by default. It deletes the scoped Cloud and local credential targets unless
`-PreserveCredentials` is explicitly supplied (`-PreserveCloudCredentials`
remains a compatibility alias).
Release/local-E2E automation may pass `-Discard` only for an isolated state
directory; normal resets preserve data in a uniquely named timestamped backup.
For a non-default installation, pass its exact `-InstallDir`; process shutdown
is restricted to that installation and the selected state directory.

Automated installed-client tests set `TECHSTACK_CLIENT_STATE_DIR` to an
isolated random directory below `%LOCALAPPDATA%\kombify`; file state and
Credential Manager targets are both namespaced to that directory. The shell
refuses override paths outside that root. Normal installations keep the stable
default state and credential targets. The smoke refuses the normal product
state and installation without an override.

Installed local smoke:

The focused PostgreSQL artifact probe is `mise run test:windows:postgres:offline`.
It prepares the pinned archive, then starts the production local store with a
fresh data directory and an unavailable download endpoint, checks SQL state
across restart, and verifies that a missing bundle cannot trigger a download.
This probe does not establish full installed-client or backup/restore acceptance.

```powershell
node app/scripts/install-deps.mjs --frozen-lockfile
.\scripts\windows-client-installed-smoke.ps1
```

The smoke packages the client, installs it (`-Installer portable`, the
default, into isolated user directories; `-Installer setup` through the shipped
`kombify-Techstack-Setup.exe` into its product location) and starts the
installed EXE. `-BlockOutbound` (elevated, disposable hosts only) blocks all
non-loopback traffic of the client, runtime and WebView2 for the whole run. It verifies the runtime version, full source revision
and PostgreSQL backend, then attaches to the actual installed WebView through
a temporary loopback debugging port (`LocalPort + 10`). The native shell must
open the dashboard using its own device session, without test-injected cookies
or manual account setup. With external browser requests blocked, the smoke
reloads the dashboard and opens Wallet and the Creation Wizard, then repeats
after a restart and checks that the local operator identity and an encrypted
Wallet item written on first start are unchanged. In setup mode it then runs
the shipped uninstaller and checks that the binaries are gone while the local
PostgreSQL data and the device credential remain. `-DiagnosticsDir` receives
the runtime and PostgreSQL logs before the isolated state is discarded.

`-SkipPackage` reuses the existing stage; `-ExpectedRevision` and `-Version`
bind it to the artifact being exercised (both otherwise resolve from `HEAD`).
`-SpecTemplatesPath` passes same-release CI templates to the existing packager.
Provider lifecycle, signed updates and backup/restore remain separate evidence.
The local debugging setting exists only in the smoke process environment and
is restored during cleanup. See the [official WebView2 integration](https://playwright.dev/docs/webview2).

Signed updates (NATIVE-CLIENT-PLATFORM-STANDARD section 7):

- The shipped installation (Setup.exe/MSI under `%ProgramFiles%\kombify\techstack`)
  checks its channel after the UI is up by running
  `techstack.exe client-update`. The runtime binary embeds the Ed25519
  update key `kombify-desktop-update-2026-v1`, accepts only a manifest signed
  with it (`client-update-manifest.v1`), refuses downgrades and channels below
  `updateMinSupportedVersion`, and stages the installer below
  `<state>\updates\<version>` only after its size and SHA-256 match.
- At the next start, before the runtime starts, the client re-verifies the
  staged installer, snapshots the runtime data, keeps a copy of the installed
  version's installer from the Burn package cache, and runs the new
  Setup.exe (`/passive`, one elevation prompt). The new version then starts
  on the retained data. If its runtime does not become healthy, the client
  uninstalls it, reinstalls the previous version, and restores the snapshot
  before that runtime starts.
- `client.json` sets `updateManifestUrl` (default: the public
  `kombifyio/TechStack` latest release asset
  `kombify-techstack-windows-update.json`), `updateChannel` (`stable`) and
  `autoUpdate` (`false` opts out). The trusted key cannot be configured.
  Portable installations never update themselves. An unreachable channel
  never delays a start.
- The release publisher signs manifests with
  `node scripts/new-client-update-manifest.mjs` (Ed25519; the private key is
  read from a file or an environment variable and never printed).

`-RequireAuthenticode` is a verifier, not a signing step. It remains
available for a future signed lane, but it is not required by the current
unsigned alpha release workflow.

Uninstall preserves runtime data, connection configuration and scoped
Credential Manager entries by default:

```powershell
.\uninstall-windows-client.ps1
```

Explicit full removal uses `-RemoveState`; the uninstaller validates that both
install and state paths are strict descendants of the Kombify LocalAppData
roots and refuses reparse-point boundaries. The
`windows-client-uninstall-check.ps1` gate proves retain-by-default and explicit
removal with isolated fixture paths.
