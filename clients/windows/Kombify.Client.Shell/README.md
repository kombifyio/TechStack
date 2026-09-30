# Kombify.Client.Shell

Shared Windows lifecycle and connection-profile code used by the Techstack
desktop client. It provides strict profile validation, Windows Credential
Manager access, isolated state paths, diagnostics and child-process supervision.
The product shell owns runtime arguments, health checks, authentication policy,
updates and UI.

The Local client builds this project from the source included in this tree:

```powershell
dotnet build clients/windows/Kombify.TechStack.Client -p:DesktopEdition=Local
```

No private package registry or kombify account is required. The neutral shell
does not depend on the optional Cloud adapter. Local installers must not contain
Cloud account or Connect components.
