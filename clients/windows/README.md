# kombify TechStack Windows Client

Native Windows shell for kombify TechStack.

- `kombify-techstack-client.exe` is the WinForms/WebView2 desktop app.
- `techstack.exe` remains the runtime/CLI binary.
- The client reads `%LOCALAPPDATA%\kombify\techstack-client\client.json`.
- The Windows taskbar/application icon uses the navy kombify mark.
- Cloud access and refresh tokens are stored only as per-user Generic
  Credentials in Windows Credential Manager. `cloud-session.json` contains
  non-secret connection metadata and credential target names only.
- The local runtime session-signing secret and local device-session token also
  live in Windows Credential Manager; neither is written into the runtime data
  directory.
- `mode=local` starts the bundled `techstack.exe` when the local runtime is
  not already reachable, then opens `/client/local?client=windows`. That route
  uses the real TechStack first-run auth setup/login APIs and redirects an
  existing local session into the operator UI.
- Local mode runs the real Postgres-backed control plane against an embedded
  PostgreSQL 16 child process (`TECHSTACK_EMBEDDED_POSTGRES=1`,
  `internal/localdb/embedded_postgres.go`); data lives under the runtime data
  directory (`postgres\data`). There is no separate desktop data backend.
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

```powershell
pnpm --dir app install --frozen-lockfile
pnpm --dir app exec playwright install chromium
.\scripts\windows-client-installed-smoke.ps1
```

The smoke packages the client, installs it locally, starts the installed EXE,
creates the first local owner, opens Wallet and the Creation Wizard, restarts
the installed client, and verifies the local device-token session.

Signed update contract (PowerShell 7.5+):

```powershell
pwsh -File .\scripts\package-windows-client.ps1 `
  -Version "0.1.0" `
  -RequireSignedUpdateManifest `
  -UpdateManifestPrivateKeyPath $env:WINDOWS_UPDATE_PRIVATE_KEY_FILE `
  -UpdateManifestPublicKeyPath $env:WINDOWS_UPDATE_PUBLIC_KEY_FILE `
  -UpdateManifestKeyId $env:WINDOWS_UPDATE_KEY_ID `
  -UpdateDownloadUrl "https://releases.kombify.io/windows/kombify-techstack-client_0.1.0_Windows_x86_64.zip"
```

The manifest signature binds version, channel, HTTPS package URL, SHA-256,
size, timestamp and key id. `test-windows-client-update-manifest.ps1` verifies
the selected public key and package bytes; the local contract gate includes a
tamper-rejection test. This is a producer/verifier contract only. The client
does not yet consume update manifests, enforce version monotonicity, download
an update, or perform an update handoff.

`-RequireAuthenticode` is likewise a verifier, not a signing step. It remains
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
