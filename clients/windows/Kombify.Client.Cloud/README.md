# Optional Windows Cloud adapter

This source project contains native browser authentication, Windows key custody
and the Connect API adapter. It is separate from the neutral
`Kombify.Client.Shell` project. Existing public types retain that namespace for
source compatibility.

The Local build neither references nor bundles this adapter. Build the Local
client from the included source with:

```powershell
dotnet build clients/windows/Kombify.TechStack.Client -p:DesktopEdition=Local
```

Cloud consumers require an approved native application registration, callback
ports, product authority and scoped credential names. Credentials and private
keys must remain outside the WebView. The presence of this source does not
establish a Cloud distribution, account entitlement or working installed login.
