# Windows installer

The public Windows release contains a WiX v4 MSI and a Burn bootstrapper:

- `kombify-Techstack-x64.msi` is the per-machine MSI authority and requests
  elevation once through the normal Windows setup flow.
- `kombify-Techstack-Setup.exe` is the user-facing bootstrapper and chains the
  exact MSI beside it.
- `install-windows-client.ps1` and the ZIP remain the portable/development
  path.

Both installer artifacts are intentionally unsigned for the current alpha.
Signing is optional release metadata and must not block packaging. Release
notes and `UNSIGNED-WINDOWS-RELEASE.txt` must make the unsigned status clear.

The MSI installs only the staged application under
`%ProgramFiles%\kombify\techstack`. Runtime data and Credential Manager
entries live below `%LOCALAPPDATA%\kombify\techstack-client`; major upgrades
and uninstall remove the installed binaries and Start Menu shortcut but retain
that user state. Use `scripts/uninstall-windows-client.ps1 -RemoveState` for an
explicit state removal.

The MSI owns one inbound Windows Firewall exception for `TCP/5264`, limited to
the `Private` profile and `LocalSubnet`. It removes the rule on uninstall. The
runtime still keeps the listener closed until the authenticated local user
explicitly enables private-LAN enrollment in the client; the rule alone does
not expose a service.

Build from a staged package with PowerShell 7.5+ and WiX 4.0.6:

```powershell
dotnet tool install --global wix --version 4.0.6
wix extension add WixToolset.Bal.wixext/4.0.6
wix extension add WixToolset.Firewall.wixext/4.0.6
pwsh -File .\installer\wix\build-windows-installer.ps1 `
  -StageDir .\dist\windows-client\stage `
  -Version 0.0.0-local
```
